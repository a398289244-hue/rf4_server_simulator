package fishing

// 这些测试是「Go 版拼出来的钓鱼帧和真服一不一样」的证据，答案全是真服原文：
//
//	14/1  应答 54B   <- 凭据 data/rpc_table_real.json（-cmd 14/1）
//	14/4  应答 325B  <- 同上（那条是「没钓上来」那次）
//	14/5  应答 174B  <- 同上（那条是 53 克的 ruffe_n）
//	14/14 推送 317B  <- 凭据 data/fish_setup_push_14_14.hex（2026-10-31 那次抓包里的咬钩推送）
//	14/1 / 14/5 请求 <- 同一次会话的原始请求帧，hex 抄在下面当输入
//
// 三条应答 + 一条推送都是逐字节比过的，所以拼帧里那些常量没一个是拍的。

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"rf4server/internal/assetpaths"
	"rf4server/internal/gamestore"
	"rf4server/internal/profile"
	"rf4server/internal/wire"
)

// loadTable 读协议字段表（凭据目录由 assetpaths 找，包自己猜路径会走偏）
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
// 测试拿考卷那个号，整帧才可能逐字节相同。
func replyCallID(t *testing.T, pay []byte) uint32 {
	t.Helper()
	if len(pay) < 9 {
		t.Fatalf("考卷帧只有 %d 字节，连应答头都不够", len(pay))
	}
	return binary.LittleEndian.Uint32(pay[5:9])
}

// firstDiff 说清「从第几个字节开始不一样」
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

// mustHex 把测试里那几串真服原文解成字节
func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatalf("测试里这串 hex 不合法: %v", err)
	}
	return b
}

// realSampleFish 真服那条 317B 咬钩推送里的全部输入值（解剖全文 D:\rf4log\dump_14_14_new.txt）。
// 每一项都是从那帧里读出来的，填错了逐字节测试立刻红。
func realSampleFish() Fish {
	return Fish{
		Key:       "ruffe_n",                          // obj27#01
		Grams:     53,                                 // obj27#04
		LengthM:   0.1314699947834015,                 // obj27#03，和 14/5 那条鱼卡片里同一个数
		Class:     ClassMedium,                        // obj27#02 = 2
		Stamina:   126.56612396240234,                 // obj27#05：这格真服按鱼种给，只能照抄
		SetupGUID: "eb2652ce7227a04ca9774d951b7a729c", // obj27#00，客户端 keep 时原样回显的就是它
		ModelGUID: "6b1783de0e14f04ebaf6127f5ed61bd0", // obj27#23
	}
}

// realSampleGear 那帧里参数表"524"的第一项：那位老号当时用的钓具 GUID
const realSampleGear = "f1289f009f09364395075c15c8655ed5"

// TestBitePushMatchesRealServerFrame 14/14：整帧 317 字节逐字节 == 真服那帧。
// 顺带证明一件事：拉力(#08)是按体重线性现算的（0.0098067 每克），
// 在 53 克这个点上和真服逐字节一致；体力池(#05)那条按鱼种给，只能当输入抄进来。
// 那个「两个搏斗行为组」的数组也是照原样拼出来的。
func TestBitePushMatchesRealServerFrame(t *testing.T) {
	tbl := loadTable(t)
	path, err := assetpaths.File("fish_setup_push_14_14.hex")
	if err != nil {
		t.Skipf("没有咬钩推送真实帧，跳过逐字节校验: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("读不到咬钩推送真实帧，跳过逐字节校验: %v", err)
	}
	real, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("咬钩推送凭据不是合法 hex：%v", err)
	}
	if len(real) != 317 {
		t.Fatalf("凭据是 %d 字节，和交接记录里的 317 不一致，先确认有没有被改", len(real))
	}
	got, err := SetupPush(tbl, realSampleGear, realSampleFish())
	if err != nil {
		t.Fatalf("拼咬钩推送失败：%v", err)
	}
	if d := firstDiff(got, real); d >= 0 || len(got) != len(real) {
		t.Fatalf("14/14 整帧没对上：长度 我%d/真服%d，第一个差异在第 %d 字节\n我  %x\n真服 %x",
			len(got), len(real), d, got, real)
	}
	if f := realSampleFish(); f.Force != 0 {
		t.Fatalf("样本里拉力(#08)那格要留空，让这边按体重现算，这条测试才算验到比例律")
	}
}

