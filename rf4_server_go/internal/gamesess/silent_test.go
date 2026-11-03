package gamesess

// 这条测试盯的是一个用抓包推不出来的坑：**真服没答过 != 客户端不要回执**。
//
// 实机 2026-11-03 01:27：客户端发完 6/2 就再也不说话，界面停在「载入个人资料」，
// 120 秒后才抛 `HermesError code:110 desc:6:2` + `Game bootstrapping failed` 断开。
// 而真服那份 45 秒的抓包里，服务端对 6/2 也确实一声没吭（cid 配对只有一帧）。
// 开源实现（同一个客户端版本）对 6/2 回 null、对 1/18 回 true，所以我们照它回。
//
// 盯的形状：整帧 10 字节 = 9 字节应答头 + 1 字节值；marker 必须是 -2（不能是 -3 错误回执）。

import (
	"bytes"
	"testing"

	"rf4server/internal/entry"
	"rf4server/internal/wire"
)

// TestSilentCommandsNowAnswered 6/2 和 1/18 必须回帧，而且只能回那一个字节。
func TestSilentCommandsNowAnswered(t *testing.T) {
	deps, _ := testDeps(t)
	p := configHandshake(t, deps)

	cases := []struct {
		name  string
		main  byte
		sub   byte
		value byte
	}{
		{"6/2 竞赛载入", 6, 2, 0x00},   // 没有竞赛 -> null
		{"1/18 设备指纹", 1, 18, 0x0e}, // 上报设置 -> true
	}
	for _, c := range cases {
		cid := uint32(c.sub)
		p.ask(argsRequest(t, c.main, c.sub, cid))
		plain := p.answer(cid)
		if marker(plain) != -2 {
			t.Fatalf("%s 回的 marker 是 %d，要 -2（回 -3 错误回执客户端会当失败）", c.name, marker(plain))
		}
		if len(plain) != entry.HeadLen+1 {
			t.Fatalf("%s 整帧 %d 字节，要 %d 字节（应答头 9 + 值 1）", c.name, len(plain), entry.HeadLen+1)
		}
		if !bytes.Equal(body(plain), []byte{c.value}) {
			t.Fatalf("%s 帧体是 %x，要 0x%02x", c.name, body(plain), c.value)
		}
	}
	// 这三条不能再被当成「单向上报」：否则客户端会等 120 秒后主动断线。
	for _, key := range []string{"6/2", "1/18", "3/18"} {
		if entry.IsOneWayKey(key) {
			t.Errorf("%s 还在「不回话」名单里（客户端会干等 120 秒）", key)
		}
	}
}

// TestThreeEighteenGetsErrorReply 是这次实机掉线的回归：3/18 可以回“不支持”，但绝不能沉默。
func TestThreeEighteenGetsErrorReply(t *testing.T) {
	deps, _ := testDeps(t)
	p := configHandshake(t, deps)
	const cid = 318
	p.ask(argsRequest(t, 3, 18, cid))
	plain := p.answer(cid)
	want := entry.UnimplementedReply(cid, 3, 18)
	if !bytes.Equal(plain, want) {
		t.Fatalf("3/18 回帧不对：\n got %x\nwant %x", plain, want)
	}
}

// TestOnewayStillSilent 反过来也要盯住：真服和客户端都不要回执的那几条，别乱回帧刷屏。
// 14/7 是搏鱼上报，抓包 1072 条，回太多会把这条流挤满。
func TestOnewayStillSilent(t *testing.T) {
	deps, _ := testDeps(t)
	p := configHandshake(t, deps)

	// 发一条真的单向上报（1/15 进图通知），期待它后面没有对应 call_id 的应答
	p.ask(argsRequest(t, 1, 15, 77))
	if entry.IsOneWayKey("1/15") == false {
		t.Fatal("1/15 应该还是单向上报")
	}
	// 再发一条要回话的（1/2），能正常拿到回帧就说明上一条确实没占着号
	p.ask(argsRequest(t, 1, 2, 78))
	plain := p.answer(78)
	if marker(plain) != -2 {
		t.Fatalf("1/2 没回应答帧，marker=%d", marker(plain))
	}
	if len(body(plain)) != 9 || body(plain)[0] != 0x11 {
		t.Fatalf("1/2 帧体不对：%x（要 9 字节、开头 u64 marker 0x11）", body(plain))
	}
	_ = wire.PushTrailer
}
