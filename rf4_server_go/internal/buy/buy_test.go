package buy

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"rf4server/internal/assetpaths"
	"rf4server/internal/gamestore"
	"rf4server/internal/itemdef"
	"rf4server/internal/itemwire"
	"rf4server/internal/wire"
)

// fakeRun 一份内存版 Runtime：够购买这条路用（读账号、扣钱、插背包行）。
// 有了它，购买逻辑就能脱离 MySQL 单测，改一条规则马上能跑。
type fakeRun struct {
	acct  gamestore.Account
	objs  []gamestore.Object
	errOn string // 让某一步故意失败，验「半截状态怎么处理」
}

func (f *fakeRun) Account(ctx context.Context, username string) (gamestore.Account, error) {
	return f.acct, nil
}

func (f *fakeRun) AddWallet(ctx context.Context, username string, silver, gold int64) (gamestore.Account, error) {
	f.acct.SilverMinor += silver
	f.acct.GoldMinor += gold
	return f.acct, nil
}

func (f *fakeRun) SkillState(ctx context.Context, username string) (gamestore.SkillState, error) {
	return gamestore.SkillState{Cats: map[int]float64{}, Nodes: map[int]int{}}, nil
}
func (f *fakeRun) SetSkillPoints(ctx context.Context, username string, granted int) error { return nil }
func (f *fakeRun) SetMastery(ctx context.Context, username string, cat int, points float64) error {
	return nil
}
func (f *fakeRun) SetSkillNode(ctx context.Context, username string, node, level int) error {
	return nil
}
func (f *fakeRun) Objects(ctx context.Context, username string) ([]gamestore.Object, error) {
	return f.objs, nil
}
func (f *fakeRun) Object(ctx context.Context, guid string) (gamestore.Object, error) {
	for _, o := range f.objs {
		if o.GUID == guid {
			return o, nil
		}
	}
	return gamestore.Object{}, gamestore.ErrNotFound
}
func (f *fakeRun) InsertObject(ctx context.Context, o gamestore.Object) error {
	f.objs = append(f.objs, o)
	return nil
}
func (f *fakeRun) UpdateObjectElem(_ context.Context, username, guid, elem string) (bool, error) {
	for i := range f.objs {
		if f.objs[i].GUID == guid && f.objs[i].Username == username {
			f.objs[i].Elem = elem
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeRun) DeleteObject(ctx context.Context, username, guid string) (bool, error) {
	return true, nil
}
func (f *fakeRun) Shortcuts(ctx context.Context, username string) ([]gamestore.Shortcut, error) {
	return nil, nil
}
func (f *fakeRun) SetShortcut(ctx context.Context, username string, slot int, guid, itemKey string) error {
	return nil
}
func (f *fakeRun) Close() error { return nil }

// fakeLog 购买流水收到几条
type fakeLog struct{ rows []gamestore.Purchase }

func (l *fakeLog) InsertPurchase(ctx context.Context, p gamestore.Purchase) error {
	l.rows = append(l.rows, p)
	return nil
}

// fakePrice 货架表里的价格
type fakePrice struct {
	price, stock int64
	found        bool
}

func (f fakePrice) PriceOf(ctx context.Context, store, key string) (int64, int64, bool, error) {
	return f.price, f.stock, f.found, nil
}

// buyRequest 拼一条 22/2 请求：参数表"524"{ 店名, 物品名, 报价 }
func buyRequest(t *testing.T, tbl *wire.Table, store, item string, price uint64) []byte {
	t.Helper()
	args := []byte{0x03, 0x03}
	args = append(args, []byte("524")...)
	args = append(args, 0x03, 0x00)
	args = append(args, 0x14, byte(len(store)))
	args = append(args, store...)
	args = append(args, 0x14, byte(len(item)))
	args = append(args, item...)
	args = append(args, 0x11)
	for i := 0; i < 8; i++ {
		args = append(args, byte(price>>(8*i)))
	}
	return append(append(wire.PushHead(0, 22, 2), args...), wire.RequestTrailer)
}

func loadTable(t *testing.T) *wire.Table {
	t.Helper()
	p, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatalf("找不到凭据目录: %v", err)
	}
	tt, err := wire.LoadTable(p)
	if err != nil {
		t.Fatalf("读协议字段表失败: %v", err)
	}
	return tt
}

// defFor 从 MySQL 拿一件货的真定义（库没开就跳过这条测试）
func defFor(t *testing.T, key string) *itemdef.Defs {
	return defsFor(t, key)
}

func defsFor(t *testing.T, keys ...string) *itemdef.Defs {
	t.Helper()
	my, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没连着，读不到 %v 的定义: %v", keys, err)
	}
	defer my.Close()
	rows, err := my.ItemDefRows(context.Background())
	if err != nil {
		t.Fatalf("读 static_item_def 失败: %v", err)
	}
	var keep []itemdef.Row
	want := map[string]bool{}
	for _, key := range keys {
		want[key] = true
	}
	for _, r := range rows {
		if want[r.Key] {
			keep = append(keep, itemdef.Row{Key: r.Key, NumericID: r.NumericID, Category: r.Category,
				Kind: r.Kind, Brand: r.Brand, AttrsJSON: r.AttrsJSON})
		}
	}
	if len(keep) != len(want) {
		t.Skipf("static_item_def 里没找齐 %v（找到 %d/%d）", keys, len(keep), len(want))
	}
	d, err := itemdef.Load(context.Background(), func(ctx context.Context) ([]itemdef.Row, error) {
		return keep, nil
	})
	if err != nil {
		t.Fatalf("造定义表失败: %v", err)
	}
	return d
}

