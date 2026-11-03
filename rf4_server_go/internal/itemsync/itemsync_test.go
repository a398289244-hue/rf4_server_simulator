package itemsync

// 空快照这一帧有真服原文可对：考卷里 14/2 的应答就是它（29 字节）。
// 这条测试的意义不只是「形状对」——它证明 800 的三个字段（两个参数表"61" + 一个 GUID 数组）
// 我们填的顺序和写法都没错，后面 4/7 删除、17/2 装配、22/2 购买全靠这个构造器。

import (
	"bytes"
	"encoding/binary"
	"testing"

	"rf4server/internal/assetpaths"
	"rf4server/internal/wire"
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

func TestEmptyFrameMatchesReal(t *testing.T) {
	tbl := loadTable(t)
	real, err := assetpaths.RealAnswer("14/2")
	if err != nil {
		t.Fatalf("%v", err)
	}
	callID := binary.LittleEndian.Uint32(real[5:9])
	got, err := EmptyFrame(tbl, callID)
	if err != nil {
		t.Fatalf("拼空快照失败：%v", err)
	}
	if !bytes.Equal(got, real) {
		t.Fatalf("空快照和真服不一样：我这边 %d 字节，真服 %d 字节\n我这边=%x\n真服  =%x",
			len(got), len(real), got, real)
	}
}

// TestFrameCarriesItemsAndGuids 有内容时：物件记录排在 #00、GUID 排在 #02，前后顺序不能反
// （客户端先按 #00 更新背包，再按 #02 把没了的那几件抹掉，反了就会出现「删了还在」）。
// 这里不整帧解码：假记录不是真记录，解码器解不动，比字节就够了。
func TestFrameCarriesItemsAndGuids(t *testing.T) {
	tbl := loadTable(t)
	empty, err := EmptyFrame(tbl, 5)
	if err != nil {
		t.Fatalf("%v", err)
	}
	rec := []byte{0x01, 0x8d, 0x00, 0x00, 0x00, 0xaa, 0xbb} // 随便一条「已经编好的记录」，只看它排哪儿
	guid := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0x00, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	got, err := Frame(tbl, 5, [][]byte{rec}, nil, []string{"11223344556677889900aabbccddeeff"})
	if err != nil {
		t.Fatalf("%v", err)
	}
	// 长度 = 空快照 + 一条记录（GUID 数组那 16 字节本来就在空帧外多出来，单独算过了）
	if at := bytes.Index(got, rec); at < 0 {
		t.Fatalf("物件记录没排进帧里：%x", got)
	} else if g := bytes.Index(got, guid); g < at {
		t.Fatalf("GUID 排到了物件记录前面（应先 #00 后 #02）：记录在 %d，GUID 在 %d", at, g)
	}
	if want := len(empty) + len(rec) + 16; len(got) != want {
		t.Fatalf("帧长 %d，按「空快照 %d + 记录 %d + 多 1 个 GUID 16」应是 %d",
			len(got), len(empty), len(rec), want)
	}
}
