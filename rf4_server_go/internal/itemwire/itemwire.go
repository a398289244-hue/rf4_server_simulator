// Package itemwire 按静态定义「现拼一条物件记录」——买东西、装配、发初始装备都要它。
//
// 为什么非要现拼：以前是拿真服录制的「鱼线模板」把编号改掉就发出去，
// 可记录本身的类还是鱼线那个（typeID 140）。客户端拿到一件「竿」却按鱼线解 ->
// InvalidCastException -> 整个背包卡在「正在下载」。正确做法是先按类别挑对 wire 类，
// 再按那个类的字段表一格一格填（glance522 的 CreateItem 就是这么干的）。
//
// 五个步骤（和他家 CreateItem 一一对应）：
//
//	1 类别 -> wire 类（wire_tables.json 的 categoryWireClass），先填合法空值
//	2 灌属性：静态 attrs 里和字段同名的直接写（浮点在 attrs 里就是 IEEE754 位模式整数）
//	3 身份字段：GUID / 数字编号 / 容器枚举 / 数量 / 创建时间 / 状态位
//	4 嵌套子件：渔轮 13 格、前导 3 格、拟饵出厂钩、竿的出厂部件 + 默认钓组（见 parts.go）
//	5 权威投影：工艺、完好、强度（attrs 的强度基准 × 完好率）
//
// 实测校准（2026-11-02 拿真服购买应答对答案）：mono_15063（鱼线）、tele_10048_SS13（竿）
// 两份真记录逐格对上，差别只有 GUID、创建时间、竿上子件 GUID（真服随机，我们按 md5 派生）。
// 这条校准写在 itemwire_test.go 里，跑不过就是这边跑偏了。
//
// 运行期一行录制都不发：这边只吃 MySQL static_item_def + 协议字段表，字节全是自己拼的。
package itemwire

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"rf4server/internal/itemdef"
	"rf4server/internal/wire"
)

// Options 造服务时的可调项。默认全关（默认口径 = 逐字节对过真服那两份记录）。
type Options struct {
	// EquipFlag 是否给竿/轮这类装配品额外置 2048 装配位。
	// 真服录制里状态位就是 attrs 原值（没这 2048），所以默认 false；
	// 哪天发现客户端不亮装配格，再打开对照。
	EquipFlag bool
}

// Receipt 购买凭证（写进物件记录 effnpjlfjai 那一格，obj166）。
// 金额一律最小单位：1 银币 = 100。
type Receipt struct {
	Store   string // 在哪家店买的
	UserID  int64  // 买家账号 id
	Balance int64  // 买完之后的余额
	Price   int64  // 成交价
	User    string // 买家名字
}

// RigDefaults 竿上「默认钓组」那格（obj153）。不给就是「未装配」空钓组。
type RigDefaults struct {
	Name  *string // 钓组名字，null = 无名
	Type  *uint32 // 钓组类型，默认 1
	Depth *uint32 // 深度的 f32 位模式，默认 1.0
}

// Input 现拼一件货要的信息。零值都是「按真服新买那一件的做法」：
// GUID 随机、工艺/完好 1.0、创建时间 1、更新时间 0、容器 0（背包）。
type Input struct {
	Key       string       // 物品编号（必填），如 tele_10112_4
	GUID      string       // 物件 GUID；空 = 现生成一个
	Units     int          // 可堆叠件数（不堆叠的类忽略）；0 按 1 算
	Parent    string       // 装在谁里面（竿/仓库），空 = 没有父
	Container *uint8       // 容器枚举：0 背包 1 帐篷 2 车 3 船 10 房；nil = 0
	Work      *uint32      // 工艺（f32 位模式）；nil = 1.0
	Dur       *uint32      // 完好率（f32 位模式）；nil = 1.0
	Created   *uint64      // 创建时间（.NET ticks）；nil = 1（真服新买的就是这个值）
	Updated   *uint64      // 更新时间（FILETIME）；nil = 0
	Receipt   *Receipt     // 购买凭证；nil = 不写这一格
	Rig       *RigDefaults // 竿的默认钓组
}