// TestBitePushScalesWithWeight 体力池和拉力得跟着体重走：
// 同一根竿上 1250 克的鱼要是和 63 克一个手感，那搏斗就没意义了。
func TestBitePushScalesWithWeight(t *testing.T) {
	tbl := loadTable(t)
	small, err := SetupBody(tbl, Fish{Key: "ruffe", Grams: 63, LengthM: 0.13, Class: ClassMedium, SetupGUID: realSampleGear})
	if err != nil {
		t.Fatalf("%v", err)
	}
	big, err := SetupBody(tbl, Fish{Key: "carp", Grams: 12500, LengthM: 0.82, Class: ClassTrophy, SetupGUID: realSampleGear})
	if err != nil {
		t.Fatalf("%v", err)
	}
	for _, idx := range []int{5, 8} {
		a, b := wire.BitsF32(small.Num(idx)), wire.BitsF32(big.Num(idx))
		if !(a < b) {
			t.Fatalf("字段 #%02d 没随体重涨：63 克=%v，12500 克=%v", idx, a, b)
		}
	}
}

// TestCastReplyMatchesCredential 14/1：应答整帧逐字节 == 真服那 54B（钓具 GUID 用真服那个）
func TestCastReplyMatchesCredential(t *testing.T) {
	tbl := loadTable(t)
	real, err := assetpaths.RealAnswer("14/1")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(real) != 54 {
		t.Fatalf("考卷 14/1 是 %d 字节，和记录的 54 不一致", len(real))
	}
	got, err := CastReply(tbl, replyCallID(t, real), realSampleGear)
	if err != nil {
		t.Fatalf("拼 14/1 应答失败：%v", err)
	}
	if d := firstDiff(got, real); d >= 0 {
		t.Fatalf("14/1 整帧没对上，第一个差异在第 %d 字节\n我  %x\n真服 %x", d, got, real)
	}
}

// TestKeepReplyMatchesCredential 14/5：应答整帧逐字节 == 真服那 174B。
// 输入全是真服那条里的值（53 克的 ruffe_n、worm_01、level_002_kirzah_river），
// 所以 obj25 那 29 个字段每个填错都会红。
func TestKeepReplyMatchesCredential(t *testing.T) {
	tbl := loadTable(t)
	real, err := assetpaths.RealAnswer("14/5")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(real) != 174 {
		t.Fatalf("考卷 14/5 是 %d 字节，和记录的 174 不一致", len(real))
	}
	card, err := CardRecord(tbl, CardInput{
		Key:            "ruffe_n",                       // #00
		Grams:          53,                              // #01
		LengthM:        0.1314699947834015,              // #02
		Class:          ClassMedium,                     // #03 = 2
		HookedFiletime: 134379144495635177,              // #05/#23 那个 FILETIME
		GameDay:        98626, GameHour: 0, GameMin: 19, // #06 那个游戏内时间
		BaitKey:   "worm_01",                          // #08
		Place:     "level_002_kirzah_river",           // #10
		SetupGUID: "eb2652ce7227a04ca9774d951b7a729c", // #16
	})
	if err != nil {
		t.Fatalf("拼鱼卡片失败：%v", err)
	}
	got, err := KeepReply(tbl, replyCallID(t, real), card)
	if err != nil {
		t.Fatalf("拼 14/5 应答失败：%v", err)
	}
	if d := firstDiff(got, real); d >= 0 {
		t.Fatalf("14/5 整帧没对上，第一个差异在第 %d 字节\n我  %x\n真服 %x", d, got, real)
	}
}

