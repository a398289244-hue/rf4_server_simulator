package gamesess

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"rf4server/internal/assetpaths"
	"rf4server/internal/logging"
	"rf4server/internal/skilldef"
	"rf4server/internal/staticdef"
	"rf4server/internal/store"
	"rf4server/internal/wire"
)

// testDeps 把真凭据读进来造一份依赖：账号库直接用 Node 版那份 db.json（只读），
// 再往令牌表里塞一把假钥匙，这样不用真登录也能把握手走完。
func testDeps(t *testing.T) (Deps, *store.DB) {
	t.Helper()
	tp, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatalf("找不到凭据目录: %v", err)
	}
	table, err := wire.LoadTable(tp)
	if err != nil {
		t.Fatalf("读协议字段表失败: %v", err)
	}
	dp, err := assetpaths.File("skill_defs.json")
	if err != nil {
		t.Fatalf("读技能定义表失败: %v", err)
	}
	defs, err := skilldef.Load(dp)
	if err != nil {
		t.Fatalf("读技能定义失败: %v", err)
	}
	dbp, err := assetpaths.File("db.json")
	if err != nil {
		t.Fatalf("找不到账号库: %v", err)
	}
	db, err := store.Load(dbp)
	if err != nil {
		t.Fatalf("读账号库失败: %v", err)
	}
	log, err := logging.New(filepath.Join(t.TempDir(), "logs"), false)
	if err != nil {
		t.Fatalf("日志起不来: %v", err)
	}
	t.Cleanup(log.Close)
	// 静态定义帧（1/13、28/1）也装上：定义文件不在就留 nil，
	// 那种情况下路由会回「没这条 RPC」，测试里对应的断言会自己说明原因。
	static, err := staticdef.Load(table, map[string]string{
		"1/13": "map_weather_1_13.json",
		"28/1": "region_explorer_28_1.json",
	})
	if err != nil {
		t.Logf("静态定义没装上（定义文件没准备好？）：%v", err)
		static = nil
	}
	return Deps{DB: db, Table: table, Defs: defs, Static: static, Log: log}, db
}

