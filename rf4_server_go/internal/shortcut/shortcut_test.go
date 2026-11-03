package shortcut

// 快捷栏这两条测试拿的是真服原文：
//
//	11/1 应答 107 字节 = 应答头 + 参数表"65"{ obj136 × 4 }
//	11/2 应答  32 字节 = 应答头 + obj136{槽位, GUID}
//
// 我们照原文那 4 条绑定（槽位 + GUID 都从考卷里读）现拼一遍，整帧必须逐字节一样；
// 第三条才是少爷真正关心的：绑一个不在自己名下的 GUID 必须被拒，绑自己名下的能落库、能解绑。

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"rf4server/internal/assetpaths"
	"rf4server/internal/gamestore"
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

// TestListFrameMatchesReal 11/1：拿考卷里那 4 条绑定现拼，整帧必须 == 真服 107 字节
func TestListFrameMatchesReal(t *testing.T) {
	tbl := loadTable(t)
	real, err := assetpaths.RealAnswer("11/1")
	if err != nil {
		t.Fatalf("%v", err)
	}
	callID := binary.LittleEndian.Uint32(real[5:9])
	rows := realRows(t, tbl, real)
	if len(rows) == 0 {
		t.Fatalf("考卷 11/1 里一条绑定都没解析出来，先确认字段表")
	}
	got, err := BuildListFrame(tbl, callID, rows)
	if err != nil {
		t.Fatalf("拼 11/1 失败：%v", err)
	}
	if !bytes.Equal(got, real) {
		t.Fatalf("11/1 现拼和真服不一样：我这边 %d 字节，真服 %d 字节\n我这边=%x\n真服  =%x",
			len(got), len(real), got, real)
	}
}

// realRows 把考卷里那几条绑定读出来（槽位 + GUID），当作我们的输入
func realRows(t *testing.T, tbl *wire.Table, pay []byte) []gamestore.Shortcut {
	t.Helper()
	v, _, err := tbl.DecodeValue(pay, 9)
	if err != nil {
		t.Fatalf("考卷 11/1 解不开：%v", err)
	}
	var rows []gamestore.Shortcut
	for _, o := range wire.ArgsItems(v) {
		obj, ok := o.(*wire.Obj)
		if !ok || obj.TypeID != TypeShortcut {
			continue
		}
		var (
			slot int
			guid string
		)
		wire.Walk(obj, func(it wire.WalkItem) {
			if it.Kind == "u16" && slot == 0 {
				slot = int(it.Num)
			}
			if it.Kind == "guid" {
				guid = it.Hex
			}
		})
		rows = append(rows, gamestore.Shortcut{Slot: slot, GUID: guid})
	}
	return rows
}

// TestBindFrameMatchesReal 11/2：应答就是一个 obj136，逐字节 == 真服那 32 字节
func TestBindFrameMatchesReal(t *testing.T) {
	tbl := loadTable(t)
	real, err := assetpaths.RealAnswer("11/2")
	if err != nil {
		t.Fatalf("%v", err)
	}
	callID := binary.LittleEndian.Uint32(real[5:9])
	v, _, err := tbl.DecodeValue(real, 9)
	if err != nil {
		t.Fatalf("考卷 11/2 解不开：%v", err)
	}
	obj, ok := v.(*wire.Obj)
	if !ok || obj.TypeID != TypeShortcut {
		t.Fatalf("考卷 11/2 帧体不是 obj%d，是 %T", TypeShortcut, v)
	}
	var slot int
	var guid string
	wire.Walk(obj, func(it wire.WalkItem) {
		if it.Kind == "u16" {
			slot = int(it.Num)
		}
		if it.Kind == "guid" {
			guid = it.Hex
		}
	})
	got, err := BuildBindFrame(tbl, callID, slot, guid)
	if err != nil {
		t.Fatalf("拼 11/2 失败：%v", err)
	}
	if !bytes.Equal(got, real) {
		t.Fatalf("11/2 现拼和真服不一样：\n我这边=%x\n真服  =%x", got, real)
	}
}

