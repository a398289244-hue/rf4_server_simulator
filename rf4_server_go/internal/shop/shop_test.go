package shop

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"rf4server/internal/assetpaths"
	"rf4server/internal/gamestore"
	"rf4server/internal/itemdef"
	"rf4server/internal/wire"
)

// fakeShelf 一家店的货架行，单测用它代替 MySQL
type fakeShelf struct {
	rows map[string][]gamestore.ShopRow
}

func (f fakeShelf) ShelfRows(ctx context.Context, store string) ([]gamestore.ShopRow, error) {
	return f.rows[store], nil
}

// table 读协议字段表
func table(t *testing.T) *wire.Table {
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

// defs 用几条真编号造一份定义表（走 itemdef.Load，保证和线上是同一条构造路径）
func defs(t *testing.T, rows ...itemdef.Row) *itemdef.Defs {
	t.Helper()
	d, err := itemdef.Load(context.Background(), func(ctx context.Context) ([]itemdef.Row, error) {
		return rows, nil
	})
	if err != nil {
		t.Fatalf("造定义表失败: %v", err)
	}
	return d
}

// TestShelfFrameMatchesRealServer 是最硬的一条：真服那条 22/1 应答只有 60 字节，
// 里面就一件 papermap_kirzah_01 价 50000。我们按同样的输入拼，必须逐字节一样。
func TestShelfFrameMatchesRealServer(t *testing.T) {
	tt := table(t)
	// 类别 21 是「地图/纸图」那类，有没有 wire 类以字段表为准；这里挑一个一定有类的类别
	real, err := assetpaths.RealAnswer("22/1")
	if err != nil {
		t.Skipf("考卷里没有 22/1，这条不跑: %v", err)
	}
	// 从考卷里读出那件货的编号和价格，别自己编
	v, used, err := tt.DecodeValue(real, 9)
	if err != nil || used != len(real) {
		t.Fatalf("考卷 22/1 解不开: %v", err)
	}
	args, ok := v.(wire.Args)
	if !ok || args.Code != argCodeShop || len(args.Items) != 1 {
		t.Fatalf("考卷 22/1 不是参数表 %q 一件货，实际 %v", argCodeShop, v)
	}
	o := args.Items[0].(*wire.Obj)
	key := o.Fields[0].Value.(wire.StrField).S
	price := o.Fields[1].Value.(wire.Num64).V
	gold := o.Fields[2].Value.(wire.Num64).V
	stock := o.Fields[3].Value.(wire.Num32).V

	// 类别挑这件货在定义表里的真实类别（跑测试时读 MySQL；库没开就跳过）
	my, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没连着，读不到这件货的类别，这条不跑: %v", err)
	}
	defer my.Close()
	rows, err := my.ItemDefRows(context.Background())
	if err != nil {
		t.Fatalf("读 static_item_def 失败: %v", err)
	}
	var want itemdef.Row
	found := false
	for _, r := range rows {
		if r.Key == key {
			want = itemdef.Row{Key: r.Key, NumericID: r.NumericID, Category: r.Category,
				Kind: r.Kind, Brand: r.Brand, AttrsJSON: r.AttrsJSON}
			found = true
			break
		}
	}
	if !found {
		t.Skipf("static_item_def 里没有 %s，这条不跑", key)
	}

	d := defs(t, itemdef.Row{Key: want.Key, NumericID: want.NumericID, Category: want.Category})
	svc := New(tt, d, fakeShelf{rows: map[string][]gamestore.ShopRow{
		"admin_02": {{ItemID: want.Key, Price: int64(price), Gold: int64(gold), Stock: int64(stock)}},
	}})

	cid := uint32(le32(real[5:9]))
	frame, st, err := svc.ShelfFrame(context.Background(), "admin_02", cid)
	if err != nil {
		t.Fatalf("拼货架失败: %v", err)
	}
	if st.Listed != 1 {
		t.Fatalf("该挂上 1 件，实际 %d 件（无定义 %d 无编号 %d 无类 %d）",
			st.Listed, st.NoDef, st.NoID, st.NoClass)
	}
	if got, want2 := hex.EncodeToString(frame), hex.EncodeToString(real); got != want2 {
		t.Fatalf("货架应答和真服不一样：\n 我们 %d 字节 %s\n 真服 %d 字节 %s", len(frame), got, len(real), want2)
	}
}