// TestEndReplyMatchesCredential 14/4：走「没钓上来」那个形状。判法比「整帧一个字节都不能差」讲得清：
//
//	obj801 这层外壳（应答头 + 十一个字段的位置和形状）必须和真服那条 325B 逐字节一样；
//	中间那个 obj307 档案允许有几个字节不同 —— 这条帧比 3/1 那份考卷晚 45 秒，
//	真服自己那两个浮点和状态时刻都在动（实测统计过：159 条推送里 #07 159 个不同值）。
//
// 档案里每个字段填得对不对有 internal/profile 的逐字节测试盯着，两边一夹就够了。
func TestEndReplyMatchesCredential(t *testing.T) {
	tbl := loadTable(t)
	real, err := assetpaths.RealAnswer("14/4")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(real) != 325 {
		t.Fatalf("考卷 14/4 是 %d 字节，和记录的 325 不一致", len(real))
	}
	prof := profile.Data{
		UserID: 7572, Nickname: "return", Level: 20,
		SilverMinor: 75752, GoldMinor: 0, SkillPoints: 148,
		XP: 522048, XPInLevel: 77566, XPToNext: 222345,
		// 状态时刻钉成真服那帧的，差异才会只剩「真服自己会动」那几格
		StateStamp: 134379133298852359,
	}
	got, err := EndReply(tbl, replyCallID(t, real), EndInput{Profile: prof})
	if err != nil {
		t.Fatalf("拼 14/4 应答失败：%v", err)
	}
	if len(got) != len(real) {
		t.Fatalf("长度就不对：我 %d，真服 %d\n我  %x\n真服 %x", len(got), len(real), got, real)
	}
	// 「物件标记 01 + u32 类型号 307」就是档案的开头，拿它把两帧切成三段比
	marker := []byte{0x01, 0x33, 0x01, 0x00, 0x00}
	mine, theirs := bytesIndex(got, marker), bytesIndex(real, marker)
	if mine < 0 || theirs < 0 {
		t.Fatalf("两帧里都没找到档案 obj307 的开头（我 %d 真服 %d）", mine, theirs)
	}
	if mine != theirs {
		t.Fatalf("档案起始位置不一样：我 %d 真服 %d，说明 obj801 前面几个字段拼歪了", mine, theirs)
	}
	if d := firstDiff(got[:mine], real[:theirs]); d >= 0 {
		t.Fatalf("obj801 外壳没对上，第一个差异在第 %d 字节\n我  %x\n真服 %x", d, got[:mine], real[:theirs])
	}
	profObj, err := profile.Build(tbl, prof)
	if err != nil {
		t.Fatalf("单独拼一份档案对照失败：%v", err)
	}
	pb, err := profObj.Encode()
	if err != nil {
		t.Fatalf("档案编码失败：%v", err)
	}
	profLen := len(pb) // 档案本体多长直接现量，不写魔数
	if profLen != 247+len(prof.Nickname) {
		t.Fatalf("档案本体 %d 字节，和「247 + 昵称字节数」这条实测规律不符", profLen)
	}
	if d := firstDiff(got[mine+profLen:], real[theirs+profLen:]); d >= 0 {
		t.Fatalf("档案后面那几个字段（#08/#09/#10）没对上，第一个差异在第 %d 字节", d)
	}
	// 档案区：开头那 60 字节是「我是谁 + 等级 + 经验 + 技能点 + 银币」（obj307 前面那几个字段），
	// 必须逐字节一样 —— 这才说明账号数据真的灌进了嵌套的 obj307，而不是只填了外层。
	if d := firstDiff(got[mine:mine+60], real[theirs:theirs+60]); d >= 0 {
		t.Fatalf("档案开头那几个身份/钱包字段没对上，档案区内第 %d 字节：我 %02x 真服 %02x",
			d, got[mine+d], real[theirs+d])
	}
	var diffs []int
	for i := mine + 60; i < mine+profLen; i++ {
		if got[i] != real[i] {
			diffs = append(diffs, i)
		}
	}
	// 剩下的差异只允许来自那几格「真服自己两帧之间也在动」的数：
	// obj308 的两个浮点 + 状态时刻(FILETIME) + obj322 那个一路在涨的数，加起来 4+4+8+4 = 20 字节
	if len(diffs) > 24 {
		t.Fatalf("档案区差异太多（%d 个字节），不像只是那几个会动的数：%v", len(diffs), diffs)
	}
	t.Logf("14/4：obj801 外壳 + 档案身份/钱包段逐字节 == 真服；剩下 %d 个字节不同（第 %d~%d 字节），"+
		"就是实测过的那几个真服自己会动的数", len(diffs), diffs[0], diffs[len(diffs)-1])
}

// bytesIndex 在 haystack 里找 needle 第一次出现的位置（没有就 -1）
func bytesIndex(haystack, needle []byte) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		ok := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

// TestGearGUIDParsesRealRequests 14/1 / 14/5 的请求偏移是从真服原帧量的，
// 用原帧当输入验一遍，以后谁改偏移立刻红。
func TestGearGUIDParsesRealRequests(t *testing.T) {
	// 真服 14/1（36B）：钓具 GUID 从第 19 字节开始
	req1 := mustHex(t, "01ffffffff250000000e01030335323401000cf1289f009f09364395075c15c8655ed500")
	got, ok := GearGUID(req1)
	if !ok || got != "f1289f009f09364395075c15c8655ed5" {
		t.Fatalf("14/1 的钓具 GUID 没读对：%q ok=%v", got, ok)
	}
	// 真服 14/5（53B）：钓具 GUID + 鱼 GUID
	req5 := mustHex(t, "01ffffffffc70500000e05030335323402000cf1289f009f09364395075c15c8655ed50ceb2652ce7227a04ca9774d951b7a729c00")
	gear, fish, ok := KeepGUIDs(req5)
	if !ok {
		t.Fatalf("14/5 读失败了")
	}
	if gear != "f1289f009f09364395075c15c8655ed5" {
		t.Fatalf("14/5 钓具 GUID 读成 %q", gear)
	}
	if fish != "eb2652ce7227a04ca9774d951b7a729c" {
		t.Fatalf("14/5 鱼 GUID 读成 %q", fish)
	}
	// 短帧不许 panic
	if _, ok := GearGUID(req1[:10]); ok {
		t.Fatal("帧这么短还说读着了")
	}
	if _, _, ok := KeepGUIDs(req5[:20]); ok {
		t.Fatal("帧这么短还说读着了")
	}
}

// ---- 下面是「出鱼」这一块的测试，不连库，靠假数据 ----