// Built 拼好的东西。Frame 就是物件记录字节（开头带 01 + typeID），
// 直接塞进参数表"61" 就能下发；SubGUIDs 是顺带生成的子件 GUID（写库/日志要用）。
type Built struct {
	Key      string   // 物品编号
	Class    string   // 用的哪个 wire 类（出岔子时先看这格对不对）
	TypeID   int      // 物件类型号
	Cat      int      // 物品类别号
	GUID     string   // 这件货自己的 GUID
	SubGUIDs []string // 子件 GUID（竿的出厂坯、手柄、拟饵钩…）
	Frame    []byte   // 物件记录字节
	Bytes    int      // Frame 长度，日志用
}

// Service 现拼服务。自己不带可变状态：定义表和字段表都是只读的。
type Service struct {
	table *wire.Table   // 协议字段表（类有哪些字段、每格怎么写）
	defs  *itemdef.Defs // 静态定义（编号、类别、属性）
	opts  Options       // 可调口径
}

// New 造服务。table/defs 缺了方法里会明确报错，不会闷头返回一条空记录。
func New(t *wire.Table, defs *itemdef.Defs, opts Options) *Service {
	return &Service{table: t, defs: defs, opts: opts}
}

// ClassFor 这件货该用哪个 wire 类（上架闸门要用：没类的类别发不出去，就别挂上架）
func (s *Service) ClassFor(def *itemdef.Def) (wire.WireClass, bool) {
	if def == nil {
		return wire.WireClass{}, false
	}
	return s.table.BuildClassFor(def.Cat)
}

// Build 现拼一件货的物件记录。
func (s *Service) Build(in Input) (*Built, error) {
	if s == nil || s.table == nil {
		return nil, errors.New("itemwire: 协议字段表没载入，拼不了物件记录")
	}
	if s.defs == nil {
		return nil, errors.New("itemwire: 物品定义表没载入，拼不了物件记录")
	}
	def, ok := s.defs.Get(in.Key)
	if !ok {
		return nil, fmt.Errorf("itemwire: 没有这件货的静态定义: %s", in.Key)
	}
	wc, ok := s.table.BuildClassFor(def.Cat)
	if !ok {
		return nil, fmt.Errorf("itemwire: 类别 %d 没有可用的 wire 类，%s 发不出去", def.Cat, in.Key)
	}
	obj, err := s.table.NewObj(wc.TypeID)
	if err != nil {
		return nil, err
	}
	// 第 2 步：先把定义属性灌进去（后面身份字段会覆盖该覆盖的那几格）
	applyDefAttrs(def, obj)

	// 第 3 步：身份字段
	guid := strings.ToLower(strings.TrimSpace(in.GUID))
	if guid == "" {
		guid = NewGUID()
	}
	setGUID(obj, fGUID, guid)
	setNum(obj, fNum, uint64(def.ID))
	if in.Parent != "" {
		setGUID(obj, fParent, strings.ToLower(in.Parent))
	}
	if idx := fieldIdx(obj, fSlot); idx >= 0 && obj.Fields[idx].Op == "BYTE" {
		setByte(obj, fSlot, byteVal(in.Container))
	}
	// 数量：只有「可数/可堆叠」那 12 个类别要乘份数；鱼线(11)、前导(15) 那类这里存的是长度，别动
	if s.table.IsCountable(def.Cat) {
		q, ok := numByName(obj, fQty)
		if !ok || q == 0 {
			q = 1
		}
		units := uint64(in.Units)
		if units == 0 {
			units = 1
		}
		setNum(obj, fQty, q*units)
	}
	// 第 5 步：工艺 / 完好 / 强度（强度 = 基准值 × 完好率，基准值在 attrs 里是位模式）
	work, dur := workBits(in.Work), workBits(in.Dur)
	setNum(obj, fWork, uint64(work))
	setNum(obj, fDur, uint64(dur))
	if bits, ok := strengthBits(def, dur); ok {
		setNum(obj, fStrength, uint64(bits))
	}
	setU64(obj, fCreated, createdTicks(in.Created))
	setU64(obj, fUpdated, updatedTicks(in.Updated))
	if v, ok := def.AttrUint(fState); ok {
		bits := v & 0xffffffff
		if s.opts.EquipFlag && equipClass2048[wc.Class] {
			bits |= equipFlag2048
		}
		setNum(obj, fState, bits)
	}

	built := &Built{Key: def.Key, Class: wc.Class, TypeID: wc.TypeID, Cat: def.Cat, GUID: guid}

	// 第 4 步：嵌套子件（渔轮 13 格 / 前导 3 格 / 拟饵出厂钩 / 竿的出厂部件 + 默认钓组）
	sub := &subOpts{work: work, dur: dur, created: createdTicks(in.Created)}
	switch wc.Class {
	case clsReel:
		materializeSubs(s, def, reelPartKeys, obj, guid, sub, true, built)
	case clsLeader:
		materializeSubs(s, def, leaderPartKeys, obj, guid, sub, true, built)
		if cur, _ := numByName(obj, fStrength); cur == 0 {
			if cfg, ok := def.AttrObj(leaderPartKeys[0]); ok {
				if base, ok := cfg[subBase]; ok {
					setNum(obj, fStrength, uint64(f32bits(f32val(base)*f32f(work))))
				}
			}
		}
	case clsLure:
		lureHooks(s, def, obj, guid, sub, built)
	case clsRod:
		rebuildRod(s, def, obj, guid, sub, in.Rig, built)
	}

	// 购买凭证：新买的现填；老记录重拼时调用方会把原来那一格搬过来（见 ReceiptRaw）
	if in.Receipt != nil {
		if rc, err := buildReceipt(s.table, *in.Receipt); err == nil && rc != nil {
			if idx := fieldIdx(obj, fOrigin); idx >= 0 {
				obj.Fields[idx].Value = rc
			}
		}
	}

	frame, err := obj.Encode()
	if err != nil {
		return nil, fmt.Errorf("itemwire: 拼 %s(%s) 的记录失败: %w", def.Key, wc.Class, err)
	}
	built.Frame, built.Bytes = frame, len(frame)
	return built, nil
}

