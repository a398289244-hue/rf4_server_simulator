// Package itemdef 是「一件货的静态定义」这一层：数字编号、类别、那一堆属性。
//
// 它只管「定义是什么」，不管怎么拼字节（拼字节在 internal/itemwire），
// 也不管数据从 MySQL 还是假表来（那是 Loader 适配器的事）—— 三件事分开，
// 以后换数据源、加缓存、单测造假表，都只动一处。
//
// 定义表就是 MySQL static_item_def（14826 行），少爷可以直接 SELECT 直接 UPDATE；
// 它是「改版本」级别的数据，不是玩家存档，所以开机拉一次进内存、不回写。
package itemdef

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const (
	bundleContentsAttr = "nbkhfhenbag"
	bundleItemKeyAttr  = "cemjmlpafmc"
	bundleQuantityAttr = "ljnnpokapig"
)

// BundleItem 是类别 55 礼包/包装里的一项真实物品。
// 包装本身不能作为普通背包物件下发；购买时要按这里的内容拆包入库。
type BundleItem struct {
	Key      string
	Quantity uint64
}

// Def 一件货的定义
type Def struct {
	Key   string           // 物品编号，如 tele_10112_4
	ID    int              // 服务器数字编号（客户端按它认货）；0 = 没编号，上架了客户端也认不出
	Cat   int              // 类别号（3=竿 6=渔轮 11=鱼线 12=钩 27=饵 28=浮漂…）
	Kind  string           // 系列名，只为日志/排查
	Brand string           // 品牌，同上
	Attrs map[string]Value // 属性表（字段名 -> 值，浮点是位模式整数）
}

// Attr 取一个属性；没有返回第二个值 false
func (d *Def) Attr(name string) (Value, bool) {
	if d == nil || d.Attrs == nil {
		return Value{}, false
	}
	v, ok := d.Attrs[name]
	return v, ok
}

// AttrUint 取一个整型属性（浮点位模式也算整数）；没有返回第二个值 false
func (d *Def) AttrUint(name string) (uint64, bool) {
	v, ok := d.Attr(name)
	if !ok {
		return 0, false
	}
	switch v.Kind {
	case Number, Bool:
		return v.U, true
	case Float:
		return uint64(v.F), true
	}
	return 0, false
}

// AttrObj 取一个嵌套对象属性（竿的出厂部件、渔轮的刹车片配置都是这种）
func (d *Def) AttrObj(name string) (map[string]Value, bool) {
	v, ok := d.Attr(name)
	if !ok || v.Kind != Obj {
		return nil, false
	}
	return v.Obj, true
}

// AttrArr 取一个数组属性（拟饵出厂钩那种）
func (d *Def) AttrArr(name string) ([]Value, bool) {
	v, ok := d.Attr(name)
	if !ok || v.Kind != Arr {
		return nil, false
	}
	return v.Arr, true
}

// BundleItems 读取类别 55 定义里的内容清单。
// 返回 false 表示定义不是礼包，或内容字段缺失/损坏；不静默猜内容，避免扣钱后发错货。
func (d *Def) BundleItems() ([]BundleItem, bool) {
	if d == nil || d.Cat != 55 {
		return nil, false
	}
	arr, ok := d.AttrArr(bundleContentsAttr)
	if !ok || len(arr) == 0 {
		return nil, false
	}
	out := make([]BundleItem, 0, len(arr))
	for _, raw := range arr {
		if raw.Kind != Obj {
			return nil, false
		}
		key, keyOK := raw.Obj[bundleItemKeyAttr]
		qty, qtyOK := raw.Obj[bundleQuantityAttr]
		if !keyOK || key.Kind != Str || strings.TrimSpace(key.S) == "" || !qtyOK {
			return nil, false
		}
		var n uint64
		switch qty.Kind {
		case Number, Bool:
			n = qty.U
		case Float:
			if qty.F > 0 {
				n = uint64(qty.F)
			}
		}
		if n == 0 {
			return nil, false
		}
		out = append(out, BundleItem{Key: strings.TrimSpace(key.S), Quantity: n})
	}
	return out, len(out) > 0
}