// TestBuyReplyMatchesRealServer 拿真服那条购买应答当标准答案：
// 输入照它给（店名、买家 id、余额、价格、买家名），输出必须逐字节一样（GUID 抹零后比）。
func TestBuyReplyMatchesRealServer(t *testing.T) {
	tbl := loadTable(t)
	const key = "mono_15063"
	defs := defFor(t, key)

	tplHex := buyTemplate(t, key)
	frame, err := hex.DecodeString(tplHex)
	if err != nil {
		t.Fatalf("考卷不是十六进制: %v", err)
	}
	real := decodeTop(t, tbl, frame)
	// 从考卷里读输入，别自己编：店名/买家/余额/价格都在记录里的凭证那一格
	store, userID, balance, price, buyer := receiptOf(t, real)

	// 买家名字也用考卷里那个：凭证最后一格写的就是它，字节要比得上
	run := &fakeRun{acct: gamestore.Account{
		Username: buyer, Nickname: buyer, UserID: userID,
		SilverMinor: balance + price, // 扣之前 = 扣之后 + 价
	}}
	log := &fakeLog{}
	svc := New(tbl, defs, itemwire.New(tbl, defs, itemwire.Options{}), run,
		fakePrice{price: price, stock: 1, found: true}, log)

	got, res, err := svc.BuyFrame(context.Background(), buyer, le32of(frame[5:9]), buyRequest(t, tbl, store, key, uint64(price)))
	if err != nil {
		t.Fatalf("购买出错: %v", err)
	}
	if !res.OK {
		t.Fatalf("这单该买成，实际没成：%s", res.Reason)
	}
	mine := decodeTop(t, tbl, got)
	zeroGUIDs(mine)
	zeroGUIDs(real)
	if enc(mine) != enc(real) {
		t.Fatalf("购买回执和真服不一样：\n 我们 %s\n 真服 %s", enc(mine), enc(real))
	}
	if len(run.objs) != 1 || run.objs[0].Elem == "" || run.objs[0].ItemID == 0 {
		t.Fatalf("背包那行不对：%+v", run.objs)
	}
	if run.acct.SilverMinor != balance {
		t.Fatalf("扣完余额该是 %d，实际 %d", balance, run.acct.SilverMinor)
	}
	if len(log.rows) != 1 || log.rows[0].GUID != res.GUID {
		t.Fatalf("购买流水没对上：%+v", log.rows)
	}
}

// TestBuyRejectsWhenShortOnSilver 钱不够：不扣钱、不发货、照样回帧，原因要说清
func TestBuyRejectsWhenShortOnSilver(t *testing.T) {
	tbl := loadTable(t)
	const key = "mono_15063"
	defs := defFor(t, key)
	run := &fakeRun{acct: gamestore.Account{Username: "return", UserID: 7, SilverMinor: 100}}
	svc := New(tbl, defs, itemwire.New(tbl, defs, itemwire.Options{}), run,
		fakePrice{price: 450, found: true}, &fakeLog{})

	frame, res, err := svc.BuyFrame(context.Background(), "return", 1, buyRequest(t, tbl, "fishing_store_01", key, 450))
	if err != nil {
		t.Fatalf("买不成也该有回帧: %v", err)
	}
	if res.OK || !strings.Contains(res.Reason, "银币不够") {
		t.Fatalf("该拒成「银币不够」，实际 OK=%v 原因=%s", res.OK, res.Reason)
	}
	if run.acct.SilverMinor != 100 {
		t.Fatalf("钱不够还扣钱：%d", run.acct.SilverMinor)
	}
	if len(run.objs) != 0 {
		t.Fatalf("没买成不该发货，发了 %d 件", len(run.objs))
	}
	if _, _, err := tbl.DecodeValue(frame, 9); err != nil {
		t.Fatalf("拒绝也得回一帧能解开的: %v", err)
	}
}