// applyDefAttrs 把 attrs 里和字段同名的直接灌进对象。
// 只认标量（数字/串/布尔/null）：对象和数组走第 4 步的专门逻辑，这儿一律跳过。
func applyDefAttrs(def *itemdef.Def, obj *wire.Obj) {
	for name, v := range def.Attrs {
		idx := fieldIdx(obj, name)
		if idx < 0 {
			continue // 这个属性在当前类里没位置（attrs 是一堆类共用一份表）
		}
		op := obj.Fields[idx].Op
		switch v.Kind {
		case itemdef.Null:
			setNullField(obj, idx, op)
		case itemdef.Bool:
			var n uint64
			if v.U != 0 {
				n = 1
			}
			setNumByOp(obj, idx, op, n)
		case itemdef.Number, itemdef.Float:
			setNumByOp(obj, idx, op, scalarBits(v))
		case itemdef.Str:
			if op == "STR" {
				obj.Fields[idx].Value = wire.StrField{S: v.S}
			} else if op == "U16STR" {
				obj.Fields[idx].Value = wire.Str16Field{S: v.S}
			} else if op == "U32N" {
				obj.Fields[idx].Value = wire.Str32Field{S: v.S}
			}
		default:
			// 对象/数组：真服这一步不写（客户端按数字编号自己查），留合法空值
		}
	}
}

// scalarBits 属性值要写进帧里的那个数：整数直接用（浮点属性本来就是位模式整数），
// 真浮点按 32 位截。
func scalarBits(v itemdef.Value) uint64 {
	if v.Kind == itemdef.Float {
		return uint64(math.Float32bits(float32(v.F)))
	}
	return v.U & 0xffffffff
}

