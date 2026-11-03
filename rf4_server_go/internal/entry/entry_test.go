package entry

// 这些测试就是「逐字节对考卷」：考卷是 2026-10-31 抓真服包存下来的应答原文
// （data/rpc_table_real.json 的 fallback.<命令>.responses[0].pay）。
// Go 版拼出来的帧必须和它一模一样，差一个字节都算没过 —— 这样才敢说
// 「客户端看到的和真服是一致的」，而不是「看着差不多」。

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"rf4server/internal/assetpaths"
	"rf4server/internal/wire"
)

// realPay 取真服某条命令的应答原始字节（考卷本体在 assetpaths 里读，别再抄一份解析）
func realPay(t *testing.T, cmd string) []byte {
	t.Helper()
	pay, err := assetpaths.RealAnswer(cmd)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return pay
}

// loadTable 读协议字段表
func loadTable(t *testing.T) *wire.Table {
	t.Helper()
	p, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatalf("找不到凭据目录: %v", err)
	}
	tt, err := wire.LoadTable(p)
	if err != nil {
		t.Fatalf("读字段表失败: %v", err)
	}
	return tt
}

// mustHex 把日志里的 hex 变成字节，读不到就直接判失败
func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("测试里写的 hex 不合法: %v", err)
	}
	return b
}

// cidOf 把考卷里那条应答的 call_id 读出来。
// 拼的时候用同一个号，才能整帧逐字节比；对客户端来说 call_id 是它自己编的，
// 我们只是原号回过去。
func cidOf(pay []byte) uint32 {
	return uint32(pay[5]) | uint32(pay[6])<<8 | uint32(pay[7])<<16 | uint32(pay[8])<<24
}

// diffAt 说清第几个字节开始不一样，省得自己对两串 hex 瞪眼
func diffAt(a, b []byte) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}

// TestServerNameMatchesReal 1/1：回 "LocalServer/<客户端报的版本>"，逐字节对答案
func TestServerNameMatchesReal(t *testing.T) {
	want := realPay(t, "1/1")
	got := ServerNameReply(1, "4.0.25034")
	if !bytes.Equal(got, want) {
		i := diffAt(got, want)
		t.Fatalf("1/1 应答不一致（第 %d 字节起）：\n拼的=%x\n真服=%x", i, got, want)
	}
	t.Logf("1/1 逐字节对上真服：%d 字节 %q", len(got), got[HeadLen+1:])
}

// TestClientVersionReadFromRequest 从客户端 1/1 请求里读版本号。
// 请求原文是 2026-11-02 本地实测那帧（cid=1）。
func TestClientVersionReadFromRequest(t *testing.T) {
	tt := loadTable(t)
	plain := mustHex(t, "01ffffffff010000000101030335323401001409342e302e323530333400")
	got := ClientVersion(tt, plain)
	if got != "4.0.25034" {
		t.Fatalf("读出来的版本号是 %q，要 4.0.25034", got)
	}
	// 版本号读对了，回包就能和真服一模一样
	if !bytes.Equal(ServerNameReply(1, got), realPay(t, "1/1")) {
		t.Fatal("版本号明明读对了，回包却和真服不一致")
	}
}

// TestServerTimeIsFiletime 1/2：回的是服务器当前时间（FILETIME），不是随机会话号
func TestServerTimeIsFiletime(t *testing.T) {
	now := time.Now().UTC()
	got := ServerTimeReply(2, now)
	want := realPay(t, "1/2")
	if len(got) != len(want) {
		t.Fatalf("1/2 应答 %d 字节，真服 %d 字节", len(got), len(want))
	}
	if got[HeadLen] != 0x11 {
		t.Fatalf("1/2 应答体第一个字节应是 u64 的 marker 0x11，实际 0x%02x", got[HeadLen])
	}
	v := uint64(got[HeadLen+1]) | uint64(got[HeadLen+2])<<8 | uint64(got[HeadLen+3])<<16 |
		uint64(got[HeadLen+4])<<24 | uint64(got[HeadLen+5])<<32 | uint64(got[HeadLen+6])<<40 |
		uint64(got[HeadLen+7])<<48 | uint64(got[HeadLen+8])<<56
	back := time.Unix(0, int64((v-ticks1601to1970)*100)).UTC()
	if d := back.Sub(now); d > 5*time.Second || d < -5*time.Second {
		t.Fatalf("1/2 回的时间换算回来是 %v，和当前时间 %v 差太多", back, now)
	}
	// 真服那帧的值也该是个合理时间（抓包那天），顺手验证常量没写错
	wv := uint64(want[HeadLen+1])
	for i := uint64(1); i < 8; i++ {
		wv |= uint64(want[HeadLen+1+i]) << (8 * i)
	}
	real := time.Unix(0, int64((wv-ticks1601to1970)*100)).UTC()
	if real.Year() < 2020 || real.Year() > 2100 {
		t.Fatalf("真服 1/2 那帧换算成公历是 %v，FILETIME 换算大概算错了", real)
	}
	t.Logf("1/2 通了：回的是 %v（真服那帧是 %v）", back, real)
}

