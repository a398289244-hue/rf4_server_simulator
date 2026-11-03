// Package shortcut 管快捷栏（快捷键）那三件事：
//
//	11/1 列表   应答 = 参数表"65"{ obj136{ 槽位 u16, 物品 GUID } × N }        （真服那条 107B）
//	11/2 绑定   请求 = 参数表"524"{ 槽位, GUID }，应答 = obj136{槽位, GUID}    （真服那条 32B）
//	11/3 解绑   请求 = 参数表"524"{ 槽位 }，      应答 = obj136{槽位, 零 GUID}
//
// 形状不是猜的：11/1、11/2 在考卷里都有原文（data/rpc_table_real.json），
// 11/3 考卷里没有，照 Node 版实测那条走（回 obj136，GUID 写零）。
//
// 数据只认一个地方：MySQL shortcuts 表（username, slot, guid, item_key）。
// 绑定前一定先确认「这件东西真是这个号名下的」—— 之前那种「删了又回来的影子物品」
// 就是把别人的 GUID 绑上了，界面上多出一根根本不存在的竿子。
package shortcut

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"rf4server/internal/gamestore"
	"rf4server/internal/wire"
)

const (
	// TypeShortcut 快捷栏一格的物件类型号（类名 iiemhjlblcc）：#00 槽位 u16、#01 物品 GUID
	TypeShortcut = 136
	// ZeroGUID 「这一格空着」的写法。解绑、或者绑定被拒时都发它。
	ZeroGUID = "00000000000000000000000000000000"
	// argOff 客户端请求里参数表的起点：9 字节应答头那种壳 + main + sub = 第 11 字节
	argOff = 11
)

// Service 快捷栏服务。自己没状态，数据每次现查 MySQL（少爷绑完马上要能在库里看到）。
type Service struct {
	table *wire.Table       // 协议字段表：obj136 的字段写法从这儿来
	run   gamestore.Runtime // 运行时数据入口（shortcuts 表 + objects 表都走它）
}

// New 造一个服务。table 或 run 为 nil 时方法会明确报错，不会闷头发空帧。
func New(t *wire.Table, run gamestore.Runtime) *Service { return &Service{table: t, run: run} }

// ListFrame 11/1：把 this 号现有的绑定拼成一帧，顺便把行带出去给日志用。
// 一条绑定都没有也照样回（回一帧空列表），不回客户端会一直等。
func (s *Service) ListFrame(ctx context.Context, user string, callID uint32) ([]byte, []gamestore.Shortcut, error) {
	rows, err := s.list(ctx, user)
	if err != nil {
		return nil, nil, err
	}
	frame, err := BuildListFrame(s.table, callID, rows)
	if err != nil {
		return nil, rows, err
	}
	return frame, rows, nil
}

