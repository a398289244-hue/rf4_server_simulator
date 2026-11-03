package gamesess

// 这条测试盯的是「客户端开机那两步能不能拿到真东西」：
//
//	1/4 要第 N 块配置 -> 帧体就是 0x08 + u32 长度 + 那块 LZMA 原文（一字节不多一字节不少）
//	1/5                -> 回 9 段 sha1，而且这 9 段必须就是 index.json 里那份
//
// 为什么 1/5 也要盯：它和 1/4 是一对。报的版本号和发出去的内容必须互相能校验上，
// 不然客户端解压完发现 sha1 不匹配，照样是开不了机。
// 实机 2026-11-03 01:12 就是栽在 1/4 上（客户端报 Game bootstrapping failed: Unimplemented RPC 1:4）。

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"rf4server/internal/assetpaths"
	"rf4server/internal/entry"
	"rf4server/internal/gamecfg"
	"rf4server/internal/store"
	"rf4server/internal/wire"
)

// configHandshake 走完握手（36 字节身份 ID -> auth -> hermes），回来一个能问业务命令的探针。
// 不接 MySQL 也能跑：进场那两条先手推送（0/1、2/1）没库也发得出来。
func configHandshake(t *testing.T, deps Deps) *entryProbe {
	t.Helper()

	const token = "cccccccccccccccccccccccccccccccc"
	// 令牌表在 deps.DB（db.json）里：塞一把假钥匙，不用真登录也能把握手走完
	deps.DB.Tokens().Save(store.Token{Token: token, Username: "return2", UserID: 1000002, Expires: time.Now().Unix() + 3600})

	srv := New(deps, Config{Addr: "127.0.0.1:0"})
	if err := srv.Listen(); err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	guid := make([]byte, 36)
	if _, err := readFull(conn, guid); err != nil {
		t.Fatalf("没收到会话身份 ID: %v", err)
	}
	auth := []byte{0x01, 0x00}
	var lb [4]byte
	binary.LittleEndian.PutUint32(lb[:], uint32(len(token)))
	auth = append(auth, lb[:]...)
	auth = append(auth, token...)
	if _, err := conn.Write(auth); err != nil {
		t.Fatalf("发 auth 失败: %v", err)
	}
	hermes := "<hermes><token>" + token + "</token><serializer>phoenix</serializer><compress>false</compress></hermes>"
	if _, err := conn.Write(wire.BuildFrame(0, 1, []byte(hermes))); err != nil {
		t.Fatalf("发 hermes 失败: %v", err)
	}
	return &entryProbe{
		t:       t,
		conn:    conn,
		rx:      wire.NewFrameReader(conn),
		decrypt: wire.NewRC4([]byte(token)),
		encrypt: wire.NewRC4([]byte(token)),
	}
}

// configRequest 拼一条 1/4 请求：参数表"524"{ 类型号 }，形状和客户端一贯的写法一致
func configRequest(t *testing.T, cid uint32, typ uint32) []byte {
	t.Helper()
	a, err := wire.Args{Code: "524", Items: []wire.Value{wire.U32(typ)}}.Encode()
	if err != nil {
		t.Fatalf("拼 1/4 请求失败: %v", err)
	}
	return append(append(wire.PushHead(cid, 1, 4), a...), wire.RequestTrailer)
}

// indexVersions 直接读凭据目录里那份 index.json 的 9 个版本号。
// 从文件读、不从 gamecfg 拿，是为了不让被测代码自己证明自己。
func indexVersions(t *testing.T) []string {
	t.Helper()
	p, err := assetpaths.File("config_wire/index.json")
	if err != nil {
		t.Fatalf("找不到凭据目录: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 index.json 失败: %v", err)
	}
	var idx struct {
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatalf("index.json 解析失败: %v", err)
	}
	return idx.Versions
}