// TestEmptyArgsMatchReal 9/5 / 23/1 / 24/12：空参数表，逐字节对答案
func TestEmptyArgsMatchReal(t *testing.T) {
	cases := []struct {
		cmd  string
		code string
	}{
		{"9/5", "97"},
		{"23/1", "147"},
		{"24/12", "524"},
	}
	for _, c := range cases {
		want := realPay(t, c.cmd)
		got := EmptyArgsReply(cidOf(want), c.code)
		if !bytes.Equal(got, want) {
			i := diffAt(got, want)
			t.Errorf("%s 应答不一致（第 %d 字节起）：拼的=%x 真服=%x", c.cmd, i, got, want)
		}
	}
	t.Log("9/5 15B、23/1 16B、24/12 16B 全部逐字节对上真服")
}

// TestEmptyGUIDsMatchReal 19/6：空 GUID 数组
func TestEmptyGUIDsMatchReal(t *testing.T) {
	want := realPay(t, "19/6")
	got := EmptyGUIDsReply(cidOf(want))
	if !bytes.Equal(got, want) {
		i := diffAt(got, want)
		t.Fatalf("19/6 应答不一致（第 %d 字节起）：拼的=%x 真服=%x", i, got, want)
	}
	t.Logf("19/6 通了：%d 字节", len(got))
}

// TestStorageInfoMatchesReal 4/1：obj133 三个 u32 全 0
func TestStorageInfoMatchesReal(t *testing.T) {
	tt := loadTable(t)
	want := realPay(t, "4/1")
	got, err := StorageInfoReply(tt, cidOf(want))
	if err != nil {
		t.Fatalf("拼 4/1 失败: %v", err)
	}
	if !bytes.Equal(got, want) {
		i := diffAt(got, want)
		t.Fatalf("4/1 应答不一致（第 %d 字节起）：拼的=%x 真服=%x", i, got, want)
	}
	t.Logf("4/1 通了：%d 字节（仓库/格位先都回 0）", len(got))
}

// TestStorageInfoCapacityValues 实机容量帧：空鱼护也必须明确回 0/100，不能再发非法的 0 上限。
func TestStorageInfoCapacityValues(t *testing.T) {
	tt := loadTable(t)
	got, err := StorageInfoReplyWithCapacity(tt, 77, 0, 100, 150)
	if err != nil {
		t.Fatalf("拼容量帧失败：%v", err)
	}
	v, _, err := tt.DecodeValue(got, HeadLen)
	if err != nil {
		t.Fatalf("容量帧解不开：%v", err)
	}
	o, ok := v.(*wire.Obj)
	if !ok || o.TypeID != TypeStorageInfo {
		t.Fatalf("容量帧不是 obj133：%T", v)
	}
	want := []uint32{0, 100, 150}
	for i, n := range want {
		field, ok := o.Fields[i].Value.(wire.Num32)
		if !ok || field.V != n {
			t.Fatalf("容量 #%d = %#v，要 %d", i, o.Fields[i].Value, n)
		}
	}
}

// TestChannelInfoMatchesReal 24/3：obj402 频道信息。
// 频道名是我们自己起的，所以塞真服那个名字进去对逐字节，
// 再用我们自己的名字确认结构没变形。
func TestChannelInfoMatchesReal(t *testing.T) {
	tt := loadTable(t)
	want := realPay(t, "24/3")

	// 真服那帧的频道名（录制片段当时那位老号自己起的）
	const realName = "大哥带带我"
	v, _, err := tt.DecodeValue(want, HeadLen)
	if err != nil {
		t.Fatalf("解真服 24/3 失败: %v", err)
	}
	o, ok := v.(*wire.Obj)
	if !ok {
		t.Fatalf("24/3 应答不是对象，是 %T", v)
	}
	nameField, ok := o.FieldByName(o.Fields[1].Name)
	if !ok {
		t.Fatal("24/3 第 1 个字段读不出来")
	}
	nameVal, ok := nameField.Value.(wire.StrField)
	if !ok {
		t.Fatalf("24/3 第 1 个字段不是短字符串，是 %T", nameField.Value)
	}
	got, err := serverInfoReplyWithName(tt, cidOf(want), nameVal.S)
	if err != nil {
		t.Fatalf("拼 24/3 失败: %v", err)
	}
	if !bytes.Equal(got, want) {
		i := diffAt(got, want)
		t.Fatalf("24/3 应答不一致（第 %d 字节起）：拼的=%x 真服=%x", i, got, want)
	}

	mine, err := ServerInfoReply(tt, cidOf(want))
	if err != nil {
		t.Fatalf("拼本地频道名那版失败: %v", err)
	}
	if len(mine) == len(got) && LocalChannelName != nameVal.S {
		t.Fatalf("换了频道名长度却没变（%d 字节），说明名字没真写进帧里", len(mine))
	}
	if len(mine) != len(got)+len([]byte(LocalChannelName))-len([]byte(nameVal.S)) {
		t.Fatalf("换名字后长度不对：本地 %d，真服名 %d", len(mine), len(got))
	}
	t.Logf("24/3 结构逐字节对上真服（%d 字节）；本地版频道名 %q、%d 字节",
		len(got), LocalChannelName, len(mine))
}

