// Package wiredef 把「解剖好的静态定义」按类表现拼成帧体。
//
// 为什么要有这一层：有些应答（1/13 地图+天气、28/1 大区浏览器）里面全是静态定义，
// 但规矩是「运行期一帧录制都不发」。办法是把真服那帧先解剖成**结构化 JSON**（像 skill_defs.json 那样），
// 运行期再由这边按类表一个字节一个字节重新拼出来 —— 发出去的都是我们自己拼的，
// 而且那份 JSON 在 data/ 里能直接看直接改（少爷的规矩：数据要能在文件里看到）。
//
// 定义文件由 cmd/rf4def 从考卷里抽，抽完测试会做「拼回去 == 原文」的逐字节校验，
// 所以定义不会和真服悄悄跑偏。
package wiredef

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"rf4server/internal/wire"
)

// Node 定义树里的一个值。kind 决定读哪个字段，其余留空就行。
//
//	"u32" "u64" "byte" "u16" -> N（u32 那种存的是位模式，浮点也走它）
//	"str" "str16"            -> S
//	"blobs"                  -> 空字节块数组（Items 目前只用不上，留 0 项）
//	"args"                   -> Code + Items
//	"obj"                    -> TypeID + Fields
//	"null"                   -> 什么都不带
type Node struct {
	Kind     string      `json:"kind"`
	N        uint64      `json:"n,omitempty"`
	S        string      `json:"s,omitempty"`
	Code     string      `json:"code,omitempty"`
	CodeNull bool        `json:"codeNull,omitempty"` // 参数表名写 null（0xff）而不是空串
	Items    []Node      `json:"items,omitempty"`
	Nums     []uint32    `json:"nums,omitempty"`   // u32arr / u32typed 用
	Hexes    []string    `json:"hexes,omitempty"`  // guidarr 用（每项 32 位十六进制）
	B        bool        `json:"b,omitempty"`      // bool 用
	IsNull   bool        `json:"isNull,omitempty"` // bytes / blobs 那一项是 null
	TypeID   int         `json:"typeId,omitempty"`
	Fields   []FieldNode `json:"fields,omitempty"`
}

// FieldNode 物件里「第 idx 个字段填这个值」。没列出来的字段走类表里的合法空值。
type FieldNode struct {
	Idx int  `json:"idx"`
	Val Node `json:"val"`
}

// LoadFile 读一份定义 JSON。文件不存在就把错误原样带出去，让调用方说清楚缺哪份凭据。
func LoadFile(path string) (Node, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Node{}, fmt.Errorf("读静态定义 %s 失败: %w", path, err)
	}
	var n Node
	if err := json.Unmarshal(b, &n); err != nil {
		return Node{}, fmt.Errorf("静态定义 %s 解析失败: %w", path, err)
	}
	return n, nil
}

// Build 按类表把一个定义节点拼成「带 marker 的字节」（就是帧体里那一坨）。
// 物件的字段写法一律从类表来，写错类型当场报错，不会闷声发怪帧。
func Build(t *wire.Table, n Node) ([]byte, error) {
	v, err := Value(t, n)
	if err != nil {
		return nil, err
	}
	return v.Encode()
}

