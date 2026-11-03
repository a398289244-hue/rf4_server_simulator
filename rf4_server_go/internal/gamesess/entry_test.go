package gamesess

// 这条测试是「Go 版能不能顶住客户端进场那一串请求」的证据。
// 请求顺序照 2026-11-02 19:51 那次真实会话日志抄（客户端进场就是这么问的）：
//
//	1/1 报名字 → 1/2 服务器时间 → 4/1 仓库 → 4/4 物品编号表 → 9/5 23/1 24/12 空清单
//	→ 19/6 空 GUID → 24/3 本地频道 → 28/2 战队面板 → 28/1（还没搬，应回「没这条 RPC」）
//	→ 6/2（真服没答过，但客户端在等：必须回 null）
//
// 答案一律拿真服那帧对（考卷在 data/rpc_table_real.json）。call_id 是客户端自己编的，
// 所以只比 9 字节帧头之后的帧体；帧头本身也检查 marker 和 call_id 对不对。

import (
	"bytes"
	"context"
	"encoding/hex"
	"net"
	"strconv"
	"testing"
	"time"

	"rf4server/internal/assetpaths"
	"rf4server/internal/entry"
	"rf4server/internal/fishing"
	"rf4server/internal/gamestore"
	"rf4server/internal/inventory"
	"rf4server/internal/store"
	"rf4server/internal/wire"
)

// entryProbe 一个极简的「脚本客户端」：装好 RC4，一条一条问，一条一条答。
// 只做这串进场请求够用的事：发一帧、跳过 ack、拿到对应 call_id 的回帧。
type entryProbe struct {
	t       *testing.T
	conn    net.Conn
	rx      *wire.FrameReader
	decrypt *wire.RC4 // 收方向：服务端 -> 客户端
	encrypt *wire.RC4 // 发方向：客户端 -> 服务端
	wid     uint64    // 自己这边的帧序号，逐条 +1
	pushed  []string  // 路上收到的主动推送命令号（0/1、2/1 这些），只为日志/报错好看
}

// ask 发一条请求（payload = 9 字节头 + main + sub + 参数表 + 尾巴 00）
func (p *entryProbe) ask(payload []byte) {
	p.t.Helper()
	p.wid++
	if _, err := p.conn.Write(wire.BuildFrame(0, p.wid, p.encrypt.Crypt(payload))); err != nil {
		p.t.Fatalf("发请求失败: %v", err)
	}
}

// answer 等到 call_id 对得上的那一帧（应答 marker=-2 或错误 marker=-3），返回明文。
//
// 中间可能先撞上服务器主动推的 0/1、2/1（marker=-1）：那类帧不算回话，跳过就行，
// 但必须照样解密一遍 —— RC4 密钥流是按写出顺序一路走的，跳着不解后面全错位。
func (p *entryProbe) answer(cid uint32) []byte {
	p.t.Helper()
	for i := 0; i < 30; i++ {
		f, err := p.rx.Next()
		if err != nil {
			p.t.Fatalf("等 call_id=%d 的回帧时读流失败: %v", cid, err)
		}
		if f.Type == 1 || len(f.Payload) == 0 {
			continue // 纯 ack，不算回话
		}
		plain := p.decrypt.Crypt(f.Payload)
		env, ok := wire.ReadEnvelope(plain)
		if !ok {
			p.t.Fatalf("回帧解不出信封: %x", plain[:min(len(plain), 12)])
		}
		if env.Marker == -1 {
			p.pushed = append(p.pushed, strconv.Itoa(int(env.Main))+"/"+strconv.Itoa(int(env.Sub)))
			continue // 服务器先开口的推送，等它的回话
		}
		if env.CallID != cid {
			p.t.Fatalf("回帧的 call_id 是 %d，要 %d（客户端按这个号对号入座，错了就全盘乱）", env.CallID, cid)
		}
		return plain
	}
	p.t.Fatalf("连读 30 帧也没等到 call_id=%d 的回帧（期间收到推送 %v）", cid, p.pushed)
	return nil
}

// marker 读出帧头的 marker（-2 应答 / -3 错误回执）
func marker(plain []byte) int32 {
	return int32(plain[1]) | int32(plain[2])<<8 | int32(plain[3])<<16 | int32(plain[4])<<24
}

