package wire

import (
	"encoding/json"
	"os"
	"strconv"
)

// FieldDef 字段定义：类表里 fields 数组的一项。
type FieldDef struct {
	Idx  int    `json:"idx"`  // 第几个字段（从 0 数，决定字节顺序，不能乱）
	Name string `json:"name"` // 混淆字段名，例如 "ifieemgdomp"；填值按这个名字找
	Zh   string `json:"zh"`   // 人话说明（逆推时猜的语义），只用来打日志和写注释
}

// ClassDef 一个 wire 类的定义：typeID + 每个字段的写法 + 字段名。
type ClassDef struct {
	TypeID int        `json:"typeId"` // 对象类型号，例如 311=技能树
	DTO    string     `json:"dto"`    // 客户端 DTO 名，只给人看
	Ops    []string   `json:"ops"`    // 每个字段怎么写：U32/VAL/U64/OPT/BYTE/U16/STR/... 长度必须等于 Fields 长度
	Fields []FieldDef `json:"fields"` // 字段名和说明，顺序和 Ops 一一对应
}

// WireClass 「物品类别 -> 用哪个 wire 类发出去」。
// 凭据：wire_tables.json 的 categoryWireClass 段（41 个类别，每个都给了类名和类型号）。
// 这一张表是「现拼物件记录」的第一步：类别找不到类就发不出去，
// 客户端拿到一件「竿」却按「鱼线」解会当场 InvalidCastException，整个背包卡在「正在下载」。
type WireClass struct {
	Class  string `json:"class"`  // 混淆类名，例如 cnolibojkao（竿）
	TypeID int    `json:"typeId"` // 物件类型号，例如 157
}

// Table 协议字段表，全后端共用一份（启动时读一次，之后只读不加锁）。
type Table struct {
	// Classes: 混淆类名 -> 类定义
	Classes map[string]ClassDef
	// TypeToClass: 类型号 -> 混淆类名（JSON 里键是字符串，读进来转成数字键）
	TypeToClass map[int]string
	// CategoryWireClass: 物品类别号 -> 该用哪个 wire 类（现拼物件记录用，见 WireClass 注释）
	CategoryWireClass map[int]WireClass
	// Countable: 哪些类别是「可数/可堆叠」的（买两件就要把数量字段乘上去）。
	// 凭据：wire_tables.json 的 countableCategories（12 个类别号）
	Countable map[int]bool
	// FallbackClass / FallbackType: 认不出类别时兜底用的类（Node 版是 ohciikgjghe/140）
	FallbackClass string
	FallbackType  int
}

// rawTable 是 wire_tables.json 的落盘形状，只有读文件时用。
type rawTable struct {
	TypeToClass map[string]string   `json:"typeToClass"`
	Classes     map[string]ClassDef `json:"classes"`
	// CategoryWireClass 的 JSON 键是类别号（字符串），读进来再转成 int 键
	CategoryWireClass map[string]WireClass `json:"categoryWireClass"`
	Countable         []int                `json:"countableCategories"`
	Fallback          struct {
		Class  string `json:"class"`
		TypeID int    `json:"typeId"`
	} `json:"fallback"`
}

// LoadTable 读一份 wire_tables.json。文件路径由 assetpaths 给，本包不自己找路。
func LoadTable(path string) (*Table, error) {
	// 整份读进来：这张表 470KB 左右，常驻内存不心疼
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw rawTable
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	// 类表里有 23 个类比的是「半成品」：ops（每个字段怎么写）齐了，但 fields（字段名）
	// 缺了好几个，甚至一个都没有（obj804 购买回执就是这种）。字节布局以 ops 为准，
	// 名字缺的就补一个 f<序号> 占位名，这样解码不会越界 panic、拼帧也能按序号填。
	// 名字比 ops 多的那种是逆推时多猜的，同样以 ops 为准裁掉，两边长度必须相等。
	for name, c := range raw.Classes {
		raw.Classes[name] = NormalizeClassFields(c)
	}
	t := &Table{
		Classes:       raw.Classes,
		TypeToClass:   make(map[int]string, len(raw.TypeToClass)),
		FallbackClass: raw.Fallback.Class,
		FallbackType:  raw.Fallback.TypeID,
	}
	// JSON 的 map 键只能是字符串，这里转成 int 键，后面按 typeID 查就不用每次转换了
	for k, cls := range raw.TypeToClass {
		id, err := strconv.Atoi(k)
		if err != nil {
			continue // 个别表项写成非数字，跳过不影响认类
		}
		t.TypeToClass[id] = cls
	}
	t.CategoryWireClass = make(map[int]WireClass, len(raw.CategoryWireClass))
	for k, wc := range raw.CategoryWireClass {
		id, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		t.CategoryWireClass[id] = wc
	}
	t.Countable = make(map[int]bool, len(raw.Countable))
	for _, c := range raw.Countable {
		t.Countable[c] = true
	}
	return t, nil
}

