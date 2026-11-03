package wire

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
)

// 协议里几个哨兵值，全后端统一从这里取，别在别处写魔数。
const (
	StrNullShort byte   = 0xff       // 1 字节长度前缀的短串：这个长度值代表 null
	StrNullU16   uint16 = 0xffff     // 2 字节长度前缀的串：代表 null
	Null32       uint32 = 0x7fffffff // 4 字节长度前缀的串/字节块：代表 null
	TypeValList  uint32 = 98765      // typeID 等于它 = 不是对象，是「一串值」
)

// Value 是「一个值」的抽象。Encode 输出这个值自己的字节形态（带 marker），
// 用在 args / 数组 / 字典这些「值的位置」上。
// 注意：对象字段位置的写法由字段 op 决定，走 EncodeField，不走这里。
type Value interface {
	Encode() ([]byte, error)
}

// ErrTooShort 串太长放不下（短串最多 254 字节等）
var ErrTooShort = errors.New("wire: 值太长，这个写法装不下")

// Null 空值：marker 0x00
type Null struct{}

// Encode 输出 1 个 0x00
func (Null) Encode() ([]byte, error) { return []byte{0x00}, nil }

// ElemNull 集合里的空元素：marker 0xff（和 Null 不是一回事，客户端分开处理）
type ElemNull struct{}

// Encode 输出 1 个 0xff
func (ElemNull) Encode() ([]byte, error) { return []byte{0xff}, nil }

// Bool 布尔：true=0x0e，false=0x0f（没有后面的值字节）
type Bool struct{ V bool }

// Encode 输出 marker 一个字节
func (b Bool) Encode() ([]byte, error) {
	if b.V {
		return []byte{0x0e}, nil
	}
	return []byte{0x0f}, nil
}

// Num32 一个 32 位整数。Marker 记原样 marker：0x10 最常见，0x0d/0x13 也见过，
// 逐字节复现录制时必须原样吐回去，不能统一改写成 0x10。
type Num32 struct {
	Marker byte
	V      uint32
}

// U32 常用构造：marker 用最常见的 0x10
func U32(v uint32) Num32 { return Num32{Marker: 0x10, V: v} }

// Encode marker(1) + 小端 u32
func (n Num32) Encode() ([]byte, error) {
	m := n.Marker
	if m == 0 {
		m = 0x10
	}
	b := make([]byte, 5)
	b[0] = m
	binary.LittleEndian.PutUint32(b[1:], n.V)
	return b, nil
}

// Num64 一个 64 位整数。Marker：0x11 常见，0x12 也见过。
type Num64 struct {
	Marker byte
	V      uint64
}

// U64 常用构造：marker 用 0x11
func U64(v uint64) Num64 { return Num64{Marker: 0x11, V: v} }

// Encode marker(1) + 小端 u64
func (n Num64) Encode() ([]byte, error) {
	m := n.Marker
	if m == 0 {
		m = 0x11
	}
	b := make([]byte, 9)
	b[0] = m
	binary.LittleEndian.PutUint64(b[1:], n.V)
	return b, nil
}

// DateTime 时间：marker 0x0b + u64，单位是 .NET ticks（自 0001-01-01 起的 100ns 数）
type DateTime struct{ V uint64 }

// Encode marker 0x0b + 小端 u64
func (d DateTime) Encode() ([]byte, error) {
	b := make([]byte, 9)
	b[0] = 0x0b
	binary.LittleEndian.PutUint64(b[1:], d.V)
	return b, nil
}

// GUID 16 字节标识。Hex 是 32 个十六进制字符；空串代表「没有 guid」（吐 marker 0x17）。
type GUID struct{ Hex string }

// Encode 有值：0x0c + 16 字节；空值：0x17
func (g GUID) Encode() ([]byte, error) {
	if g.Hex == "" {
		return []byte{0x17}, nil
	}
	raw, err := hex.DecodeString(g.Hex)
	if err != nil {
		return nil, fmt.Errorf("wire: guid 不是十六进制 %q: %w", g.Hex, err)
	}
	if len(raw) != 16 {
		return nil, fmt.Errorf("wire: guid 长度要 16 字节，实际 %d", len(raw))
	}
	return append([]byte{0x0c}, raw...), nil
}

// Bytes 一块二进制：marker 0x08 + u32 长度 + 内容。IsNull=true 时长度写 Null32。
type Bytes struct {
	Data   []byte
	IsNull bool
}

// Encode 见上面类型说明
func (b Bytes) Encode() ([]byte, error) {
	out := bytes.NewBuffer(nil)
	out.WriteByte(0x08)
	if b.IsNull {
		out.Write(le32(Null32))
		return out.Bytes(), nil
	}
	out.Write(le32(uint32(len(b.Data))))
	out.Write(b.Data)
	return out.Bytes(), nil
}

// Str 一个字符串值，Marker 记原样写法：0x14(1 字节长度) / 0x15(2 字节) / 0x16(4 字节)。
// IsNull=true 时按各自 marker 写各自的 null 哨兵。逐字节复现录制时 marker 不能改。
type Str struct {
	Marker byte
	S      string
	IsNull bool
}