// list 真的去库里拿一遍（没接上 MySQL 就说清楚，别发一帧空的骗客户端）
func (s *Service) list(ctx context.Context, user string) ([]gamestore.Shortcut, error) {
	if s.run == nil {
		return nil, fmt.Errorf("没接上 MySQL，快捷栏拼不出来")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := s.run.Shortcuts(ctx, user)
	if err != nil {
		return nil, fmt.Errorf("读 %s 的快捷栏失败: %w", user, err)
	}
	return rows, nil
}

// BindResult 一次绑定的结果，字段全是为日志准备的（出了问题日志要能一眼看懂）
type BindResult struct {
	Slot    int    // 第几格
	GUID    string // 要绑的物品 GUID（32 位十六进制小写）
	ItemKey string // 物品编号，绑上了才有（日志里显示这个比显示 GUID 好认）
	OK      bool   // 绑上了没有
	Reason  string // 没绑上的原因
}

// BindFrame 11/2：从请求里抠出槽位和 GUID，验货、写库、拼应答。
// 验不过也照样回帧（GUID 写零），客户端才会把那一格显示成空的 —— 不回它就一直转。
func (s *Service) BindFrame(ctx context.Context, user string, callID uint32, plain []byte) ([]byte, BindResult, error) {
	slot, guid, err := slotAndGUID(s.table, plain)
	if err != nil {
		return nil, BindResult{}, err
	}
	res := BindResult{Slot: slot, GUID: guid}
	if s.run == nil {
		res.Reason = "没接上 MySQL，绑不了"
	} else if guid == "" || guid == ZeroGUID {
		res.Reason = "请求里没带物品 GUID"
	} else {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		key, ownErr := s.ownerItemKey(ctx, user, guid)
		if ownErr != nil {
			res.Reason = "查名下物品失败: " + ownErr.Error()
		} else if key == "" {
			res.Reason = "这件东西不在 " + user + " 名下（影子物品，多半是录制带出来的）"
		} else {
			if err := s.run.SetShortcut(ctx, user, slot, guid, key); err != nil {
				res.Reason = "写 MySQL shortcuts 失败: " + err.Error()
			} else {
				res.OK, res.ItemKey = true, key
			}
		}
	}
	frame, err := BuildBindFrame(s.table, callID, res.Slot, chooseStr(res.OK, res.GUID, ZeroGUID))
	if err != nil {
		return nil, res, err
	}
	return frame, res, nil
}

// UnbindFrame 11/3：清空那一格（库里 guid 写空串就是解绑），应答照真服发 obj136{槽位, 零 GUID}
func (s *Service) UnbindFrame(ctx context.Context, user string, callID uint32, plain []byte) ([]byte, BindResult, error) {
	slot, _, err := slotAndGUID(s.table, plain)
	if err != nil {
		return nil, BindResult{}, err
	}
	res := BindResult{Slot: slot}
	if s.run == nil {
		res.Reason = "没接上 MySQL，解不了"
	} else {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := s.run.SetShortcut(ctx, user, slot, "", ""); err != nil {
			res.Reason = "写 MySQL shortcuts 失败: " + err.Error()
		} else {
			res.OK = true
		}
	}
	frame, err := BuildBindFrame(s.table, callID, res.Slot, ZeroGUID)
	if err != nil {
		return nil, res, err
	}
	return frame, res, nil
}

// ownerItemKey 这件 GUID 是不是这个号名下的；是就把物品编号带回来（绑定时顺手存一份，下发时不用回查）
func (s *Service) ownerItemKey(ctx context.Context, user, guid string) (string, error) {
	objs, err := s.run.Objects(ctx, user)
	if err != nil {
		return "", err
	}
	want := strings.ToLower(guid)
	for _, o := range objs {
		if strings.ToLower(o.GUID) == want {
			return o.ItemKey, nil
		}
	}
	return "", nil
}

// BuildListFrame 把绑定列表拼成 11/1 应答：9 字节应答头 + 参数表"65"{ obj136 × N }
func BuildListFrame(t *wire.Table, callID uint32, rows []gamestore.Shortcut) ([]byte, error) {
	items := make([]wire.Value, 0, len(rows))
	for _, r := range rows {
		o, err := shortcutObj(t, r.Slot, r.GUID)
		if err != nil {
			return nil, err
		}
		items = append(items, o)
	}
	body, err := wire.Args{Code: "65", Items: items}.Encode()
	if err != nil {
		return nil, err
	}
	return append(wire.ResponseHead(callID), body...), nil
}

// BuildBindFrame 11/2、11/3 的应答：9 字节应答头 + 一个 obj136
func BuildBindFrame(t *wire.Table, callID uint32, slot int, guid string) ([]byte, error) {
	obj, err := shortcutObj(t, slot, guid)
	if err != nil {
		return nil, err
	}
	body, err := obj.Encode()
	if err != nil {
		return nil, err
	}
	return append(wire.ResponseHead(callID), body...), nil
}

// shortcutObj 造一个 obj136：槽位 u16 + 物品 GUID。GUID 不合法就当空的发（写零 GUID）。
func shortcutObj(t *wire.Table, slot int, guid string) (*wire.Obj, error) {
	if t == nil {
		return nil, fmt.Errorf("shortcut: 协议字段表没载入，obj%d 造不出来", TypeShortcut)
	}
	o, err := t.NewObj(TypeShortcut)
	if err != nil {
		return nil, err
	}
	if slot < 0 {
		slot = 0
	}
	if slot > 0xffff {
		slot = 0xffff
	}
	if err := o.SetU16At(0, uint16(slot)); err != nil {
		return nil, err
	}
	g := strings.ToLower(strings.ReplaceAll(guid, "-", ""))
	if len(g) != 32 {
		g = ZeroGUID
	}
	if err := o.SetGUIDAt(1, g); err != nil {
		return nil, err
	}
	return o, nil
}

// slotAndGUID 从请求里捞「第一个整数 + 第一个 GUID」。
// 参数表里东西不多，但位置未必固定（客户端有时中间还包一层物件），所以整棵树走一遍最稳。
func slotAndGUID(t *wire.Table, plain []byte) (int, string, error) {
	if len(plain) <= argOff {
		return 0, "", fmt.Errorf("快捷栏请求太短（%d 字节），槽位和 GUID 都读不出来", len(plain))
	}
	if !isArgs(plain, argOff) {
		return 0, "", fmt.Errorf("快捷栏请求第 %d 字节不是参数表（0x03），实为 0x%02x", argOff, plain[argOff])
	}
	// 这里不用管解到哪儿为止：多解出来的尾巴不影响「第一个整数 + 第一个 GUID」
	ints, guids := collect(t, plain, argOff)
	if len(ints) == 0 {
		return 0, "", fmt.Errorf("快捷栏请求里没有槽位号")
	}
	guid := ""
	if len(guids) > 0 {
		guid = guids[0]
	}
	return ints[0], guid, nil
}

// isArgs 那个位置是不是参数表 marker（0x03）
func isArgs(b []byte, off int) bool { return off < len(b) && b[off] == 0x03 }

// collect 解一层参数表，把里面的整数和 GUID 按出现顺序捞出来。
// 解不开就返回空（调用方会拒这次操作，并在日志里说清楚是包读不懂）。
func collect(t *wire.Table, plain []byte, off int) (ints []int, guids []string) {
	if t == nil {
		return nil, nil
	}
	v, _, err := t.DecodeValue(plain, off)
	if err != nil {
		return nil, nil
	}
	wire.Walk(v, func(it wire.WalkItem) {
		switch it.Kind {
		case "u32", "u64", "u16", "byte":
			ints = append(ints, int(it.Num))
		case "guid":
			guids = append(guids, it.Hex)
		}
	})
	return ints, guids
}

// chooseStr 条件成立取 a，否则取 b
func chooseStr(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}

// Describe 给日志用的一行人话：槽位:物品编号(GUID 前 8 位)
func Describe(rows []gamestore.Shortcut) string {
	if len(rows) == 0 {
		return "一条都没绑"
	}
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		key := r.ItemKey
		if key == "" {
			key = "?"
		}
		g := r.GUID
		if len(g) > 8 {
			g = g[:8]
		}
		parts = append(parts, strconv.Itoa(r.Slot)+":"+key+"("+g+")")
	}
	return strings.Join(parts, " ")
}