// setNumByOp 按字段写法决定存 32 位还是 64 位
func setNumByOp(obj *wire.Obj, idx int, op string, v uint64) {
	switch op {
	case "U64", "DOUBLE":
		obj.Fields[idx].Value = wire.U64(v)
	case "U16":
		obj.Fields[idx].Value = wire.U16Field{V: uint16(v)}
	case "OPT", "BYTE":
		obj.Fields[idx].Value = wire.OptByte{V: uint8(v)}
	case "FLOAT":
		obj.Fields[idx].Value = wire.U32(uint32(v)) // 浮点在帧里就是位模式整数
	default:
		obj.Fields[idx].Value = wire.U32(uint32(v))
	}
}

// setNullField 把这一格写回「合法空值」（null 和 0 在协议里是两回事）
func setNullField(obj *wire.Obj, idx int, op string) {
	switch op {
	case "STR":
		obj.Fields[idx].Value = wire.StrField{IsNull: true}
	case "U16STR":
		obj.Fields[idx].Value = wire.Str16Field{IsNull: true}
	case "U32N":
		obj.Fields[idx].Value = wire.Str32Field{IsNull: true}
	case "OPTGUID":
		obj.Fields[idx].Value = wire.OptGUIDField{Present: false}
	case "BYTES":
		obj.Fields[idx].Value = wire.Bytes{IsNull: true}
	default:
		obj.Fields[idx].Value = wire.Null{}
	}
}

// fieldIdx 按混淆字段名找第几格；没这个名字返回 -1（同一含义的字段在不同类里位置不同，只能按名字找）
func fieldIdx(o *wire.Obj, name string) int {
	if o == nil {
		return -1
	}
	for i := range o.Fields {
		if o.Fields[i].Name == name {
			return i
		}
	}
	return -1
}

// setNum 写一个数字字段（U32/FLOAT 存 32 位，U64/DOUBLE 存 64 位）。没这个字段就 quietly 跳过。
func setNum(o *wire.Obj, name string, v uint64) bool {
	idx := fieldIdx(o, name)
	if idx < 0 {
		return false
	}
	setNumByOp(o, idx, o.Fields[idx].Op, v)
	return true
}

// setU64 写一个必须是 64 位的字段（创建/更新时间那种）
func setU64(o *wire.Obj, name string, v uint64) bool {
	idx := fieldIdx(o, name)
	if idx < 0 {
		return false
	}
	if op := o.Fields[idx].Op; op == "U64" || op == "DOUBLE" {
		o.Fields[idx].Value = wire.U64(v)
		return true
	}
	o.Fields[idx].Value = wire.U32(uint32(v))
	return true
}

// setByte 写一个单字节字段
func setByte(o *wire.Obj, name string, v uint8) bool {
	idx := fieldIdx(o, name)
	if idx < 0 {
		return false
	}
	o.Fields[idx].Value = wire.OptByte{V: v}
	return true
}

// setGUID 写 GUID：普通 GUID16 直接写；可空的 OPTGUID 带一个「有没有」前缀
func setGUID(o *wire.Obj, name, hexv string) bool {
	idx := fieldIdx(o, name)
	if idx < 0 {
		return false
	}
	switch o.Fields[idx].Op {
	case "GUID16":
		o.Fields[idx].Value = wire.GUID{Hex: hexv}
	case "OPTGUID":
		o.Fields[idx].Value = wire.OptGUIDField{Present: true, Hex: hexv}
	default:
		return false
	}
	return true
}

// setStr 写一个短串字段
func setStr(o *wire.Obj, name, s string) bool {
	idx := fieldIdx(o, name)
	if idx < 0 {
		return false
	}
	if o.Fields[idx].Op != "STR" {
		return false
	}
	o.Fields[idx].Value = wire.StrField{S: s}
	return true
}

// numByName 读一个数字字段的值（数量、创建时间那种）
func numByName(o *wire.Obj, name string) (uint64, bool) {
	idx := fieldIdx(o, name)
	if idx < 0 {
		return 0, false
	}
	switch v := o.Fields[idx].Value.(type) {
	case wire.Num32:
		return uint64(v.V), true
	case wire.Num64:
		return v.V, true
	case wire.OptByte:
		return uint64(v.V), true
	case wire.U16Field:
		return uint64(v.V), true
	}
	return 0, false
}

