package wire

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// 解码。原则：宁可返回错误也不 panic（服务器会收到各种乱七八糟的帧），
// 而且 marker 一律原样记在值里，保证「解码 -> 再编码」能把字节一字不差吐回去。

// need 检查还剩够不够 n 个字节，不够就报错，不让调用方 slice 越界
func need(b []byte, off, n int, what string) error {
	if off < 0 || off+n > len(b) {
		return fmt.Errorf("wire: 读 %s 越界：偏移 %d 要 %d 字节，总共才 %d", what, off, n, len(b))
	}
	return nil
}

// need16 读 1 字节无符号数
func need16(b []byte, off int) (uint16, int, error) {
	if err := need(b, off, 2, "u16"); err != nil {
		return 0, off, err
	}
	return binary.LittleEndian.Uint16(b[off:]), off + 2, nil
}

// need32 读 4 字节小端
func need32(b []byte, off int) (uint32, int, error) {
	if err := need(b, off, 4, "u32"); err != nil {
		return 0, off, err
	}
	return binary.LittleEndian.Uint32(b[off:]), off + 4, nil
}

// need64 读 8 字节小端
func need64(b []byte, off int) (uint64, int, error) {
	if err := need(b, off, 8, "u64"); err != nil {
		return 0, off, err
	}
	return binary.LittleEndian.Uint64(b[off:]), off + 8, nil
}

// readShortStr 读 1 字节长度前缀的短串，只要内容（不关心是不是 null 的地方用它）
func readShortStr(b []byte, off int) (string, int, error) {
	so, next, err := readShortStrOpt(b, off)
	if err != nil {
		return "", next, err
	}
	return so.S, next, nil
}

// readShortStrOpt 同上，但把「是不是 null」也带出来：0xff=null，0x00=空串，两者不同
func readShortStrOpt(b []byte, off int) (StrOpt, int, error) {
	if err := need(b, off, 1, "短串长度"); err != nil {
		return StrOpt{}, off, err
	}
	n := int(b[off])
	off++
	if n == int(StrNullShort) {
		return NullShort(), off, nil
	}
	if err := need(b, off, n, "短串内容"); err != nil {
		return StrOpt{}, off, err
	}
	return Short(string(b[off : off+n])), off + n, nil
}