// body 去掉 9 字节帧头，只留帧体
func body(plain []byte) []byte { return plain[entry.HeadLen:] }

// realBody 考卷的帧体（真服那帧去掉 9 字节帧头）
func realBody(t *testing.T, cmd string) []byte {
	t.Helper()
	pay, err := assetpaths.RealAnswer(cmd)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return pay[entry.HeadLen:]
}

// argsRequest 按客户端真实帧形拼请求：9 头 + main + sub + 参数表"524" + 尾巴 00（共 19 字节）
func argsRequest(t *testing.T, main, sub byte, cid uint32) []byte {
	t.Helper()
	a, err := wire.Args{Code: "524"}.Encode()
	if err != nil {
		t.Fatalf("拼参数表失败: %v", err)
	}
	return append(append(wire.PushHead(cid, main, sub), a...), wire.RequestTrailer)
}

// containerRequest 拼带容器号的 4/x 请求：args("524")[u32 container]。
func containerRequest(t *testing.T, main, sub byte, cid, container uint32) []byte {
	t.Helper()
	a, err := wire.Args{Code: "524", Items: []wire.Value{wire.U32(container)}}.Encode()
	if err != nil {
		t.Fatalf("拼带容器号的参数表失败: %v", err)
	}
	return append(append(wire.PushHead(cid, main, sub), a...), wire.RequestTrailer)
}

// versionRequest 1/1 那条请求：参数表"524" 里带版本号，整帧应和日志里那 30 字节一致
func versionRequest(t *testing.T, cid uint32, version string) []byte {
	t.Helper()
	a, err := wire.Args{Code: "524", Items: []wire.Value{wire.GoStr(version)}}.Encode()
	if err != nil {
		t.Fatalf("拼 1/1 请求失败: %v", err)
	}
	return append(append(wire.PushHead(cid, 1, 1), a...), wire.RequestTrailer)
}

