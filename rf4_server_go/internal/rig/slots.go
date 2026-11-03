package rig

// 钓组槽位表 + 几个协议常量。表是机器抄来的（见下面注释），常量是拿真服记录对出来的。

// 几个 wire 类的类名（类型号一律从字段表里查，别写死数字）
const (
	rodClass  = "cnolibojkao" // obj 157 竿
	reelClass = "dgjinoebdop" // obj 149 渔轮
	rigClass  = "gbbhdkefnkn" // obj 153 竿上那个钓组对象
	partClass = "gcfcdnfmnbh" // obj 152 钓组里的一条零件
	lineClass = "ohciikgjghe" // obj 140 鱼线那类记录（只是日志里认一认）
)

// 物件记录里几个通用字段名（同一含义在不同类里位置不同，只能按名字找）
const (
	fParent    = "odflendafcg" // 父物品 GUID（装在谁身上）
	fCreated   = "adhajflcpcd" // 创建时间/版本戳（装配要求它严格变大）
	fRigName   = "nhjofmmmngl" // 153 的钓组名
	fRigType   = "fopbhlmkcpe" // 153 的钓组类型
	fRigParts  = "kifjgajijkb" // 153 的零件表（参数表"81"）
	fPartCat   = "fopbhlmkcpe" // 152 的品类
	fPartGUID  = "jgobhijiihn" // 152 的零件 GUID
	fPartIndex = "opbohcdkhlo" // 152 的槽索引
)

// 竿上两个专用组件格：真服的记录里第 3 格是鱼线、第 4 格是渔轮（都是 OPTGUID）
const (
	slotLine = 3
	slotReel = 4
)

// 几个物品品类号（装配就按品类决定进哪个槽）
const (
	catReel   = 6  // 渔轮
	catLine   = 11 // 鱼线
	catHook   = 12 // 鱼钩
	catLeader = 13 // 前导/子线
	catLure   = 17 // 拟饵
	catBait   = 27 // 鱼饵
	catFloat  = 28 // 浮漂
)

// zeroGUID 「这一格空着」
const zeroGUID = "00000000000000000000000000000000"

// 下面三张表是从 Node 版 src/rig.js 的 RIG_SLOT_TEMPLATES / CANONICAL_RIG_INDEX / CAT_NAMES
// 机器抄过来的（抄表脚本 D:\rf4log\gen_slots.py），源头是 glance522 的 rigSlotTemplates/canonicalRigIndex。
// 一行都别手改：改槽索引等于改规矩，要去改源头那张表。

// slotSlot 模板里的一项：某个钓组类型下，品类 cat 能用槽索引 index。
type slotSlot struct {
	cat   int // 物品品类（28 浮漂 12 鱼钩 13 前导 29 铅坠…）
	index int // 153 钓组对象里的槽索引
}