// Row 定义表的一行原文（AttrsJSON 还没解析）
type Row struct {
	Key       string
	NumericID int
	Category  int
	Kind      string
	Brand     string
	AttrsJSON string
}

// Loader 把定义表整张交出来。开机拉一次；失败就说清楚是连不上库还是查询挂了。
type Loader func(ctx context.Context) ([]Row, error)

// Defs 查表用的定义库（内存）。键统一小写：物品编号在库里是小写，
// 帧里解出来的名字大小写不一定，两边先归一再比，省得「明明有却说没有」。
type Defs struct {
	byKey map[string]*Def
	byID  map[int]*Def // 数字编号 -> 定义（排查「客户端显示成另一件货」时好用）
	rows  int          // 原始行数，只为日志
}

// Load 拉一遍定义表并解析属性。个别行属性解析不了不会把整张表带崩：
// 它们退化成「没属性」的定义（发出去仍是合法空值），错误列表里会报出来。
func Load(ctx context.Context, load Loader) (*Defs, error) {
	if load == nil {
		return nil, fmt.Errorf("itemdef: 没给定义表来源")
	}
	rows, err := load(ctx)
	if err != nil {
		return nil, err
	}
	d := &Defs{byKey: make(map[string]*Def, len(rows)), byID: map[int]*Def{}, rows: len(rows)}
	var bad []string
	badCount := 0
	for _, r := range rows {
		attrs, err := ParseAttrs(r.AttrsJSON)
		if err != nil {
			badCount++
			if len(bad) < 5 {
				bad = append(bad, r.Key+": "+err.Error())
			}
		}
		def := &Def{Key: r.Key, ID: r.NumericID, Cat: r.Category, Kind: r.Kind, Brand: r.Brand, Attrs: attrs}
		key := strings.ToLower(strings.TrimSpace(r.Key))
		if key == "" {
			continue
		}
		d.byKey[key] = def
		if def.ID != 0 {
			if _, dup := d.byID[def.ID]; !dup {
				d.byID[def.ID] = def
			}
		}
	}
	if len(bad) > 0 {
		return d, fmt.Errorf("itemdef: %d 行属性解析失败（头几条：%s）", badCount, strings.Join(bad, "; "))
	}
	return d, nil
}

// Get 按物品编号取定义；没有返回 false
func (d *Defs) Get(key string) (*Def, bool) {
	if d == nil {
		return nil, false
	}
	def, ok := d.byKey[strings.ToLower(strings.TrimSpace(key))]
	return def, ok
}

// ByID 按服务器数字编号取定义（同一个编号只留第一条）
func (d *Defs) ByID(id int) (*Def, bool) {
	if d == nil {
		return nil, false
	}
	def, ok := d.byID[id]
	return def, ok
}

// Count 一共几件货有定义
func (d *Defs) Count() int {
	if d == nil {
		return 0
	}
	return len(d.byKey)
}

// Rows 原始读了多少行（和 Count 的差就是键为空的行）
func (d *Defs) Rows() int {
	if d == nil {
		return 0
	}
	return d.rows
}

// Categories 按类别号分组列物品编号（只为日志/体检用，比如「类别 3 有 80 根竿」）。
// 每组的编号排序，输出稳定。
func (d *Defs) Categories() map[int][]string {
	out := map[int][]string{}
	if d == nil {
		return out
	}
	for _, def := range d.byKey {
		out[def.Cat] = append(out[def.Cat], def.Key)
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

// CategoryOf 按物品编号查类别号。装配（往竿上装东西）就靠它决定进哪个槽，
// 所以单独给一个最小方法，别让 rig 包为了拿类别去依赖整张 Def 结构。
func (d *Defs) CategoryOf(key string) (int, bool) {
	def, ok := d.Get(key)
	if !ok {
		return 0, false
	}
	return def.Cat, true
}
