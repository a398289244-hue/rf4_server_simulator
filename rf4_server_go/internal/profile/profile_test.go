package profile

// 这些测试是「Go 版拼出来的玩家档案和真服一不一样」的证据，答案全是真服原文：
//
//	3/1  应答 262 字节  <- 凭据 data/rpc_table_real.json（2026-10-31 抓真服包）
//	2/1  推送 272 字节  <- 凭据 data/state_push_2_1_real.hex（同一次抓包里挑出来的第一条状态推送）
//	0/1  推送 19  字节  <- 同一次抓包第一条，写死在下面那串 hex 里
//
// 3/1 那条是逐字节全等（连 obj308 里那七个浮点都对上了），所以档案里每个常量都不是拍脑袋。
// 2/1 那条只有三个字节位置不比：obj308#01、#03、#07 —— 真服自己两帧之间也在变的数
// （含义还没查实，看着像幸运值/心情这类，#07 是这帧状态的时刻）。区间写在下面，动一下就红。

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"rf4server/internal/assetpaths"
	"rf4server/internal/wire"
)

// loadTable 读协议字段表（凭据目录由 assetpaths 找，包自己不猜路径）
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

// replyCallID 从考卷整帧里把 call_id 抠出来（应答头第 6~9 字节），
// 客户端的 call_id 是它自己编的，测试就拿考卷那个，整帧才能逐字节比。
func replyCallID(t *testing.T, pay []byte) uint32 {
	t.Helper()
	if len(pay) < 9 {
		t.Fatalf("考卷帧只有 %d 字节，连 9 字节应答头都不够", len(pay))
	}
	return binary.LittleEndian.Uint32(pay[5:9])
}

// firstDiff 说清楚「从第几个字节开始不一样」，省得对着两串 hex 瞪眼
func firstDiff(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return min(len(a), len(b))
	}
	return -1
}

// oldTimer 真服那帧里的老号：3/1 应答和 2/1 推送里都是这位（同一个人在同一次登录里）。
// 每一项都是从真服帧里反推出来的输入值，填错了测试立刻红。
func oldTimer() Data {
	return Data{
		UserID:      7572,               // obj307#00
		Nickname:    "return",           // #01
		Level:       20,                 // #02
		SilverMinor: 75752,              // #07（757.52 银币）
		GoldMinor:   0,                  // #08
		SkillPoints: 148,                // #06 未分配技能点
		XP:          522048,             // #03 累计经验（真服自己的值，不是曲线值）
		XPInLevel:   77566,              // #04 本级内经验
		XPToNext:    222345,             // #05 升下一级所需
		StateStamp:  134379133298852359, // obj308#07 那帧的状态时刻，钉死才能逐字节比
	}
}

// TestReplyMatchesRealServerFrame 3/1：整帧（含 9 字节应答头）逐字节 == 真服那帧
func TestReplyMatchesRealServerFrame(t *testing.T) {
	tbl := loadTable(t)
	real, err := assetpaths.RealAnswer("3/1")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(real) != 262 {
		t.Fatalf("考卷 3/1 是 %d 字节，和交接记录里的 262 不一致，先确认凭据有没有被改", len(real))
	}
	got, err := Reply(tbl, replyCallID(t, real), oldTimer())
	if err != nil {
		t.Fatalf("拼 3/1 应答失败：%v", err)
	}
	if !bytes.Equal(got, real) {
		i := firstDiff(got, real)
		t.Fatalf("3/1 帧体和真服不一致：我这边 %d 字节，真服 %d 字节，第 %d 字节起不同\n我这边=%s\n真服  =%s",
			len(got), len(real), i, hex.EncodeToString(got), hex.EncodeToString(real))
	}
}