// GoStr 常用构造：短串形态（<=254 字节时用），null 传空串也行
func GoStr(s string) Str { return Str{Marker: 0x14, S: s} }

// Encode 按 marker 选写法；marker 没设时按长度自动挑
func (s Str) Encode() ([]byte, error) {
	m := s.Marker
	if m == 0 {
		switch {
		case s.IsNull:
			m = 0x14
		case len(s.S) > 254:
			if len(s.S) <= 0xfffe {
				m = 0x15
			} else {
				m = 0x16
			}
		default:
			m = 0x14
		}
	}
	raw := []byte(s.S)
	switch m {
	case 0x14:
		if s.IsNull {
			return []byte{0x14, StrNullShort}, nil
		}
		if len(raw) > 254 {
			return nil, fmt.Errorf("%w: 短串 %d 字节", ErrTooShort, len(raw))
		}
		return append([]byte{0x14, byte(len(raw))}, raw...), nil
	case 0x15:
		if s.IsNull {
			return append([]byte{0x15}, le16(StrNullU16)...), nil
		}
		return append(append([]byte{0x15}, le16(uint16(len(raw)))...), raw...), nil
	case 0x16:
		if s.IsNull {
			return append([]byte{0x16}, le32(Null32)...), nil
		}
		return append(append([]byte{0x16}, le32(uint32(len(raw)))...), raw...), nil
	}
	return nil, fmt.Errorf("wire: 不认识字符串 marker 0x%02x", m)
}

// Args 一组带编码名的值：03 | 编码短串("524"/"121"/"122") | 条数 u16 | 各项。
// 客户端很多命令都靠这个编码名去认「这是什么请求/列表」。
type Args struct {
	Code     string  // 编码名，例如 "524"/"121"/"122"
	CodeNull bool    // true = 编码名位置是 null（写 0xff）；空串和 null 是两回事
	Items    []Value // 列表里的每一项都必须自己带 marker
}

// Encode 见上面格式
func (a Args) Encode() ([]byte, error) {
	out := bytes.NewBuffer(nil)
	out.WriteByte(0x03)
	out.Write(StrOpt{S: a.Code, IsNull: a.CodeNull}.bytes())
	items := a.Items
	if items == nil {
		items = []Value{}
	}
	out.Write(le16(uint16(len(items))))
	for i, it := range items {
		b, err := it.Encode()
		if err != nil {
			return nil, fmt.Errorf("args 第 %d 项: %w", i, err)
		}
		out.Write(b)
	}
	return out.Bytes(), nil
}

// BlobItem args"04" 数组里的一项：IsNull=true 时只写 4 字节 Null32
type BlobItem struct {
	Data   []byte
	IsNull bool
}

// BytesArray 字节块数组：04 | 条数 u16 | (u32 长度 + 内容)*
type BytesArray struct{ Items []BlobItem }

// Encode 见上面格式
func (a BytesArray) Encode() ([]byte, error) {
	out := bytes.NewBuffer(nil)
	out.WriteByte(0x04)
	out.Write(le16(uint16(len(a.Items))))
	for _, it := range a.Items {
		if it.IsNull {
			out.Write(le32(Null32))
			continue
		}
		out.Write(le32(uint32(len(it.Data))))
		out.Write(it.Data)
	}
	return out.Bytes(), nil
}

// GUIDArray guid 数组：06 | 条数 u16 | 16 字节*。每项必须正好 16 字节（没有补零的写法）。
type GUIDArray struct{ Items [][]byte }

// Encode 见上面格式
func (a GUIDArray) Encode() ([]byte, error) {
	out := bytes.NewBuffer(nil)
	out.WriteByte(0x06)
	out.Write(le16(uint16(len(a.Items))))
	for i, g := range a.Items {
		if len(g) != 16 {
			return nil, fmt.Errorf("guidarr 第 %d 项要 16 字节，实际 %d", i, len(g))
		}
		out.Write(g)
	}
	return out.Bytes(), nil
}

// U32Array u32 数组：05 | 条数 u16 | u32*
type U32Array struct{ Items []uint32 }

// TypedU32Array 带编码名的 u32 数组：07 | 编码短串 | 条数 u16 | u32*。
// 以前把它当 05 解，编码回去就少了 marker 和编码名，逐字节复现当场对不上。
type TypedU32Array struct {
	Code     string   // 编码名，例如 "331"
	CodeNull bool     // true = 编码名位置是 null
	Items    []uint32 // 一串 u32
}

// Encode 见上面格式
func (a TypedU32Array) Encode() ([]byte, error) {
	out := bytes.NewBuffer(nil)
	out.WriteByte(0x07)
	out.Write(StrOpt{S: a.Code, IsNull: a.CodeNull}.bytes())
	out.Write(le16(uint16(len(a.Items))))
	for _, n := range a.Items {
		out.Write(le32(n))
	}
	return out.Bytes(), nil
}

