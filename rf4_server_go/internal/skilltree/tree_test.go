package skilltree

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"rf4server/internal/assetpaths"
	"rf4server/internal/skilldef"
	"rf4server/internal/wire"
)

// loadAll 把协议字段表和技能定义表都读进来，两个测试都要用
func loadAll(t *testing.T) (*wire.Table, *skilldef.Defs) {
	t.Helper()
	tp, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatalf("找不到凭据目录: %v", err)
	}
	tt, err := wire.LoadTable(tp)
	if err != nil {
		t.Fatalf("读协议字段表失败: %v", err)
	}
	dp, err := assetpaths.File("skill_defs.json")
	if err != nil {
		t.Fatalf("找不到技能定义表: %v", err)
	}
	d, err := skilldef.Load(dp)
	if err != nil {
		t.Fatalf("读技能定义表失败: %v", err)
	}
	if d.Meta.TopTypeID != 311 || d.Meta.NodeCount == 0 {
		t.Fatalf("技能定义表不对：顶层 typeID=%d 节点数=%d", d.Meta.TopTypeID, d.Meta.NodeCount)
	}
	return tt, d
}

// recordedState 用「录制那位老号当时」的熟练度和已学等级造一个 State，
// 这样造出来的字节应该和真服当时发的一模一样——这是技能域最硬的验收标准。
func recordedState(d *skilldef.Defs) State {
	st := State{Cats: map[int]float64{}, Nodes: map[int]int{}, Granted: 0, Level: 20}
	for catID, c := range d.Cats {
		st.Cats[catID] = wire.BitsF32(c.MasteryBits) // 熟练度按存的原始位还原，别用四舍五入过的十进制
		for _, n := range c.Nodes {
			if n.LevelOfRecorded > 0 {
				st.Nodes[n.Node] = n.LevelOfRecorded
			}
		}
	}
	// Available() = Granted - 已投入。要让可分配点数等于录制那个值，就把发放点数倒推出来。
	st.Granted = d.Meta.AvailPointsOfRecordedPlayer + st.Spent()
	return st
}

