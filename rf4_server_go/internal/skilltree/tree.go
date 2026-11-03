// Package skilltree 负责技能域的「造字节」：obj311 技能树、5/1~5/4 的应答帧、2/4 全量推送帧。
//
// 注意这里不做剧本回放：整帧每一段都是自己拼的，数据全从传进来的 State（账号状态）+ 静态定义表现算。
// 凭据（真服录制）只拿来当测试标准答案：
//
//	5/1 应答 = 01 | -2 | call_id | obj311          录制里 5522B
//	2/4 推送 = 01 | -1 | 1 | 02 04 | args"524"[obj311] | 01   录制里 5532B
package skilltree

import (
	"fmt"

	"rf4server/internal/skilldef"
	"rf4server/internal/wire"
)

// 三个 wire 类名和 obj 类型号（凭据：data/wire_tables.json + skill_defs.json 的 meta）
const (
	ClassTree = "djndmghegld" // obj 311：整棵技能树
	ClassCat  = "kmepameiibi" // obj 312：一个大类
	ClassNode = "bmdafhbpfci" // obj 313：一个技能节点
)

// 311/312/313 里的混淆字段名（照抄 wire_tables.json，别改名，改名就填错字段）
const (
	f311Points = "ifieemgdomp" // 311.f0 可分配技能点（u32）
	f311Cats   = "fmnfmoeopif" // 311.f1 args"121"：大类列表
	f311F2     = "olccocjinkf" // 311.f2 录制里恒 500（u64），含义没查出来，照录
	f311F3     = "ejljipgbpfm" // 311.f3 录制里恒 0（1 字节）

	f312Cat     = "ndnngghaelp" // 312.f0 大类号（BYTE，不是 u32——他家写成了 u32，以真服为准）
	f312Mastery = "ggflehiachl" // 312.f1 熟练度，f32 的原始位塞在 u32 里
	f312F2      = "oalocpjcokm" // 312.f2 录制里全是 0
	f312Nodes   = "hfahneklkcp" // 312.f3 args"122"：节点列表

	f313Node   = "iiijkdapekg" // 313.f0 节点号（U16，不是 u32）
	f313Unlock = "dlmaaeecbci" // 313.f1 学习门槛（f32 原始位）
	f313Max    = "dgpphdckegl" // 313.f2 最高等级
	f313Level  = "dfmeofjoiio" // 313.f3 当前学到几级
	f313Effect = "oalocpjcokm" // 313.f4 每级效果（f32 原始位，真服这份全是 0）
	f313Group  = "nlhmndaggei" // 313.f5 互斥组
	f313Open   = "amfiahegdje" // 313.f6 解锁位：客户端直接看这个亮不亮格子
)

// MasteryCap 熟练度封顶：解锁判定按 min(熟练度, 110) 算。
// 凭据：录制里 190 个节点的解锁位用这条规则能一个不差全对上。
const MasteryCap = 110.0

// State 账号的技能状态。由上层从 MySQL 读进来，本包不连库，方便单测。
type State struct {
	// Cats 大类号 -> 熟练度百分比（0~110）。没在这个 map 里就是 0。
	Cats map[int]float64
	// Nodes 节点号 -> 已学等级。没在这个 map 里就是没学（0 级）。
	Nodes map[int]int
	// Granted 这个号一共被发放了多少技能点。0 表示没设过，上层应按等级曲线算好再填进来。
	Granted int
	// Level 角色等级，只用于日志和「没设 Granted 时按曲线算」的场景。
	Level int
}

// Spent 已经投出去多少点：规则是「每级 1 点」（来源 glance522 的 skill_node.level 累加）。
func (s State) Spent() int {
	total := 0
	for _, lv := range s.Nodes {
		if lv > 0 {
			total += lv
		}
	}
	return total
}

// Available 还能投多少点 = 发放 - 已投入，负数按 0。
func (s State) Available() uint32 {
	left := s.Granted - s.Spent()
	if left < 0 {
		left = 0
	}
	return uint32(left)
}

// Unlocked 这个节点现在能不能点：min(熟练度, 110) >= 门槛。
// enabled 那位（表里 enabled=1 的节点）直接放行——不过目前 190 个节点里没有 enabled 的。
func Unlocked(n skilldef.Node, mastery float64) uint8 {
	m := mastery
	if m > MasteryCap {
		m = MasteryCap
	}
	if m+1e-6 >= n.UnlockPct {
		return 1
	}
	return 0
}

