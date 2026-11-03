package gamesess

// 这条测试盯的是「档案帧里到底是谁的数」。
// 拿少爷那个真号 return2 走一遍完整进场：装完 RC4 就该收到 0/1 和 2/1（服务器先开口），
// 然后自己问 3/1，两条里的 obj307 必须和 MySQL 里那几格完全一致。
//
// 之前栽的跟头都在这里：面板上显示的是录制那位老号的银币/技能点，或者干脆卡在
// 「载入个人资料」不动。所以这条既查「发没发」，也查「发的是不是这个号自己的数」。
// 没有 MySQL 就整条跳过（档案的真值只在库里，不该拿假数据凑）。

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"rf4server/internal/gamestore"
	"rf4server/internal/profile"
	"rf4server/internal/skilltree"
	"rf4server/internal/store"
	"rf4server/internal/wire"
)

// helloProbeClient 走完握手，装好两条密钥流，返回一个能读能写的客户端。
// 和 entry_test 里那个探测客户端各管各的：这边只关心「服务器先开口」这两帧。
type helloProbeClient struct {
	t       *testing.T
	conn    net.Conn
	rx      *wire.FrameReader
	decrypt *wire.RC4
	encrypt *wire.RC4
	wid     uint64
}

// dialAndHandshake 起一台只听随机端口的后端，把 GUID -> token -> hermes 走完
func dialAndHandshake(t *testing.T, srv *Server, token string) *helloProbeClient {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("连不上后端: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))

	guid := make([]byte, 36)
	if _, err := readFull(conn, guid); err != nil {
		t.Fatalf("没收到会话 GUID: %v", err)
	}
	auth := []byte{0x01, 0x00}
	auth = append(auth, le32Bytes(uint32(len(token)))...)
	auth = append(auth, token...)
	if _, err := conn.Write(auth); err != nil {
		t.Fatalf("发 token 失败: %v", err)
	}
	hermes := "<hermes><token>" + token + "</token></hermes>"
	if _, err := conn.Write(wire.BuildFrame(0, 1, []byte(hermes))); err != nil {
		t.Fatalf("发 hermes 失败: %v", err)
	}
	return &helloProbeClient{
		t:       t,
		conn:    conn,
		rx:      wire.NewFrameReader(conn),
		decrypt: wire.NewRC4([]byte(token)),
		encrypt: wire.NewRC4([]byte(token)),
	}
}

// le32Bytes 小端 4 字节（auth 段里那个长度前缀）
func le32Bytes(v uint32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}

// nextData 读下一条有正文的帧，明文返回（纯 ack / 全零空帧自动跳过）
func (p *helloProbeClient) nextData(what string) []byte {
	p.t.Helper()
	for i := 0; i < 20; i++ {
		f, err := p.rx.Next()
		if err != nil {
			p.t.Fatalf("等%s时读流失败: %v", what, err)
		}
		if f.Type == 1 || len(f.Payload) == 0 {
			continue
		}
		return p.decrypt.Crypt(f.Payload)
	}
	p.t.Fatalf("连读 20 帧也没等到%s", what)
	return nil
}

// ask 发一条请求（正文自己 RC4，帧头明文）
func (p *helloProbeClient) ask(main, sub byte, cid uint32) {
	p.t.Helper()
	p.wid++
	if _, err := p.conn.Write(wire.BuildFrame(0, p.wid, p.encrypt.Crypt(requestPayload(main, sub, cid)))); err != nil {
		p.t.Fatalf("发 %d/%d 失败: %v", main, sub, err)
	}
}

// profileFrom 把一帧（推送或应答）里的 obj307 解出来。
// off 是帧体起点：推送跳 11 字节头 + 7 字节参数表壳，应答跳 9 字节头。
func profileFrom(t *testing.T, tbl *wire.Table, plain []byte, off int, what string) *wire.Obj {
	t.Helper()
	v, _, err := tbl.DecodeValue(plain, off)
	if err != nil {
		t.Fatalf("%s 帧体解不开: %v（头 16 字节=%x）", what, err, plain[:min(16, len(plain))])
	}
	if args, ok := v.(wire.Args); ok {
		objs := args.Objs()
		if len(objs) != 1 {
			t.Fatalf("%s 参数表里物件 %d 个，应该就一个 obj%d", what, len(objs), profile.TypeProfile)
		}
		v = objs[0]
	}
	o, ok := v.(*wire.Obj)
	if !ok {
		t.Fatalf("%s 帧体不是物件，是 %T", what, v)
	}
	if o.TypeID != profile.TypeProfile {
		t.Fatalf("%s 里的物件 typeID 是 %d，要 %d（玩家档案）", what, o.TypeID, profile.TypeProfile)
	}
	return o
}

// wantField 把档案里第 idx 个字段和期望值对一遍，说清是哪个字段不对
func wantField(t *testing.T, o *wire.Obj, idx int, name string, want string) {
	t.Helper()
	got := fieldText(o.Fields[idx].Value)
	if got != want {
		t.Fatalf("档案第 %d 个字段 %s（%s）= %s，库里是 %s", idx, o.Fields[idx].Name, name, got, want)
	}
}

// fieldText 把字段值变成可比的文本（u32/u64/短串各自一种写法，统一压成字符串好看）
func fieldText(v wire.Value) string {
	switch x := v.(type) {
	case wire.Num32:
		return strconv.FormatUint(uint64(x.V), 10)
	case wire.Num64:
		return strconv.FormatUint(x.V, 10)
	case wire.StrField:
		if x.IsNull {
			return "<null>"
		}
		return x.S
	}
	return "<" + strconv.FormatInt(0, 10) + ">"
}

// TestSpeaksFirstThenProfileMatchesMySQL 实机那条链路的最小复刻：
// 先手推送 -> 3/1 应答，两处档案内容都必须等于 MySQL。
func TestSpeaksFirstThenProfileMatchesMySQL(t *testing.T) {
	deps, db := testDeps(t)
	my, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没连着，这条不跑（档案真值只在库里）: %v", err)
	}
	t.Cleanup(func() { _ = my.Close() })
	deps.Run = my

	ctx := context.Background()
	acct, err := my.Account(ctx, "return2")
	if err != nil {
		t.Fatalf("读 return2 账号失败: %v", err)
	}
	st, err := my.SkillState(ctx, "return2")
	if err != nil {
		t.Fatalf("读 return2 技能失败: %v", err)
	}
	wantPoints := strconv.FormatUint(uint64(skilltree.State{
		Cats: st.Cats, Nodes: st.Nodes, Granted: st.Granted, Level: acct.Level,
	}.Available()), 10)

	const token = "cccccccccccccccccccccccccccccccc"
	db.Tokens().Save(store.Token{Token: token, Username: "return2", UserID: int(acct.UserID),
		Expires: time.Now().Unix() + 3600})

	// StatePush 传 -1：New 会折算成 0 = 不挂机补推，测试只盯进场那两条，读数才确定
	srv := New(deps, Config{Addr: "127.0.0.1:0", StatePush: -1})
	if err := srv.Listen(); err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() { _ = srv.Close() })

	p := dialAndHandshake(t, srv, token)

	// ---- 第一条必须是 0/1，而且和真服原文一模一样（19 字节）----
	plain := p.nextData("进场推送 0/1")
	if string(plain) != string(profile.HelloPush()) {
		t.Fatalf("0/1 推送不对：\n收到=%x\n该是=%x", plain, profile.HelloPush())
	}

	// ---- 第二条是 2/1 玩家状态 ----
	plain = p.nextData("玩家状态 2/1")
	if len(plain) < 19 || plain[1] != 0xff || plain[9] != 2 || plain[10] != 1 {
		t.Fatalf("这帧不是 2/1 推送：%x", plain[:min(16, len(plain))])
	}
	// 11 字节推送头 + 7 字节参数表壳（03 | 短串"524" | 条数 u16）之后才是 obj307
	pushLen := len(plain) // 推送整帧多长，日志里要跟应答对照着看
	pushed := profileFrom(t, deps.Table, plain, 18, "2/1 推送")
	wantField(t, pushed, 0, "userid", strconv.FormatInt(acct.UserID, 10))
	wantField(t, pushed, 1, "昵称", acct.Nickname)
	wantField(t, pushed, 2, "等级", strconv.Itoa(acct.Level))
	wantField(t, pushed, 6, "可用技能点", wantPoints)
	wantField(t, pushed, 7, "银币(最小单位)", strconv.FormatInt(acct.SilverMinor, 10))
	wantField(t, pushed, 8, "金币(最小单位)", strconv.FormatInt(acct.GoldMinor, 10))

	// ---- 客户端接着问 3/1，应答里必须是同一份内容 ----
	p.ask(3, 1, 7)
	plain = p.nextData("3/1 应答")
	if plain[1] != 0xfe {
		t.Fatalf("3/1 回的 marker 不是应答：%x", plain[:min(12, len(plain))])
	}
	replied := profileFrom(t, deps.Table, plain, 9, "3/1 应答")
	wantField(t, replied, 0, "userid", strconv.FormatInt(acct.UserID, 10))
	wantField(t, replied, 1, "昵称", acct.Nickname)
	wantField(t, replied, 2, "等级", strconv.Itoa(acct.Level))
	wantField(t, replied, 6, "可用技能点", wantPoints)
	wantField(t, replied, 7, "银币(最小单位)", strconv.FormatInt(acct.SilverMinor, 10))

	t.Logf("档案对上了：return2 等级 %d 银币 %s 可用技能点 %s；2/1 推送 %d 字节、3/1 应答 %d 字节",
		acct.Level, fieldText(pushed.Fields[7].Value), wantPoints, pushLen, len(plain))
}

// TestProfileBodyLengthOnlyFollowsNickname 档案本体长度必须是「定长 247 + 昵称字节数」：
// 真服那帧 253 = 247 + len("return")。少填一个字段、或者把空串写成 null，都会让长度错位，
// 客户端解到一半就崩。这条测试用同一个函数把两个号的长度都算一遍。
func TestProfileBodyLengthOnlyFollowsNickname(t *testing.T) {
	deps, _ := testDeps(t)
	for _, nick := range []string{"return", "return2", "少爷"} {
		d := profile.Data{UserID: 1, Nickname: nick, Level: 40}
		b, err := profile.Body(deps.Table, d)
		if err != nil {
			t.Fatalf("%v", err)
		}
		want := 247 + len(nick)
		if len(b) != want {
			t.Fatalf("昵称 %q 的档案本体 %d 字节，该是 247 + %d = %d", nick, len(b), len(nick), want)
		}
	}
}
