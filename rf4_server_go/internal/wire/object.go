package wire

import (
	"bytes"
	"encoding/hex"
	"fmt"
)

// 字段位置的专用值类型。它们既能当 Value 用（Encode 吐自己的 marker 形态），
// 又能被 EncodeField 按字段 op 写成「没有 marker 的裸字节」——对象里就是后者。
// 之所以分开：对象字段怎么写由类表 ops 决定，不靠值自己猜，这样逐字节复现才不会跑偏。

// OptByte 1 字节小整数，用在 op=OPT 和 op=BYTE 的字段上。
// 当 Value 单独用时按 u32 形态吐（协议里单独出现的数都是 0x10+u32）。
type OptByte struct{ V uint8 }

// Encode 单独形态：0x10 + u32
func (o OptByte) Encode() ([]byte, error) { return append([]byte{0x10}, le32(uint32(o.V))...), nil }

// U16Field 2 字节数，用在 op=U16（逆推里有些字符串字段是这种，null 写 0xffff）。
type U16Field struct {
	V      uint16
	IsNull bool // true = 这个字段是 null，写 0xffff
}

// Encode 单独形态：0x10 + u32
func (u U16Field) Encode() ([]byte, error) {
	if u.IsNull {
		return append([]byte{0x10}, le32(uint32(StrNullU16))...), nil
	}
	return append([]byte{0x10}, le32(uint32(u.V))...), nil
}

// StrField 短串（1 字节长度前缀），用在 op=STR。IsNull=true 写 0xff。
type StrField struct {
	S      string
	IsNull bool
}

// Encode 单独形态：0x14 + 短串
func (s StrField) Encode() ([]byte, error) {
	if s.IsNull {
		return []byte{0x14, StrNullShort}, nil
	}
	return append([]byte{0x14}, shortStrBytes(s.S)...), nil
}

// Str16Field 2 字节长度前缀串，用在 op=U16STR。IsNull=true 写 0xffff。
type Str16Field struct {
	S      string
	IsNull bool
}

// Encode 单独形态：0x15 + u16 长度 + 内容
func (s Str16Field) Encode() ([]byte, error) {
	if s.IsNull {
		return append([]byte{0x15}, le16(StrNullU16)...), nil
	}
	return append(append([]byte{0x15}, le16(uint16(len(s.S)))...), []byte(s.S)...), nil
}

// Str32Field 4 字节长度前缀串，用在 op=U32N。IsNull=true 写 0x7fffffff。
type Str32Field struct {
	S      string
	IsNull bool
}

// Encode 单独形态：0x16 + u32 长度 + 内容
func (s Str32Field) Encode() ([]byte, error) {
	if s.IsNull {
		return append([]byte{0x16}, le32(Null32)...), nil
	}
	return append(append([]byte{0x16}, le32(uint32(len(s.S)))...), []byte(s.S)...), nil
}

// OptGUIDField 带「有没有」前缀的 guid，用在 op=OPTGUID。Present=false 只写一个 0。
type OptGUIDField struct {
	Hex     string // 32 个十六进制字符；Present=true 时必填
	Present bool
}

// Encode 单独形态：走 GUID（0x0c + 16 字节 / 0x17 空）
func (g OptGUIDField) Encode() ([]byte, error) {
	return GUID{Hex: guidHex(g.Present, g.Hex)}.Encode()
}

// guidHex 把「有没有 + 十六进制」组合成 GUID 要的形态：没有就给空串
func guidHex(present bool, h string) string {
	if !present {
		return ""
	}
	return h
}

// Field 对象里的一个字段。Idx/Ops 顺序就是字节顺序，动不得。
type Field struct {
	Idx   int    // 第几个字段（0 起）
	Op    string // 写法：U32/VAL/U64/OPT/BYTE/U16/STR/U16STR/U32N/GUID16/OPTGUID/BYTES/...
	Name  string // 混淆字段名，填值按这个名字找
	Zh    string // 人话说明，只有日志和注释用
	Value Value  // 值，具体类型要和 Op 对得上
}