// strengthBits 强度 = 基准值（attrs 里是 f32 位模式）× 完好率。
// 渔轮那种基准值嵌在子配置里的，先认嵌套那格（glance522 strengthBitsFor 的口径）。
func strengthBits(def *itemdef.Def, dur uint32) (uint32, bool) {
	if nested, ok := def.AttrObj(subNested); ok {
		if v, ok := nested[subDrag]; ok && numeric(v) {
			return f32bits(f32val(v) * f32f(dur)), true
		}
	}
	if v, ok := def.Attr(subBase); ok && numeric(v) {
		return f32bits(f32val(v) * f32f(dur)), true
	}
	return 0, false
}

func numeric(v itemdef.Value) bool {
	return v.Kind == itemdef.Number || v.Kind == itemdef.Float
}

// f32val 属性里的位模式整数 -> 浮点值
func f32val(v itemdef.Value) float64 {
	if v.Kind == itemdef.Float {
		return v.F
	}
	return float64(math.Float32frombits(uint32(v.U)))
}

// f32f 帧里的 f32 位模式 -> 浮点值
func f32f(bits uint32) float64 { return float64(math.Float32frombits(bits)) }

// f32bits 浮点值 -> 帧里的 f32 位模式
func f32bits(f float64) uint32 { return math.Float32bits(float32(f)) }

// workBits 工艺/完好率：不给就是 1.0
func workBits(p *uint32) uint32 {
	if p == nil {
		return f32One
	}
	return *p
}

// byteVal 容器枚举：不给就是 0（背包）
func byteVal(p *uint8) uint8 {
	if p == nil {
		return 0
	}
	return *p
}

// createdTicks 创建时间（.NET ticks）。真服新买的那一件填的是 1，不是时刻，
// 所以默认跟着填 1；要发真时刻就显式传一个值进来。
func createdTicks(p *uint64) uint64 {
	if p == nil {
		return 1
	}
	return *p
}

// updatedTicks 更新时间（FILETIME）：真服新买的那一件是 0
func updatedTicks(p *uint64) uint64 {
	if p == nil {
		return 0
	}
	return *p
}

// NewGUID 生成一个物件 GUID（随机 + 版本号那两位，和真服那种 v4 形状一致）
func NewGUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源出问题就用当前时刻凑一个：GUID 只要不撞就行，不值得为它打断整条命令
		ns := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(ns >> (8 * (i % 8)))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[:])
}

// ChildGUID 子件的 GUID：按「母 GUID|盐」md5 派生，同一个母物品重登不会变。
// 真服是随机的，这一处刻意的不同不影响客户端（子件 GUID 只要求自洽）。
func ChildGUID(hostGUID, salt string) string {
	sum := md5.Sum([]byte(hostGUID + "|" + salt))
	return hex.EncodeToString(sum[:])
}

// buildReceipt 拼购买凭证 obj166
func buildReceipt(t *wire.Table, r Receipt) (*wire.Obj, error) {
	typeID, ok := typeIDOfClass(t, clsReceipt)
	if !ok {
		return nil, fmt.Errorf("itemwire: 字段表里没有购买凭证类 %s", clsReceipt)
	}
	obj, err := t.NewObj(typeID)
	if err != nil {
		return nil, err
	}
	setStr(obj, rcStore, r.Store)
	setNum(obj, rcBuyerID, uint64(r.UserID))
	setNum(obj, rcBalance, uint64(max64(0, r.Balance)))
	setU64(obj, rcPrice, uint64(max64(0, r.Price)))
	setNum(obj, rcFlag, 0) // 这一格是个 OPT 小旗子，真服那条填 0
	if !setStr(obj, rcBuyer, r.User) {
		return nil, fmt.Errorf("itemwire: 凭证类里没有买家名字那格（%s）", rcBuyer)
	}
	return obj, nil
}

// typeIDOfClass 按混淆类名查类型号（类表才是权威，别处不写死数字）
func typeIDOfClass(t *wire.Table, class string) (int, bool) {
	c, ok := t.Class(class)
	if !ok {
		return 0, false
	}
	return c.TypeID, true
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