// TestConfigCommandsOverSocket 1/4 和 1/5 走一遍真 socket，两条都对答案。
func TestConfigCommandsOverSocket(t *testing.T) {
	deps, _ := testDeps(t)
	lib, err := gamecfg.Load()
	if err != nil {
		t.Skipf("配置块没准备好（data/config_wire/），这条先不跑: %v", err)
	}
	deps.GameCfg = lib

	p := configHandshake(t, deps)

	// ---- 1/4：要第 3 块配置（16KB 那块），必须原样吐回来 ----
	want, _ := lib.Blob(3)
	p.ask(configRequest(t, 1, 3))
	plain := p.answer(1)
	if marker(plain) != -2 {
		t.Fatalf("1/4 回的是 marker=%d，要应答帧的 -2（给成错误回执客户端会当场崩）", marker(plain))
	}
	got := body(plain)
	if got[0] != 0x08 {
		t.Fatalf("1/4 帧体开头应是字节块 marker 0x08，实际 0x%02x", got[0])
	}
	n := binary.LittleEndian.Uint32(got[1:5])
	if int(n) != len(want) {
		t.Fatalf("1/4 说块长 %d，配置块实际 %d 字节", n, len(want))
	}
	if len(got) != 5+int(n) {
		t.Fatalf("1/4 帧体 %d 字节，按 %d 字节块应是 %d 字节", len(got), n, 5+int(n))
	}
	if !bytes.Equal(got[5:5+int(n)], want) {
		t.Fatal("1/4 发出去的字节和配置块不一致 —— 客户端解压完 sha1 对不上，照样开不了机")
	}

	// ---- 1/4：要一块没有的类型（42），只能回空参数表，不能回「没这条 RPC」 ----
	p.ask(configRequest(t, 2, 42))
	plain = p.answer(2)
	if marker(plain) != -2 {
		t.Fatalf("类型 42 也该回应答帧，实际 marker=%d", marker(plain))
	}
	if b := body(plain); !bytes.Equal(b, []byte{0x03, 0x03, '5', '2', '4', 0x00, 0x00}) {
		t.Fatalf("没有的类型应回空 args\"524\"，实际 %x", b)
	}

	// ---- 1/5：那 9 段必须是 index.json 里那份（和上面发的内容是同一对） ----
	p.ask(argsRequest(t, 1, 5, 3))
	plain = p.answer(3)
	if len(plain) != entry.SessionFrameLen {
		t.Fatalf("1/5 整帧 %d 字节，真服那帧是 %d 字节", len(plain), entry.SessionFrameLen)
	}
	v, _, err := deps.Table.DecodeValue(plain, entry.HeadLen)
	if err != nil {
		t.Fatalf("1/5 帧体解不开: %v", err)
	}
	arr, ok := v.(wire.BytesArray)
	if !ok {
		t.Fatalf("1/5 应是字节块数组，实际 %T", v)
	}
	if len(arr.Items) != entry.SessionIDCount {
		t.Fatalf("1/5 给了 %d 段，要 %d 段", len(arr.Items), entry.SessionIDCount)
	}
	wantVersions := indexVersions(t)
	for i, it := range arr.Items {
		gotV := string(it.Data)
		if gotV != wantVersions[i] {
			t.Fatalf("1/5 第 %d 段是 %q，index.json 写的是 %q（第 %d 块配置发的是这份，版本号必须一致）",
				i+1, gotV, wantVersions[i], i+1)
		}
		if gotV != lib.Version(i+1) {
			t.Fatalf("1/5 第 %d 段和 1/4 那块配置的 sha1 不一致: %q vs %q", i+1, gotV, lib.Version(i+1))
		}
	}
	t.Logf("开机两条通：1/4 原样下发 %d 字节配置块，1/5 回 %s 这 9 个版本号",
		len(want), strconv.Itoa(entry.SessionIDCount)+" 段（"+wantVersions[0][:8]+"…）")
}

// TestConfigCommandsWithoutLibrary 配置库没装时的兜底：两条都得回东西，不能抛「没这条 RPC」。
// 客户端对 1/4 的错误回执是直接崩的，所以这条是防回归的底线。
func TestConfigCommandsWithoutLibrary(t *testing.T) {
	deps, _ := testDeps(t)
	deps.GameCfg = nil // 就是要没装

	p := configHandshake(t, deps)

	p.ask(configRequest(t, 1, 1))
	plain := p.answer(1)
	if marker(plain) != -2 {
		t.Fatalf("配置库没装也该回应答帧，实际 marker=%d（回 -3 错误回执客户端会抛 Unimplemented）", marker(plain))
	}
	if b := body(plain); !bytes.Equal(b, []byte{0x03, 0x03, '5', '2', '4', 0x00, 0x00}) {
		t.Fatalf("没配置时该回空 args\"524\"，实际 %x", b)
	}

	p.ask(argsRequest(t, 1, 5, 2))
	plain = p.answer(2)
	if len(plain) != entry.SessionFrameLen {
		t.Fatalf("没装配置库时 1/5 也得给 9 段占位串（整帧 %d 字节），实际 %d 字节", entry.SessionFrameLen, len(plain))
	}
}
