package skilltree

// 这一层管「学技能 / 重置技能」的判定：只讲数据规则，不连库、不拼字节。
// 规矩是从 Node 版 src/skills.js 的 learn()/resetNode()/resetAll() 一条一条搬过来的：
//
//	5/2 学一级：节点定义存在 -> 没学满 -> 熟练度够门槛 -> 还有余点 -> 该节点 +1
//	5/3 重置单个节点：学过的那格清掉，投出去的点退回来
//	5/4 全部重置：这个号的已学节点全清
//
// 「同互斥组只能选一个」默认不拦：真服录制里那位在大类 4 同时学了 101 和 125（两个都是组 1），
// 这条规则当场就不成立，所以只有显式传 groupMutex=true 才按它拦（和 Node 版同一个口径）。

import (
	"strconv"

	"rf4server/internal/skilldef"
	"rf4server/internal/wire"
)

// ArgOff 客户端请求里参数表的起点：9 字节应答式壳 + main + sub = 第 11 字节。
// 5/2 那种「参数表里就两个整数」的请求从这儿起解就够。
const ArgOff = 11

// Result 一次技能操作的结果。字段基本是给日志用的：
// 拒绝的时候要能说清「为什么没加成」，少爷在游戏里点了没反应也能从日志看到原因。
type Result struct {
	OK      bool   // 这次改动生效了没有
	Reason  string // 没生效的原因（生效时是空串）
	Cat     int    // 涉及的大类号（1 浮钓 2 路亚 3 喂食器 4 抛竿 5 飞钓 6 烹饪 7 打窝 8 拟饵 9 船 10 冰钓 11 海钓）
	Node    int    // 涉及的节点号
	Level   int    // 操作完之后这个节点是几级
	Max     int    // 这个节点最高几级
	Cleared int    // 这次清掉了几个节点（5/3 最多 1 个，5/4 是全部）
	Avail   uint32 // 操作完还剩几点可投（面板上那个「剩余点数」）
}

// Learn 学一级。改的是传进来的 st，写不写回 MySQL 由调用方决定（这样单测不用起库）。
// 定义表没载入就直接失败并说清原因，不会返回一个「成功」骗人。
func Learn(d *skilldef.Defs, st *State, cat, node int, groupMutex bool) Result {
	res := Result{Cat: cat, Node: node, Avail: st.Available()}
	if d == nil {
		res.Reason = "技能定义表没载入，判不了能不能学"
		return res
	}
	def, ok := defOf(d, cat, node)
	if !ok {
		res.Reason = "没有这个技能节点（大类 " + strconv.Itoa(cat) + " 节点 " + strconv.Itoa(node) + "）"
		return res
	}
	res.Max = def.MaxLevel

	// 互斥组：只有显式打开才拦（真服自己都不拦，见包注释）
	if groupMutex && def.Group != 0 {
		for _, other := range d.NodesOf(cat) {
			if other.Node != def.Node && other.Group == def.Group && st.Nodes[other.Node] > 0 {
				res.Reason = "互斥组 " + strconv.Itoa(def.Group) + " 里已经选了节点 " + strconv.Itoa(other.Node) + "，只能选一个"
				return res
			}
		}
	}
	if def.MaxLevel <= 0 {
		res.Reason = "这个节点学不了（最高等级 0）"
		return res
	}
	cur := st.Nodes[def.Node]
	res.Level = cur
	if cur >= def.MaxLevel {
		res.Reason = "已经学满 " + strconv.Itoa(cur) + "/" + strconv.Itoa(def.MaxLevel) + " 级了"
		return res
	}
	mastery := st.Cats[cat]
	if Unlocked(def, mastery) == 0 {
		res.Reason = "熟练度不够：大类 " + strconv.Itoa(cat) + " 要 " +
			strconv.FormatFloat(def.UnlockPct, 'g', -1, 64) + "%，现在 " +
			strconv.FormatFloat(mastery, 'g', -1, 64) + "%"
		return res
	}
	if st.Available() == 0 {
		res.Reason = "技能点用完了（等级 " + strconv.Itoa(st.Level) + "）"
		return res
	}
	st.Nodes[def.Node] = cur + 1
	res.OK, res.Level, res.Avail = true, cur+1, st.Available()
	return res
}