// fakeStore 内存版数据入口：两张静态表塞几条假鱼种，写库就记下来。
type fakeStore struct {
	weights map[string]gamestore.SpeciesWeight
	rosters map[string][]string
	keep    []gamestore.KeepnetRow
	recs    []gamestore.FishRecord
	objs    []gamestore.Object
	failOn  string // "keepnet" / "record"：让某一步写失败，验「写库失败不拦游戏」
}

func (f *fakeStore) SpeciesWeights(ctx context.Context) (map[string]gamestore.SpeciesWeight, error) {
	return f.weights, nil
}
func (f *fakeStore) Roster(ctx context.Context) (map[string][]string, error) { return f.rosters, nil }
func (f *fakeStore) SaveKeepnet(ctx context.Context, u string, row gamestore.KeepnetRow) error {
	if f.failOn == "keepnet" {
		return errors.New("库里 keepnets 这张表写不进去")
	}
	f.keep = append(f.keep, row)
	return nil
}
func (f *fakeStore) Keepnet(ctx context.Context, u string) ([]gamestore.KeepnetRow, error) {
	return append([]gamestore.KeepnetRow(nil), f.keep...), nil
}
func (f *fakeStore) InsertFishRecord(ctx context.Context, r gamestore.FishRecord) error {
	if f.failOn == "record" {
		return errors.New("库里 fish_records 这张表写不进去")
	}
	f.recs = append(f.recs, r)
	return nil
}
func (f *fakeStore) Account(ctx context.Context, u string) (gamestore.Account, error) {
	// 渔获流水里那一格要的是数字 id，给个固定的就行（真库里 return2 是 1000002）
	return gamestore.Account{Username: u, UserID: 1000002, Nickname: u}, nil
}
func (f *fakeStore) Objects(ctx context.Context, u string) ([]gamestore.Object, error) {
	return f.objs, nil
}

// ruffeLike 一条和库里 ruffe 一样的假鱼种，够用就行
func ruffeLike() gamestore.SpeciesWeight {
	return gamestore.SpeciesWeight{
		FishKey: "ruffe", MinG: 20, MaxG: 300, TrophyLineG: 140, SuperTrophyLineG: 220,
		LengthK: 0.0355, LengthP: 1.0 / 3.0, HasRange: true,
	}
}

func newFakeCat() (*Catalog, *fakeStore) {
	fs := &fakeStore{
		weights: map[string]gamestore.SpeciesWeight{
			"ruffe":   ruffeLike(),
			"carp":    {FishKey: "carp", MinG: 500, MaxG: 12000, TrophyLineG: 4000, SuperTrophyLineG: 8000, LengthK: 0.0355, LengthP: 1.0 / 3.0, HasRange: true},
			"ruffe_n": {FishKey: "ruffe_n", TrophyLineG: 250, SuperTrophyLineG: 350, LengthK: 0.0355, LengthP: 1.0 / 3.0}, // 库里这种没范围的
		},
		rosters: map[string][]string{
			"level_000_home": {"ruffe", "ruffe_n"},
			"level_014_carp": {"carp"},
		},
	}
	cat := NewCatalog(fs)
	return cat, fs
}

// TestPickerStaysInsideRoster 出鱼只能出这个水域名册里的，而且体重必须落在库里给的范围里。
func TestPickerStaysInsideRoster(t *testing.T) {
	cat, _ := newFakeCat()
	if _, _, err := cat.Reload(context.Background()); err != nil {
		t.Fatalf("装鱼种表失败：%v", err)
	}
	p := NewPicker(cat, "level_000_home", "", rand.New(rand.NewSource(7)))
	seen := map[string]int{}
	for i := 0; i < 200; i++ {
		f, ok := p.Pick()
		if !ok {
			t.Fatalf("第 %d 次配不出鱼", i)
		}
		if f.Key != "ruffe" {
			t.Fatalf("名册里只有 ruffe 有体重范围，结果出了 %q", f.Key)
		}
		if f.Grams < 20 || f.Grams > 300 {
			t.Fatalf("体重 %d 克跑到了库里范围 [20,300] 外面", f.Grams)
		}
		if f.LengthM <= 0 {
			t.Fatalf("体长算成了 %v", f.LengthM)
		}
		if f.SetupGUID == "" || f.ModelGUID == "" || len(f.SetupGUID) != 32 {
			t.Fatalf("GUID 不对：setup=%q model=%q", f.SetupGUID, f.ModelGUID)
		}
		if f.Class < ClassSmall || f.Class > ClassTrophy {
			t.Fatalf("大小档算成了 %d", f.Class)
		}
		seen[f.SetupGUID]++
	}
	if len(seen) < 150 {
		t.Fatalf("200 次出鱼只有 %d 个不同 GUID，GUID 是不是复用过头了", len(seen))
	}
}

