// Package itemops 管背包那几只动手的命令：
//
//	4/7 丢东西（删除）  -> 回 800 快照：#00 = 删完还剩的整份清单，#02 = 这次删掉的 GUID
//	4/6 挪位置          -> 先照真服回空 800（客户端自己已经搬完了）
//	4/8 使用            -> gamesess 回 bool，铲子/长柄勺走 baitdig
//
// 为什么 4/7 要把「剩下整份清单」一起塞进 #00：只回「删掉了哪个 GUID」的话，
// 客户端界面不会实时刷新（少爷实测：日志说删了、背包里还杵着，退出重进才没了）。
// 这个口径是 Node 版实测出来的（它日志里那句「回包 4/7=full800」就是这套做法）。
//
// 数据只有一个来源：MySQL objects 表。删的时候连挂在这件东西下面的零件一起删
// （竿删了留在里面的线/轮不删，就会变成「库里没了但界面上还在」的影子物件）。
package itemops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"rf4server/internal/gamestore"
	"rf4server/internal/inventory"
	"rf4server/internal/itemsync"
	"rf4server/internal/wire"
)

// Service 背包操作服务。自己没状态：数据每次现查 MySQL，改完立刻能在库里看到。
type Service struct {
	tbl *wire.Table        // 拼 800 快照要用协议字段表
	run gamestore.Runtime  // 数据入口
	bag *inventory.Service // 删完之后「还剩哪些」复用背包清单那套口径
}

// New 造服务。bag 用同一个数据源建，保证 4/5 和 4/7 下发的物件是同一批。
func New(t *wire.Table, run gamestore.Runtime) *Service {
	return &Service{tbl: t, run: run, bag: inventory.New(run)}
}

// DeleteResult 一次删除的结果，字段都是给日志准备的（哪几件删了、还剩几件、没删成为啥）
type DeleteResult struct {
	Deleted  []string // 真从库里删掉的 GUID（含连带删掉的零件）
	Left     int      // 删完之后还能下发给客户端的件数（容器/套装暂缓下发，不算在内）
	Deferred []string // 那些「暂时不下发」的容器/套装编号，日志里要说清为什么件数对不上
	Reason   string   // 一件都没删时的原因（日志里要说清，别只写「删了 0 件」）
}