// Obj 对象记录：01 | typeID(u32) | 按类表顺序排的字段。
// Unknown=true 表示类表里查不到这个 typeID，这时 Raw 存原始字节，Encode 原样吐回去
// （这样解码再编码也不会丢东西，认不出的帧也能参与逐字节回归）。
type Obj struct {
	Class   string  // 混淆类名，例如 "djndmghegld"（技能树）
	TypeID  uint32  // 对象类型号，例如 311
	Fields  []Field // 字段列表，顺序 == 类表 ops 顺序
	Unknown bool    // 类表里没有这个类型
	Raw     []byte  // Unknown 时的原始字节（不含 01 和 typeID）
}

// Encode 输出整个对象：01 | typeID | 各字段字节
func (o *Obj) Encode() ([]byte, error) {
	if o.Unknown {
		return append(append([]byte{0x01}, le32(o.TypeID)...), o.Raw...), nil
	}
	out := bytes.NewBuffer(nil)
	out.WriteByte(0x01)
	out.Write(le32(o.TypeID))
	for i, f := range o.Fields {
		b, err := EncodeField(f.Op, f.Value)
		if err != nil {
			return nil, fmt.Errorf("对象 %s(typeID %d) 第 %d 个字段 %s(op=%s): %w", o.Class, o.TypeID, i, f.Name, f.Op, err)
		}
		out.Write(b)
	}
	return out.Bytes(), nil
}

// EncodeField 按字段写法写字节。注意这里写的是「对象字段位置」的裸字节，没有 marker。
func EncodeField(op string, v Value) ([]byte, error) {
	if v == nil {
		v = Null{}
	}
	switch op {
	case "U32", "FLOAT":
		n, ok := v.(Num32)
		if !ok {
			return nil, fmt.Errorf("要 Num32，给了 %T", v)
		}
		return le32(n.V), nil
	case "OPT", "BYTE":
		b, ok := v.(OptByte)
		if !ok {
			return nil, fmt.Errorf("要 OptByte，给了 %T", v)
		}
		return []byte{b.V}, nil
	case "U64", "DOUBLE":
		n, ok := v.(Num64)
		if !ok {
			return nil, fmt.Errorf("要 Num64，给了 %T", v)
		}
		return le64(n.V), nil
	case "U16":
		u, ok := v.(U16Field)
		if !ok {
			return nil, fmt.Errorf("要 U16Field，给了 %T", v)
		}
		if u.IsNull {
			return le16(StrNullU16), nil
		}
		return le16(u.V), nil
	case "STR":
		s, ok := v.(StrField)
		if !ok {
			return nil, fmt.Errorf("要 StrField，给了 %T", v)
		}
		if s.IsNull {
			return []byte{StrNullShort}, nil
		}
		if len(s.S) > 254 {
			return nil, fmt.Errorf("%w: STR 字段 %d 字节", ErrTooShort, len(s.S))
		}
		return shortStrBytes(s.S), nil
	case "U16STR":
		s, ok := v.(Str16Field)
		if !ok {
			return nil, fmt.Errorf("要 Str16Field，给了 %T", v)
		}
		if s.IsNull {
			return le16(StrNullU16), nil
		}
		return append(le16(uint16(len(s.S))), []byte(s.S)...), nil
	case "U32N":
		s, ok := v.(Str32Field)
		if !ok {
			return nil, fmt.Errorf("要 Str32Field，给了 %T", v)
		}
		if s.IsNull {
			return le32(Null32), nil
		}
		return append(le32(uint32(len(s.S))), []byte(s.S)...), nil
	case "GUID16":
		g, ok := v.(GUID)
		if !ok {
			return nil, fmt.Errorf("要 GUID，给了 %T", v)
		}
		raw, err := hex.DecodeString(g.Hex)
		if err != nil {
			return nil, fmt.Errorf("guid 不是十六进制: %w", err)
		}
		if len(raw) == 0 {
			raw = make([]byte, 16) // 没有 guid 就写 16 个 0（类表这写法没有 null 表达）
		}
		if len(raw) != 16 {
			return nil, fmt.Errorf("guid 要 16 字节，实际 %d", len(raw))
		}
		return raw, nil
	case "OPTGUID":
		g, ok := v.(OptGUIDField)
		if !ok {
			return nil, fmt.Errorf("要 OptGUIDField，给了 %T", v)
		}
		if !g.Present {
			return []byte{0x00}, nil
		}
		raw, err := hex.DecodeString(g.Hex)
		if err != nil || len(raw) != 16 {
			return nil, fmt.Errorf("guid 要 16 字节十六进制，got %q", g.Hex)
		}
		return append([]byte{0x01}, raw...), nil
	case "BYTES":
		b, ok := v.(Bytes)
		if !ok {
			return nil, fmt.Errorf("要 Bytes，给了 %T", v)
		}
		if b.IsNull {
			return le32(Null32), nil
		}
		return append(le32(uint32(len(b.Data))), b.Data...), nil
	case "VAL", "DICT", "ARGLIST", "ARRAY", "U32ARR", "STR3", "KV":
		// 这几种写法就是「一个带 marker 的值」，直接走值自己的编码
		return v.Encode()
	}
	return nil, fmt.Errorf("不认识字段写法 %q", op)
}