var rigSlotTemplates = map[int][]slotSlot{
	1:  {{cat: 28, index: 125}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	2:  {{cat: 28, index: 125}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	3:  {{cat: 28, index: 125}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	4:  {{cat: 28, index: 125}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	5:  {{cat: 13, index: 150}},
	6:  {{cat: 13, index: 150}, {cat: 12, index: 250}},
	7:  {{cat: 13, index: 150}, {cat: 29, index: 200}, {cat: 33, index: 325}, {cat: 12, index: 250}},
	8:  {{cat: 13, index: 150}, {cat: 29, index: 200}, {cat: 12, index: 250}},
	9:  {{cat: 29, index: 200}, {cat: 33, index: 325}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	10: {{cat: 29, index: 200}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	11: {{cat: 13, index: 150}, {cat: 12, index: 250}},
	12: {{cat: 47, index: 100}, {cat: 12, index: 250}},
	13: {{cat: 47, index: 100}, {cat: 12, index: 250}},
	14: {{cat: 47, index: 100}, {cat: 12, index: 250}},
	15: {{cat: 47, index: 100}, {cat: 12, index: 250}},
	16: {{cat: 47, index: 100}, {cat: 57, index: 175}, {cat: 31, index: 200}, {cat: 12, index: 250}},
	17: {{cat: 47, index: 100}, {cat: 29, index: 200}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	18: {{cat: 38, index: 200}},
	19: {{cat: 44, index: 125}, {cat: 13, index: 150}},
	20: {{cat: 44, index: 125}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	21: {{cat: 28, index: 125}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	22: {{cat: 13, index: 150}},
	23: {{cat: 28, index: 125}, {cat: 29, index: 200}},
	24: {{cat: 47, index: 100}, {cat: 57, index: 175}, {cat: 29, index: 200}, {cat: 29, index: 200}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	25: {{cat: 47, index: 100}, {cat: 57, index: 175}, {cat: 29, index: 200}, {cat: 29, index: 200}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	26: {{cat: 47, index: 100}, {cat: 57, index: 175}, {cat: 29, index: 200}, {cat: 29, index: 200}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	27: {{cat: 47, index: 100}, {cat: 57, index: 175}, {cat: 29, index: 200}, {cat: 29, index: 200}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	28: {{cat: 47, index: 100}, {cat: 57, index: 175}, {cat: 29, index: 200}, {cat: 29, index: 200}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	29: {{cat: 47, index: 100}, {cat: 57, index: 175}, {cat: 29, index: 200}, {cat: 29, index: 200}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	30: {{cat: 28, index: 125}, {cat: 12, index: 250}},
	31: {{cat: 28, index: 125}, {cat: 12, index: 250}},
	32: {{cat: 47, index: 100}, {cat: 57, index: 175}, {cat: 31, index: 200}, {cat: 12, index: 250}},
	33: {{cat: 11, index: 0}, {cat: 69, index: 150}, {cat: 70, index: 151}, {cat: 13, index: 152}},
	34: {{cat: 47, index: 100}, {cat: 57, index: 175}, {cat: 29, index: 200}, {cat: 29, index: 200}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	35: {{cat: 13, index: 150}},
	50: {{cat: 13, index: 175}, {cat: 12, index: 250}},
	51: {{cat: 13, index: 175}, {cat: 12, index: 250}},
	52: {{cat: 13, index: 175}, {cat: 12, index: 250}},
	53: {{cat: 13, index: 175}},
	54: {{cat: 13, index: 175}},
	55: {{cat: 13, index: 175}, {cat: 29, index: 200}, {cat: 13, index: 150}, {cat: 17, index: 325}, {cat: 12, index: 250}, {cat: 13, index: 151}, {cat: 17, index: 326}, {cat: 12, index: 251}},
	56: {{cat: 13, index: 175}, {cat: 29, index: 200}, {cat: 13, index: 150}, {cat: 17, index: 325}, {cat: 12, index: 250}, {cat: 13, index: 151}, {cat: 17, index: 326}, {cat: 12, index: 251}},
	57: {{cat: 13, index: 175}, {cat: 29, index: 200}, {cat: 17, index: 325}, {cat: 17, index: 326}, {cat: 17, index: 327}, {cat: 12, index: 252}, {cat: 17, index: 252}},
	58: {{cat: 13, index: 175}, {cat: 29, index: 200}, {cat: 13, index: 150}, {cat: 12, index: 250}},
	59: {{cat: 13, index: 175}, {cat: 29, index: 200}, {cat: 13, index: 150}, {cat: 12, index: 250}, {cat: 12, index: 251}, {cat: 12, index: 252}},
}

var canonicalRigIndex = map[int]int{
	12: 250,
	13: 150,
	17: 300,
	27: 350,
	28: 125,
}

var catNames = map[int]string{
	6:  "渔轮",
	11: "鱼线",
	12: "鱼钩",
	13: "前导",
	17: "拟饵",
	27: "鱼饵",
	28: "浮漂",
	29: "铅坠/饵团",
	31: "食笼",
	33: "拟饵组",
	35: "线组主体",
	38: "火箭喂食器",
	44: "水雷浮漂",
	47: "坠",
	55: "包装盒",
	56: "竿尖梢",
	57: "方法食笼",
}
