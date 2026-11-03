package itemwire

// 嵌套子件这一层（glance522 materializeSubObjects / buildFactoryChild / rebuildRod 的 Go 版）。
//
// 为什么必须把子件一起拼出来：竿的定义里写着出厂坯、手柄是哪些货（attrs 那三格），
// 真服发的那条竿记录里也是「母记录里嵌着两条子记录」。少拼一层，客户端开背包就会
// 在解到那一格时断掉；多拼一层又会凭空多出装不上的东西。所以这块照真服形状来。

import (
	"strconv"

	"rf4server/internal/itemdef"
	"rf4server/internal/wire"
)

// subOpts 造子件时从母物品继承下来的那几项
type subOpts struct {
	work    uint32 // 工艺（f32 位模式）
	dur     uint32 // 完好率
	created uint64 // 创建时间：子件继承母物品那一格（真服就是这样）
}

// materializeSubs 按 keys 指定的那几格配置，把子件拼出来塞进母物品对应的位置。
//
// keys 是 attrs 里的键名（也是母物品类里那一格的名字）：渔轮 13 格、前导 3 格都走这儿。
// 已经有值的那格不覆盖 —— 调用方可能已经塞过真实物（装配后的件），别拿定义表把它盖掉。
func materializeSubs(s *Service, def *itemdef.Def, keys []string, obj *wire.Obj,
	hostGUID string, o *subOpts, mount bool, built *Built) {
	for _, key := range keys {
		cfg, ok := def.AttrObj(key)
		if !ok {
			continue
		}
		subKey, ok := cfg[subKey]
		if !ok || subKey.Kind != itemdef.Str || subKey.S == "" {
			continue // 这一格没有「子件是哪个货」
		}
		if kind, ok := cfg[subKind]; ok && subUnbuiltKinds[int(kind.U)] {
			continue // 连 glance522 都还没物化的类别（81）
		}
		idx := fieldIdx(obj, key)
		if idx < 0 || idx >= len(obj.Fields) {
			continue
		}
		if !isEmptyValue(obj.Fields[idx].Value) {
			continue
		}
		sub, ok := buildSub(s, subKey.S, hostGUID, key, cfg, o, mount, built)
		if !ok {
			continue
		}
		obj.Fields[idx].Value = sub
	}
}

// buildSub 拼一个子件。cfg 是母物品 attrs 里那一格的配置（曳力、状态位可能写在里面）。
// 返回 false = 这件子件没定义/没编号/类别没类，母物品照样能发，只是少这一格。
func buildSub(s *Service, subItemKey, hostGUID, salt string, cfg map[string]itemdef.Value,
	o *subOpts, mount bool, built *Built) (*wire.Obj, bool) {
	def, ok := s.defs.Get(subItemKey)
	if !ok || def.ID == 0 {
		return nil, false // 没定义或编号 0（比如 ab_tea_can）不物化
	}
	wc, ok := s.table.BuildClassFor(def.Cat)
	if !ok {
		return nil, false
	}
	obj, err := s.table.NewObj(wc.TypeID)
	if err != nil {
		return nil, false
	}
	applyDefAttrs(def, obj)

	guid := ChildGUID(hostGUID, salt)
	if built != nil {
		built.SubGUIDs = append(built.SubGUIDs, guid)
	}
	setGUID(obj, fGUID, guid)
	setNum(obj, fNum, uint64(def.ID))
	setGUID(obj, fParent, hostGUID)
	setNum(obj, fSlot, 4) // 4 = 装在钓组/装配位上（出厂嵌入件由 rebuildRod 那条改成 0，见下面注释）
	setNum(obj, fWork, uint64(o.work))
	setNum(obj, fDur, uint64(o.dur))
	if bits, ok := strengthBits(def, o.dur); ok {
		setNum(obj, fStrength, uint64(bits))
	}
	// 创建/更新：子件继承母物品那两格（真服录制的出厂坯就是 1 / 0）
	if cur, ok := numByName(obj, fCreated); ok && cur == 0 {
		setU64(obj, fCreated, o.created)
	}
	if cur, ok := numByName(obj, fUpdated); ok && cur == 0 {
		setU64(obj, fUpdated, 0)
	}
	// 状态位：优先用「母物品这一格配置里的值」（这是拿真服记录对答案对出来的），
	// 没有才退回子定义自己的；挂载中的件再补 1024 那一位
	base, hasBase := stateOf(cfg)
	if !hasBase {
		base, hasBase = def.AttrUint(fState)
	}
	if hasBase {
		bits := base & 0xffffffff
		if mount {
			bits |= subMountFlag
		}
		setNum(obj, fState, bits)
	}
	// 渔轮刹车片(类别 9)的曳力来自母轮嵌套配置里的 omghfmdfjpc × 工艺，不是母轮顶层强度
	if def.Cat == 9 {
		if v, ok := cfg[subDrag]; ok && numeric(v) {
			setNum(obj, fDrag, uint64(f32bits(f32val(v)*f32f(o.work))))
		}
	}
	return obj, true
}

