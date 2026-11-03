package gamesess

// 这条测试走的是「钓鱼整条链路」：抛竿 -> 服务器排咬钩 -> 推 14/14 -> 客户端回 14/5 -> 入库。
// 单测（internal/fishing）已经逐字节对过真服答案，这边盯的是**接线**：
// 路由挂上了没、定时器能不能把推进来、keep 里的鱼 GUID 能不能对上我们推出去那条、
// 鱼护和流水有没有真写进去。少爷实机测之前先由这条兜住，免得又是「日志说发了但界面没动静」。

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"rf4server/internal/entry"
	"rf4server/internal/fishing"
	"rf4server/internal/gamestore"
	"rf4server/internal/store"
	"rf4server/internal/wire"
)

// fishStore 钓鱼数据入口的内存版：一个鱼种、一份名册，写库就是往切片里记一笔。
type fishStore struct {
	keep  []gamestore.KeepnetRow
	recs  []gamestore.FishRecord
	objs  []gamestore.Object
	calls int
}

func (f *fishStore) SpeciesWeights(ctx context.Context) (map[string]gamestore.SpeciesWeight, error) {
	return map[string]gamestore.SpeciesWeight{
		"ruffe": {FishKey: "ruffe", MinG: 20, MaxG: 300, TrophyLineG: 140, SuperTrophyLineG: 220,
			LengthK: 0.0355, LengthP: 1.0 / 3.0, HasRange: true},
	}, nil
}
func (f *fishStore) Roster(ctx context.Context) (map[string][]string, error) {
	return map[string][]string{"level_000_home": {"ruffe"}}, nil
}
func (f *fishStore) SaveKeepnet(ctx context.Context, u string, row gamestore.KeepnetRow) error {
	f.calls++
	f.keep = append(f.keep, row)
	return nil
}
func (f *fishStore) Keepnet(ctx context.Context, u string) ([]gamestore.KeepnetRow, error) {
	return append([]gamestore.KeepnetRow(nil), f.keep...), nil
}
func (f *fishStore) InsertFishRecord(ctx context.Context, r gamestore.FishRecord) error {
	f.recs = append(f.recs, r)
	return nil
}
func (f *fishStore) Objects(ctx context.Context, u string) ([]gamestore.Object, error) {
	return f.objs, nil
}
func (f *fishStore) Account(ctx context.Context, u string) (gamestore.Account, error) {
	return gamestore.Account{Username: u, UserID: 1000002, Nickname: u}, nil
}

// askArgs 发一条带内容的请求（正文自己拼，RC4 照走）
func (p *helloProbeClient) askArgs(main, sub byte, cid uint32, items []wire.Value) {
	p.t.Helper()
	a, err := wire.Args{Code: "524", Items: items}.Encode()
	if err != nil {
		p.t.Fatalf("拼 %d/%d 请求参数表失败: %v", main, sub, err)
	}
	p.wid++
	frame := append(append(wire.PushHead(cid, main, sub), a...), wire.RequestTrailer)
	if _, err := p.conn.Write(wire.BuildFrame(0, p.wid, p.encrypt.Crypt(frame))); err != nil {
		p.t.Fatalf("发 %d/%d 失败: %v", main, sub, err)
	}
}

// nextFrame 读下一条有正文的帧，返回明文 + 命令号（推送和应答都认）
func (p *helloProbeClient) nextFrame(what string) (plain []byte, main, sub byte) {
	p.t.Helper()
	for i := 0; i < 30; i++ {
		f := p.nextData(what)
		if len(f) < 11 {
			continue
		}
		return f, f[9], f[10]
	}
	p.t.Fatalf("等 %s 等了 30 帧还没等到", what)
	return nil, 0, 0
}

// decodeAt 从指定位置解一个值。
// 应答帧是 9 字节头 + 物件本体（01 | typeID | 各字段），所以从 entry.HeadLen 起步；
// 推送帧是 11 字节头（里面才有 main/sub）+ 参数表，所以从 11 起步。
// 别记成「应答也带 main/sub」：真服那 54 字节的 14/1 应答 = 9 + obj158，一字节不多。
func decodeAt(t *testing.T, tbl *wire.Table, plain []byte, off int, what string) wire.Value {
	t.Helper()
	v, _, err := tbl.DecodeValue(plain, off)
	if err != nil {
		t.Fatalf("解 %s 失败: %v", what, err)
	}
	return v
}

