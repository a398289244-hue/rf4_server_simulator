package itemops

// 删除这几条测试跑在一个临时账号 zz_itemops_test 身上，不动少爷那个 return2：
// 插一件测试物件 -> 删 -> 看库里没了、回包对不对 -> 收尾删干净。
// 这样 `go test ./...` 里各个包并行跑也不会互相踩（之前跟背包那条测试就抢过同一行数据）。
//
//	1) 删自己名下的一件：GUID 要从 objects 表掉出去，回包是 800 快照，
//	   #00 = 删完还剩的整份清单（界面才会实时刷新）、#02 = 这次删掉的 GUID；
//	2) 删一个名下一个都没有的 GUID：一件都不能删，原因要说清；
//	3) 删竿子：挂在它下面的零件要一起掉，不然又变「影子物品」。

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"
	"time"

	"rf4server/internal/assetpaths"
	"rf4server/internal/gamestore"
	"rf4server/internal/wire"
)

// testUser 这条测试专属的临时账号，跑完东西清干净，不污染少爷的数据
const testUser = "zz_itemops_test"

// donorUser 借一条真实物件记录的账号。测试物件的记录字节必须是客户端/解码器认得的真记录，
// 自己瞎编一段字节 obj307 都解不开，800 回包就没法验（之前栽过一次）。
const donorUser = "return2"

// 临时物件的 GUID：必须是纯十六进制 —— 帧里 GUID 字段就是按 hex 编解码的，
// 多一个字母当场报错（之前写成 zz000...，z 不是十六进制，请求压根拼不出来）。
const (
	testGUIDRod   = "7a00000000000000000000000000000a" // 当作「竿」
	testGUIDChild = "7a00000000000000000000000000000b" // 当作挂在竿上的「零件」
)

func loadTable(t *testing.T) *wire.Table {
	t.Helper()
	path, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatalf("找不到凭据目录：%v", err)
	}
	tbl, err := wire.LoadTable(path)
	if err != nil {
		t.Fatalf("读字段表失败：%v", err)
	}
	return tbl
}

// seed 连上 MySQL、把临时账号清干净并插一件测试物件，返回字段表 + 数据入口 + 那件物件
func seed(t *testing.T) (*wire.Table, gamestore.Runtime, gamestore.Object) {
	t.Helper()
	my, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没连着，这条不跑：%v", err)
	}
	t.Cleanup(func() { _ = my.Close() })
	tbl := loadTable(t)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// 借一条真记录（只读 return2，不写它）
	donors, err := my.Objects(ctx, donorUser)
	if err != nil {
		t.Fatalf("读借记录的账号 %s 失败：%v", donorUser, err)
	}
	if len(donors) == 0 || donors[0].Elem == "" {
		t.Skipf("%s 名下没有带记录的货，借不到物件记录，这条不跑", donorUser)
	}
	elem := donors[0].Elem

	// 先把这个临时账号清干净（上一次跑挂了可能留了行）
	objs, err := my.Objects(ctx, testUser)
	if err != nil {
		t.Fatalf("读临时账号失败：%v", err)
	}
	for _, o := range objs {
		if _, err := my.DeleteObject(ctx, testUser, o.GUID); err != nil {
			t.Fatalf("清临时账号失败：%v", err)
		}
	}
	row := gamestore.Object{
		GUID:     testGUIDRod,
		Username: testUser,
		ItemKey:  donors[0].ItemKey,
		ItemID:   donors[0].ItemID,
		How:      "Go 测试自动插的",
		Source:   "go-test",
		Elem:     elem,
	}
	if err := my.InsertObject(ctx, row); err != nil {
		t.Fatalf("插测试物件失败：%v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_, _ = my.DeleteObject(c, testUser, row.GUID)
	})
	return tbl, my, row
}

// deleteRequest 拼一条 4/7 请求：11 字节请求头 + 参数表"524"{ GUID... } + 尾巴 00
func deleteRequest(t *testing.T, tbl *wire.Table, guids ...string) []byte {
	t.Helper()
	items := make([]wire.Value, 0, len(guids))
	for _, g := range guids {
		items = append(items, wire.GUID{Hex: g})
	}
	args, err := wire.Args{Code: "524", Items: items}.Encode()
	if err != nil {
		t.Fatalf("拼请求失败：%v", err)
	}
	return append(append(wire.PushHead(0, 4, 7), args...), wire.RequestTrailer)
}