// realReply 取真服 5/1 那条应答的原始字节（base64 存在 rpc_table_real.json 里）
func realReply(t *testing.T, cmd string) []byte {
	t.Helper()
	p, err := assetpaths.File("rpc_table_real.json")
	if err != nil {
		t.Skipf("没有历史抓包凭据，跳过技能树逐字节对账: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("读不到历史抓包凭据，跳过技能树逐字节对账: %v", err)
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
	rs := tab.Fallback[cmd].Responses
	if len(rs) == 0 {
		t.Fatalf("凭据里没有 %s", cmd)
	}
	pay, err := base64.StdEncoding.DecodeString(rs[0].Pay)
	if err != nil {
		t.Fatalf("pay 不是 base64: %v", err)
	}
	return pay
}

// recordedPush24 从 45 秒录制里取真服那条 2/4 全量技能树推送（5532B）。
// 这是「整帧自己造」的对照答案：帧头、args"524" 外壳、尾巴那个 0x01，全都要对上。
func recordedPush24(t *testing.T) []byte {
	t.Helper()
	p, err := assetpaths.File("replay_bootstrap.json")
	if err != nil {
		t.Skipf("没有历史录制，跳过技能树推送逐字节对账: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("读不到历史录制，跳过技能树推送逐字节对账: %v", err)
	}
	var raw struct {
		Events []struct {
			Dir  string `json:"dir"`
			Main int    `json:"main"`
			Sub  int    `json:"sub"`
			T    int    `json:"t"` // 录制里的时刻（毫秒），日志里那条推送是 17704ms
			Pay  string `json:"pay"`
		} `json:"events"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("解析录制失败: %v", err)
	}
	for _, e := range raw.Events {
		if e.Dir == "s2c" && e.Main == 2 && e.Sub == 4 {
			pay, err := base64.StdEncoding.DecodeString(e.Pay)
			if err != nil {
				t.Fatalf("录制 pay 不是 base64: %v", err)
			}
			return pay
		}
	}
	t.Fatal("录制里没有 2/4 那条全量技能树推送")
	return nil
}

// TestReply311ByteIdentical 技能域第一硬指标：
// 用录制那位的熟练度和已学等级造 5/1 应答，必须和真服那条 5522B 逐字节一模一样。
func TestReply311ByteIdentical(t *testing.T) {
	tt, d := loadAll(t)
	st := recordedState(d)
	got, err := ReplyTree(tt, d, st, 9) // call_id 照录制写 9
	if err != nil {
		t.Fatalf("造应答帧失败: %v", err)
	}
	want := realReply(t, "5/1")
	if len(got) != len(want) {
		t.Fatalf("长度就不对：我 %dB，真服 %dB", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 字节不同：我=% x 真服=% x", i, safe(got, i, 12), safe(want, i, 12))
		}
	}
	if st.Available() != uint32(d.Meta.AvailPointsOfRecordedPlayer) {
		t.Errorf("可分配点数应是录制的 %d，算出来 %d", d.Meta.AvailPointsOfRecordedPlayer, st.Available())
	}
}

// TestPush24MatchesRecording 「不搞剧本回放」的凭据：2/4 整帧自己造，
// 和录制里真服那条比，只能差在熟练度那几个字节上——因为录制里这位老号在这 45 秒里熟练度涨了
// （登录后 15ms 的 5/1 应答里是 11.0009%，到 17704ms 推 2/4 时变成 11.1%），
// 而我们喂进去的是 5/1 那份定义表里的熟练度。除此以外必须一字不差。
func TestPush24MatchesRecording(t *testing.T) {
	tt, d := loadAll(t)
	st := recordedState(d)
	got, err := PushTree(tt, d, st)
	if err != nil {
		t.Fatalf("造 2/4 推送帧失败: %v", err)
	}
	want := recordedPush24(t)
	if len(got) != len(want) {
		t.Fatalf("长度不对：我 %dB，真服 %dB（帧头/外壳/尾巴拼错就是这个下场）", len(got), len(want))
	}
	// 前 18 字节是帧头 + args"524" 外壳，必须完全一样
	for i := 0; i < 18; i++ {
		if got[i] != want[i] {
			t.Fatalf("帧头第 %d 字节不同：我=% x 真服=% x", i, safe(got, i, 20), safe(want, i, 20))
		}
	}
	// 最后一个字节（推送尾巴 0x01）也必须一样
	if got[len(got)-1] != want[len(want)-1] {
		t.Fatalf("推送尾巴不对：我=0x%02x 真服=0x%02x", got[len(got)-1], want[len(want)-1])
	}
	var diffs []int
	for i := range want {
		if got[i] != want[i] {
			diffs = append(diffs, i)
		}
	}
	// 只允许差在大类 1 的熟练度那 4 个字节里（偏移 39~42），别的都不许差
	for _, i := range diffs {
		if i < 39 || i > 42 {
			t.Fatalf("第 %d 字节不该不同：我=% x 真服=% x", i, safe(got, i, 12), safe(want, i, 12))
		}
	}
	t.Logf("整帧 %d 字节全对，只有熟练度那几个字节因录制期间涨了而不同（%d 处：%v）", len(got), len(diffs), diffs)
}

// TestUnlockedRule 解锁位规则：min(熟练度,110) >= 门槛。
// 拿录制那 190 个节点当时的解锁位当答案，必须一个不错。
func TestUnlockedRule(t *testing.T) {
	_, d := loadAll(t)
	wrong := 0
	for catID, c := range d.Cats {
		mastery := wire.BitsF32(c.MasteryBits)
		for _, n := range c.Nodes {
			if got := Unlocked(n, mastery); int(got) != n.UnlockedOfRecorded {
				wrong++
				if wrong <= 3 {
					t.Errorf("大类 %d 节点 %d：熟练度 %.4f%% 应解锁=%d 我们算=%d（门槛 %.4f%%）",
						catID, n.Node, mastery, n.UnlockedOfRecorded, got, n.UnlockPct)
				}
			}
		}
	}
	if wrong != 0 {
		t.Fatalf("解锁位规则和录制对不上的一共 %d 个节点", wrong)
	}
}

// safe 截一段字节用于报错，避免越界 panic
func safe(b []byte, from, n int) []byte {
	if from >= len(b) {
		return nil
	}
	end := from + n
	if end > len(b) {
		end = len(b)
	}
	return b[from:end]
}