// TestShelfGatesBadItems 只挡硬错误：没定义、没编号。
// 类别没有专属 wire 类时必须走通用类，不能因此让整家店显示没库存。
func TestShelfGatesBadItems(t *testing.T) {
	tt := table(t)
	d := defs(t,
		itemdef.Row{Key: "good_item", NumericID: 9199, Category: 11},  // 鱼线：有类
		itemdef.Row{Key: "no_id_item", NumericID: 0, Category: 11},    // 没编号
		itemdef.Row{Key: "fallback_item", NumericID: 1, Category: 41}, // 普通商品无专属类，应走 fallback
	)
	svc := New(tt, d, fakeShelf{rows: map[string][]gamestore.ShopRow{
		"store": {
			{ItemID: "good_item", Price: 100, Stock: 7},
			{ItemID: "no_id_item", Price: 100, Stock: 8},
			{ItemID: "fallback_item", Price: 100, Stock: 9},
			{ItemID: "not_in_defs_item", Price: 100, Stock: 10},
		},
	}})
	frame, st, err := svc.ShelfFrame(context.Background(), "store", 1)
	if err != nil {
		t.Fatalf("拼货架失败: %v", err)
	}
	if st.Listed != 2 || st.NoID != 1 || st.NoClass != 0 || st.NoDef != 1 {
		t.Fatalf("闸门数不对：上架 %d 无编号 %d 无类 %d 无定义 %d", st.Listed, st.NoID, st.NoClass, st.NoDef)
	}
	v, used, err := tt.DecodeValue(frame, 9)
	if err != nil || used != len(frame) {
		t.Fatalf("回包解不开: %v", err)
	}
	args, ok := v.(wire.Args)
	if !ok || len(args.Items) != 2 {
		t.Fatalf("回包该有两件可售货，实际 %v", v)
	}
}

func TestShelfAllowsValidBundleButRejectsBrokenBundle(t *testing.T) {
	tt := table(t)
	d := defs(t,
		itemdef.Row{Key: "loaf_01", NumericID: 8861, Category: 26},
		itemdef.Row{Key: "box_ok", NumericID: 2695, Category: 55,
			AttrsJSON: `{"nbkhfhenbag":[{"cemjmlpafmc":"loaf_01","ljnnpokapig":10}]}`},
		itemdef.Row{Key: "box_bad", NumericID: 2696, Category: 55, AttrsJSON: `{}`},
	)
	svc := New(tt, d, fakeShelf{rows: map[string][]gamestore.ShopRow{"food": {
		{ItemID: "box_ok", Price: 401, Stock: 10},
		{ItemID: "box_bad", Price: 401, Stock: 10},
	}}})
	_, st, err := svc.ShelfFrame(context.Background(), "food", 9)
	if err != nil {
		t.Fatal(err)
	}
	if st.Listed != 1 || st.NoClass != 1 {
		t.Fatalf("有效礼包应上架、损坏礼包应拦住: %+v", st)
	}
}

// TestLiveShelvesListFallbackCategories 真实 MySQL 对账：
// 工具店所有有定义/编号的货都必须上架；钓鱼店只允许真正没定义的条目被藏。
func TestLiveShelvesListFallbackCategories(t *testing.T) {
	tt := table(t)
	my, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没起来，跳过实库货架测试: %v", err)
	}
	t.Cleanup(func() { _ = my.Close() })
	d, err := itemdef.Load(context.Background(), itemdef.FromMySQL(my))
	if err != nil {
		t.Fatalf("读 static_item_def 失败: %v", err)
	}
	svc := New(tt, d, my)
	for _, store := range []string{"tool_store_002", "fishing_store_02"} {
		frame, st, err := svc.ShelfFrame(context.Background(), store, 77)
		if err != nil {
			t.Fatalf("%s 货架拼不出来: %v", store, err)
		}
		want := st.Rows - st.NoDef - st.NoID - st.NoClass
		if st.Listed != want {
			t.Fatalf("%s 应有 %d 件可显示，实际 %d（无定义 %d 无编号 %d 无类 %d，回包 %dB）",
				store, want, st.Listed, st.NoDef, st.NoID, st.NoClass, len(frame))
		}
		if st.NoClass != 0 {
			t.Fatalf("%s 还有 %d 件因 wire 类被藏，通用回退没生效", store, st.NoClass)
		}
		if st.Listed == 0 {
			t.Fatalf("%s 可售清单为空", store)
		}
		t.Logf("%s：库 %d 件，上架 %d 件（无定义 %d）", store, st.Rows, st.Listed, st.NoDef)
	}
}