// stateOf 从一段属性里取状态位（attrs 里那个 hgaijieklek）
func stateOf(attrs map[string]itemdef.Value) (uint64, bool) {
	if attrs == nil {
		return 0, false
	}
	v, ok := attrs[fState]
	if !ok {
		return 0, false
	}
	switch v.Kind {
	case itemdef.Number, itemdef.Bool:
		return v.U, true
	case itemdef.Float:
		return uint64(v.F), true
	}
	return 0, false
}

// lureHooks 拟饵的出厂钩：attrs 里是一串配置，真服把它们塞进 args"60" 容器里。
// 一件都没拼成就不写这一格（留合法空值），别塞一个空容器进去让客户端解歪。
func lureHooks(s *Service, def *itemdef.Def, obj *wire.Obj, hostGUID string, o *subOpts, built *Built) {
	cfgs, ok := def.AttrArr(lureHookKey)
	if !ok || len(cfgs) == 0 {
		return
	}
	idx := fieldIdx(obj, lureHookKey)
	if idx < 0 || obj.Fields[idx].Op != "VAL" {
		return
	}
	var items []wire.Value
	for i, cfgV := range cfgs {
		if cfgV.Kind != itemdef.Obj {
			continue
		}
		subKey, ok := cfgV.Obj[subKey]
		if !ok || subKey.Kind != itemdef.Str || subKey.S == "" {
			continue
		}
		// 出厂钩不算「已挂载」（真服那几枚钩的状态位没有 1024）
		sub, ok := buildSub(s, subKey.S, hostGUID, lureHookKey+strconv.Itoa(i), cfgV.Obj, o, false, built)
		if !ok {
			continue
		}
		items = append(items, sub)
	}
	if len(items) == 0 {
		return
	}
	obj.Fields[idx].Value = wire.Args{Code: "60", Items: items}
}

// rebuildRod 竿：f0/f1/f2 三格出厂部件 + f5 默认钓组。
// 这三格是按位置写的（不是按名字）—— 真服那条竿记录的前三格就是坯/手柄/第三节，
// glance522 也是这么写；换成按名字找会找不到（那三格的名字在类表里还没逆出来）。
func rebuildRod(s *Service, def *itemdef.Def, obj *wire.Obj, hostGUID string, o *subOpts,
	rig *RigDefaults, built *Built) {
	for i, key := range rodPartKeys {
		cfg, ok := def.AttrObj(key)
		if !ok {
			continue
		}
		subKey, ok := cfg[subKey]
		if !ok || subKey.Kind != itemdef.Str || subKey.S == "" {
			continue
		}
		if i >= len(obj.Fields) {
			continue
		}
		if !isEmptyValue(obj.Fields[i].Value) {
			continue
		}
		sub, ok := buildSub(s, subKey.S, hostGUID, rodPartSalts[i], cfg, o, false, built)
		if !ok {
			continue
		}
		// 出厂嵌入件（坯/手柄）真服那一格填 0，不是 4：填错了客户端会当成「另装上去的」
		setNum(sub, fSlot, 0)
		obj.Fields[i].Value = sub
	}
	if idx := fieldIdx(obj, fRig); idx >= 0 && isEmptyValue(obj.Fields[idx].Value) {
		if r := defaultRig(s.table, rig); r != nil {
			obj.Fields[idx].Value = r
		}
	}
}

// defaultRig 竿上那格「默认钓组」（obj153）。没给参数就是未装配的空钓组：
// 名字 null、类型 1、组件表 args"81" 空、深度 1.0 —— 真服新买那根竿就是这个形状。
func defaultRig(t *wire.Table, rig *RigDefaults) *wire.Obj {
	typeID, ok := typeIDOfClass(t, clsRig)
	if !ok {
		return nil
	}
	obj, err := t.NewObj(typeID)
	if err != nil {
		return nil
	}
	if rig != nil && rig.Name != nil {
		setStr(obj, "nhjofmmmngl", *rig.Name) // #00 钓组名（类表里这一格逆出来就叫这名字）
	}
	if idx := fieldIdx(obj, "fopbhlmkcpe"); idx >= 0 { // #01 钓组类型
		typ := uint64(1)
		if rig != nil && rig.Type != nil {
			typ = uint64(*rig.Type)
		}
		setNumByOp(obj, idx, obj.Fields[idx].Op, typ)
	}
	if idx := fieldIdx(obj, "kifjgajijkb"); idx >= 0 && obj.Fields[idx].Op == "VAL" { // #02 组件表
		obj.Fields[idx].Value = wire.Args{Code: "81", Items: nil}
	}
	if idx := fieldIdx(obj, "laigjanbknk"); idx >= 0 { // #03 深度（f32 位模式）
		depth := uint64(f32One)
		if rig != nil && rig.Depth != nil {
			depth = uint64(*rig.Depth)
		}
		setNumByOp(obj, idx, obj.Fields[idx].Op, depth)
	}
	return obj
}

// isEmptyValue 这一格还是「合法空值」吗？是才允许填子件，别把已有的真值盖掉。
func isEmptyValue(v wire.Value) bool {
	if v == nil {
		return true
	}
	switch x := v.(type) {
	case wire.Null:
		return true
	case wire.Args:
		return len(x.Items) == 0
	case wire.OptGUIDField:
		return !x.Present
	case *wire.Obj:
		return false
	}
	return false
}