// WireClassFor 按物品类别挑该用的 wire 类；没这个类返回 false（这件货就发不出去）。
func (t *Table) WireClassFor(category int) (WireClass, bool) {
	if t == nil {
		return WireClass{}, false
	}
	wc, ok := t.CategoryWireClass[category]
	if !ok || wc.Class == "" {
		return WireClass{}, false
	}
	if _, ok := t.Classes[wc.Class]; !ok {
		return WireClass{}, false // 类名有了但类定义没逆出来，一样发不动
	}
	return wc, true
}

// IsCountable 这个类别是不是可数/可堆叠的（买两件要把数量乘上去）
// BuildClassFor 选「真正用于现拼物件记录」的 wire 类：
// 有专属映射就用专属类；没有映射就回退到 FallbackClass/FallbackType。
// 商店里大量工具、材料和小包装商品没有专属类，但它们仍有数字编号和定义，
// 协议允许按通用物品类下发；这里统一在底层兜底。
func (t *Table) BuildClassFor(category int) (WireClass, bool) {
	if t == nil {
		return WireClass{}, false
	}
	if wc, ok := t.WireClassFor(category); ok {
		return wc, true
	}
	// 未覆盖类别优先回退到真实基础物品类 163（jceogpbfpjh）。
	// 凭据：真服 4/5 里的 kettle（茶壶，类别41，编号8381）实际就是 type163。
	// 旧 fallback 140 是鱼线类，拿来装工具会出现空图标、无详情。
	const miscClass, miscType = "jceogpbfpjh", 163
	if _, ok := t.Classes[miscClass]; ok {
		return WireClass{Class: miscClass, TypeID: miscType}, true
	}
	if t.FallbackClass == "" || t.FallbackType == 0 {
		return WireClass{}, false
	}
	if _, ok := t.Classes[t.FallbackClass]; !ok {
		return WireClass{}, false
	}
	return WireClass{Class: t.FallbackClass, TypeID: t.FallbackType}, true
}

func (t *Table) IsCountable(category int) bool {
	if t == nil {
		return false
	}
	return t.Countable[category]
}

// ClassOf 按类型号找混淆类名；认不出返回空串（调用方自己决定是报错还是当未知对象留着）。
func (t *Table) ClassOf(typeID int) string { return t.TypeToClass[typeID] }

// Class 按类名取定义；第二个返回值 false = 表里没有这个类。
func (t *Table) Class(name string) (ClassDef, bool) {
	c, ok := t.Classes[name]
	return c, ok
}

// ClassByType 按类型号取定义，一步到位。
func (t *Table) ClassByType(typeID int) (ClassDef, string, bool) {
	cls := t.ClassOf(typeID)
	if cls == "" {
		return ClassDef{}, "", false
	}
	c, ok := t.Classes[cls]
	return c, cls, ok
}

// NormalizeClassFields 把类定义里的字段名补齐/裁到和 ops 一样长。
// 字节布局由 ops 说了算（它就是这个类每个字段的写法），fields 只是给人看的名字和说明。
func NormalizeClassFields(c ClassDef) ClassDef {
	for len(c.Fields) < len(c.Ops) {
		i := len(c.Fields)
		c.Fields = append(c.Fields, FieldDef{
			Idx:  i,
			Name: "f" + strconv.Itoa(i), // 占位名：类表还没逆出这一格叫什么
			Zh:   "类表里还没逆出名字，只能按序号填",
		})
	}
	if len(c.Fields) > len(c.Ops) {
		c.Fields = c.Fields[:len(c.Ops)]
	}
	return c
}