// TestPickerForcedAndEmptyRoster 指定鱼种必须出那种；指定水域但没名册时宁可不发，也不能串地区。
func TestPickerForcedAndEmptyRoster(t *testing.T) {
	cat, _ := newFakeCat()
	if _, _, err := cat.Reload(context.Background()); err != nil {
		t.Fatalf("%v", err)
	}
	p := NewPicker(cat, "level_000_home", "carp", rand.New(rand.NewSource(1)))
	for i := 0; i < 20; i++ {
		f, ok := p.Pick()
		if !ok || f.Key != "carp" {
			t.Fatalf("指定了 carp 却出 %q ok=%v", f.Key, ok)
		}
	}
	// 名册里只有没体重范围的鱼：不能配不出来，也不能 panic
	cat2, fs2 := newFakeCat()
	fs2.rosters = map[string][]string{"only_null": {"ruffe_n"}}
	if _, _, err := cat2.Reload(context.Background()); err != nil {
		t.Fatalf("%v", err)
	}
	p2 := NewPicker(cat2, "only_null", "", rand.New(rand.NewSource(3)))
	if f, ok := p2.Pick(); !ok || f.Key != "ruffe_n" {
		t.Fatalf("只有没范围的鱼时应该出它，结果 %q ok=%v", f.Key, ok)
	}
	// 指定了水域名却没有该水域名册：必须明确返回没鱼，不能从全鱼种乱抓。
	p3 := NewPicker(cat2, "level_missing_roster", "", rand.New(rand.NewSource(4)))
	if f, ok := p3.Pick(); ok {
		t.Fatalf("水域没名册却从全鱼种出了 %q，地区隔离失效", f.Key)
	}
	// 只有 loc 明确为空才是全鱼种模式。
	if f, ok := NewPicker(cat2, "", "", rand.New(rand.NewSource(5))).Pick(); !ok {
		t.Fatal("未绑定水域的全鱼种模式应该能出鱼")
	} else if f.Key != "ruffe" && f.Key != "carp" && f.Key != "ruffe_n" {
		t.Fatalf("全鱼种模式出了未知鱼 %q", f.Key)
	}
	// 鱼种表整个是空的：必须明确说「配不出来」，让日志能说清为什么没鱼
	empty := NewCatalog(&fakeStore{})
	if _, ok := NewPicker(empty, "", "", rand.New(rand.NewSource(6))).Pick(); ok {
		t.Fatal("鱼种表空的还说能配出鱼")
	}
}

// TestPickerUsesLiveLocationRosters 用当前 MySQL 的 19 片水域逐片抽鱼，
// 每条都必须落在该水域名册里，证明运行时的地区限制不是测试假数据写出来的。
func TestPickerUsesLiveLocationRosters(t *testing.T) {
	db, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没起来，跳过实库水域名册测试: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	cat := NewCatalog(db)
	if _, _, err := cat.Reload(ctx); err != nil {
		t.Fatalf("装实库鱼种/名册失败: %v", err)
	}
	rosters, err := db.Roster(ctx)
	if err != nil {
		t.Fatalf("读 MySQL 水域名册失败: %v", err)
	}
	for i, loc := range cat.Locs() {
		allowed := map[string]bool{}
		for _, key := range rosters[loc] {
			allowed[key] = true
		}
		p := NewPicker(cat, loc, "", rand.New(rand.NewSource(int64(100+i))))
		for n := 0; n < 20; n++ {
			f, ok := p.Pick()
			if !ok {
				t.Fatalf("%s 实库里配不出鱼（名册 %d 种）", loc, len(allowed))
			}
			if !allowed[f.Key] {
				t.Fatalf("%s 抽到了名册外的 %q", loc, f.Key)
			}
		}
	}
	t.Logf("真实 MySQL：%d 片水域全部只从各自名册出鱼", len(cat.Locs()))
}

// TestSizeClassBoundaries 大小档的门槛（奖杯线、特大线）必须按库里的数来
func TestSizeClassBoundaries(t *testing.T) {
	// 和库里 ruffe 那行一样的域内定义：min 20 / 奖杯线 140 / 特大线 220
	sp := Species{Key: "ruffe", MinG: 20, MaxG: 300, TrophyLineG: 140, SuperTrophyLineG: 220,
		LengthK: 0.0355, LengthP: 1.0 / 3.0, HasRange: true}
	cases := []struct {
		grams int64
		want  byte
	}{
		{10, ClassSmall},
		{55, ClassSmall},  // 20 + (140-20)*0.3 = 56，55 还差一点
		{56, ClassMedium}, // 刚好到中档
		{139, ClassMedium},
		{140, ClassBig},
		{219, ClassBig},
		{220, ClassTrophy},
		{5000, ClassTrophy},
	}
	for _, c := range cases {
		if got := SizeClass(sp, c.grams); got != c.want {
			t.Errorf("%d 克应该算 %d 档，算成了 %d", c.grams, c.want, got)
		}
	}
	// 真服那条 63 克的 ruffe 标的是中档，按这条规则也得是中档
	if got := SizeClass(sp, 63); got != ClassMedium {
		t.Errorf("63 克对不上真服那条：真服 2，我们 %d", got)
	}
}