// Encode 见上面格式
func (a U32Array) Encode() ([]byte, error) {
	out := bytes.NewBuffer(nil)
	out.WriteByte(0x05)
	out.Write(le16(uint16(len(a.Items))))
	for _, n := range a.Items {
		out.Write(le32(n))
	}
	return out.Bytes(), nil
}

// Pair 字典里的一对：键是短串（可以为 null），值是任意值
type Pair struct {
	Key     string // 键
	KeyNull bool   // true = 键位置是 null（写 0xff）
	Val     Value  // 值
}

// Dict 字典：02 | 类型名短串 | 条数(1 字节) | (键短串 + 值)*
type Dict struct {
	TypeName     string // 客户端类名
	TypeNameNull bool   // true = 类名位置是 null（写 0xff）
	Pairs        []Pair
}

// Encode 见上面格式
func (d Dict) Encode() ([]byte, error) {
	out := bytes.NewBuffer(nil)
	out.WriteByte(0x02)
	out.Write(StrOpt{S: d.TypeName, IsNull: d.TypeNameNull}.bytes())
	if len(d.Pairs) > 254 {
		return nil, fmt.Errorf("%w: 字典 %d 对，1 字节条数装不下", ErrTooShort, len(d.Pairs))
	}
	out.WriteByte(byte(len(d.Pairs)))
	for _, p := range d.Pairs {
		out.Write(StrOpt{S: p.Key, IsNull: p.KeyNull}.bytes())
		b, err := p.Val.Encode()
		if err != nil {
			return nil, fmt.Errorf("字典键 %q: %w", p.Key, err)
		}
		out.Write(b)
	}
	return out.Bytes(), nil
}

// Str3 三个短串连排（marker 0x09，没有条数字节）
type Str3 struct{ Items []StrOpt }

// Encode 见上面格式
func (s Str3) Encode() ([]byte, error) {
	out := bytes.NewBuffer(nil)
	out.WriteByte(0x09)
	for _, v := range s.Items {
		out.Write(v.bytes())
	}
	return out.Bytes(), nil
}

// KV 键值两个短串（marker 0x0a）
type KV struct {
	K StrOpt // 键
	V StrOpt // 值
}

// Encode 见上面格式
func (k KV) Encode() ([]byte, error) {
	out := bytes.NewBuffer([]byte{0x0a})
	out.Write(k.K.bytes())
	out.Write(k.V.bytes())
	return out.Bytes(), nil
}

// ValList 「一串值」：01 | u32(98765) | 各项。看着像对象，其实是列表。
type ValList struct{ Items []Value }

// Encode 见上面格式
func (v ValList) Encode() ([]byte, error) {
	out := bytes.NewBuffer(nil)
	out.WriteByte(0x01)
	out.Write(le32(TypeValList))
	for _, it := range v.Items {
		b, err := it.Encode()
		if err != nil {
			return nil, err
		}
		out.Write(b)
	}
	return out.Bytes(), nil
}

// ---- 小工具：小端写数、短串 ----

// le16 把数写成 2 字节小端
func le16(v uint16) []byte { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); return b }

// le32 把数写成 4 字节小端
func le32(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }

// le64 把数写成 8 字节小端
func le64(v uint64) []byte { b := make([]byte, 8); binary.LittleEndian.PutUint64(b, v); return b }

// StrOpt 一个「可能是 null 的短串」。协议里 null 写 0xff，空串写 0x00（长度 0），
// 这两个不一样，客户端分开处理。以前我们一律把空串写成 0xff，逐字节复现就在这一格对不上。
type StrOpt struct {
	S      string // 字符串内容
	IsNull bool   // true = 这个位置是 null（写 0xff）
}

// Short 构造一个非 null 短串
func Short(s string) StrOpt { return StrOpt{S: s} }

// NullShort 构造一个 null 短串
func NullShort() StrOpt { return StrOpt{IsNull: true} }

// bytes 写成 1 字节长度前缀的短串：null=0xff，空串=长度 0
func (s StrOpt) bytes() []byte {
	if s.IsNull {
		return []byte{StrNullShort}
	}
	raw := []byte(s.S)
	if len(raw) > 254 {
		raw = raw[:254] // 短串最多 254 字节，更长得用 0x15/0x16 那种带 2/4 字节长度的写法
	}
	return append([]byte{byte(len(raw))}, raw...)
}

// shortStrBytes 兼容写法：传进来的串确定不是 null
func shortStrBytes(s string) []byte { return StrOpt{S: s}.bytes() }

// f32bits 把浮点按 IEEE754 单精度转成 u32 原始位。
// 技能门槛/熟练度这种「必须逐字节一样」的地方一定要用它存的位，不能拿十进制数重算，
// 因为 0.1 这种数转一圈末位会变。
func F32Bits(v float64) uint32 { return math.Float32bits(float32(v)) }

// BitsF32 是 F32Bits 的反向：u32 原始位还原单精度浮点
func BitsF32(bits uint32) float64 { return float64(math.Float32frombits(bits)) }