// DecodeValue 从 off 处解一个「带 marker 的值」，返回值和读完的位置。
func (t *Table) DecodeValue(b []byte, off int) (Value, int, error) {
	if err := need(b, off, 1, "marker"); err != nil {
		return nil, off, err
	}
	m := b[off]
	off++
	switch m {
	case 0x00:
		return Null{}, off, nil
	case 0xff:
		return ElemNull{}, off, nil
	case 0x0d, 0x10, 0x13:
		v, off2, err := need32(b, off)
		if err != nil {
			return nil, off, err
		}
		return Num32{Marker: m, V: v}, off2, nil
	case 0x0e:
		return Bool{V: true}, off, nil
	case 0x0f:
		return Bool{V: false}, off, nil
	case 0x11, 0x12:
		v, off2, err := need64(b, off)
		if err != nil {
			return nil, off, err
		}
		return Num64{Marker: m, V: v}, off2, nil
	case 0x0c:
		if err := need(b, off, 16, "guid"); err != nil {
			return nil, off, err
		}
		return GUID{Hex: hex.EncodeToString(b[off : off+16])}, off + 16, nil
	case 0x17:
		return GUID{Hex: ""}, off, nil
	case 0x0b:
		v, off2, err := need64(b, off)
		if err != nil {
			return nil, off, err
		}
		return DateTime{V: v}, off2, nil
	case 0x08:
		n, off2, err := need32(b, off)
		if err != nil {
			return nil, off, err
		}
		if n == Null32 {
			return Bytes{IsNull: true}, off2, nil
		}
		if err := need(b, off2, int(n), "字节块"); err != nil {
			return nil, off, err
		}
		cp := make([]byte, n)
		copy(cp, b[off2:off2+int(n)])
		return Bytes{Data: cp}, off2 + int(n), nil
	case 0x14:
		if err := need(b, off, 1, "短串长度"); err != nil {
			return nil, off, err
		}
		n := int(b[off])
		off++
		if n == int(StrNullShort) {
			return Str{Marker: m, IsNull: true}, off, nil
		}
		if err := need(b, off, n, "短串"); err != nil {
			return nil, off, err
		}
		return Str{Marker: m, S: string(b[off : off+n])}, off + n, nil
	case 0x15:
		n, off2, err := need16(b, off)
		if err != nil {
			return nil, off, err
		}
		if n == StrNullU16 {
			return Str{Marker: m, IsNull: true}, off2, nil
		}
		if err := need(b, off2, int(n), "串"); err != nil {
			return nil, off, err
		}
		return Str{Marker: m, S: string(b[off2 : off2+int(n)])}, off2 + int(n), nil
	case 0x16:
		n, off2, err := need32(b, off)
		if err != nil {
			return nil, off, err
		}
		if n == Null32 {
			return Str{Marker: m, IsNull: true}, off2, nil
		}
		if err := need(b, off2, int(n), "长串"); err != nil {
			return nil, off, err
		}
		return Str{Marker: m, S: string(b[off2 : off2+int(n)])}, off2 + int(n), nil
	case 0x01:
		return t.decodeObjBody(b, off)
	case 0x03:
		code, off2, err := readShortStrOpt(b, off)
		if err != nil {
			return nil, off, err
		}
		cnt, off3, err := need16(b, off2)
		if err != nil {
			return nil, off, err
		}
		items := make([]Value, 0, cnt)
		cur := off3
		for i := 0; i < int(cnt); i++ {
			v, next, err := t.DecodeValue(b, cur)
			if err != nil {
				return nil, off, fmt.Errorf("args(%s) 第 %d 项: %w", code.S, i, err)
			}
			items = append(items, v)
			cur = next
		}
		return Args{Code: code.S, CodeNull: code.IsNull, Items: items}, cur, nil
	case 0x04, 0x06:
		cnt, off2, err := need16(b, off)
		if err != nil {
			return nil, off, err
		}
		cur := off2
		if m == 0x04 {
			items := make([]BlobItem, 0, cnt)
			for i := 0; i < int(cnt); i++ {
				n, next, err := need32(b, cur)
				if err != nil {
					return nil, off, err
				}
				if n == Null32 {
					items = append(items, BlobItem{IsNull: true})
					cur = next
					continue
				}
				if err := need(b, next, int(n), "数组元素"); err != nil {
					return nil, off, err
				}
				cp := make([]byte, n)
				copy(cp, b[next:next+int(n)])
				items = append(items, BlobItem{Data: cp})
				cur = next + int(n)
			}
			return BytesArray{Items: items}, cur, nil
		}
		gs := make([][]byte, 0, cnt)
		for i := 0; i < int(cnt); i++ {
			if err := need(b, cur, 16, "guid 数组元素"); err != nil {
				return nil, off, err
			}
			cp := make([]byte, 16)
			copy(cp, b[cur:cur+16])
			gs = append(gs, cp)
			cur += 16
		}
		return GUIDArray{Items: gs}, cur, nil
	case 0x05, 0x07:
		// 0x07 比 0x05 多一个编码短串，必须原样留着，不然编码回去少字节
		var code StrOpt
		off2 := off
		var err error
		if m == 0x07 {
			code, off2, err = readShortStrOpt(b, off)
			if err != nil {
				return nil, off, err
			}
		}
		cnt, off3, err := need16(b, off2)
		if err != nil {
			return nil, off, err
		}
		nums := make([]uint32, 0, cnt)
		cur := off3
		for i := 0; i < int(cnt); i++ {
			n, next, err := need32(b, cur)
			if err != nil {
				return nil, off, err
			}
			nums = append(nums, n)
			cur = next
		}
		if m == 0x07 {
			return TypedU32Array{Code: code.S, CodeNull: code.IsNull, Items: nums}, cur, nil
		}
		return U32Array{Items: nums}, cur, nil
	case 0x02:
		typeName, off2, err := readShortStrOpt(b, off)
		if err != nil {
			return nil, off, err
		}
		if err := need(b, off2, 1, "字典条数"); err != nil {
			return nil, off, err
		}
		cnt := int(b[off2])
		cur := off2 + 1
		pairs := make([]Pair, 0, cnt)
		for i := 0; i < cnt; i++ {
			k, next, err := readShortStrOpt(b, cur)
			if err != nil {
				return nil, off, err
			}
			v, next2, err := t.DecodeValue(b, next)
			if err != nil {
				return nil, off, fmt.Errorf("字典第 %d 对: %w", i, err)
			}
			pairs = append(pairs, Pair{Key: k.S, KeyNull: k.IsNull, Val: v})
			cur = next2
		}
		return Dict{TypeName: typeName.S, TypeNameNull: typeName.IsNull, Pairs: pairs}, cur, nil
	case 0x09, 0x0a:
		cnt := 3 // 0x09：三个短串；0x0a：键值两个短串。都没有条数字节
		if m == 0x0a {
			cnt = 2
		}
		parts := make([]StrOpt, 0, cnt)
		cur := off
		for i := 0; i < cnt; i++ {
			so, next, err := readShortStrOpt(b, cur)
			if err != nil {
				return nil, off, err
			}
			parts = append(parts, so)
			cur = next
		}
		if m == 0x0a {
			return KV{K: parts[0], V: parts[1]}, cur, nil
		}
		return Str3{Items: parts}, cur, nil
	}
	return nil, off, fmt.Errorf("wire: 未知 value marker 0x%02x（偏移 %d）", m, off-1)
}

