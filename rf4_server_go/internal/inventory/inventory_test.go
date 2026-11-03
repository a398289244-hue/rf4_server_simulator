package inventory

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"rf4server/internal/gamestore"
)

// TestFrameHead 帧头必须一字不差：01 | -2 | call_id | 03 02 "61" | 条数 u16。
// 这几个字节错一个，客户端就会把整个背包卡在「正在下载」。
func TestFrameHead(t *testing.T) {
	elems := [][]byte{
		[]byte{0x01, 0xa6, 0x01, 0x02},
		[]byte{0x01, 0xa6, 0x03, 0x04, 0x05},
	}
	frame := BuildFrame(20, elems)
	want := []byte{0x01, 0xfe, 0xff, 0xff, 0xff, 20, 0, 0, 0, 0x03, 0x02, 0x36, 0x31, 0x02, 0x00}
	if !bytes.Equal(frame[:HeadLen], want) {
		t.Fatalf("帧头不对：\n  实际 %x\n  想要 %x", frame[:HeadLen], want)
	}
	if len(frame) != HeadLen+9 {
		t.Fatalf("整帧长度 %d，应该 %d", len(frame), HeadLen+9)
	}
	if !bytes.Equal(frame[HeadLen:], append(elems[0], elems[1]...)) {
		t.Fatalf("记录排错了：%x", frame[HeadLen:])
	}
}

// TestFrameEmptyList 这个号一件没有时，条数必须写 0，不能留录制里那位老号的件数
func TestFrameEmptyList(t *testing.T) {
	frame := BuildFrame(9, nil)
	if len(frame) != HeadLen {
		t.Fatalf("空清单应该只有帧头 %d 字节，实际 %d", HeadLen, len(frame))
	}
	if frame[13] != 0 || frame[14] != 0 {
		t.Fatalf("条数应该是 0，实际 %d %d", frame[13], frame[14])
	}
}

// TestStripReceipt 物件记录尾巴上混了购买回执的，要剪掉换成该有的 7 个 0
func TestStripReceipt(t *testing.T) {
	in := append([]byte{0x01, 0xa6, 0xaa, 0xbb}, receiptMark...)
	in = append(in, 0x11, 0x22, 0x33)
	out := StripReceipt(in)
	want := append([]byte{0x01, 0xa6, 0xaa, 0xbb}, recordTail...)
	if !bytes.Equal(out, want) {
		t.Fatalf("剪完不对：\n  实际 %x\n  想要 %x", out, want)
	}
}