// FromValue 把 wire 解码树抽成可落盘的结构化定义。录制帧只在离线生成阶段进这里；
// 运行时读取定义后仍由 Build 按当前类表现重新编码，不回放原始字节。
func FromValue(v wire.Value) (Node, error) {
	switch x := v.(type) {
	case wire.Null:
		return Node{Kind: "null"}, nil
	case wire.Num32:
		return Node{Kind: "u32", N: uint64(x.V)}, nil
	case wire.Num64:
		return Node{Kind: "u64", N: x.V}, nil
	case wire.OptByte:
		return Node{Kind: "byte", N: uint64(x.V)}, nil
	case wire.U16Field:
		if x.IsNull {
			return Node{Kind: "null"}, nil
		}
		return Node{Kind: "u16", N: uint64(x.V)}, nil
	case wire.StrField:
		if x.IsNull {
			return Node{Kind: "null"}, nil
		}
		return Node{Kind: "str", S: x.S}, nil
	case wire.Str16Field:
		if x.IsNull {
			return Node{Kind: "null"}, nil
		}
		return Node{Kind: "str16", S: x.S}, nil
	case wire.Str32Field:
		if x.IsNull {
			return Node{Kind: "null"}, nil
		}
		return Node{Kind: "str", S: x.S}, nil
	case wire.Str:
		return Node{Kind: "str", S: x.S}, nil
	case wire.GUID:
		return Node{Kind: "guid", S: x.Hex}, nil
	case *wire.Obj:
		if x.Unknown {
			return Node{}, fmt.Errorf("typeID %d 类表里没有，抽不成定义", x.TypeID)
		}
		out := Node{Kind: "obj", TypeID: int(x.TypeID)}
		for _, f := range x.Fields {
			n, err := FromValue(f.Value)
			if err != nil {
				return out, fmt.Errorf("obj %d 第 %d 个字段 %s: %w", x.TypeID, f.Idx, f.Name, err)
			}
			out.Fields = append(out.Fields, FieldNode{Idx: f.Idx, Val: n})
		}
		return out, nil
	case wire.Args:
		out := Node{Kind: "args", Code: x.Code, CodeNull: x.CodeNull}
		for i, it := range x.Items {
			n, err := FromValue(it)
			if err != nil {
				return out, fmt.Errorf("参数表 %q 第 %d 项: %w", x.Code, i, err)
			}
			out.Items = append(out.Items, n)
		}
		return out, nil
	case wire.ElemNull:
		return Node{Kind: "elemnull"}, nil
	case wire.Bool:
		return Node{Kind: "bool", B: x.V}, nil
	case wire.DateTime:
		return Node{Kind: "datetime", N: x.V}, nil
	case wire.Bytes:
		return Node{Kind: "bytes", S: string(x.Data), IsNull: x.IsNull}, nil
	case wire.U32Array:
		return Node{Kind: "u32arr", Nums: x.Items}, nil
	case wire.TypedU32Array:
		return Node{Kind: "u32typed", Code: x.Code, CodeNull: x.CodeNull, Nums: x.Items}, nil
	case wire.GUIDArray:
		out := Node{Kind: "guidarr"}
		for _, g := range x.Items {
			out.Hexes = append(out.Hexes, hex.EncodeToString(g))
		}
		return out, nil
	case wire.BytesArray:
		out := Node{Kind: "blobs"}
		for _, it := range x.Items {
			out.Items = append(out.Items, Node{Kind: "bytes", S: string(it.Data), IsNull: it.IsNull})
		}
		return out, nil
	}
	return Node{}, fmt.Errorf("值类型 %T 还不能抽成定义", v)
}

// Value 把定义节点变成 wire 里的一个值（物件、参数表、标量都行）。
// 物件要先有类表才能造，所以这里必须带 table。
func Value(t *wire.Table, n Node) (wire.Value, error) {
	switch n.Kind {
	case "u32":
		return wire.U32(uint32(n.N)), nil
	case "u64":
		return wire.U64(n.N), nil
	case "byte":
		return wire.OptByte{V: uint8(n.N)}, nil
	case "u16":
		return wire.U16Field{V: uint16(n.N)}, nil
	case "str":
		return wire.StrField{S: n.S}, nil
	case "str16":
		return wire.Str16Field{S: n.S}, nil
	case "guid":
		return wire.GUID{Hex: n.S}, nil
	case "null":
		return wire.Null{}, nil
	case "elemnull":
		return wire.ElemNull{}, nil
	case "bool":
		return wire.Bool{V: n.B}, nil
	case "datetime":
		return wire.DateTime{V: n.N}, nil
	case "bytes":
		return wire.Bytes{Data: []byte(n.S), IsNull: n.IsNull}, nil
	case "u32arr":
		return wire.U32Array{Items: n.Nums}, nil
	case "u32typed":
		return wire.TypedU32Array{Code: n.Code, CodeNull: n.CodeNull, Items: n.Nums}, nil
	case "guidarr":
		out := wire.GUIDArray{}
		for i, h := range n.Hexes {
			raw, err := hex.DecodeString(h)
			if err != nil || len(raw) != 16 {
				return nil, fmt.Errorf("guidarr 第 %d 项要 32 位十六进制，给了 %q", i, h)
			}
			out.Items = append(out.Items, raw)
		}
		return out, nil
	case "blobs":
		out := wire.BytesArray{}
		for _, it := range n.Items {
			out.Items = append(out.Items, wire.BlobItem{Data: []byte(it.S), IsNull: it.IsNull})
		}
		return out, nil
	case "args":
		out := wire.Args{Code: n.Code, CodeNull: n.CodeNull}
		for _, it := range n.Items {
			v, err := Value(t, it)
			if err != nil {
				return nil, fmt.Errorf("参数表 %q 第 %d 项: %w", n.Code, len(out.Items), err)
			}
			out.Items = append(out.Items, v)
		}
		return out, nil
	case "obj":
		o, err := t.NewObj(n.TypeID)
		if err != nil {
			return nil, err
		}
		for _, f := range n.Fields {
			if err := setField(t, o, f.Idx, f.Val); err != nil {
				return nil, fmt.Errorf("obj %d 第 %d 个字段: %w", n.TypeID, f.Idx, err)
			}
		}
		return o, nil
	}
	return nil, fmt.Errorf("静态定义里不认识的值类型 kind=%q", n.Kind)
}