// TestBodyLengthIsStable 档案本体必须正好 253 字节：
// 真服 3/1 = 9 头 + 253，2/1 = 11 头 + 7 参数表壳 + 253 + 1 尾巴。
// 少一个字段（比如把空串发成 null）长度就会变，这里先当场抓住。
func TestBodyLengthIsStable(t *testing.T) {
	tbl := loadTable(t)
	body, err := Body(tbl, oldTimer())
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(body) != 253 {
		t.Fatalf("档案本体 %d 字节，真服是 253 字节（少了什么字段？还是空串写成 null 了？）", len(body))
	}
}

// TestHelloPushBytes 0/1：进场第一条推送，真服原文 19 字节
func TestHelloPushBytes(t *testing.T) {
	const realHex = "01ffffffff0100000000010303353234000001"
	got := hex.EncodeToString(HelloPush())
	if got != realHex {
		t.Fatalf("0/1 推送帧不对：\n我这边=%s\n真服  =%s", got, realHex)
	}
}

// 真服 2/1 那帧里「自己会动」的四个位置（从帧头数起的字节区间），见文件头说明。
// 这不是猜的：把那 159 条 2/1 推送逐条拆出来数过（脚本结果在 D:\rf4log\real_push_variability.txt）——
//
//	obj308#01 float：87 个不同值，一直往下掉（3/1 那帧 0.23833，第一条 2/1 是 0.23676）
//	obj308#03 float：10 个不同值，同样在往下掉
//	obj308#07 FILETIME：159 条全不一样 -> 就是「这帧状态是什么时候的」，我们发当前时间
//	obj322#00 u32  ：一路往上涨（10905 起，最大 12030；3/1 那帧是 10901），含义待查
//
// 同一段统计里，#02 等级 159 条全是 20、银币从 75752 跳到 77342（他卖了鱼），
// 这两条正好反过来证明：#02/#07 这类「账号数据」是真按这个号发的，我们必须在帧里填自己的值。
//
// 偏移算法：11 推送头 + 7 参数表壳 = 18 起步，物件头 5，标量字段按类表顺序排下来，
// obj308 本体从 98 开始（marker 01 + typeID 4），它自己的 #01 落在 102、#03 落在 110、#07 落在 126；
// obj322#00 落在 143。
var realPushDynamicRanges = [][2]int{
	{102, 106}, // obj308#01
	{110, 114}, // obj308#03
	{126, 134}, // obj308#07 状态时刻
	{143, 147}, // obj322#00
}

// TestStatePushMatchesRealPushFrame 2/1：拿真服那条推送逐字节比（上面那三段除外）。
// 这条同时验证了两件事：档案字段填得对、推送那层外壳（11 字节头 + 参数表"524" + 尾巴 01）拼得对。
func TestStatePushMatchesRealPushFrame(t *testing.T) {
	tbl := loadTable(t)
	path, err := assetpaths.File("state_push_2_1_real.hex")
	if err != nil {
		t.Fatalf("找不到凭据目录：%v", err)
	}
	rawHex, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("没有 data/state_push_2_1_real.hex 这份凭据，这条先不跑：%v", err)
	}
	real, err := hex.DecodeString(strings.TrimSpace(string(rawHex)))
	if err != nil {
		t.Fatalf("凭据 hex 读不动：%v", err)
	}
	if len(real) != 272 {
		t.Fatalf("真服那条 2/1 是 %d 字节，预期 272，先确认凭据没被改", len(real))
	}

	d := oldTimer()
	d.StateStamp = 134379133438456975 // 这条推送自己的状态时刻
	got, err := StatePush(tbl, d)
	if err != nil {
		t.Fatalf("拼 2/1 推送失败：%v", err)
	}
	if len(got) != len(real) {
		t.Fatalf("推送长度不对：我这边 %d，真服 %d\n我这边=%s\n真服  =%s",
			len(got), len(real), hex.EncodeToString(got), hex.EncodeToString(real))
	}
	// 那三段以外，必须一个字节都不差
	skip := map[int]bool{}
	for _, r := range realPushDynamicRanges {
		for i := r[0]; i < r[1]; i++ {
			skip[i] = true
		}
	}
	for i := range got {
		if skip[i] {
			continue
		}
		if got[i] != real[i] {
			t.Fatalf("第 %d 字节不同：我这边 0x%02x，真服 0x%02x\n我这边=%s\n真服  =%s",
				i, got[i], real[i], hex.EncodeToString(got), hex.EncodeToString(real))
		}
	}
	// 那三段以外不比，但这三段的值也得能自证：状态时刻必须是我们钉进去的那个
	if stamp := binary.LittleEndian.Uint64(got[126:134]); stamp != d.StateStamp {
		t.Fatalf("obj308#07 状态时刻写成了 %d，要 %d", stamp, d.StateStamp)
	}
}