// TestBuyRejectsUnknownItem 库里没这件货的定义就干脆不卖（卖了客户端也认不出）
func TestBuyRejectsUnknownItem(t *testing.T) {
	tbl := loadTable(t)
	defs := defFor(t, "mono_15063")
	run := &fakeRun{acct: gamestore.Account{Username: "return", SilverMinor: 1 << 20}}
	svc := New(tbl, defs, itemwire.New(tbl, defs, itemwire.Options{}), run,
		fakePrice{price: 10, found: true}, &fakeLog{})

	_, res, err := svc.BuyFrame(context.Background(), "return", 2, buyRequest(t, tbl, "fishing_store_01", "这件货不存在", 10))
	if err != nil {
		t.Fatalf("出错: %v", err)
	}
	if res.OK || !strings.Contains(res.Reason, "没有这件货的静态定义") {
		t.Fatalf("该拒成「没定义」，实际 OK=%v 原因=%s", res.OK, res.Reason)
	}
	if len(run.objs) != 0 {
		t.Fatalf("没定义还发货：%+v", run.objs)
	}
}

// buyTemplate 读真服那条购买应答（考卷）
func buyTemplate(t *testing.T, key string) string {
	t.Helper()
	p, err := assetpaths.File("buy_templates.json")
	if err != nil {
		t.Skipf("没有 buy_templates.json 这份考卷: %v", err)
	}
	raw, err := readFile(p)
	if err != nil {
		t.Fatalf("读考卷失败: %v", err)
	}
	var m map[string]string
	if err := jsonUnmarshal(raw, &m); err != nil {
		t.Fatalf("考卷解析失败: %v", err)
	}
	v := m[key]
	if v == "" {
		t.Skipf("考卷里没有 %s", key)
	}
	return strings.TrimSpace(v)
}

// decodeTop 把应答帧解成顶层对象（应答头 9 字节）
func decodeTop(t *testing.T, tbl *wire.Table, frame []byte) *wire.Obj {
	t.Helper()
	v, used, err := tbl.DecodeValue(frame, 9)
	if err != nil {
		t.Fatalf("应答解不开: %v", err)
	}
	if used != len(frame) {
		t.Fatalf("只解到 %d 字节，整帧 %d 字节", used, len(frame))
	}
	o, ok := v.(*wire.Obj)
	if !ok {
		t.Fatalf("顶层不是对象，是 %T", v)
	}
	return o
}