// TestEntrySequenceOverSocket 挨个问一遍进场链路，每条都对答案
func TestEntrySequenceOverSocket(t *testing.T) {
	deps, db := testDeps(t)

	my, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没连着，这条端到端不跑: %v", err)
	}
	t.Cleanup(func() { _ = my.Close() })
	deps.Run = my
	deps.Fishing = fishing.New(deps.Table, fishing.NewCatalog(my), my)

	ctx := context.Background()
	keepnetRows, err := my.Keepnet(ctx, "return2")
	if err != nil {
		t.Fatalf("读 MySQL 鱼护失败: %v", err)
	}
	objs, err := my.Objects(ctx, "return2")
	if err != nil {
		t.Fatalf("读 MySQL 背包失败: %v", err)
	}
	if len(objs) == 0 {
		t.Skip("return2 名下没物品，4/4 那段没法验")
	}
	keys := make([]string, 0, len(objs))
	for _, o := range objs {
		keys = append(keys, o.ItemKey)
	}
	cats, err := my.Categories(ctx, keys)
	if err != nil {
		t.Fatalf("读物品类别失败: %v", err)
	}
	downloadable := make([]gamestore.Object, 0, len(objs))
	for _, o := range objs {
		if cats[o.ItemKey] == inventory.CategoryContainer || o.Elem == "" {
			continue
		}
		if _, err := hex.DecodeString(o.Elem); err == nil {
			downloadable = append(downloadable, o)
		}
	}

	const token = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	db.Tokens().Save(store.Token{Token: token, Username: "return2", UserID: 1000002, Expires: time.Now().Unix() + 3600})

	srv := New(deps, Config{Addr: "127.0.0.1:0"})
	if err := srv.Listen(); err != nil {
		t.Fatalf("起服务失败: %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))

	guid := make([]byte, 36)
	if _, err := readFull(conn, guid); err != nil {
		t.Fatalf("没收到会话 GUID: %v", err)
	}
	auth := []byte{0x01, 0x00}
	var lb [4]byte
	putU32Test(lb[:], uint32(len(token)))
	auth = append(auth, lb[:]...)
	auth = append(auth, token...)
	if _, err := conn.Write(auth); err != nil {
		t.Fatalf("发 auth 失败: %v", err)
	}
	hermes := "<hermes><token>" + token + "</token></hermes>"
	if _, err := conn.Write(wire.BuildFrame(0, 1, []byte(hermes))); err != nil {
		t.Fatalf("发 hermes 失败: %v", err)
	}

	p := &entryProbe{
		t:       t,
		conn:    conn,
		rx:      wire.NewFrameReader(conn),
		decrypt: wire.NewRC4([]byte(token)),
		encrypt: wire.NewRC4([]byte(token)),
	}

	// ---- 1/1：报名字 + 版本 ----
	req := versionRequest(t, 1, "4.0.25034")
	if hexEncode(req) != "01ffffffff010000000101030335323401001409342e302e323530333400" {
		t.Fatalf("1/1 请求帧和实测不一致：%s", hexEncode(req))
	}
	p.ask(req)
	plain := p.answer(1)
	if marker(plain) != -2 {
		t.Fatalf("1/1 回的 marker 是 %d，要 -2", marker(plain))
	}
	if !bytes.Equal(body(plain), realBody(t, "1/1")) {
		t.Fatalf("1/1 帧体和真服不一致：\n收到=%x\n真服=%x", body(plain), realBody(t, "1/1"))
	}

	// ---- 1/2：服务器时间 ----
	p.ask(argsRequest(t, 1, 2, 2))
	plain = p.answer(2)
	if len(body(plain)) != len(realBody(t, "1/2")) || body(plain)[0] != 0x11 {
		t.Fatalf("1/2 帧体不对：%x（要 9 字节、开头是 u64 marker 0x11）", body(plain))
	}

	// ---- 4/1：鱼护容量。旧真服考卷是 0/0/0，但实机必须明确回 0/100/150，避免 HUD 显示缓存数/0 ----
	p.ask(containerRequest(t, 4, 1, 3, containerKeepnet))
	plain = p.answer(3)
	wantCapacity, err := entry.StorageInfoReplyWithCapacity(deps.Table, 3, uint32(len(keepnetRows)), 100, 150)
	if err != nil {
		t.Fatalf("拼 4/1 标准答案失败：%v", err)
	}
	if !bytes.Equal(body(plain), body(wantCapacity)) {
		t.Fatalf("4/1 容量不对：\n收到=%x\n应为=%x", body(plain), body(wantCapacity))
	}

	// ---- 4/4：物品编号表，必须就是 MySQL 里那批 GUID ----
	p.ask(containerRequest(t, 4, 4, 4, containerBackpack))
	plain = p.answer(4)
	got := body(plain)
	if got[0] != 0x06 {
		t.Fatalf("4/4 帧体开头应是 GUID 数组 marker 0x06，实际 0x%02x", got[0])
	}
	count := int(got[1]) | int(got[2])<<8
	wantObjectCount := len(downloadable)
	if count != wantObjectCount {
		t.Fatalf("4/4(背包) 报了 %d 件，MySQL 里有可下载物件 %d 件（原始行 %d）", count, len(downloadable), len(objs))
	}
	if len(got) != 3+16*count {
		t.Fatalf("4/4 帧体 %d 字节，按 %d 件应是 %d 字节", len(got), count, 3+16*count)
	}
	for i, o := range downloadable {
		wantGUID, err := hex.DecodeString(o.GUID)
		if err != nil {
			t.Fatalf("%s 的 GUID 不是十六进制: %v", o.ItemKey, err)
		}
		if !bytes.Equal(got[3+16*i:3+16*i+16], wantGUID) {
			t.Fatalf("4/4 第 %d 件 GUID 对不上：收到 %x，MySQL 是 %x", i, got[3+16*i:3+16*i+16], wantGUID)
		}
	}

	// ---- 9/5 / 23/1 / 24/12 / 19/6：如实回空表 ----
	for _, c := range []struct {
		cid  uint32
		main byte
		sub  byte
		cmd  string
	}{
		{5, 9, 5, "9/5"},
		{6, 23, 1, "23/1"},
		{7, 24, 12, "24/12"},
		{8, 19, 6, "19/6"},
	} {
		p.ask(argsRequest(t, c.main, c.sub, c.cid))
		plain = p.answer(c.cid)
		if !bytes.Equal(body(plain), realBody(t, c.cmd)) {
			t.Fatalf("%s 帧体和真服不一致：\n收到=%x\n真服=%x", c.cmd, body(plain), realBody(t, c.cmd))
		}
	}

	// ---- 24/3：本地频道。名字是我们自己起的，所以直接和 entry 拼出来的那帧比 ----
	p.ask(argsRequest(t, 24, 3, 9))
	plain = p.answer(9)
	wantFrame, err := entry.ServerInfoReply(deps.Table, 9)
	if err != nil {
		t.Fatalf("拼 24/3 期望值失败: %v", err)
	}
	if !bytes.Equal(body(plain), body(wantFrame)) {
		t.Fatalf("24/3 帧体不对：\n收到=%x\n应为=%x", body(plain), body(wantFrame))
	}

	// ---- 28/2：战队面板 ----
	p.ask(argsRequest(t, 28, 2, 10))
	plain = p.answer(10)
	if !bytes.Equal(body(plain), realBody(t, "28/2")) {
		t.Fatalf("28/2 帧体和真服不一致：\n收到=%x\n真服=%x", body(plain), realBody(t, "28/2"))
	}

	// ---- 28/1（大区/赛季浏览器 9825 字节）：整帧都是静态定义，现拼之后逐字节 == 真服 ----
	// 定义 JSON 没装上的话 testDeps 会留 nil，那时候改成确认回了「没这条 RPC」，别让客户端干等
	p.ask(argsRequest(t, 28, 1, 11))
	plain = p.answer(11)
	if deps.Static == nil || !deps.Static.Has("28/1") {
		if marker(plain) != -3 {
			t.Fatalf("还没装 28/1 的静态定义，应该回 marker=-3 的「没这条 RPC」，实际 marker=%d", marker(plain))
		}
	} else {
		if marker(plain) != -2 {
			t.Fatalf("28/1 回的不是应答帧，marker=%d", marker(plain))
		}
		if !bytes.Equal(body(plain), realBody(t, "28/1")) {
			t.Fatalf("28/1 现拼帧体和真服不一样：我这边 %d 字节，真服 %d 字节",
				len(body(plain)), len(realBody(t, "28/1")))
		}
	}

	// ---- 1/5（9 段会话指纹串）：内容按账号现生成，不能逐字节抄真服，只比整帧长度和形状 ----
	// 真实顺序里它排在 1/2 后面；这儿排在最后只是为了不打乱前面那些 call_id
	p.ask(argsRequest(t, 1, 5, 12))
	plain = p.answer(12)
	if len(plain) != entry.SessionFrameLen {
		t.Fatalf("1/5 回帧 %d 字节，真服那帧应该是 %d 字节", len(plain), entry.SessionFrameLen)
	}

	// ---- 6/2：真服那份抓包里服务端没答，但实机证明这个客户端会等它的回执
	// （发完 6/2 就不说话，120 秒后 HermesError code:110 desc:6:2 断开），所以必须回一帧 null。
	// 紧接着再问 19/6，两帧各归各的 call_id，谁也别串号。
	p.ask(argsRequest(t, 6, 2, 13))
	plain = p.answer(13)
	if marker(plain) != -2 {
		t.Fatalf("6/2 回的 marker 是 %d，要应答帧的 -2", marker(plain))
	}
	if len(plain) != entry.HeadLen+1 || body(plain)[0] != 0x00 {
		t.Fatalf("6/2 应回一帧 null（整帧 %d 字节、帧体 0x00），实际 %x", entry.HeadLen+1, plain)
	}
	p.ask(argsRequest(t, 19, 6, 14))
	plain = p.answer(14)
	if !bytes.Equal(body(plain), realBody(t, "19/6")) {
		t.Fatalf("补问的 19/6 帧体不对：%x", body(plain))
	}

	t.Logf("进场链路 %d 条请求全部有下场：1/1 1/2 4/1 4/4(%d 件) 9/5 23/1 24/12 19/6 24/3 28/2 "+
		"关键门禁全部通过；28/1 回了未实现；6/2 回 null（客户端就在等这一帧）", 12, len(objs))
}

// hexEncode 小助手：字节转小写十六进制，报错信息里好看
func hexEncode(b []byte) string { return hex.EncodeToString(b) }