// TestTeamPanelMatchesReal 28/2：战队面板三层空表
func TestTeamPanelMatchesReal(t *testing.T) {
	tt := loadTable(t)
	want := realPay(t, "28/2")
	got, err := ChannelListReply(tt, cidOf(want))
	if err != nil {
		t.Fatalf("拼 28/2 失败: %v", err)
	}
	if !bytes.Equal(got, want) {
		i := diffAt(got, want)
		t.Fatalf("28/2 应答不一致（第 %d 字节起）：拼的=%x 真服=%x", i, got, want)
	}
	t.Logf("28/2 通了：%d 字节（战队/工会/编队都是空表）", len(got))
}

// TestUnimplementedFrameMatchesReal 没实现的命令回那帧错误，和 Node 版实测的一模一样。
// 凭据：2026-11-02 19:51 本地会话里 3/18 的回帧（71 字节，cid=32）。
func TestUnimplementedFrameMatchesReal(t *testing.T) {
	want := mustHex(t, "01fdffffff20000000"+
		"200000004c6f63616c5365727665722e556e737570706f727465644f7065726174696f6e"+
		"16000000556e696d706c656d656e7465642052504320333a3138")
	got := UnimplementedReply(32, 3, 18)
	if !bytes.Equal(got, want) {
		i := diffAt(got, want)
		t.Fatalf("未实现回帧不一致（第 %d 字节起）：\n拼的=%x\n实测=%x", i, got, want)
	}
	if got[1] != 0xfd {
		t.Fatalf("错误帧 marker 应是 -3（0xfd 开头），实际 0x%02x", got[1])
	}
	t.Logf("未实现回帧逐字节对上实测：%d 字节，客户端不会干等 120 秒", len(got))
}