// ResetNode 重置一个节点（5/3）。请求只给节点号，所以大类是反查出来的（日志要指对地方）。
func ResetNode(d *skilldef.Defs, st *State, node int) Result {
	res := Result{Node: node, Avail: st.Available()}
	if st.Nodes[node] <= 0 {
		res.Reason = "节点 " + strconv.Itoa(node) + " 本来就没学"
		return res
	}
	if d != nil {
		if def, cat, ok := d.Node(node); ok {
			res.Cat, res.Max = cat, def.MaxLevel
		}
	}
	delete(st.Nodes, node)
	res.OK, res.Cleared, res.Avail = true, 1, st.Available()
	return res
}

// ResetAll 全部重置（5/4）：只清已学节点；发放点数、熟练度都不动。
func ResetAll(st *State) Result {
	cleared := 0
	for _, lv := range st.Nodes {
		if lv > 0 {
			cleared++
		}
	}
	st.Nodes = map[int]int{}
	return Result{OK: true, Cleared: cleared, Avail: st.Available()}
}

// IntsIn 把请求正文里的整数按帧里的先后捞出来。
// 5/2 里面就是 [大类, 节点] 两个整数，但真服有时在前面多塞一个数，
// 所以不能按固定偏移抠 —— 整棵值树走一遍更稳（和 Node 版 intsOf 一个办法）。
// 解不动就当「一个整数都没带」，调用方照样回本号的树并写一行日志。
func IntsIn(t *wire.Table, plain []byte, off int) []uint64 {
	if t == nil || len(plain) <= off || plain[off] != 0x03 {
		return nil
	}
	v, _, err := t.DecodeValue(plain, off)
	if err != nil {
		return nil
	}
	var out []uint64
	wire.Walk(v, func(it wire.WalkItem) {
		switch it.Kind {
		case "u32", "u64", "u16", "byte", "datetime":
			out = append(out, it.Num)
		}
	})
	return out
}

// PickCatNode 从那串整数里挑出（大类, 节点）。
// 先试前两个；不行再试第二、三个（真服有些请求前面多一个整数）；
// 还不行就退回「按位置猜」，让 Learn 去报「没有这个节点」，日志里能看到原始整数。
func PickCatNode(d *skilldef.Defs, ints []uint64) (int, int, bool) {
	if len(ints) < 2 {
		return 0, 0, false
	}
	if d != nil {
		if _, ok := defOf(d, int(ints[0]), int(ints[1])); ok {
			return int(ints[0]), int(ints[1]), true
		}
		if len(ints) >= 3 {
			if _, ok := defOf(d, int(ints[1]), int(ints[2])); ok {
				return int(ints[1]), int(ints[2]), true
			}
		}
	}
	return int(ints[0]), int(ints[1]), true
}

// DefOfFor 按（大类, 节点）找定义，给上层打日志用（拼帧本身不需要，客户端自己按节点号认）。
func DefOfFor(d *skilldef.Defs, cat, node int) (skilldef.Node, bool) { return defOf(d, cat, node) }

// defOf 按（大类, 节点）找定义。大类别传错就找不到（返回 false），
// 调用方会照实写日志，不会出现「日志指到大类 8、其实学的是大类 4」那种误导。
func defOf(d *skilldef.Defs, cat, node int) (skilldef.Node, bool) {
	if d == nil {
		return skilldef.Node{}, false
	}
	for _, n := range d.NodesOf(cat) {
		if n.Node == node {
			return n, true
		}
	}
	return skilldef.Node{}, false
}