func TestDeleteRemovesFromMySQLAndRepliesFull800(t *testing.T) {
	tbl, my, row := seed(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	svc := New(tbl, my)
	frame, res, err := svc.DeleteFrame(ctx, testUser, 7, deleteRequest(t, tbl, row.GUID))
	if err != nil {
		t.Fatalf("删除出错：%v", err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != row.GUID {
		t.Fatalf("该删掉那 1 件 %s，实际删了 %v", row.GUID, res.Deleted)
	}

	after, err := my.Objects(ctx, testUser)
	if err != nil {
		t.Fatalf("再读一遍失败：%v", err)
	}
	for _, o := range after {
		if o.GUID == row.GUID {
			t.Fatalf("库里还留着这件：%s", row.GUID)
		}
	}
	if res.Left+len(res.Deferred) != len(after) {
		t.Fatalf("回包说还剩 %d 件（另有 %d 件容器暂不发），库里实际 %d 件", res.Left, len(res.Deferred), len(after))
	}

	// 回包必须是 obj800：#00 = 剩下整份清单、#02 = 删掉的那些 GUID
	v, _, err := tbl.DecodeValue(frame, 9)
	if err != nil {
		t.Fatalf("回包解不开：%v", err)
	}
	o, ok := v.(*wire.Obj)
	if !ok || o.TypeID != 800 {
		t.Fatalf("回包不是 obj800，是 %T", v)
	}
	list, ok := o.Fields[0].Value.(wire.Args)
	if !ok || list.Code != "61" || len(list.Items) != res.Left {
		t.Fatalf("800 的 #00 该是参数表 61 且 %d 件：%T 实际 %v", res.Left, o.Fields[0].Value, len(list.Items))
	}
	guids, ok := o.Fields[2].Value.(wire.GUIDArray)
	if !ok || len(guids.Items) != 1 {
		t.Fatalf("800 的 #02 该带 1 个被删 GUID：%T", o.Fields[2].Value)
	}
	raw, err := hex.DecodeString(row.GUID)
	if err != nil {
		t.Fatalf("测试 GUID 不是十六进制：%v", err)
	}
	if !bytes.Equal(guids.Items[0], raw) {
		t.Fatalf("800 里写的 GUID 和删掉那件对不上：%x vs %x", guids.Items[0], raw)
	}
}

func TestDeleteForeignGuidChangesNothing(t *testing.T) {
	tbl, my, row := seed(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	svc := New(tbl, my)
	foreign := "aabbccddeeff00112233445566778899"
	frame, res, err := svc.DeleteFrame(ctx, testUser, 8, deleteRequest(t, tbl, foreign))
	if err != nil {
		t.Fatalf("删除出错：%v", err)
	}
	if len(res.Deleted) != 0 {
		t.Fatalf("名下一个都没有的 GUID 竟然被删了：%v", res.Deleted)
	}
	if res.Reason == "" {
		t.Fatalf("一件都没删却没写原因，日志上看不出为什么")
	}
	after, err := my.Objects(ctx, testUser)
	if err != nil {
		t.Fatalf("再读一遍失败：%v", err)
	}
	if len(after) != 1 || after[0].GUID != row.GUID {
		t.Fatalf("库里那件不该被动过，现在剩 %d 件", len(after))
	}
	if _, _, err := tbl.DecodeValue(frame, 9); err != nil {
		t.Fatalf("没删成也得回一帧能解开的 800：%v", err)
	}
}

// TestDeleteTakesChildrenToo 删竿子的时候挂在线上的零件要一起掉，
// 不然库里没了、界面上还杵着（之前那种「影子物件」就是这么来的）。
func TestDeleteTakesChildrenToo(t *testing.T) {
	tbl, my, rod := seed(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	child := rod
	child.GUID = testGUIDChild
	child.ItemKey = "go_test_line"
	child.ParentGUID = rod.GUID
	if err := my.InsertObject(ctx, child); err != nil {
		t.Fatalf("插零件失败：%v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_, _ = my.DeleteObject(c, testUser, child.GUID)
	})

	svc := New(tbl, my)
	frame, res, err := svc.DeleteFrame(ctx, testUser, 9, deleteRequest(t, tbl, rod.GUID))
	if err != nil {
		t.Fatalf("删除出错：%v", err)
	}
	if len(res.Deleted) != 2 {
		t.Fatalf("删竿子该连零件一起删（2 件），实际 %v", res.Deleted)
	}
	after, err := my.Objects(ctx, testUser)
	if err != nil {
		t.Fatalf("再读一遍失败：%v", err)
	}
	if len(after) != 0 {
		t.Fatalf("竿和零件都该没了，还剩 %d 件", len(after))
	}
	if _, _, err := tbl.DecodeValue(frame, 9); err != nil {
		t.Fatalf("回包解不开：%v", err)
	}
}
