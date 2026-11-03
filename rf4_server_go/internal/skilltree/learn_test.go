package skilltree

// 技能学习的判定规则单测：这一层不连库，规则对不对一眼就能看出来。
// 节点号用的是真服定义表里真有的那几个（data/skill_defs.json）：
//
//	大类 8 节点 601：门槛 0%、最高 5 级、互斥组 1   —— 正常能学
//	大类 8 节点 602：同组另一个                     —— 只在开了互斥开关时才该被拦
//	大类 8 节点 603：门槛 20%、最高 5 级            —— 熟练度不够就该被拦
//	大类 8 节点 607：门槛 10%、最高 0 级            —— 「开放但学不了」那种，要能拒
import (
	"rf4server/internal/wire"
	"strings"
	"testing"
)

// newState 造一个干净账号状态：发放点数 + 各大类熟练度（不给的全 0）
func newState(granted int, mastery map[int]float64) State {
	st := State{Cats: map[int]float64{}, Nodes: map[int]int{}, Granted: granted, Level: 40}
	for c, v := range mastery {
		st.Cats[c] = v
	}
	return st
}

// TestLearnLevelsUpAndSpendsPoints 连学五级：每次 +1 级、每次扣一点，第六次要拒
func TestLearnLevelsUpAndSpendsPoints(t *testing.T) {
	_, d := loadAll(t)
	st := newState(5, nil)
	for want := 1; want <= 5; want++ {
		res := Learn(d, &st, 8, 601, false)
		if !res.OK {
			t.Fatalf("第 %d 次该学成，结果拒了：%s", want, res.Reason)
		}
		if res.Level != want {
			t.Fatalf("第 %d 次学完应该是 %d 级，实际 %d 级", want, want, res.Level)
		}
		if want6 := uint32(5 - want); res.Avail != want6 {
			t.Fatalf("学完剩 %d 点才对，实际 %d 点", want6, res.Avail)
		}
	}
	res := Learn(d, &st, 8, 601, false)
	if res.OK {
		t.Fatal("学到最高级之后还让继续学，点数要浪费了")
	}
	if !strings.Contains(res.Reason, "已经学满") {
		t.Fatalf("拒绝原因该说「已经学满」，实际：%s", res.Reason)
	}
}

// TestLearnRejectsWithoutPoints 点数用完必须拒，而且节点等级不能偷偷涨
func TestLearnRejectsWithoutPoints(t *testing.T) {
	_, d := loadAll(t)
	st := newState(0, nil)
	res := Learn(d, &st, 8, 601, false)
	if res.OK {
		t.Fatal("一点都没有还让学")
	}
	if !strings.Contains(res.Reason, "技能点用完") {
		t.Fatalf("原因该说点数用完，实际：%s", res.Reason)
	}
	if st.Nodes[601] != 0 {
		t.Fatalf("被拒之后节点等级不该动，现在 %d 级", st.Nodes[601])
	}
}

// TestLearnRejectsLockedNode 熟练度没到门槛要拒（大类 8 节点 603 要 20%）
func TestLearnRejectsLockedNode(t *testing.T) {
	_, d := loadAll(t)
	st := newState(10, map[int]float64{8: 19.9})
	res := Learn(d, &st, 8, 603, false)
	if res.OK {
		t.Fatal("熟练度 19.9%% 不该能学要 20%% 的节点")
	}
	if !strings.Contains(res.Reason, "熟练度不够") {
		t.Fatalf("原因该说熟练度不够，实际：%s", res.Reason)
	}
	// 到 20% 就该放行（Unlocked 那条规则按 min(熟练度,110) >= 门槛）
	st.Cats[8] = 20
	if res = Learn(d, &st, 8, 603, false); !res.OK {
		t.Fatalf("熟练度到了还拒：%s", res.Reason)
	}
}

// TestLearnRejectsUnlearnableNode 「最高 0 级」那种节点（大类 8 节点 607）要拒得明白
func TestLearnRejectsUnlearnableNode(t *testing.T) {
	_, d := loadAll(t)
	st := newState(10, map[int]float64{8: 100})
	res := Learn(d, &st, 8, 607, false)
	if res.OK {
		t.Fatal("最高 0 级的节点不该能学")
	}
	if !strings.Contains(res.Reason, "最高等级 0") {
		t.Fatalf("原因该说这节点学不了，实际：%s", res.Reason)
	}
}

// TestLearnRejectsUnknownNode 客户端报一个大类+节点组合不在定义表里，要拒而且要说是哪个
func TestLearnRejectsUnknownNode(t *testing.T) {
	_, d := loadAll(t)
	st := newState(10, nil)
	res := Learn(d, &st, 8, 999999, false)
	if res.OK || !strings.Contains(res.Reason, "没有这个技能节点") {
		t.Fatalf("该拒成「没有这个节点」，实际 OK=%v 原因=%s", res.OK, res.Reason)
	}
}