// TestShelfWritesStockAndGold 22/1 的 #02 是金币价、#03 是库存数量；
// 这两个字段写错时，客户端会显示空卡或误判无库存。
func TestShelfWritesStockAndGold(t *testing.T) {
	tt := table(t)
	d := defs(t, itemdef.Row{Key: "tool_item", NumericID: 15348, Category: 41})
	svc := New(tt, d, fakeShelf{rows: map[string][]gamestore.ShopRow{
		"store": {{ItemID: "tool_item", Price: 3800, Gold: 38, Stock: 39004}},
	}})
	frame, _, err := svc.ShelfFrame(context.Background(), "store", 5)
	if err != nil {
		t.Fatalf("拼货架失败: %v", err)
	}
	v, _, err := tt.DecodeValue(frame, 9)
	if err != nil {
		t.Fatalf("货架解不开: %v", err)
	}
	args := v.(wire.Args)
	o := args.Items[0].(*wire.Obj)
	if got := o.Fields[2].Value.(wire.Num64).V; got != 38 {
		t.Fatalf("金币价=%d，应为38", got)
	}
	if got := o.Fields[3].Value.(wire.Num32).V; got != 39004 {
		t.Fatalf("库存=%d，应为39004", got)
	}
}

// TestEmptyStoreStillReplies 库里没这家店也要回一帧合法的空货架（客户端才不会一直转）
func TestEmptyStoreStillReplies(t *testing.T) {
	tt := table(t)
	svc := New(tt, defs(t), fakeShelf{rows: map[string][]gamestore.ShopRow{}})
	frame, st, err := svc.ShelfFrame(context.Background(), "没这家店", 7)
	if err != nil {
		t.Fatalf("空货架也报错了: %v", err)
	}
	if st.Rows != 0 || st.Listed != 0 {
		t.Fatalf("空店就该是 0 件，实际 库里 %d 挂上 %d", st.Rows, st.Listed)
	}
	if _, _, err := tt.DecodeValue(frame, 9); err != nil {
		t.Fatalf("空货架回包解不开: %v", err)
	}
}

// TestRequestNameMatchesRealOffset 请求里那个「店名/容器名」的位置是真服实测的偏移（19 是长度字节）。
// 这边改成从参数表结构里找，位置必须和老口径一模一样，否则商店会读成空名字。
func TestRequestNameMatchesRealOffset(t *testing.T) {
	name := "fishing_store_01"
	plain := requestShell(22, 1, []byte{0x14, byte(len(name))}, []byte(name))
	if got := RequestName(plain); got != name {
		t.Fatalf("店名没读对，读到 %q（整帧 %x）", got, plain)
	}
	// 老口径：长度字节确实在第 19 个
	if plain[19] != byte(len(name)) {
		t.Fatalf("偏移和实测口径不一样：第 19 个字节是 %d，要 %d", plain[19], len(name))
	}
	if got := RequestName(nil); got != "" {
		t.Fatalf("空请求该读到空串，实际 %q", got)
	}
}

// TestRequestPurchaseReadsStoreItemPrice 22/2 请求里是 店名 + 物品名 + 报价，
// 走协议解码整棵树找，不写死偏移（Node 版写死 18，帧形一变就读不到）。
func TestRequestPurchaseReadsStoreItemPrice(t *testing.T) {
	tt := table(t)
	args := []byte{0x03, 0x03}
	args = append(args, []byte("524")...)
	args = append(args, 0x03, 0x00) // 三项：店名、物品名、报价
	store, item := "fishing_store_01", "mono_15063"
	args = append(args, 0x14, byte(len(store)))
	args = append(args, store...)
	args = append(args, 0x14, byte(len(item)))
	args = append(args, item...)
	args = append(args, 0x11)
	args = append(args, le64(450)...)
	plain := append(append(wire.PushHead(0, 22, 2), args...), wire.RequestTrailer)

	got, ok := RequestPurchase(tt, plain)
	if !ok {
		t.Fatalf("购买请求没读出来：%x", plain)
	}
	if got.Store != store || got.Item != item || got.PriceClient != 450 {
		t.Fatalf("读到的不对：%+v", got)
	}
}

// requestShell 拼一条客户端请求壳：01 | marker | cid | main | sub | 参数表 | 尾巴 00
func requestShell(main, sub byte, head []byte, tail []byte) []byte {
	body := []byte{0x03, 0x03}
	body = append(body, []byte("524")...)
	body = append(body, 0x01, 0x00) // 条数 1
	body = append(body, head...)
	body = append(body, tail...)
	body = append(body, wire.RequestTrailer)
	return append(wire.PushHead(0, main, sub), body...)
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func le64(v uint64) []byte {
	out := make([]byte, 8)
	for i := 0; i < 8; i++ {
		out[i] = byte(v >> (8 * i))
	}
	return out
}

var _ = strings.TrimSpace