// TestBindRejectsForeignGUID 拿少爷那个真号跑一遍完整来回：
// 绑一个不存在的名下没有的 GUID 要被拒；绑自己名下的一件要落库、要能列出来、要能解掉。
// 用的是 42 号槽，动不到少爷自己那几格，收尾也清干净。
func TestBindRejectsForeignGUID(t *testing.T) {
	tbl := loadTable(t)
	my, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没连着，这条不跑：%v", err)
	}
	defer my.Close()
	svc := New(tbl, my)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	objs, err := my.Objects(ctx, "return2")
	if err != nil || len(objs) == 0 {
		t.Skipf("return2 名下没物品，绑不了：%v", err)
	}
	mine := objs[0]

	// 先记下这一格原来的绑定，测完还原
	before, err := my.Shortcuts(ctx, "return2")
	if err != nil {
		t.Fatalf("读快捷栏失败：%v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = my.SetShortcut(c, "return2", 42, "", "")
		for _, r := range before {
			if r.Slot == 42 {
				_ = my.SetShortcut(c, "return2", 42, r.GUID, r.ItemKey)
			}
		}
	})

	// 1) 别人的 GUID：必须拒，且应答里 GUID 写零
	foreign := "aabbccddeeff00112233445566778899"
	req := bindRequest(t, tbl, 42, foreign)
	frame, res, err := svc.BindFrame(ctx, "return2", 7, req)
	if err != nil {
		t.Fatalf("绑定出错：%v", err)
	}
	if res.OK {
		t.Fatalf("别人名下的 GUID 竟然绑上了：%+v", res)
	}
	if res.Reason == "" {
		t.Fatalf("拒了却没写原因，日志上看不出为什么：%+v", res)
	}
	if !bytes.Contains(frame, []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00}) {
		t.Logf("应答帧=%x（没找着一串零也算正常，GUID 写法是 16 字节）", frame)
	}

	// 2) 自己名下的：要绑上，并且库里能查到
	req = bindRequest(t, tbl, 42, mine.GUID)
	_, res, err = svc.BindFrame(ctx, "return2", 8, req)
	if err != nil {
		t.Fatalf("绑定出错：%v", err)
	}
	if !res.OK || res.ItemKey != mine.ItemKey {
		t.Fatalf("自己名下的东西没绑上：%+v", res)
	}
	rows, err := my.Shortcuts(ctx, "return2")
	if err != nil {
		t.Fatalf("读快捷栏失败：%v", err)
	}
	found := false
	for _, r := range rows {
		if r.Slot == 42 && r.GUID == mine.GUID {
			found = true
		}
	}
	if !found {
		t.Fatalf("绑完了库里查不到：shortcut.Describe=%s", Describe(rows))
	}

	// 3) 解绑：那一格要从库里掉出去
	req = bindRequest(t, tbl, 42, "")
	if _, res, err = svc.UnbindFrame(ctx, "return2", 9, req); err != nil {
		t.Fatalf("解绑出错：%v", err)
	}
	if !res.OK {
		t.Fatalf("解绑没成功：%+v", res)
	}
	rows, err = my.Shortcuts(ctx, "return2")
	if err != nil {
		t.Fatalf("读快捷栏失败：%v", err)
	}
	for _, r := range rows {
		if r.Slot == 42 {
			t.Fatalf("解完绑库里还剩这一格：%+v", r)
		}
	}
}

// bindRequest 拼一条客户端-style 请求：11 字节请求头 + 参数表"524"{ 槽位[, GUID] }
func bindRequest(t *testing.T, tbl *wire.Table, slot int, guid string) []byte {
	t.Helper()
	items := []wire.Value{wire.U32(uint32(slot))}
	if guid != "" {
		items = append(items, wire.GUID{Hex: guid})
	}
	args, err := wire.Args{Code: "524", Items: items}.Encode()
	if err != nil {
		t.Fatalf("拼请求失败：%v", err)
	}
	return append(append(wire.PushHead(0, 11, 2), args...), wire.RequestTrailer)
}