// TestGroupMutexIsOffByDefault 互斥组只在显式打开时才拦：
// 真服录制里同组俩节点都学过，所以默认不能拦（和 Node 版 skillGroupMutex 默认关一个口径）。
func TestGroupMutexIsOffByDefault(t *testing.T) {
	_, d := loadAll(t)
	st := newState(10, nil)
	if res := Learn(d, &st, 8, 601, false); !res.OK {
		t.Fatalf("先学 601 就该成：%s", res.Reason)
	}
	if res := Learn(d, &st, 8, 602, false); !res.OK {
		t.Fatalf("默认不该拦同组的 602：%s", res.Reason)
	}

	st2 := newState(10, nil)
	if res := Learn(d, &st2, 8, 601, false); !res.OK {
		t.Fatalf("先学 601 就该成：%s", res.Reason)
	}
	res := Learn(d, &st2, 8, 602, true)
	if res.OK || !strings.Contains(res.Reason, "互斥组") {
		t.Fatalf("开了互斥开关就该拦，实际 OK=%v 原因=%s", res.OK, res.Reason)
	}
}

// TestResetNodeRefundsPoints 退一个节点要把点退回来，再退一次就得说「本来就没学」
func TestResetNodeRefundsPoints(t *testing.T) {
	_, d := loadAll(t)
	st := newState(3, nil)
	if res := Learn(d, &st, 8, 601, false); !res.OK {
		t.Fatalf("先学一级：%s", res.Reason)
	}
	if st.Available() != 2 {
		t.Fatalf("学完该剩 2 点，实际 %d", st.Available())
	}
	res := ResetNode(d, &st, 601)
	if !res.OK || res.Cleared != 1 || st.Available() != 3 {
		t.Fatalf("退节点该退成、点该回到 3：OK=%v 清了 %d 个 剩 %d 点", res.OK, res.Cleared, st.Available())
	}
	if res.Cat != 8 || res.Max != 5 {
		t.Fatalf("日志要指对地方：大类 %d 最高 %d 级", res.Cat, res.Max)
	}
	if res = ResetNode(d, &st, 601); res.OK || !strings.Contains(res.Reason, "本来就没学") {
		t.Fatalf("没学过的节点不该再退一次：OK=%v 原因=%s", res.OK, res.Reason)
	}
}

// TestResetAllClearsEverythingButMastery 5/4 全清：节点清空、点数回来，熟练度和发放点数不动
func TestResetAllClearsEverythingButMastery(t *testing.T) {
	_, d := loadAll(t)
	st := newState(10, map[int]float64{8: 20})
	for _, n := range []int{601, 602, 603} {
		if res := Learn(d, &st, 8, n, false); !res.OK {
			t.Fatalf("学 %d 该成：%s", n, res.Reason)
		}
	}
	res := ResetAll(&st)
	if !res.OK || res.Cleared != 3 {
		t.Fatalf("该清掉 3 个节点，实际 %d", res.Cleared)
	}
	if len(st.Nodes) != 0 {
		t.Fatalf("全清之后不该留节点，还剩 %d 个", len(st.Nodes))
	}
	if st.Available() != 10 {
		t.Fatalf("全清后点数该全回来(10)，实际 %d", st.Available())
	}
	if st.Cats[8] != 20 {
		t.Fatalf("熟练度不该被全清动到：现在 %v", st.Cats[8])
	}
	if st.Granted != 10 {
		t.Fatalf("发放点数不该被改动：现在 %d", st.Granted)
	}
}

// TestPickCatNodeHandlesShiftedInts 真服有些请求前面多塞一个整数，
// (0,1) 对不上时要改试 (1,2)，不然点了技能没反应就是栽在这种偏移上。
func TestPickCatNodeHandlesShiftedInts(t *testing.T) {
	_, d := loadAll(t)
	if cat, node, ok := PickCatNode(d, []uint64{8, 601}); !ok || cat != 8 || node != 601 {
		t.Fatalf("正常形状没挑对：%d/%d ok=%v", cat, node, ok)
	}
	if cat, node, ok := PickCatNode(d, []uint64{999, 8, 601}); !ok || cat != 8 || node != 601 {
		t.Fatalf("挪一位的形状没挑对：%d/%d ok=%v", cat, node, ok)
	}
	if _, _, ok := PickCatNode(d, []uint64{8}); ok {
		t.Fatal("只给一个整数时说不出节点，应该返回 false")
	}
}

// TestIntsInReadsRequestArgs 整条 5/2 请求（11 字节壳 + 参数表"524" 两个整数）要能捞出那两个整数
func TestIntsInReadsRequestArgs(t *testing.T) {
	tt, _ := loadAll(t)
	args, err := wire.Args{Code: "524", Items: []wire.Value{wire.U32(8), wire.U32(601)}}.Encode()
	if err != nil {
		t.Fatalf("拼请求失败: %v", err)
	}
	plain := append(append(wire.PushHead(0, 5, 2), args...), wire.RequestTrailer)
	ints := IntsIn(tt, plain, ArgOff)
	if len(ints) < 2 || ints[0] != 8 || ints[1] != 601 {
		t.Fatalf("没捞出 [8,601]：%v", ints)
	}
}