// TestStripReceiptKeepsCleanRecord 本来就没有回执的记录，一个字节都不许动
func TestStripReceiptKeepsCleanRecord(t *testing.T) {
	in := []byte{0x01, 0xa6, 0x99, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	out := StripReceipt(in)
	if !bytes.Equal(in, out) {
		t.Fatalf("干净记录被改坏了：\n  进 %x\n  出 %x", in, out)
	}
}

// TestCategoriesFromRealTable 物品类别要能在 static_item_def 里查到，
// 而且 box_spin_l_start 必须是类别 55（容器/套装）——背包要不要跳过它就靠这个数。
func TestCategoriesFromRealTable(t *testing.T) {
	db := openLiveStore(t)
	cats, err := db.Categories(context.Background(), []string{"box_spin_l_start", "worm_01", "查不到的编号"})
	if err != nil {
		t.Fatalf("查类别失败: %v", err)
	}
	if cats["box_spin_l_start"] != CategoryContainer {
		t.Fatalf("box_spin_l_start 类别是 %d，应该是容器类别 %d", cats["box_spin_l_start"], CategoryContainer)
	}
	if cats["worm_01"] == 0 {
		t.Fatalf("worm_01 类别查出来是 0，八成查询写错了")
	}
	if _, ok := cats["查不到的编号"]; ok {
		t.Errorf("查不到的编号不该出现在结果里")
	}
	t.Logf("类别查得对：box_spin_l_start=%d（容器） worm_01=%d", cats["box_spin_l_start"], cats["worm_01"])
}

// TestNeedSkipsContainersAndUsesRealRows 拿 return2 在 MySQL 里的真货跑一遍：
// 该发的件数、整帧长度都由 MySQL + 类别表现算，容器类必须被挑出来暂不发。
func TestNeedSkipsContainersAndUsesRealRows(t *testing.T) {
	db := openLiveStore(t)
	ctx := context.Background()

	objs, err := db.Objects(ctx, "return2")
	if err != nil {
		t.Fatalf("读 objects 失败: %v", err)
	}
	if len(objs) == 0 {
		t.Skipf("return2 名下没物品，跳过了测不出东西")
	}
	keys := make([]string, 0, len(objs))
	for _, o := range objs {
		keys = append(keys, o.ItemKey)
	}
	cats, err := db.Categories(ctx, keys)
	if err != nil {
		t.Fatalf("查类别失败: %v", err)
	}

	wantCount, wantBytes := 0, HeadLen
	wantDeferred := 0
	for _, o := range objs {
		if cats[o.ItemKey] == CategoryContainer {
			if o.Elem != "" {
				wantDeferred++
			}
			continue
		}
		if o.Elem == "" {
			continue
		}
		raw, err := hex.DecodeString(o.Elem)
		if err != nil {
			t.Fatalf("%s 的物件记录不是十六进制: %v", o.ItemKey, err)
		}
		wantCount++
		wantBytes += len(StripReceipt(raw))
	}

	svc := New(db)
	res, err := svc.Need(ctx, "return2", nil)
	if err != nil {
		t.Fatalf("算该发哪些货失败: %v", err)
	}
	if len(res.Elems) != wantCount {
		t.Fatalf("该发 %d 件，实际算出 %d 件（点名 %v 缺记录 %v）", wantCount, len(res.Elems), res.Requested, res.Missing)
	}
	if wantDeferred > 0 && len(res.Deferred) != wantDeferred {
		t.Fatalf("容器类应该暂不发 %d 件，实际挑出 %d 件", wantDeferred, len(res.Deferred))
	}
	frame := BuildFrame(20, res.Elems)
	if len(frame) != wantBytes {
		t.Fatalf("整帧 %d 字节，应该 %d 字节", len(frame), wantBytes)
	}
	got := int(frame[13]) | int(frame[14])<<8
	if got != wantCount {
		t.Fatalf("帧里条数写的是 %d，应该 %d", got, wantCount)
	}
	t.Logf("背包清单：发 %d 件 / %d 字节；容器暂不发 %v；客户端点名 %v",
		len(res.Elems), len(frame), res.Deferred, res.Requested)
}

// TestNeedSendsGUIDRequestedByClient 盯住重登丢装备的根因：4/5 里的 GUID 是点名下载，不是去重。
func TestNeedSendsGUIDRequestedByClient(t *testing.T) {
	db := openLiveStore(t)
	ctx := context.Background()
	objs, err := db.Objects(ctx, "return2")
	if err != nil {
		t.Fatalf("读 objects 失败: %v", err)
	}
	cats, err := db.Categories(ctx, objectKeys(objs))
	if err != nil {
		t.Fatalf("查类别失败: %v", err)
	}
	var target gamestore.Object
	for _, o := range objs {
		if o.Elem != "" && cats[o.ItemKey] != CategoryContainer {
			target = o
			break
		}
	}
	if target.GUID == "" {
		t.Skip("return2 名下没有可测试的普通物件")
	}
	guid, err := hex.DecodeString(target.GUID)
	if err != nil || len(guid) != 16 {
		t.Fatalf("测试物件 GUID 坏了: %q", target.GUID)
	}
	request := append([]byte{1, 2, 3}, guid...)
	res, err := New(db).Need(ctx, "return2", request)
	if err != nil {
		t.Fatalf("按 GUID 点名下载失败: %v", err)
	}
	if len(res.Elems) != 1 || len(res.Requested) != 1 || res.Requested[0] != target.ItemKey {
		t.Fatalf("点名 %s 应只发这一件，实际 elems=%d requested=%v", target.ItemKey, len(res.Elems), res.Requested)
	}
}

// TestGUIDsMatchNeedDownloadableRecords 防止背包再次卡“正在下载”：4/4 报几个 GUID，
// 4/5 全量就必须恰好能给几个详情。类别55包装、空记录、坏 hex 两边都要一起排除。
func TestGUIDsMatchNeedDownloadableRecords(t *testing.T) {
	db := openLiveStore(t)
	ctx := context.Background()
	svc := New(db)
	guids, err := svc.GUIDs(ctx, "return2")
	if err != nil {
		t.Fatalf("算4/4 GUID失败: %v", err)
	}
	need, err := svc.Need(ctx, "return2", nil)
	if err != nil {
		t.Fatalf("算4/5详情失败: %v", err)
	}
	if len(guids) != len(need.Elems) {
		t.Fatalf("4/4报%d个GUID，但4/5只能给%d条详情；客户端会一直下载（容器=%v 缺失=%v）",
			len(guids), len(need.Elems), need.Deferred, need.Missing)
	}
	t.Logf("4/4与4/5一致：%d件；暂缓包装=%v", len(guids), need.Deferred)
}

func objectKeys(objs []gamestore.Object) []string {
	keys := make([]string, 0, len(objs))
	for _, o := range objs {
		keys = append(keys, o.ItemKey)
	}
	return keys
}

// openLiveStore 连本机 MySQL；库没起来就跳过，不算失败
func openLiveStore(t *testing.T) *gamestore.MySQL {
	t.Helper()
	db, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没起来，跳过实库测试: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