// TestOnewayListMatchesCredential 单向名单要和抓包凭据一致。
// 谁往名单里加/删一条而不改凭据，这条测试立刻红。
func TestOnewayListMatchesCredential(t *testing.T) {
	p, err := assetpaths.File("rpc_table_real.json")
	if err != nil {
		t.Skipf("没有历史抓包凭据，跳过单向名单对账: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("读不到历史抓包凭据，跳过单向名单对账: %v", err)
	}
	var tab struct {
		Oneway map[string]interface{} `json:"oneway"`
	}
	if err := json.Unmarshal(b, &tab); err != nil {
		t.Fatalf("解析凭据失败: %v", err)
	}
	if len(tab.Oneway) == 0 {
		t.Skip("凭据里没有 oneway 段，跳过对名单")
	}
	// 真服沉默的每条，我们要么也不吭声，要么就明确列为「客户端还在等」的例外
	awaits := ClientAwaitsReply()
	for k := range tab.Oneway {
		if awaits[k] {
			continue // 例外：真服没答，但这个客户端会等回执（见 policy.go 的说明）
		}
		if !IsOneWayBytes(k) {
			t.Errorf("凭据说 %s 是单向上报，代码里却当成要回话（也不是例外表里的）", k)
		}
	}
	// 反向：不能把凭据之外的命令偷偷加进「不回话」名单
	for k := range oneWayCommands {
		if _, ok := tab.Oneway[k]; !ok {
			t.Errorf("代码里把 %s 当单向上报，抓包凭据里却没有这条", k)
		}
	}
	// 例外表也得自证：既要在凭据里（真服确实没答过），又要真的从沉默名单里剔掉了
	for k := range awaits {
		if _, ok := tab.Oneway[k]; !ok {
			t.Errorf("例外 %s 不在真服沉默凭据里，这条例外是哪来的？", k)
		}
		if IsOneWayBytes(k) {
			t.Errorf("例外 %s 还在「不回话」名单里，等于没生效（客户端会干等 120 秒）", k)
		}
	}
	t.Logf("单向名单 %d 条 = 凭据 %d 条 - 客户端还在等的例外 %d 条",
		len(oneWayCommands), len(tab.Oneway), len(awaits))
}

// IsOneWayBytes 给测试用：直接拿 "主/子" 字符串问
func IsOneWayBytes(key string) bool { return oneWayCommands[key] }

// TestSessionIDsShapeMatchesReal 1/5：整帧必须 408 字节、9 段各 40 字符，和真服那条同形状。
// 内容我们不抄录制（那 9 串的含义还没查实，整场抓包里只在这一帧出现过），改成按账号现生成，
// 所以这里比的是形状 + 同账号稳定 + 不同账号不同，不比那 9 串的字面值。
func TestSessionIDsShapeMatchesReal(t *testing.T) {
	real := realPay(t, "1/5")
	if len(real) != SessionFrameLen {
		t.Fatalf("考卷 1/5 是 %d 字节，代码算出来的整帧长度是 %d，先确认哪个变了", len(real), SessionFrameLen)
	}
	got := SessionIDsReply(cidOf(real), "return2")
	if len(got) != len(real) {
		t.Fatalf("1/5 回帧 %d 字节，真服 %d 字节", len(got), len(real))
	}
	tbl := loadTable(t)
	v, used, err := tbl.DecodeValue(got, HeadLen)
	if err != nil {
		t.Fatalf("1/5 帧体解不开: %v", err)
	}
	if used != len(got) {
		t.Fatalf("1/5 只解到 %d 字节，整帧 %d 字节", used, len(got))
	}
	arr, ok := v.(wire.BytesArray)
	if !ok {
		t.Fatalf("1/5 帧体不是字节块数组，是 %T", v)
	}
	if len(arr.Items) != SessionIDCount {
		t.Fatalf("1/5 回了 %d 段，真服是 %d 段", len(arr.Items), SessionIDCount)
	}
	for i, it := range arr.Items {
		if it.IsNull || len(it.Data) != SessionIDLenChars {
			t.Fatalf("第 %d 段指纹串长度 %d（要 %d 字符）", i, len(it.Data), SessionIDLenChars)
		}
	}
	// 同账号两次必须一样（客户端要是拿它做缓存比对，飘了就会反复重新拉）
	again := SessionIDsReply(cidOf(real), "return2")
	if hex.EncodeToString(got) != hex.EncodeToString(again) {
		t.Fatalf("同一个号两次生成的 1/5 不一样：\n第一次=%x\n第二次=%x", got, again)
	}
	other := SessionIDsReply(cidOf(real), "return")
	if hex.EncodeToString(got) == hex.EncodeToString(other) {
		t.Fatalf("不同的号生成的 1/5 一模一样，那还怎么区分账号")
	}
}

// TestNullAndBoolReplyShape 「回一帧最小合法值」这两条的形状：整帧 10 字节、marker=-2、call_id 对上。
// 值本身只有一个字节（null=0x00 / true=0x0e / false=0x0f），多一个字节客户端都会当成残帧。
func TestNullAndBoolReplyShape(t *testing.T) {
	cases := []struct {
		name string
		got  []byte
		body byte
	}{
		{"6/2 无竞赛回 null", NullReply(11), 0x00},
		{"1/18 设备指纹回 true", BoolReply(13, true), 0x0e},
		{"布尔 false 回 0x0f", BoolReply(13, false), 0x0f},
	}
	for _, c := range cases {
		if len(c.got) != HeadLen+1 {
			t.Fatalf("%s 整帧 %d 字节，要 %d 字节", c.name, len(c.got), HeadLen+1)
		}
		if c.got[0] != 0x01 {
			t.Fatalf("%s 帧头第一字节要 0x01，实际 0x%02x", c.name, c.got[0])
		}
		m := int32(binary.LittleEndian.Uint32(c.got[1:5]))
		if m != -2 {
			t.Fatalf("%s marker 是 %d，要应答帧的 -2（给成 -3 客户端会当错误）", c.name, m)
		}
		if c.got[HeadLen] != c.body {
			t.Fatalf("%s 帧体是 0x%02x，要 0x%02x", c.name, c.got[HeadLen], c.body)
		}
	}
	// call_id 必须原样带回去：客户端按这个号对号入座
	cid := uint32(0x01020304)
	got := NullReply(cid)
	if binary.LittleEndian.Uint32(got[5:9]) != cid {
		t.Fatalf("null 回帧的 call_id 不对：%x", got[5:9])
	}
}