// DeleteFrame 4/7：从请求里捞要丢的 GUID，验货、删库、清快捷栏、拼 800 回包。
// 一件都没删掉也照样回帧（客户端在等这一条收尾），原因写进日志。
func (s *Service) DeleteFrame(ctx context.Context, user string, callID uint32, plain []byte) ([]byte, DeleteResult, error) {
	var res DeleteResult
	if s.tbl == nil {
		return nil, res, fmt.Errorf("itemops: 协议字段表没载入，800 快照拼不出来")
	}
	if s.run == nil {
		res.Reason = "没接上 MySQL，删不了"
		frame, err := s.ackFrame(callID)
		return frame, res, err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	wanted := s.guidList(plain)
	removed, err := s.RemoveRequested(ctx, user, wanted)
	if err != nil {
		return nil, res, err
	}
	for _, o := range removed {
		res.Deleted = append(res.Deleted, o.GUID)
	}
	if len(wanted) == 0 {
		res.Reason = "请求里没带 GUID"
	} else if len(removed) == 0 {
		res.Reason = "这些 GUID 不在 " + user + " 名下（多半是界面上残留的影子物件）"
	}

	left, err := s.bag.Need(ctx, user, nil) // 传 nil = 不带「客户端已有」过滤，整份清单都算上
	if err != nil {
		return nil, res, fmt.Errorf("删完再算一遍还剩哪些失败: %w", err)
	}
	res.Left = len(left.Elems)
	res.Deferred = left.Deferred
	frame, err := itemsync.Frame(s.tbl, callID, left.Elems, nil, res.Deleted)
	if err != nil {
		return nil, res, err
	}
	return frame, res, nil
}

// RemoveRequested 删除指定 GUID 以及挂在它们下面的零件，并清掉快捷栏引用。
// 9/3 分解和 4/7 丢弃共用这套真实库操作；返回真正删掉的物件，调用方按自己的协议回包。
func (s *Service) RemoveRequested(ctx context.Context, user string, wanted []string) ([]gamestore.Object, error) {
	if s.run == nil {
		return nil, fmt.Errorf("没接上 MySQL，删不了")
	}
	objs, err := s.run.Objects(ctx, user)
	if err != nil {
		return nil, fmt.Errorf("读 %s 名下物品失败: %w", user, err)
	}
	victims := pickVictims(objs, wanted)
	removed := make([]gamestore.Object, 0, len(victims))
	for _, o := range victims {
		ok, err := s.run.DeleteObject(ctx, user, o.GUID)
		if err != nil {
			return nil, fmt.Errorf("删 %s(%s) 失败: %w", o.ItemKey, o.GUID, err)
		}
		if ok {
			removed = append(removed, o)
		}
	}
	if len(removed) > 0 {
		guids := make([]string, 0, len(removed))
		for _, o := range removed {
			guids = append(guids, o.GUID)
		}
		s.clearShortcuts(ctx, user, guids)
	}
	return removed, nil
}

// ackFrame 4/6 挪位置：回一个空 800（真服 14/2 那条就是这个形状）。
// 客户端那边东西已经自己搬好了，服务端只要认账就行；先不往库里改 parent_guid，
// 真要持久化挪动结果再加一个 Runtime.SetParent 就行（只动这一处）。
func (s *Service) ackFrame(callID uint32) ([]byte, error) {
	if s.tbl == nil {
		return nil, fmt.Errorf("itemops: 协议字段表没载入，空 800 也拼不出来")
	}
	return itemsync.EmptyFrame(s.tbl, callID)
}

// AckFrame 给 4/6 路由用的出口
func (s *Service) AckFrame(callID uint32) ([]byte, error) { return s.ackFrame(callID) }

// clearShortcuts 把指向已被删除物品的快捷栏格子清掉。
// 这一步失败不算大事（顶多快捷栏多一格空的），所以只回一句日志，不影响删除本身。
func (s *Service) clearShortcuts(ctx context.Context, user string, deleted []string) {
	rows, err := s.run.Shortcuts(ctx, user)
	if err != nil {
		return
	}
	gone := map[string]bool{}
	for _, g := range deleted {
		gone[strings.ToLower(g)] = true
	}
	for _, r := range rows {
		if gone[strings.ToLower(r.GUID)] {
			// 错误故意忽略：删都删成了，快捷栏残留不值得把整条操作判失败
			_ = s.run.SetShortcut(ctx, user, r.Slot, "", "")
		}
	}
}

// guidList 从请求正文里把 GUID 捞出来（顺序照帧里的先后）。
// 请求壳是 11 字节（9 头 + main + sub），参数表从第 11 字节开始。
// 解不动就当「一个 GUID 都没带」：删除会变成「一件都没删」并写清原因，不会误删东西。
func (s *Service) guidList(plain []byte) []string {
	const argOff = 11
	if s.tbl == nil || len(plain) <= argOff {
		return nil
	}
	// 请求顶层不一定是 args：9/3 这类命令会直接包一个带 GUID 的对象。
	// 从参数起点按字段表解任意值，再递归收集 GUID，才能同时覆盖两种形状。
	v, _, err := s.tbl.DecodeValue(plain, argOff)
	if err != nil {
		return nil
	}
	var out []string
	wire.Walk(v, func(it wire.WalkItem) {
		if it.Kind == "guid" && it.Hex != "" && it.Hex != "00000000000000000000000000000000" {
			out = append(out, it.Hex)
		}
	})
	return out
}

// Involved 把请求里点到的 GUID 列出来（4/6 挪位置、4/8 使用都靠它定位物件）
func (s *Service) Involved(plain []byte) []string { return s.guidList(plain) }

// pickVictims 在名下单子里挑要删的那些，顺手把挂在其下面的零件一起带上。
// 顺序保持库里的顺序，日志里看着跟背包一致。
func pickVictims(objs []gamestore.Object, wanted []string) []gamestore.Object {
	want := map[string]bool{}
	for _, g := range wanted {
		want[strings.ToLower(g)] = true
	}
	if len(want) == 0 {
		return nil
	}
	// 先挑本体，再按 parent_guid 补齐零件（可能要嵌好几层，循环到没有新增为止）
	var out []gamestore.Object
	taken := map[string]bool{}
	for _, o := range objs {
		if want[strings.ToLower(o.GUID)] {
			out = append(out, o)
			taken[strings.ToLower(o.GUID)] = true
		}
	}
	for {
		added := false
		for _, o := range objs {
			g := strings.ToLower(o.GUID)
			if taken[g] || o.ParentGUID == "" {
				continue
			}
			if taken[strings.ToLower(o.ParentGUID)] {
				out = append(out, o)
				taken[g] = true
				added = true
			}
		}
		if !added {
			break
		}
	}
	return out
}