// asObj 解出来的值不管是 Obj 还是 *Obj，统一取出来
func asObj(t *testing.T, v wire.Value, what string) *wire.Obj {
	t.Helper()
	switch o := v.(type) {
	case *wire.Obj:
		return o
	}
	t.Fatalf("%s 解出来不是物件，是 %T", what, v)
	return nil
}

// TestFishingFlowOverSocket 抛竿 -> 咬钩推送 -> 留鱼 -> 入库，一条龙
func TestFishingFlowOverSocket(t *testing.T) {
	deps, db := testDeps(t)
	tbl := deps.Table

	fs := &fishStore{objs: []gamestore.Object{
		{GUID: gearGUIDTest, Username: "return2", ItemKey: "spin_10032_S78MH", Elem: "010203"},
		{GUID: "aabbccddeeff00112233445566778899", Username: "return2", ParentGUID: gearGUIDTest, ItemKey: "mono_15063", Elem: "040506"},
	}}
	cat := fishing.NewCatalog(fs)
	if n, l, err := cat.Reload(context.Background()); err != nil {
		t.Fatalf("装鱼种表失败: %v", err)
	} else if n != 1 || l != 1 {
		t.Fatalf("鱼种表读到的数不对: %d 种 %d 片水域", n, l)
	}
	deps.Fishing = fishing.New(tbl, cat, fs)

	const token = "0123456789abcdef0123456789abcdef"
	db.Tokens().Save(store.Token{Token: token, Username: "return2", UserID: 1000002, Expires: time.Now().Unix() + 3600})

	srv := New(deps, Config{
		Addr:      "127.0.0.1:0",
		StatePush: -1, // 关掉挂机补推，帧流里就只有钓鱼这几条，断言才干净
		AutoBite:  true,
		BiteMin:   20 * time.Millisecond,
		BiteMax:   40 * time.Millisecond,
		FishLoc:   "level_000_home",
		FishBait:  "worm_01",
	})
	if err := srv.Listen(); err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() { _ = srv.Close() })

	p := dialAndHandshake(t, srv, token)
	// 服务器先开口的两条（0/1、2/1）：缺了客户端就不会问后面的东西
	if _, m, s := p.nextFrame("进场推送"); m != 0 || s != 1 {
		t.Fatalf("第一条推送应该是 0/1，结果是 %d/%d", m, s)
	}
	if _, m, s := p.nextFrame("玩家状态推送"); m != 2 || s != 1 {
		t.Fatalf("第二条推送应该是 2/1，结果是 %d/%d", m, s)
	}

	// 14/1 抛竿准备：请求里带钓具 GUID，应答应该是 obj158 且把 GUID 原样带回
	p.askArgs(14, 1, 1, []wire.Value{wire.GUID{Hex: gearGUIDTest}})
	ans, _, _ := p.nextFrame("14/1 应答")
	// 注意：应答帧里没有命令号（9 字节头 = 01 | marker(-2) | call_id，后面直接是物件），
	// 客户端是靠 call_id 对号的。所以这儿对的是 call_id 和物件类型号，不对 main/sub。
	if marker(ans) != -2 {
		t.Fatalf("14/1 回的应该是应答帧（marker=-2），实际 %d", marker(ans))
	}
	if cid := callIDOf(ans); cid != 1 {
		t.Fatalf("14/1 应答里的 call_id 不对: %d，要 1", cid)
	}
	o158 := asObj(t, decodeAt(t, tbl, ans, entry.HeadLen, "14/1 应答帧体"), "14/1 应答")
	if o158.TypeID != 158 {
		t.Fatalf("14/1 应该回 obj158，结果 typeID %d（类名 %s）", o158.TypeID, o158.Class)
	}
	if got := guidText(o158.Fields[0].Value); got != gearGUIDTest {
		t.Fatalf("14/1 应答里的钓具 GUID 不对: %q，要 %q", got, gearGUIDTest)
	}

	// 咬钩推送：定时器在那边，得等一小会儿。中间可能夹着别的推送，所以按命令号挑。
	var bite *wire.Obj
	var fishGUID string
	deadline := time.Now().Add(4 * time.Second)
	for fishGUID == "" && time.Now().Before(deadline) {
		plain, pm, ps := p.nextFrame("14/14 咬钩推送")
		if pm != 14 || ps != 14 {
			continue // 2/1 之类补推，忽略
		}
		items := wire.ArgsItems(decodeAt(t, tbl, plain, 11, "14/14 推送正文"))
		if len(items) != 2 {
			t.Fatalf("14/14 正文应该是「钓具 GUID + 鱼事实」两项，实际 %d 项", len(items))
		}
		if got := guidText(items[0]); got != gearGUIDTest {
			t.Fatalf("14/14 里那个钓具 GUID 不对: %q，要 %q", got, gearGUIDTest)
		}
		bite = asObj(t, items[1], "14/14 里的鱼事实")
		if bite.TypeID != 27 {
			t.Fatalf("14/14 应该是 obj27，结果 typeID %d", bite.TypeID)
		}
		if got := fieldText(bite.Fields[1].Value); got != "ruffe" {
			t.Fatalf("出鱼没按名册来: %q", got)
		}
		grams := bite.Fields[4].Value
		if n, ok := grams.(wire.Num32); !ok || n.V < 20 || n.V > 300 {
			t.Fatalf("体重 %v 不在库里给的 [20,300] 里", grams)
		}
		fishGUID = guidText(bite.Fields[0].Value)
	}
	if fishGUID == "" {
		t.Fatal("等 4 秒也没收到 14/14 咬钩推送（定时器没起？路由没挂？）")
	}

	// 14/5 留鱼：把刚推出去那条鱼的 GUID 原样回显
	p.askArgs(14, 5, 2, []wire.Value{wire.GUID{Hex: gearGUIDTest}, wire.GUID{Hex: fishGUID}})
	keep, _, _ := p.nextFrame("14/5 应答")
	if cid := callIDOf(keep); cid != 2 {
		t.Fatalf("14/5 应答里的 call_id 不对: %d，要 2", cid)
	}
	if marker(keep) != -2 {
		t.Fatalf("14/5 回的应该是应答帧，实际 marker=%d", marker(keep))
	}
	snap := asObj(t, decodeAt(t, tbl, keep, entry.HeadLen, "14/5 应答帧体"), "14/5 应答")
	if snap.TypeID != 800 {
		t.Fatalf("14/5 应该回 obj800 快照，结果 typeID %d", snap.TypeID)
	}
	cards := wire.ArgsItems(snap.Fields[0].Value)
	if len(cards) != 1 {
		t.Fatalf("14/5 应答里应该带一条鱼记录，实际 %d 条", len(cards))
	}
	card := asObj(t, cards[0], "14/5 里的鱼记录")
	if card.TypeID != 25 {
		t.Fatalf("鱼记录应该是 obj25，结果 typeID %d", card.TypeID)
	}
	if got := guidText(card.Fields[16].Value); got != fishGUID {
		t.Fatalf("鱼记录里那个咬钩 GUID 和推出去的串不上: %q vs %q", got, fishGUID)
	}
	if got := fieldText(card.Fields[8].Value); got != "worm_01" {
		t.Fatalf("鱼记录里饵那格不对: %q", got)
	}

	// 库里两笔都得有（这条是最容易漏的：应答发了、库没写，重启就「鱼没了」）
	if len(fs.keep) != 1 {
		t.Fatalf("鱼护没写进去（keepnets 表那笔没有）：%+v", fs.keep)
	}
	if fs.keep[0].Key != "ruffe" || fs.keep[0].Grams <= 0 {
		t.Fatalf("鱼护内容不对: %+v", fs.keep[0])
	}
	if len(fs.recs) != 1 || fs.recs[0].UserID != 1000002 || fs.recs[0].GUID == "" {
		t.Fatalf("渔获流水不对: %+v", fs.recs)
	}

	// 4/22「装备更新了」那条补推：真服留完鱼就推，我们推自己名下和这根竿有关的记录
	plain, pm, ps := p.nextFrame("4/22 装备更新推送")
	if pm != 4 || ps != 22 {
		t.Fatalf("14/5 之后应该补一条 4/22，结果 %d/%d", pm, ps)
	}
	if len(plain) < 12 {
		t.Fatalf("4/22 帧太短: %d 字节", len(plain))
	}
}

// guidText 把 GUID 字段值变成 32 位十六进制（fieldText 只管数字和串，GUID 得单独认）
func guidText(v wire.Value) string {
	switch g := v.(type) {
	case wire.GUID:
		return g.Hex
	case *wire.GUID:
		return g.Hex
	case wire.OptGUIDField:
		if !g.Present {
			return ""
		}
		return g.Hex
	}
	return fieldText(v)
}

// callIDOf 应答帧头里那 4 字节 call_id（第 6~9 字节）
func callIDOf(plain []byte) uint32 {
	return binary.LittleEndian.Uint32(plain[5:9])
}

// gearGUIDTest 测试里当钓具用的那个 GUID（真服那次会话里那位用的就是它，见 req_fishing.txt）
const gearGUIDTest = "f1289f009f09364395075c15c8655ed5"