// TestTrophyFlagFollowsClass 鱼卡片 #07 那个 flag = 「中档以上」，和真服那帧互相对得上
func TestTrophyFlagFollowsClass(t *testing.T) {
	for class, want := range map[byte]byte{
		ClassSmall: 0, ClassMedium: 1, ClassBig: 1, ClassTrophy: 1,
	} {
		if got := TrophyFlag(class); got != want {
			t.Errorf("%d 档的 flag 应该是 %d，成了 %d", class, want, got)
		}
	}
}

// TestKeepWritesBothTables 留鱼必须同时写鱼护和流水，且应答里有鱼卡片。
func TestKeepWritesBothTables(t *testing.T) {
	tbl := loadTable(t)
	cat, fs := newFakeCat()
	if _, _, err := cat.Reload(context.Background()); err != nil {
		t.Fatalf("%v", err)
	}
	fs.objs = []gamestore.Object{
		{GUID: realSampleGear, Username: "return2", ItemKey: "spin_10032_S78MH", Elem: "0102"},
		{GUID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Username: "return2", ParentGUID: realSampleGear, ItemKey: "mono_15063", Elem: "0304"},
		{GUID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Username: "return2", ItemKey: "mono_15092", Elem: "0506"}, // 背包里另一件，不该推
	}
	svc := New(tbl, cat, fs)
	f := Fish{Key: "ruffe", Grams: 53, LengthM: 0.1314, Class: ClassMedium, SetupGUID: "eb2652ce7227a04ca9774d951b7a729c"}
	res, err := svc.Keep(context.Background(), KeepInput{
		CallID: 42, Username: "return2", GearGUID: realSampleGear, FishGUID: f.SetupGUID, Fish: f,
		Place: "level_000_home", Bait: "worm_01",
	})
	if err != nil {
		t.Fatalf("留鱼失败：%v", err)
	}
	if len(res.Reply) == 0 {
		t.Fatal("应答是空的")
	}
	if !res.Saved || res.SaveErr != "" {
		t.Fatalf("说好了入库，结果 Saved=%v 原因=%q", res.Saved, res.SaveErr)
	}
	if len(fs.keep) != 1 || fs.keep[0].Key != "ruffe" || fs.keep[0].Grams != 53 {
		t.Fatalf("鱼护写的内容不对：%+v", fs.keep)
	}
	if !strings.Contains(fs.keep[0].GUID, "-") {
		t.Fatalf("鱼护里的 GUID 该是带横线那种给人看的写法：%q", fs.keep[0].GUID)
	}
	if len(fs.recs) != 1 || fs.recs[0].Key != "ruffe" {
		t.Fatalf("流水写的内容不对：%+v", fs.recs)
	}
	// 4/22 只该推「这根竿 + 挂在它上面的零件」，背包里那件不算
	if len(res.Push422) == 0 {
		t.Fatal("4/22 没拼出来")
	}
	if strings.Contains(strings.ToLower(hex.EncodeToString(res.Push422)), "bbbb") {
		t.Fatalf("4/22 把跟这根竿无关的货也推了：%x", res.Push422)
	}
}

