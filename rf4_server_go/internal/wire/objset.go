package wire

import (
	"fmt"
	"strings"
)

// 按「第几个字段」往对象里填值这一组小助手，是给上层拼帧用的。
//
// 为什么按序号写而不按混淆字段名：解剖出来的字段表就是按 #00 #01 #02 排的，
// 照着序号填一眼能对上凭据；序号写错、写法不对都会当场报错，不会闷声写歪一帧。
// 拼帧那层（entry 进场小帧、profile 玩家档案）共用这一份，免得两边各写一套 setter。

// NewObj 按类型号从类表造一个「骨架对象」：字段个数、顺序、写法全从类表来，每个字段先填合法空值。
// 用类型号（307、133 这种）而不是那串乱码类名，是为了字段表重新生成也不会一下全断。
func (t *Table) NewObj(typeID int) (*Obj, error) {
	if t == nil {
		return nil, fmt.Errorf("wire: 字段表还没载入，造不出 typeID %d", typeID)
	}
	_, class, ok := t.ClassByType(typeID)
	if !ok {
		return nil, fmt.Errorf("wire: 字段表里没有 typeID %d 这个类型", typeID)
	}
	return MakeObj(t, class)
}

// SetU32At 按序号写一个 u32 字段（写法 U32/FLOAT）
func (o *Obj) SetU32At(idx int, v uint32) error {
	f, err := o.fieldChecked(idx, "U32", "FLOAT")
	if err != nil {
		return err
	}
	f.Value = U32(v)
	return nil
}

// SetU64At 按序号写一个 u64 字段（写法 U64/DOUBLE）
func (o *Obj) SetU64At(idx int, v uint64) error {
	f, err := o.fieldChecked(idx, "U64", "DOUBLE")
	if err != nil {
		return err
	}
	f.Value = U64(v)
	return nil
}

// SetByteAt 按序号写一个单字节字段（写法 BYTE/OPT）
func (o *Obj) SetByteAt(idx int, v uint8) error {
	f, err := o.fieldChecked(idx, "BYTE", "OPT")
	if err != nil {
		return err
	}
	f.Value = OptByte{V: v}
	return nil
}

// SetOptGUIDAt 按序号写一个「可空 GUID」字段（写法 OPTGUID）。
//
// 为什么要单独一个 setter：物件里 GUID16 和 OPTGUID 是两种写法 —— GUID16 一定要有值，
// OPTGUID 可以发「空」（一个 0x17 那种空标记），装配的父物品、鱼记录的父容器都是可空的。
// 拿 SetGUIDAt 去写 OPTGUID 会被 fieldChecked 当场拦下来（这正是它该干的活）。
// hex 传空串就是「没有」，跟真服那条留空的做法一致。
func (o *Obj) SetOptGUIDAt(idx int, hex string) error {
	f, err := o.fieldChecked(idx, "OPTGUID")
	if err != nil {
		return err
	}
	f.Value = OptGUIDField{Hex: hex, Present: hex != ""}
	return nil
}

// SetU16At 按序号写一个 u16 字段（写法 U16）
func (o *Obj) SetU16At(idx int, v uint16) error {
	f, err := o.fieldChecked(idx, "U16")
	if err != nil {
		return err
	}
	f.Value = U16Field{V: v}
	return nil
}

// SetShortStrAt 按序号写一个短串字段（写法 STR，1 字节长度前缀）
func (o *Obj) SetShortStrAt(idx int, s string) error {
	f, err := o.fieldChecked(idx, "STR")
	if err != nil {
		return err
	}
	f.Value = StrField{S: s}
	return nil
}

// SetOptShortStrAt 按序号写一个「可空短串」（写法 STR）。
//
// STR 这格有两种长相：真值是「1 字节长度 + 内容」（空串就是长度 0），
// 「这里没有值」是单独一个 0xff。鱼卡片里那两格还没填上的文本（备用名、显示名）
// 真服发的就是 0xff，用 SetShortStrAt("") 会变成 0x00，客户端就当成「有个空名字」。
// present=false 就发 0xff；真要发空串就 present=true、s=""。
func (o *Obj) SetOptShortStrAt(idx int, s string, present bool) error {
	f, err := o.fieldChecked(idx, "STR")
	if err != nil {
		return err
	}
	f.Value = StrField{S: s, IsNull: !present}
	return nil
}

func (o *Obj) SetStr16At(idx int, s string) error {
	f, err := o.fieldChecked(idx, "U16STR")
	if err != nil {
		return err
	}
	f.Value = Str16Field{S: s}
	return nil
}

// SetGUIDAt 按序号写一个物件 GUID（写法 GUID16：16 字节裸排，没有 marker）
func (o *Obj) SetGUIDAt(idx int, hex string) error {
	f, err := o.fieldChecked(idx, "GUID16")
	if err != nil {
		return err
	}
	f.Value = GUID{Hex: hex}
	return nil
}

// SetValAt 按序号写一个「带 marker 的值」字段（VAL/ARGLIST/ARRAY/U32ARR/DICT/STR3/KV）
func (o *Obj) SetValAt(idx int, v Value) error {
	f, err := o.fieldChecked(idx, "VAL", "ARGLIST", "ARRAY", "U32ARR", "DICT", "STR3", "KV")
	if err != nil {
		return err
	}
	f.Value = v
	return nil
}

// fieldChecked 找第 idx 个字段，顺手确认它的写入法在 want 里；不对就给一句说得清的错误
func (o *Obj) fieldChecked(idx int, want ...string) (*Field, error) {
	if idx < 0 || idx >= len(o.Fields) {
		return nil, fmt.Errorf("对象 %s(typeID %d) 只有 %d 个字段，要第 %d 个",
			o.Class, o.TypeID, len(o.Fields), idx)
	}
	f := &o.Fields[idx]
	for _, w := range want {
		if f.Op == w {
			return f, nil
		}
	}
	return nil, fmt.Errorf("对象 %s(typeID %d) 第 %d 个字段 %s 的写入法是 %q，这边要的是 %s",
		o.Class, o.TypeID, idx, f.Name, f.Op, strings.Join(want, "/"))
}