// TestXpDefaultsFollowCurve 经验三个数留空时要按等级曲线补，而且三个数得自洽：
// 累计经验 = 到本级为止的门槛，本级内 = 0，下一级 = 下一级增量。
// （注意：真服那两个数是玩家真实值，和曲线对不上，见 curve.go 开头那段丑话。）
func TestXpDefaultsFollowCurve(t *testing.T) {
	tbl := loadTable(t)
	o, err := Build(tbl, Data{UserID: 1, Nickname: "x", Level: 40, SkillPoints: 100})
	if err != nil {
		t.Fatalf("%v", err)
	}
	want := map[int]uint64{3: BaseXP(40), 4: 0, 5: NextLevelReq(40)}
	for idx, expect := range want {
		got, ok := o.Fields[idx].Value.(wire.Num64)
		if !ok {
			t.Fatalf("档案第 %d 个字段不是 u64，是 %T", idx, o.Fields[idx].Value)
		}
		if got.V != expect {
			t.Fatalf("40 级第 %d 个字段（%s）写成 %d，该是 %d", idx, o.Fields[idx].Name, got.V, expect)
		}
	}
	if BaseXP(40) != 106000000 || NextLevelReq(40) != 22000000 {
		t.Fatalf("40 级门槛变了：baseXp=%d nextLevelReq=%d（曲线表被动过？）", BaseXP(40), NextLevelReq(40))
	}
}

// TestLevelXPTableMatchesCredential 等级经验表必须和凭据 level_xp_req.json 一字不差。
// 这份表是从 glance522 那份开源后端搬过来的，和真服自己那两帧的数字对不上（档案里那三个
// 经验数字真服是按玩家真实值发的），所以它只当「显示用的门槛表」，别再当真理。
func TestLevelXPTableMatchesCredential(t *testing.T) {
	path, err := assetpaths.File("level_xp_req.json")
	if err != nil {
		t.Fatalf("找不到凭据目录：%v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("没有 data/level_xp_req.json 这份凭据，这条先不跑：%v", err)
	}
	var want []uint64
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("读等级经验表失败：%v", err)
	}
	if len(want) != len(levelXPReq) {
		t.Fatalf("凭据有 %d 项，代码里是 %d 项", len(want), len(levelXPReq))
	}
	for i := range want {
		if want[i] != levelXPReq[i] {
			t.Fatalf("第 %d 项不一样：凭据 %d，代码 %d", i, want[i], levelXPReq[i])
		}
	}
}

// TestSkillPointsFavourDatabase 技能点以库为准：return2 的发放点是少爷手动改成 100 的，
// 按等级曲线算只有 72。这条测试就是「别拿曲线值覆盖库里值」这条规矩的提醒牌。
func TestSkillPointsFavourDatabase(t *testing.T) {
	if SkillPointsTotal(40) != 72 {
		t.Fatalf("40 级按曲线该发 %d 点，和交接记录里的 72 不一致", SkillPointsTotal(40))
	}
	d := Data{Level: 40, SkillPoints: 100}.withDefaults()
	if d.SkillPoints != 100 {
		t.Fatalf("库里的 100 点被改成了 %d，档案这边不该动它", d.SkillPoints)
	}
}
