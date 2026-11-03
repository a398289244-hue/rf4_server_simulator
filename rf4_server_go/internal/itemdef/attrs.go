package itemdef

// attrs_json 里存的是「字段名 -> 值」。这些字段名是协议逆推出来的混淆名（像 kngaaefkpka），
// 值有一个很关键的口径：**浮点属性存的是 IEEE754 位模式整数**（不是 0.1 这种十进制）。
// 所以解析时必须原样保住那个整数 —— 走一遍 float64 再转回来，末位就可能变，
// 现拼的记录和真服记录就对不上一个字节。

import (
	"encoding/json"
	"errors"
	"strings"
)

// Kind 属性值的种类。拼字节时按这个挑写法，不靠猜。
type Kind int

const (
	Null   Kind = iota // 明确写了 null（这一格发 null）
	Number             // 整数；浮点属性在这里就是位模式整数
	Float              // 真浮点（attrs 里极少见，个别带小数点的）
	Str                // 字符串
	Bool               // 布尔（拼字节时写 1/0）
	Obj                // 嵌套对象（比如竿的出厂部件配置）
	Arr                // 数组（比如拟饵的出厂钩列表）
)

// Value 一个属性值。U 是 Number 的整数（含浮点位模式），F 是 Float 的值，
// S 是字符串，Obj/Arr 是嵌套结构。
type Value struct {
	Kind Kind
	U    uint64
	F    float64
	S    string
	Obj  map[string]Value
	Arr  []Value
}

// IsNull 这一格是不是「明确为空」。拼记录时要发 null 而不是 0。
func (v Value) IsNull() bool { return v.Kind == Null }

// Bits32 按 u32 用这个值（超过 32 位就截低 32 位，和 JS 的 >>>0 一个口径）
func (v Value) Bits32() uint32 { return uint32(v.U & 0xffffffff) }

// ParseAttrs 解析 attrs_json。用 json.Number 是为了大整数不经过 float64 掉精度。
// 空串 -> 返回空 map（不是错误）：确实有一两件货没属性。
func ParseAttrs(text string) (map[string]Value, error) {
	text = strings.TrimSpace(text)
	if text == "" || text == "null" {
		return map[string]Value{}, nil
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	out := make(map[string]Value, len(raw))
	for k, v := range raw {
		out[k] = convert(v)
	}
	return out, nil
}

// convert JSON 解码出来的通用值 -> 我们的 Value。认不出的种类当 Null 处理（宁可少写一格，别写错）。
func convert(v any) Value {
	switch x := v.(type) {
	case nil:
		return Value{Kind: Null}
	case bool:
		return Value{Kind: Bool, U: b2u(x)}
	case json.Number:
		return fromNumber(x)
	case string:
		return Value{Kind: Str, S: x}
	case map[string]any:
		obj := make(map[string]Value, len(x))
		for k, sub := range x {
			obj[k] = convert(sub)
		}
		return Value{Kind: Obj, Obj: obj}
	case []any:
		arr := make([]Value, 0, len(x))
		for _, sub := range x {
			arr = append(arr, convert(sub))
		}
		return Value{Kind: Arr, Arr: arr}
	}
	return Value{Kind: Null}
}

// fromNumber 整数走 U（保住位模式整数），带小数点/指数的走 F。
func fromNumber(n json.Number) Value {
	s := n.String()
	if strings.ContainsAny(s, ".eE") {
		f, err := n.Float64()
		if err != nil {
			return Value{Kind: Null}
		}
		return Value{Kind: Float, F: f}
	}
	// 整数一律走 U：json.Number 没有 Uint64，所以先按有符号读，
	// 负数（attrs 里少见）就地转成补码位模式存起来；真超 int64 的再退回浮点。
	i64, err := n.Int64()
	if err == nil {
		return Value{Kind: Number, U: uint64(i64)}
	}
	f, err := n.Float64()
	if err != nil {
		return Value{Kind: Null}
	}
	return Value{Kind: Float, F: f}
}

func b2u(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// ErrNoAttrs 这件货没有属性表（不算错误，只是调用方要知道少了这一步）
var ErrNoAttrs = errors.New("这件货没有属性表")