// decodeObjBody 解对象体（marker 0x01 已经吃掉了，off 指向 typeID）
func (t *Table) decodeObjBody(b []byte, off int) (Value, int, error) {
	typeID, off2, err := need32(b, off)
	if err != nil {
		return nil, off, err
	}
	if typeID == TypeValList { // 看着像对象，其实是一串值
		items := []Value{}
		cur := off2
		for cur < len(b) {
			v, next, err := t.DecodeValue(b, cur)
			if err != nil {
				return nil, off, fmt.Errorf("vallist 第 %d 项: %w", len(items), err)
			}
			items = append(items, v)
			cur = next
		}
		return ValList{Items: items}, cur, nil
	}
	def, cls, ok := t.ClassByType(int(typeID))
	if !ok { // 类表里没有：原始字节整段留着，编码时原样吐，保证不丢字节
		raw := make([]byte, len(b)-off2)
		copy(raw, b[off2:])
		return &Obj{Class: t.ClassOf(int(typeID)), TypeID: typeID, Unknown: true, Raw: raw}, len(b), nil
	}
	fields := make([]Field, 0, len(def.Ops))
	cur := off2
	for i, op := range def.Ops {
		v, next, err := t.DecodeField(b, cur, op)
		if err != nil {
			return nil, off, fmt.Errorf("对象 %s(typeID %d) 第 %d 个字段: %w", cls, typeID, i, err)
		}
		f := def.Fields[i]
		fields = append(fields, Field{Idx: i, Op: op, Name: f.Name, Zh: f.Zh, Value: v})
		cur = next
	}
	return &Obj{Class: cls, TypeID: typeID, Fields: fields}, cur, nil
}

// DecodeField 解对象里的一个字段（写法由类表给定，没有 marker）
func (t *Table) DecodeField(b []byte, off int, op string) (Value, int, error) {
	switch op {
	case "U32", "FLOAT":
		v, off2, err := need32(b, off)
		if err != nil {
			return nil, off, err
		}
		return Num32{Marker: 0x10, V: v}, off2, nil
	case "OPT", "BYTE":
		if err := need(b, off, 1, op); err != nil {
			return nil, off, err
		}
		return OptByte{V: b[off]}, off + 1, nil
	case "U64", "DOUBLE":
		v, off2, err := need64(b, off)
		if err != nil {
			return nil, off, err
		}
		return Num64{Marker: 0x11, V: v}, off2, nil
	case "U16":
		v, off2, err := need16(b, off)
		if err != nil {
			return nil, off, err
		}
		return U16Field{V: v, IsNull: v == StrNullU16}, off2, nil
	case "STR":
		if err := need(b, off, 1, "STR 长度"); err != nil {
			return nil, off, err
		}
		n := int(b[off])
		off++
		if n == int(StrNullShort) {
			return StrField{IsNull: true}, off, nil
		}
		if err := need(b, off, n, "STR 内容"); err != nil {
			return nil, off, err
		}
		return StrField{S: string(b[off : off+n])}, off + n, nil
	case "U16STR":
		n, off2, err := need16(b, off)
		if err != nil {
			return nil, off, err
		}
		if n == StrNullU16 {
			return Str16Field{IsNull: true}, off2, nil
		}
		if err := need(b, off2, int(n), "U16STR 内容"); err != nil {
			return nil, off, err
		}
		return Str16Field{S: string(b[off2 : off2+int(n)])}, off2 + int(n), nil
	case "U32N":
		n, off2, err := need32(b, off)
		if err != nil {
			return nil, off, err
		}
		if n == Null32 {
			return Str32Field{IsNull: true}, off2, nil
		}
		if err := need(b, off2, int(n), "U32N 内容"); err != nil {
			return nil, off, err
		}
		return Str32Field{S: string(b[off2 : off2+int(n)])}, off2 + int(n), nil
	case "GUID16":
		if err := need(b, off, 16, "GUID16"); err != nil {
			return nil, off, err
		}
		return GUID{Hex: hex.EncodeToString(b[off : off+16])}, off + 16, nil
	case "OPTGUID":
		if err := need(b, off, 1, "OPTGUID 有没有"); err != nil {
			return nil, off, err
		}
		if b[off] == 0 {
			return OptGUIDField{Present: false}, off + 1, nil
		}
		off++
		if err := need(b, off, 16, "OPTGUID 内容"); err != nil {
			return nil, off, err
		}
		return OptGUIDField{Present: true, Hex: hex.EncodeToString(b[off : off+16])}, off + 16, nil
	case "BYTES":
		n, off2, err := need32(b, off)
		if err != nil {
			return nil, off, err
		}
		if n == Null32 {
			return Bytes{IsNull: true}, off2, nil
		}
		if err := need(b, off2, int(n), "BYTES 内容"); err != nil {
			return nil, off, err
		}
		cp := make([]byte, n)
		copy(cp, b[off2:off2+int(n)])
		return Bytes{Data: cp}, off2 + int(n), nil
	case "VAL", "DICT", "ARGLIST", "ARRAY", "U32ARR", "STR3", "KV":
		return t.DecodeValue(b, off)
	}
	return nil, off, fmt.Errorf("wire: 未知字段写法 %q", op)
}