// Build311 造 obj311 整棵技能树。avail 传「可分配点数」，直接写进 311.f0
// （客户端拿这个数字的前后差值显示「新增了多少点」）。
func Build311(t *wire.Table, d *skilldef.Defs, st State, avail uint32) (*wire.Obj, error) {
	tree, err := wire.MakeObj(t, ClassTree)
	if err != nil {
		return nil, err
	}
	catItems := make([]wire.Value, 0, len(d.CatOrder))
	for _, catID := range d.CatOrder {
		mastery := st.Cats[catID]
		nodeItems := make([]wire.Value, 0, 24)
		for _, n := range d.NodesOf(catID) {
			node, err := wire.MakeObj(t, ClassNode)
			if err != nil {
				return nil, err
			}
			level := st.Nodes[n.Node]
			if err := node.Set(f313Node, wire.U16Field{V: uint16(n.Node)}); err != nil {
				return nil, err
			}
			if err := node.Set(f313Unlock, wire.U32(n.UnlockBits)); err != nil { // 门槛用原始位，别用十进制重算
				return nil, err
			}
			if err := node.Set(f313Max, wire.U32(uint32(n.MaxLevel))); err != nil {
				return nil, err
			}
			if err := node.Set(f313Level, wire.U32(uint32(level))); err != nil {
				return nil, err
			}
			if err := node.Set(f313Effect, wire.U32(n.EffectBits)); err != nil {
				return nil, err
			}
			if err := node.Set(f313Group, wire.U32(uint32(n.Group))); err != nil {
				return nil, err
			}
			if err := node.Set(f313Open, wire.OptByte{V: Unlocked(n, mastery)}); err != nil {
				return nil, err
			}
			nodeItems = append(nodeItems, node)
		}
		cat, err := wire.MakeObj(t, ClassCat)
		if err != nil {
			return nil, err
		}
		if err := cat.Set(f312Cat, wire.OptByte{V: uint8(catID)}); err != nil {
			return nil, err
		}
		if err := cat.Set(f312Mastery, wire.U32(wire.F32Bits(mastery))); err != nil { // 熟练度：f32 位塞 u32
			return nil, err
		}
		if err := cat.Set(f312F2, wire.U32(0)); err != nil {
			return nil, err
		}
		if err := cat.Set(f312Nodes, wire.Args{Code: "122", Items: nodeItems}); err != nil {
			return nil, err
		}
		catItems = append(catItems, cat)
	}
	if err := tree.Set(f311Points, wire.U32(avail)); err != nil {
		return nil, err
	}
	if err := tree.Set(f311Cats, wire.Args{Code: "121", Items: catItems}); err != nil {
		return nil, err
	}
	if err := tree.Set(f311F2, wire.U64(d.Meta.F2OfRecorded)); err != nil {
		return nil, err
	}
	if err := tree.Set(f311F3, wire.OptByte{V: d.Meta.F3OfRecorded}); err != nil {
		return nil, err
	}
	return tree, nil
}

// ReplyTree 造「5/1 拉树」「5/2 学完」「5/3/5/4 重置后」的统一应答帧：
// 01 | -2 | call_id(u32) | obj311     —— 真服那条 5522B 就是这个形状
func ReplyTree(t *wire.Table, d *skilldef.Defs, st State, callID uint32) ([]byte, error) {
	body, err := body311(t, d, st)
	if err != nil {
		return nil, err
	}
	return append(wire.ResponseHead(callID), body...), nil
}

// PushTree 造 2/4 全量技能树推送帧（整帧自造，不吃录制）：
// 01 | -1 | 1 | 02 04 | args"524"[ obj311 ] | 01
// 客户端打开技能面板第一眼读的就是这条推送，不是 5/1 的应答，所以这条必须发得对。
func PushTree(t *wire.Table, d *skilldef.Defs, st State) ([]byte, error) {
	body, err := body311(t, d, st)
	if err != nil {
		return nil, err
	}
	head := wire.PushHead(1, 2, 4) // 序号照录制写 1（录制里每条推送这个位置都是 1）
	// WrapArgs524 已经带上了推送尾巴 0x01，这里别再补一个字节，多了 1 字节客户端就解不动了
	return append(head, wire.WrapArgs524([][]byte{body})...), nil
}

// body311 造 obj311 并编成字节，应答和推送共用
func body311(t *wire.Table, d *skilldef.Defs, st State) ([]byte, error) {
	tree, err := Build311(t, d, st, st.Available())
	if err != nil {
		return nil, err
	}
	b, err := tree.Encode()
	if err != nil {
		return nil, fmt.Errorf("skilltree: 311 编码失败: %w", err)
	}
	return b, nil
}