// realReply51 取真服那条 5/1 应答的字节长度（5522B），拿它当尺子量我们造的包。
// 凭据来源：data/rpc_table_real.json 里 fallback["5/1"].responses[0].pay。
func realReply51(t *testing.T) int {
	t.Helper()
	p, err := assetpaths.File("rpc_table_real.json")
	if err != nil {
		t.Skipf("没有历史抓包凭据，跳过技能树真服长度对账: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("读不到历史抓包凭据，跳过技能树真服长度对账: %v", err)
	}
	var tab struct {
		Fallback map[string]struct {
			Responses []struct {
				Pay string `json:"pay"` // base64
			} `json:"responses"`
		} `json:"fallback"`
	}
	if err := json.Unmarshal(b, &tab); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	rs := tab.Fallback["5/1"].Responses
	if len(rs) == 0 {
		t.Fatalf("凭据里没有 5/1")
	}
	pay, err := base64.StdEncoding.DecodeString(rs[0].Pay)
	if err != nil {
		t.Fatalf("pay 不是 base64: %v", err)
	}
	return len(pay)
}

// requestPayload 造一条客户端请求：01 | marker(-1) | call_id | main | sub | 尾巴 00。
// 请求和推送共用 11 字节那个头，区别只在尾巴那个字节（请求 00、推送 01）。
func requestPayload(main, sub byte, callID uint32) []byte {
	return append(wire.PushHead(callID, main, sub), wire.RequestTrailer)
}

// TestHandshakeThenSkillTree 把整条游戏通道走一遍：
// 36 字节 GUID -> token -> hermes -> 装弹 -> 发一条 5/1 -> 收到应答 + 2/4 推送。
// 这是「Go 版能不能顶住 TCP 这条口」的最小证据，也是以后加命令时的回归底座。
func TestHandshakeThenSkillTree(t *testing.T) {
	deps, db := testDeps(t)

	const token = "0123456789abcdef0123456789abcdef" // 32 个字符，和真钥匙同形状
	db.Tokens().Save(store.Token{Token: token, Username: "return2", UserID: 42, Expires: time.Now().Unix() + 3600})

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

	// 第一步：服务器先给 36 字节明文 GUID
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	guid := make([]byte, 36)
	if _, err := readFull(conn, guid); err != nil {
		t.Fatalf("没收到会话 GUID: %v", err)
	}
	if string(guid[8:9]) != "-" || string(guid[36:36]) != "" {
		t.Fatalf("GUID 形状不对: %q", string(guid))
	}
	// 首包这 36 字节是「服务器身份 ID」，不是玩家快照。真客户端在 connect 阶段就要对它的号：
	// 2026-11-02 22:42 实机 A/B 过 —— 发随机 UUID 客户端直接报
	// HermesTcpClient::connect: invalid response data；发这份固定 ID 才往下走（Node 版一直是它，所以从没出过事）。
	// 这份值的来历见 identity.go / identity_test.go，不是随手抄的录制快照。
	if string(guid) != ServerSessionGUID {
		t.Fatalf("首包身份 ID 不对: %q，要 %q", string(guid), ServerSessionGUID)
	}

	// 第二步：auth 段（01 00 | u32 长度 | 令牌），第三条起才是帧壳里的 hermes
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

	// 第三步：装弹之后发一条 5/1（正文先 RC4，帧头留明文）
	outStream := wire.NewRC4([]byte(token))
	if _, err := conn.Write(wire.BuildFrame(0, 7, outStream.Crypt(requestPayload(5, 1, 9)))); err != nil {
		t.Fatalf("发 5/1 失败: %v", err)
	}

	// 收：装完弹服务器先开口（0/1、2/1 两条推送），然后才是 5/1 的应答和 2/4 技能树补推。
	// 前两条是「客户端肯开始说话」的开关，缺了就等于实机那个「载入个人资料」卡住，所以一并盯住。
	rx := wire.NewFrameReader(conn)
	inStream := wire.NewRC4([]byte(token))
	var replies int
	pushKinds := map[string]int{} // 命令号 -> 收到几条，例如 "0/1"、"2/1"、"2/4"
	wantLen := realReply51(t)
	for i := 0; i < 14; i++ {
		f, err := rx.Next()
		if err != nil {
			t.Fatalf("第 %d 帧没读到: %v", i+1, err)
		}
		if f.Type == 1 || len(f.Payload) == 0 {
			if f.Type == 1 && f.Wid == 7 && replies == 0 {
				t.Logf("收到客户端 wid=7 的传输 ack，规矩守住了")
			}
			continue
		}
		plain := inStream.Crypt(f.Payload)
		env, ok := wire.ReadEnvelope(plain)
		if !ok {
			t.Fatalf("第 %d 帧解不出信封，头 12 字节=%x", i+1, plain[:min(12, len(plain))])
		}
		switch env.Marker {
		case -2:
			replies++
			if env.CallID != 9 {
				t.Fatalf("应答没对上 call_id，想要 9 实际 %d", env.CallID)
			}
			if len(plain) != wantLen {
				t.Fatalf("5/1 应答长度 %d，真服那条是 %d —— 形状不对", len(plain), wantLen)
			}
		case -1:
			key := strconv.Itoa(int(env.Main)) + "/" + strconv.Itoa(int(env.Sub))
			switch key {
			case "0/1", "2/1", "2/4":
				pushKinds[key]++
			default:
				t.Fatalf("冒出个没准备好的推送 %s，眼下这条只该见 0/1、2/1、2/4", key)
			}
		default:
			t.Fatalf("冒出个没见过的 marker=%d", env.Marker)
		}
		if replies == 1 && pushKinds["2/4"] == 1 && pushKinds["0/1"] == 1 && pushKinds["2/1"] == 1 {
			break
		}
	}
	if replies != 1 || pushKinds["2/4"] != 1 {
		t.Fatalf("5/1 应答 %d 条、2/4 补推 %d 条，应该各 1 条（收到的推送：%v）", replies, pushKinds["2/4"], pushKinds)
	}
	if pushKinds["0/1"] != 1 || pushKinds["2/1"] != 1 {
		t.Fatalf("进场先手推送没到位：%v（0/1 和 2/1 各该一条，缺了客户端就不往下问）", pushKinds)
	}
	t.Logf("握手全通：服务器先开口 0/1+2/1 都在，5/1 应答 %d 字节（和真服那条一样长），补推 2/4 也到了", wantLen)
}

// readFull 读满 len(b) 字节，读不满就把错误带出来
func readFull(r net.Conn, b []byte) (int, error) {
	total := 0
	for total < len(b) {
		n, err := r.Read(b[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// min 小助手（测试里截十六进制用，写成函数省得引包）
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