// TestKeepnetLoginSync 兜住这次实机故障：鱼护不是点开面板才单独请求，
// 而是登录时跟装备一起走 4/4（GUID）+ 4/5（obj25 详情）。旧 payload 没 card 字段也必须能恢复。
func TestKeepnetLoginSync(t *testing.T) {
	tbl := loadTable(t)
	cat, fs := newFakeCat()
	if _, _, err := cat.Reload(context.Background()); err != nil {
		t.Fatalf("%v", err)
	}
	exactGUID := "eb2652ce7227a04ca9774d951b7a729c"
	oldGUID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	exactCard, err := CardRecord(tbl, CardInput{
		Key: "ruffe", Grams: 53, LengthM: .1314, Class: ClassMedium,
		BaitKey: "worm_01", Place: "level_000_home", SetupGUID: exactGUID,
	})
	if err != nil {
		t.Fatalf("造鱼卡失败：%v", err)
	}
	fs.keep = []gamestore.KeepnetRow{
		{Key: "ruffe", Grams: 53, Cm: 13.14, GUID: dashGUID(exactGUID), CardHex: hex.EncodeToString(exactCard)},
		// 模拟这次修复前已经存在的旧鱼：只有五个老字段，没有 card/class/place/bait。
		{Key: "ruffe", Grams: 80, Cm: 15.2, GUID: dashGUID(oldGUID), At: 1700000000000},
	}
	svc := New(tbl, cat, fs)
	ids, err := svc.KeepnetGUIDs(context.Background(), "return2")
	if err != nil || len(ids) != 2 || !bytes.Equal(ids[0], mustHex(t, exactGUID)) {
		t.Fatalf("4/4 鱼 GUID 不对：ids=%x err=%v", ids, err)
	}
	// 客户端点名第一条时只能回第一条，并优先复用当时存下来的原始鱼卡。
	recs, requested, missing, err := svc.KeepnetRecords(context.Background(), "return2", mustHex(t, exactGUID))
	if err != nil || len(recs) != 1 || !bytes.Equal(recs[0], exactCard) || len(requested) != 1 || len(missing) != 0 {
		t.Fatalf("4/5 点名同步不对：records=%d requested=%v missing=%v err=%v", len(recs), requested, missing, err)
	}
	// 空请求要全量；第二条旧鱼必须现场重建，不能因为没有 CardHex 就丢掉。
	recs, _, missing, err = svc.KeepnetRecords(context.Background(), "return2", nil)
	if err != nil || len(recs) != 2 || len(missing) != 0 || !bytes.Contains(recs[1], mustHex(t, oldGUID)) {
		t.Fatalf("4/5 全量/旧鱼恢复不对：records=%d missing=%v err=%v", len(recs), missing, err)
	}
}

// TestKeepnetRecordsReadsLiveMySQL 鱼护完整同步只能以 MySQL keepnets 表为准：
// 不读录制、不读模板，库里有几条就还原几条；当前 return2 为空时必须得到 0 条。
func TestKeepnetRecordsReadsLiveMySQL(t *testing.T) {
	db, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没起来，跳过实库鱼护测试: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	rows, err := db.Keepnet(ctx, "return2")
	if err != nil {
		t.Fatalf("读 MySQL keepnets 失败: %v", err)
	}
	cat := NewCatalog(db)
	if _, _, err := cat.Reload(ctx); err != nil {
		t.Fatalf("装鱼种/水域表失败: %v", err)
	}
	svc := New(loadTable(t), cat, db)
	records, requested, missing, err := svc.KeepnetRecords(ctx, "return2", nil)
	if err != nil {
		t.Fatalf("从 MySQL 还原鱼护记录失败: %v", err)
	}
	if len(records) != len(rows) || len(requested) != 0 || len(missing) != 0 {
		t.Fatalf("keepnets 里有 %d 条，完整同步得到 records=%d requested=%v missing=%v", len(rows), len(records), requested, missing)
	}
	t.Logf("真实 MySQL keepnets：%d 条，完整同步还原 %d 条", len(rows), len(records))
}

// TestKeepSurvivesStoreFailure 写库失败不该把这次留鱼搞失败：应答照发，原因带出去写日志。
func TestKeepSurvivesStoreFailure(t *testing.T) {
	tbl := loadTable(t)
	cat, fs := newFakeCat()
	fs.failOn = "keepnet"
	svc := New(tbl, cat, fs)
	res, err := svc.Keep(context.Background(), KeepInput{
		Username: "return2", GearGUID: realSampleGear,
		FishGUID: "eb2652ce7227a04ca9774d951b7a729c",
		Fish:     Fish{Key: "ruffe", Grams: 53, LengthM: 0.13, Class: ClassMedium},
	})
	if err != nil {
		t.Fatalf("写库失败不该把整个留鱼流程弄错：%v", err)
	}
	if res.Saved || res.SaveErr == "" {
		t.Fatalf("库里写失败了，结果还说 Saved=%v 原因空", res.Saved)
	}
	if len(res.Reply) == 0 {
		t.Fatal("应答还是得发出去")
	}
}

// TestStateArmResolveClear 会话那份钓鱼状态：推了咬钩要记着，keep 回来要认得，办完要清掉。
// 顺带验一下并发：定时器写、帧循环读，别打架（-race 跑的时候这条最有价值）。
func TestStateArmResolveClear(t *testing.T) {
	var st State
	if _, ok := st.Pending(); ok {
		t.Fatal("刚上来就该没鱼")
	}
	f := Fish{Key: "bream", Grams: 900, LengthM: 0.35, Class: ClassBig, SetupGUID: "cafebabe000000000000000000000000"}
	st.SetGear(realSampleGear)
	st.Arm(f)
	if got, ok := st.Pending(); !ok || got.Key != "bream" {
		t.Fatalf("钩着的鱼没记下来：%+v ok=%v", got, ok)
	}
	if _, ok := st.Resolve(f.SetupGUID); !ok {
		t.Fatal("按 GUID 认不出这条鱼")
	}
	if _, ok := st.Resolve("ffffffff000000000000000000000000"); ok {
		t.Fatal("不是这条鱼却说认得出")
	}
	if st.Gear() != realSampleGear {
		t.Fatalf("钓具 GUID 记成了 %q", st.Gear())
	}
	st.ClearPending()
	if _, ok := st.Pending(); ok {
		t.Fatal("清完还说钩着鱼")
	}
	if _, ok := st.Resolve(f.SetupGUID); !ok {
		t.Fatal("清完 pending 之后，last 那一条还得能认出来（客户端 keep 可能慢半拍）")
	}
	st.MarkKept()
	casts, bites, kept := st.Stats()
	if casts != 1 || bites != 1 || kept != 1 {
		t.Fatalf("计数不对：抛%d 咬%d 留%d", casts, bites, kept)
	}

	// 并发写：定时器那边一直在 Arm，这边一直在读
	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			st.Arm(Fish{Key: "ruffe", Grams: int64(i), SetupGUID: f.SetupGUID})
		}
		done <- struct{}{}
	}()
	for i := 0; i < 500; i++ {
		st.Pending()
		st.Resolve(f.SetupGUID)
	}
	<-done
}