// setField 按类表给物件第 idx 个字段填值：写法从类表来，值的 kind 必须对得上。
// 这里刻意不「猜」：类表说是 U64 就必须给 u64，给成 u32 就报错，免得定义写歪了没人知道。
func setField(t *wire.Table, o *wire.Obj, idx int, n Node) error {
	if idx < 0 || idx >= len(o.Fields) {
		return fmt.Errorf("对象只有 %d 个字段，要第 %d 个", len(o.Fields), idx)
	}
	f := &o.Fields[idx]
	switch f.Op {
	case "U32", "FLOAT":
		if n.Kind != "u32" {
			return fmt.Errorf("字段 %s 写法 %s，定义里却给了 kind=%q", f.Name, f.Op, n.Kind)
		}
		f.Value = wire.U32(uint32(n.N))
	case "U64", "DOUBLE":
		if n.Kind != "u64" && n.Kind != "u32" {
			return fmt.Errorf("字段 %s 写法 %s，定义里却给了 kind=%q", f.Name, f.Op, n.Kind)
		}
		f.Value = wire.U64(n.N)
	case "BYTE", "OPT":
		if n.Kind != "byte" {
			return fmt.Errorf("字段 %s 写法 %s，定义里却给了 kind=%q", f.Name, f.Op, n.Kind)
		}
		f.Value = wire.OptByte{V: uint8(n.N)}
	case "U16":
		if n.Kind != "u16" && n.Kind != "byte" {
			return fmt.Errorf("字段 %s 写法 %s，定义里却给了 kind=%q", f.Name, f.Op, n.Kind)
		}
		f.Value = wire.U16Field{V: uint16(n.N)}
	case "STR":
		if n.Kind != "str" {
			return fmt.Errorf("字段 %s 写法 STR，定义里却给了 kind=%q", f.Name, n.Kind)
		}
		f.Value = wire.StrField{S: n.S}
	case "U16STR":
		if n.Kind != "str16" {
			return fmt.Errorf("字段 %s 写法 U16STR，定义里却给了 kind=%q", f.Name, n.Kind)
		}
		f.Value = wire.Str16Field{S: n.S}
	case "GUID16":
		if n.Kind != "guid" {
			return fmt.Errorf("字段 %s 写法 GUID16，定义里却给了 kind=%q", f.Name, n.Kind)
		}
		f.Value = wire.GUID{Hex: n.S}
	case "VAL", "DICT", "ARGLIST", "ARRAY", "U32ARR", "STR3", "KV", "BYTES":
		v, err := Value(t, n)
		if err != nil {
			return err
		}
		f.Value = v
	default:
		return fmt.Errorf("字段 %s 的写法 %q 拼帧这边还没支持", f.Name, f.Op)
	}
	return nil
}