// receiptOf 从真服那条记录里读出购买凭证（店名/买家 id/余额/价格/买家名）
func receiptOf(t *testing.T, top *wire.Obj) (store string, userID, balance, price int64, buyer string) {
	t.Helper()
	args := top.ArgsOf(itemwire.P804Objects)
	if args == nil || len(args.Items) != 1 {
		t.Fatalf("考卷里没有物件记录那一项")
	}
	rec := args.Items[0].(*wire.Obj)
	idx := -1
	for i, f := range rec.Fields {
		if f.Name == "effnpjlfjai" { // 凭证那一格
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("考卷记录里没有凭证那一格")
	}
	rc, ok := rec.Fields[idx].Value.(*wire.Obj)
	if !ok {
		t.Fatalf("凭证不是对象：%T", rec.Fields[idx].Value)
	}
	for _, f := range rc.Fields {
		switch f.Name {
		case "pgdemfofgdm":
			if s, ok := f.Value.(wire.StrField); ok {
				store = s.S
			}
		case "jfflfjglkhc":
			if n, ok := f.Value.(wire.Num32); ok {
				userID = int64(n.V)
			}
		case "oegjbfmmeoe":
			if n, ok := f.Value.(wire.Num32); ok {
				balance = int64(n.V)
			}
		case "phdhnkpngpc":
			if n, ok := f.Value.(wire.Num64); ok {
				price = int64(n.V)
			}
		case "dhaenhabecc":
			if s, ok := f.Value.(wire.StrField); ok {
				buyer = s.S
			}
		}
	}
	if store == "" || buyer == "" {
		t.Fatalf("凭证读残了：%q %q", store, buyer)
	}
	return
}

// zeroGUIDs 把树里所有 GUID 抹零：两边的 GUID 都是各自生成的，比字节时得豁免
func zeroGUIDs(v wire.Value) {
	switch x := v.(type) {
	case *wire.Obj:
		for i := range x.Fields {
			switch fv := x.Fields[i].Value.(type) {
			case wire.GUID:
				x.Fields[i].Value = wire.GUID{Hex: "00000000000000000000000000000000"}
			case wire.OptGUIDField:
				if fv.Present {
					x.Fields[i].Value = wire.OptGUIDField{Present: true, Hex: "00000000000000000000000000000000"}
				}
			default:
				zeroGUIDs(fv)
			}
		}
	case wire.Args:
		for i := range x.Items {
			switch iv := x.Items[i].(type) {
			case wire.GUID:
				x.Items[i] = wire.GUID{Hex: "00000000000000000000000000000000"}
			default:
				zeroGUIDs(iv)
			}
		}
	}
}

func enc(o *wire.Obj) string {
	b, err := o.Encode()
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func le32of(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func TestBuyStacksCountableItem(t *testing.T) {
	tbl := loadTable(t)
	defs := defFor(t, "worm_01")
	run := &fakeRun{acct: gamestore.Account{Username: "return", UserID: 7, SilverMinor: 1 << 20}}
	log := &fakeLog{}
	iw := itemwire.New(tbl, defs, itemwire.Options{})
	svc := New(tbl, defs, iw, run, fakePrice{price: 180, stock: 10, found: true}, log)
	req := buyRequest(t, tbl, "fishing_store_02", "worm_01", 180)

	_, first, err := svc.BuyFrame(context.Background(), "return", 41, req)
	if err != nil || !first.OK {
		t.Fatalf("第一次购买失败: res=%+v err=%v", first, err)
	}
	_, second, err := svc.BuyFrame(context.Background(), "return", 42, req)
	if err != nil || !second.OK {
		t.Fatalf("第二次购买失败: res=%+v err=%v", second, err)
	}
	if len(run.objs) != 1 {
		t.Fatalf("可堆叠饵应只有一行，实际 %d 行", len(run.objs))
	}
	if first.GUID == "" || first.GUID != second.GUID {
		t.Fatalf("两次购买应复用同一 GUID: first=%s second=%s", first.GUID, second.GUID)
	}
	raw, err := hex.DecodeString(run.objs[0].Elem)
	if err != nil {
		t.Fatal(err)
	}
	qty, err := iw.Quantity(raw)
	if err != nil || qty != 60 {
		t.Fatalf("两包蚯蚓应为 60，实际 %d err=%v", qty, err)
	}
}

func TestBuyNonCountableItemStillEntersBackpack(t *testing.T) {
	tbl := loadTable(t)
	defs := defFor(t, "tool_scoop")
	run := &fakeRun{acct: gamestore.Account{Username: "return", UserID: 7, SilverMinor: 1 << 20}}
	svc := New(tbl, defs, itemwire.New(tbl, defs, itemwire.Options{}), run, fakePrice{price: 289, stock: 10, found: true}, &fakeLog{})
	_, res, err := svc.BuyFrame(context.Background(), "return", 43, buyRequest(t, tbl, "tool_store_002", "tool_scoop", 289))
	if err != nil || !res.OK {
		t.Fatalf("工具购买应成功: res=%+v err=%v", res, err)
	}
	if len(run.objs) != 1 || run.objs[0].ItemKey != "tool_scoop" {
		t.Fatalf("工具应直接入包: %+v", run.objs)
	}
}

func TestBuyBundleUnboxesIntoRealItem(t *testing.T) {
	tbl := loadTable(t)
	defs := defsFor(t, "box_loaf_01_10", "loaf_01")
	run := &fakeRun{acct: gamestore.Account{Username: "return", UserID: 7, SilverMinor: 1 << 20}}
	svc := New(tbl, defs, itemwire.New(tbl, defs, itemwire.Options{}), run,
		fakePrice{price: 401, stock: 10, found: true}, &fakeLog{})
	frame, res, err := svc.BuyFrame(context.Background(), "return", 44,
		buyRequest(t, tbl, "food_store_02", "box_loaf_01_10", 401))
	if err != nil || !res.OK {
		t.Fatalf("食品礼包购买应成功: res=%+v err=%v", res, err)
	}
	if len(run.objs) != 1 || run.objs[0].ItemKey != "loaf_01" {
		t.Fatalf("礼包外壳不该入包，应直接得到 loaf_01: %+v", run.objs)
	}
	if res.Delivered != 1 || res.SentID != 8861 {
		t.Fatalf("购买结果应报告一条真实食品记录: %+v", res)
	}
	top := decodeTop(t, tbl, frame)
	items := top.ArgsOf(itemwire.P804Objects)
	if items == nil || len(items.Items) != 1 {
		t.Fatalf("购买回包应只带真实食品记录: %#v", items)
	}
	rec, ok := items.Items[0].(*wire.Obj)
	if !ok || rec.TypeID == 163 {
		t.Fatalf("食品礼包不能再回通用 type163 外壳: %T %#v", items.Items[0], items.Items[0])
	}
}