// TestStateOnlyArmsWhileBaitIsInWater 锁住本轮修复的核心规则：14/1、14/2 都不能出鱼，
// 只有首条 14/7 之后能出；收线后旧定时器即使醒来也必须作废。
func TestStateOnlyArmsWhileBaitIsInWater(t *testing.T) {
	var st State
	f := Fish{Key: "ruffe", Grams: 88, SetupGUID: "11111111111111111111111111111111"}
	st.SetGear(realSampleGear)
	if st.InWater(0) || st.ArmIfInWater(0, f) {
		t.Fatal("只有 14/1 准备阶段时不该允许出鱼")
	}
	st.MarkThrown(realSampleGear)
	if st.ArmIfInWater(0, f) {
		t.Fatal("只有 14/2、钓组仍在飞行时不该允许出鱼")
	}
	seq, first := st.MarkInWater()
	if !first || seq == 0 || !st.InWater(seq) {
		t.Fatalf("首条 14/7 没把状态切成入水：seq=%d first=%v", seq, first)
	}
	if _, again := st.MarkInWater(); again {
		t.Fatal("后续 14/7 不该重复启动出鱼任务")
	}
	st.LeaveWater()
	if st.InWater(seq) || st.ArmIfInWater(seq, f) {
		t.Fatal("收线后旧定时器仍能挂鱼")
	}
}

// TestBiteDelayStaysInsideWindow 咬钩延迟必须落在给定的区间里（真服也不是定值，但不能离谱）
func TestBiteDelayStaysInsideWindow(t *testing.T) {
	rnd := rand.New(rand.NewSource(11))
	for i := 0; i < 200; i++ {
		d := BiteDelay(rnd, 4*time.Second, 16*time.Second)
		if d < 4*time.Second || d >= 16*time.Second {
			t.Fatalf("延迟 %v 跑出了 [4s,16s)", d)
		}
	}
	if d := BiteDelay(rnd, 4*time.Second, 4*time.Second); d != 4*time.Second {
		t.Fatalf("区间退成一个点时应该就取那个点，结果 %v", d)
	}
}

// TestCatalogReloadCounts 缓存装好了要能报出「几个鱼种几个水域」，水域还得排过序（日志好对照）
func TestCatalogReloadCounts(t *testing.T) {
	cat, _ := newFakeCat()
	n, locs, err := cat.Reload(context.Background())
	if err != nil {
		t.Fatalf("%v", err)
	}
	if n != 3 || locs != 2 {
		t.Fatalf("报的数不对：%d 个鱼种 %d 个水域", n, locs)
	}
	got := cat.Locs()
	if len(got) != 2 || got[0] > got[1] {
		t.Fatalf("水域列表没排序：%v", got)
	}
	if len(cat.Roster("level_000_home")) != 2 {
		t.Fatal("名册读错了")
	}
	if len(cat.allSpeciesKeys()) != 3 {
		t.Fatal("全鱼种清单读错了")
	}
	// 没接上库要明确报错，别默默给一份空表
	if _, _, err := NewCatalog(nil).Reload(context.Background()); err == nil {
		t.Fatal("没数据入口时应该报错")
	}
}

// TestKeepPush422EmptyMeansNoPush 没装备可推就返回 nil，让调用方决定要不要记日志
func TestKeepPush422EmptyMeansNoPush(t *testing.T) {
	tbl := loadTable(t)
	emptyFrame, err := KeepPush422(tbl, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if emptyFrame != nil {
		t.Fatalf("没装备应该返回 nil，结果 %x", emptyFrame)
	}
	p, err := KeepPush422(tbl, [][]byte{{0x01, 0x02}})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(p) < 12 || p[9] != 4 || p[10] != 22 {
		t.Fatalf("4/22 的推送头不对：%x", p[:12])
	}
}
