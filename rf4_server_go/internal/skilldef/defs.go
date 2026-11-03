// Package skilldef 只管一件事：读那份「190 个技能节点」的静态定义表。
// 这张表是从真服录制那条 5522B 应答里挖出来的（tools/skill_defs_from_tape.js 生成的 data/skill_defs.json），
// 不猜不编。门槛/效果这种浮点数一律存 f32 原始位（xxx_bits），发字节时必须用位，
// 因为 0.1 这种十进制数转一圈末位会变，逐字节复现真服就会差一个字节。
package skilldef

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
)

// Node 一个技能节点的定义（静态，和游戏账号无关）
type Node struct {
	Node               int     `json:"node"`                 // 节点号，例如 601；客户端按它认是哪个技能
	UnlockPct          float64 `json:"unlock_pct"`           // 学习门槛：这个大类熟练度要到百分之多少才能点，人看的数
	UnlockBits         uint32  `json:"unlock_bits"`          // 门槛的 f32 原始位，发字节用这个
	MaxLevel           int     `json:"max_level"`            // 最高能学到几级；0 = 这个节点现在开放但学不了（真服有这种）
	EffectPct          float64 `json:"effect_pct"`           // 每级效果百分比，人看的数（真服这份全是 0）
	EffectBits         uint32  `json:"effect_bits"`          // 每级效果的 f32 原始位
	Group              int     `json:"group"`                // 互斥组号，0 = 不成组。注意：真服录制里同组学过俩，所以不能拿它当硬规则拦人
	LevelOfRecorded    int     `json:"level_of_recorded"`    // 录制那位当时学到几级 —— 只给测试当标准答案用，不参与线上逻辑
	UnlockedOfRecorded int     `json:"unlocked_of_recorded"` // 录制那位当时是否解锁（1/0）—— 同上，只当答案
	Ordinal            int     `json:"ordinal"`              // 在这棵树里的排列序号，必须照录制的顺序发，客户端格子才对得上
}

// Cat 一个技能大类（1=浮钓 2=路亚 3=喂食器 4=抛竿底钓 5=飞钓 6=烹饪 7=打窝 8=拟饵 9=船 10=冰钓 11=海钓）
type Cat struct {
	Ordinal           int     `json:"ordinal"`             // 大类在 args"121" 列表里的顺序，同样照录制
	MasteryOfRecorded float64 `json:"mastery_of_recorded"` // 录制那位当时的熟练度%，只当答案
	MasteryBits       uint32  `json:"mastery_bits"`        // 那个熟练度的 f32 原始位
	F2OfRecorded      uint32  `json:"f2_of_recorded"`      // 录制里 312 第三个字段的值（全是 0）
	NodeCount         int     `json:"node_count"`          // 这个大类有几个节点，读表时用来自检
	Nodes             []Node  `json:"nodes"`               // 节点定义，顺序就是发包顺序
}

// Meta 这份定义表的出处和一些录制原值，测试要用
type Meta struct {
	Source                      string `json:"source"` // 凭据来源说明
	TopClass                    string `json:"topClass"`
	TopTypeID                   int    `json:"topTypeId"`
	AvailPointsOfRecordedPlayer int    `json:"availPointsOfRecordedPlayer"` // 录制那位的可分配点数（148）
	F2OfRecorded                uint64 `json:"f2_of_recorded"`              // 录制里 311 第三个字段（500）
	F3OfRecorded                uint8  `json:"f3_of_recorded"`              // 录制里 311 第四个字段（0）
	CatCount                    int    `json:"cat_count"`
	NodeCount                   int    `json:"node_count"`
}

// Defs 整张定义表
type Defs struct {
	Meta     Meta        // 出处和录制原值
	CatOrder []int       // 大类发送顺序，录制里是 1,2,3,4,6,7,8,11
	Cats     map[int]Cat // 大类号 -> 定义
}

// rawDefs 是 skill_defs.json 的落盘形状（JSON 里 map 键只能是字符串，所以要转一遍）
type rawDefs struct {
	Meta     Meta           `json:"meta"`
	CatOrder []int          `json:"catOrder"`
	Cats     map[string]Cat `json:"cats"`
}

// Load 读一份 skill_defs.json，顺手把大类号从字符串转成数字，并自检节点个数。
func Load(path string) (*Defs, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw rawDefs
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	d := &Defs{Meta: raw.Meta, CatOrder: raw.CatOrder, Cats: make(map[int]Cat, len(raw.Cats))}
	total := 0
	for k, c := range raw.Cats {
		id, err := strconv.Atoi(k)
		if err != nil {
			return nil, fmt.Errorf("skilldef: 大类键 %q 不是数字", k)
		}
		if c.NodeCount != 0 && c.NodeCount != len(c.Nodes) {
			return nil, fmt.Errorf("skilldef: 大类 %d 写着 %d 个节点，实际 %d 个", id, c.NodeCount, len(c.Nodes))
		}
		d.Cats[id] = c
		total += len(c.Nodes)
	}
	if d.Meta.NodeCount != 0 && d.Meta.NodeCount != total {
		return nil, fmt.Errorf("skilldef: 表里写着共 %d 个节点，实际读到 %d 个", d.Meta.NodeCount, total)
	}
	// 发送顺序照 Meta.CatOrder；表里没列到的大类补在后面，避免整棵树少发一个大类
	seen := map[int]bool{}
	order := make([]int, 0, len(d.Cats))
	for _, c := range raw.CatOrder {
		if _, ok := d.Cats[c]; ok {
			order = append(order, c)
			seen[c] = true
		}
	}
	var rest []int
	for c := range d.Cats {
		if !seen[c] {
			rest = append(rest, c)
		}
	}
	sort.Ints(rest)
	order = append(order, rest...)
	d.CatOrder = order
	return d, nil
}

// NodesOf 取某大类的节点定义（按录制顺序）。没有这个大类返回空切片。
func (d *Defs) NodesOf(cat int) []Node {
	c, ok := d.Cats[cat]
	if !ok {
		return nil
	}
	return c.Nodes
}

// Node 查某个节点定义（跨大类找，因为 5/3 重置只给节点号）。找不到返回 false。
func (d *Defs) Node(nodeID int) (Node, int, bool) {
	for catID, c := range d.Cats {
		for _, n := range c.Nodes {
			if n.Node == nodeID {
				return n, catID, true
			}
		}
	}
	return Node{}, 0, false
}
