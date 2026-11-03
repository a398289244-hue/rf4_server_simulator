package gamesess

import (
	"context"
	"encoding/hex"
	"net"
	"testing"
	"time"

	"rf4server/internal/fishing"
	"rf4server/internal/gamestore"
	"rf4server/internal/inventory"
	"rf4server/internal/store"
	"rf4server/internal/wire"
)

// TestBagListOverSocket 端到端走一遍背包清单：
// 真握手 -> 客户端发 4/5 -> 服务端回一帧「15 字节头 + 这个号自己的物件记录」。
// 期望值不是写死的常数，是同一趟里从 MySQL objects + static_item_def 现算出来的，两边必须一模一样。
// 这条通了就说明：Go 版发的背包是 return2 自己的货，一条都不借录制。
func TestBagListOverSocket(t *testing.T) {
	deps, db := testDeps(t)

	my, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没起来，跳过背包端到端测试: %v", err)
	}
	t.Cleanup(func() { _ = my.Close() })
	deps.Run = my

	// 鱼护用内存数据固定成两条：背包请求必须只回装备，鱼护请求只回点名的鱼。
	fishGUIDOne := "00112233445566778899aabbccddeeff"
	fishGUIDTwo := "ffeeddccbbaa99887766554433221100"
	fishCardOne := []byte{0x01, 0x8c, 0x09}
	keepnetStore := &fishStore{keep: []gamestore.KeepnetRow{
		{Key: "ruffe", Grams: 53, Cm: 13.1, GUID: fishGUIDOne, CardHex: "018c09"},
		{Key: "ruffe", Grams: 81, Cm: 15.0, GUID: fishGUIDTwo, CardHex: "018c0a"},
	}}
	deps.Fishing = fishing.New(deps.Table, fishing.NewCatalog(keepnetStore), keepnetStore)
	fishGUIDOneRaw, err := hex.DecodeString(fishGUIDOne)
	if err != nil {
		t.Fatalf("测试鱼 GUID 不合法: %v", err)
	}

	// 先现算一遍期望值：有物件记录、且不是容器/套装类别的，有几件就该发几件
	ctx := context.Background()
	objs, err := my.Objects(ctx, "return2")
	if err != nil {
		t.Fatalf("读 objects 失败: %v", err)
	}
	keys := make([]string, 0, len(objs))
	for _, o := range objs {
		keys = append(keys, o.ItemKey)
	}
	cats, err := my.Categories(ctx, keys)
	if err != nil {
		t.Fatalf("查类别失败: %v", err)
	}
	wantCount, wantBytes := 0, inventory.HeadLen
	for _, o := range objs {
		if cats[o.ItemKey] == inventory.CategoryContainer || o.Elem == "" {
			continue
		}
		raw, err := hex.DecodeString(o.Elem)
		if err != nil {
			t.Fatalf("%s 的物件记录不是十六进制: %v", o.ItemKey, err)
		}
		wantCount++
		wantBytes += len(inventory.StripReceipt(raw))
	}
	if wantCount == 0 {
		t.Skipf("return2 名下没有可发的装备，跳过")
	}

	const token = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	db.Tokens().Save(store.Token{Token: token, Username: "return2", UserID: 1000002, Expires: time.Now().Unix() + 3600})

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
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

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

	outStream := wire.NewRC4([]byte(token))
	request45 := containerRequest(t, 4, 5, 20, containerBackpack)
	if _, err := conn.Write(wire.BuildFrame(0, 3, outStream.Crypt(request45))); err != nil {
		t.Fatalf("发背包 4/5 失败: %v", err)
	}

	// 收：跳过 ack 和心跳，直到那条 call_id=20 的应答
	rx := wire.NewFrameReader(conn)
	inStream := wire.NewRC4([]byte(token))
	gotAnswer := false
	for i := 0; i < 8; i++ {
		f, err := rx.Next()
		if err != nil {
			t.Fatalf("第 %d 帧没读到: %v", i+1, err)
		}
		if f.Type == 1 || len(f.Payload) == 0 {
			continue
		}
		plain := inStream.Crypt(f.Payload)
		env, ok := wire.ReadEnvelope(plain)
		if !ok {
			t.Fatalf("第 %d 帧解不出信封：%x", i+1, plain[:12])
		}
		if env.Marker != -2 {
			continue // 先路过的推送，不算这条的答案
		}
		if env.CallID != 20 {
			t.Fatalf("应答没对上 call_id：想要 20，实际 %d", env.CallID)
		}
		if len(plain) != wantBytes {
			t.Fatalf("背包清单 %d 字节，按 MySQL 现算应该 %d 字节", len(plain), wantBytes)
		}
		got := int(plain[13]) | int(plain[14])<<8
		if got != wantCount {
			t.Fatalf("帧里条数 %d，应该 %d", got, wantCount)
		}
		t.Logf("背包清单端到端通了：%d 件 / %d 字节，件件都是 return2 自己名下的（objects + static_item_def 现算）", got, len(plain))
		gotAnswer = true
		break
	}
	if !gotAnswer {
		t.Fatalf("发了 8 帧也没等到背包 4/5 的应答")
	}

	// 再问一次容器 5（鱼护），只点名第一条鱼；响应里只能有鱼，不能混装备。
	fishRequest := append(containerRequest(t, 4, 5, 21, containerKeepnet), fishGUIDOneRaw...)
	if _, err := conn.Write(wire.BuildFrame(0, 4, outStream.Crypt(fishRequest))); err != nil {
		t.Fatalf("发鱼护 4/5 失败: %v", err)
	}
	for i := 0; i < 8; i++ {
		f, err := rx.Next()
		if err != nil {
			t.Fatalf("等鱼护 4/5 第 %d 帧失败: %v", i+1, err)
		}
		if f.Type == 1 || len(f.Payload) == 0 {
			continue
		}
		plain := inStream.Crypt(f.Payload)
		env, ok := wire.ReadEnvelope(plain)
		if !ok || env.Marker != -2 || env.CallID != 21 {
			continue
		}
		if len(plain) != inventory.HeadLen+len(fishCardOne) {
			t.Fatalf("鱼护清单 %d 字节，应该只含一条 %d 字节鱼卡", len(plain), len(fishCardOne))
		}
		got := int(plain[13]) | int(plain[14])<<8
		if got != 1 {
			t.Fatalf("鱼护清单条数 %d，应该 1", got)
		}
		t.Logf("容器分流端到端通了：背包只回 %d 件装备；鱼护只回点名的那条鱼", wantCount)
		return
	}
	t.Fatalf("发了 8 帧也没等到鱼护 4/5 的应答")
}

// putU32Test 小端写长度（auth 帧里那个令牌长度）
func putU32Test(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}