// MakeObj 按类名造一个「全是合法空值」的对象，再用 Set 系列往里填。
// 这么绕一圈的好处：字段个数、顺序、写法都直接从类表来，写错一个字段编译期过不了也能在编码时报出来。
func MakeObj(t *Table, class string) (*Obj, error) {
	def, ok := t.Class(class)
	if !ok {
		return nil, fmt.Errorf("wire: 类表里没有这个类 %q", class)
	}
	if len(def.Ops) != len(def.Fields) {
		return nil, fmt.Errorf("wire: 类 %s 的 ops 有 %d 个但 fields 有 %d 个，表本身不对", class, len(def.Ops), len(def.Fields))
	}
	o := &Obj{Class: class, TypeID: uint32(def.TypeID), Fields: make([]Field, 0, len(def.Ops))}
	for i, op := range def.Ops {
		f := def.Fields[i]
		var v Value
		switch op {
		case "U32", "FLOAT":
			v = U32(0)
		case "U64", "DOUBLE":
			v = U64(0)
		case "OPT", "BYTE":
			v = OptByte{0}
		case "U16":
			v = U16Field{V: 0}
		case "STR":
			v = StrField{IsNull: true}
		case "U16STR":
			v = Str16Field{IsNull: true}
		case "U32N":
			v = Str32Field{IsNull: true}
		case "GUID16":
			v = GUID{Hex: ""}
		case "OPTGUID":
			v = OptGUIDField{Present: false}
		case "BYTES":
			v = Bytes{IsNull: true}
		case "VAL", "DICT", "ARGLIST", "ARRAY", "U32ARR", "STR3", "KV":
			v = Null{}
		default:
			return nil, fmt.Errorf("wire: 类 %s 第 %d 个字段的写法 %q 不认识", class, i, op)
		}
		o.Fields = append(o.Fields, Field{Idx: i, Op: op, Name: f.Name, Zh: f.Zh, Value: v})
	}
	return o, nil
}

// Set 按混淆字段名填一个值；名字不在类表里就报错（拼错字段名最怕悄悄什么都不干）
func (o *Obj) Set(name string, v Value) error {
	for i := range o.Fields {
		if o.Fields[i].Name == name {
			o.Fields[i].Value = v
			return nil
		}
	}
	return fmt.Errorf("wire: 对象 %s 没有字段 %q", o.Class, name)
}

// FieldByName 按名字取字段（只读）
func (o *Obj) FieldByName(name string) (Field, bool) {
	for _, f := range o.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// Num 按下标取一个 u32 字段的值；取不到或类型不对返回 0（日志/测试用）
func (o *Obj) Num(idx int) uint32 {
	if idx < 0 || idx >= len(o.Fields) {
		return 0
	}
	if n, ok := o.Fields[idx].Value.(Num32); ok {
		return n.V
	}
	return 0
}

// ArgsOf 按下标取一个 args 字段（VAL/ARGLIST 写法），取不到返回 nil
func (o *Obj) ArgsOf(idx int) *Args {
	if idx < 0 || idx >= len(o.Fields) {
		return nil
	}
	if a, ok := o.Fields[idx].Value.(Args); ok {
		return &a
	}
	return nil
}

// Objs 取 args 列表里所有对象项（312/313 这种就是 args 里塞一串对象）
func (a *Args) Objs() []*Obj {
	out := make([]*Obj, 0, len(a.Items))
	for _, it := range a.Items {
		if o, ok := it.(*Obj); ok {
			out = append(out, o)
		}
	}
	return out
}
