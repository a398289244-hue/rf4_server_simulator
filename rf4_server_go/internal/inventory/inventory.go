// Package inventory 管「背包清单」这一件事：客户端问 4/5，我们回 3/2 那一帧物件清单。
//
// 帧形状（真服实测拆过：15 字节的头 + 一条一条物件记录排到帧尾）：
//
//	01 | marker(-2, i32) | call_id u32 | 03 02 "61" | 条数 u16 | 记录1 记录2 ...
//
// 记录全部来自这个号自己在 MySQL objects 表里存着的那些字节（objects.elem），
// 一条都不借录制里那一大帧 —— 录制那帧装的是抓包那个人的 55 件货，挪给谁用都成别人的世界。
// 逻辑照 Node 版 src/player.js 的 buildObjectList / stripReceipt 复刻，换成 Go。
// 注意：4/5 请求里出现的 GUID 是客户端点名要下载的物件，不是“已经有了”的去重表。
package inventory

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"rf4server/internal/gamestore"
)

// HeadLen 物件清单帧头长度：1(类型) + 4(marker) + 4(call_id) + 1(args 容器) + 3(短串 "61") + 2(条数)
const HeadLen = 15

// CategoryContainer 容器/套装这个类别（55）。这类记录只是一件外壳，
// 内容物要连着一起发才有意义，那块还没搬，所以先不下发；
// 凭据：static_item_def 里 box_spin_l_start 就是类别 55，Node 版那边同一时刻也把它跳过
// （日志原文：跳过 1 件: box_spin_l_start(发不出去)），两边行为一致。
const CategoryContainer = 55

// receiptMark 购买回执（01 a6 00 00 00）的开头。它只该出现在 1/36 的买入应答里；
// 物件清单里每条记录到尾巴那 7 个 0 就结束了，多留一段回执客户端解不下去，
// 背包会一直卡在「正在下载」（实测 2026-11-02，Node 版踩过同一个坑）。
var receiptMark = []byte{0x01, 0xa6, 0x00, 0x00, 0x00}

// recordTail 物件记录结尾那 7 个 0
var recordTail = []byte{0, 0, 0, 0, 0, 0, 0}

// BuildFrame 整帧自己拼：写死 15 字节头，后面按顺序排物件记录。
// 条数必须等于记录条数；传空数组就是「这个号现在一件没有」，不能偷偷拿别人的顶上。
func BuildFrame(callID uint32, elems [][]byte) []byte {
	out := make([]byte, HeadLen)
	out[0] = 0x01
	// marker = -2（应答）。负数转 uint32 要先落到 int32 变量，Go 不允许常量直接绕
	var marker int32 = -2
	putU32(out[1:5], uint32(marker))
	putU32(out[5:9], callID)
	out[9] = 0x03 // args：容器
	out[10] = 0x02
	out[11] = 0x36 // '6'
	out[12] = 0x31 // '1'
	n := len(elems)
	if n > 0xffff {
		n = 0xffff // 条数是 u16，超了只能截；真人背包远没到
	}
	out[13] = byte(n)
	out[14] = byte(n >> 8)
	for _, e := range elems {
		out = append(out, e...)
	}
	return out
}

// StripReceipt 把混在物件记录尾巴上的购买回执剪掉，换成该有的 7 个 0。
// 找不到回执标记就原样返回（大多数记录本来就没有）。
func StripReceipt(elem []byte) []byte {
	i := indexOf(elem, receiptMark)
	if i < 0 {
		return elem
	}
	out := make([]byte, 0, i+len(recordTail))
	out = append(out, elem[:i]...)
	return append(out, recordTail...)
}

// Result 一次背包清单的计算结果。分四种情况列出来，是为了让日志能把「为什么这件没发」说清，
// 别再像以前那样只回一句「发不出去」让人猜。
type Result struct {
	Elems     [][]byte // 该发的物件记录，顺序就是下发顺序
	Requested []string // 客户端在 4/5 里点名要下载的 GUID 对应物品
	Deferred  []string // 容器/套装（类别 55）：内容还没搬，暂时不发
	Missing   []string // 库里没存物件记录（或记录不是十六进制）的：发不出，得回头补
}

// Service 背包清单服务：数据只从 gamestore 拿（Runtime = 这个号的数据，Catalog = 物品类别），
// 本包不连库、不读文件，所以拿假实现就能单测。
type Service struct {
	Run gamestore.Runtime
	Cat gamestore.Catalog // 允许为空：没类别信息就不做容器筛选，照常下发（日志会带上这条说明）
}

// New 建服务。数据源要同时是 Catalog（我们的 MySQL 实现两个接口都满足），
// 传进来不是的话就降级成「不筛容器」，不至于背包整个发不出去。
func New(run gamestore.Runtime) *Service {
	svc := &Service{Run: run}
	if cat, ok := run.(gamestore.Catalog); ok {
		svc.Cat = cat
	}
	return svc
}

// Need 算出这次 4/5 该发哪些物件记录。
// requestPlain 是客户端那条 4/5 请求的明文：其中的 GUID 是“我要下载这些物件”。
// 2026-11-03 实机反证：把它当“客户端已有”跳过，会让重登后的 25 个 GUID 全被过滤，
// 4/4 明明报 25 件、4/5 却回 0 件，刚买的装备就像消失了一样。
// 请求里没带任何本号 GUID 时仍发全量，兼容开机时的空参数请求和测试客户端。
func (s *Service) Need(ctx context.Context, username string, requestPlain []byte) (Result, error) {
	var res Result
	if s.Run == nil {
		return res, errors.New("没接上 MySQL，背包清单暂时发不出来")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	objs, err := s.Run.Objects(ctx, username)
	if err != nil {
		return res, fmt.Errorf("读名下物品失败: %w", err)
	}

	// 先把类别一次查齐（容器/套装要挑出来）
	cats := map[string]int{}
	if s.Cat != nil {
		keys := make([]string, 0, len(objs))
		for _, o := range objs {
			keys = append(keys, o.ItemKey)
		}
		if cats, err = s.Cat.Categories(ctx, keys); err != nil {
			return res, fmt.Errorf("读物品类别失败: %w", err)
		}
	}

	// 先认出客户端这次点名要哪些 GUID。只要命中至少一个，就按这份点名单筛选；
	// 一个都没命中则视为“要全量”，不能把空请求误判成“什么都不要”。
	requested := make(map[string]bool)
	for _, o := range objs {
		gb, err := hex.DecodeString(o.GUID)
		if err == nil && len(gb) == 16 && indexOf(requestPlain, gb) >= 0 {
			requested[o.GUID] = true
			res.Requested = append(res.Requested, o.ItemKey)
		}
	}
	filterRequested := len(requested) > 0

	for _, o := range objs {
		if cats[o.ItemKey] == CategoryContainer {
			res.Deferred = append(res.Deferred, o.ItemKey)
			continue
		}
		if o.Elem == "" {
			res.Missing = append(res.Missing, o.ItemKey)
			continue
		}
		raw, err := hex.DecodeString(o.Elem)
		if err != nil {
			res.Missing = append(res.Missing, o.ItemKey+"(记录不是十六进制)")
			continue
		}
		if filterRequested && !requested[o.GUID] {
			continue
		}
		res.Elems = append(res.Elems, StripReceipt(raw))
	}
	return res, nil
}

// putU32 小端写 4 字节
func putU32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

// indexOf 在 haystack 里找 needle，找不到返回 -1。
// 空 needle 直接算没找到，免得一搜就命中 0 把「客户端已有」判错。
func indexOf(haystack, needle []byte) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
