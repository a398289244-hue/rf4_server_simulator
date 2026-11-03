package wire

import "encoding/hex"

// Walk 沿着一棵解码出来的值树走一遍，看到什么就回调什么。
//
// 为什么要有这个：很多请求（11/2 绑快捷栏、4/7 丢东西、17/2 装配、14/2 抛竿）
// 里面就是「一串槽位号 + 几个 GUID」混在参数表/物件/数组里，位置还不固定。
// 与其给每条命令写一套「按偏移抠字节」的解析（错位一次查半天），
// 不如整棵树走一遍，把要的东西捞出来 —— 顺序仍然按帧里的先后，不会乱。
type WalkItem struct {
	Kind   string // "u32" "u64" "byte" "u16" "guid" "str" "blobs" "obj" "args"
	Num    uint64 // u32/u64/byte/u16 的数
	Hex    string // guid：32 位十六进制小写
	S      string // str / str16 / 字节块内容
	TypeID uint32 // obj 的类型号（想按物件挑字段时用）
	Depth  int    // 在第几层，调试日志好用
}

// Walk 深度优先遍历。fn 看到每一项都会被调一次（容器项自己也算一项）。
// 认不出来的值就跳过，不报错：这里是「捞东西」，不是「校验协议」。
func Walk(v Value, fn func(WalkItem)) { walkAt(v, 0, fn) }

func walkAt(v Value, depth int, fn func(WalkItem)) {
	if v == nil {
		return
	}
	switch x := v.(type) {
	case *Obj:
		fn(WalkItem{Kind: "obj", TypeID: x.TypeID, Depth: depth})
		for _, f := range x.Fields {
			walkAt(f.Value, depth+1, fn)
		}
	case Args:
		fn(WalkItem{Kind: "args", S: x.Code, Depth: depth})
		for _, it := range x.Items {
			walkAt(it, depth+1, fn)
		}
	case Num32:
		fn(WalkItem{Kind: "u32", Num: uint64(x.V), Depth: depth})
	case Num64:
		fn(WalkItem{Kind: "u64", Num: x.V, Depth: depth})
	case OptByte:
		fn(WalkItem{Kind: "byte", Num: uint64(x.V), Depth: depth})
	case U16Field:
		if !x.IsNull {
			fn(WalkItem{Kind: "u16", Num: uint64(x.V), Depth: depth})
		}
	case GUID:
		if x.Hex != "" {
			fn(WalkItem{Kind: "guid", Hex: lowerHex(x.Hex), Depth: depth})
		}
	case GUIDArray:
		fn(WalkItem{Kind: "guidarr", Depth: depth})
		for _, g := range x.Items {
			if len(g) == 16 {
				fn(WalkItem{Kind: "guid", Hex: lowerHex(hexEncode(g)), Depth: depth})
			}
		}
	case StrField:
		if !x.IsNull {
			fn(WalkItem{Kind: "str", S: x.S, Depth: depth})
		}
	case Str16Field:
		if !x.IsNull {
			fn(WalkItem{Kind: "str", S: x.S, Depth: depth})
		}
	case Str:
		fn(WalkItem{Kind: "str", S: x.S, Depth: depth})
	case BytesArray:
		fn(WalkItem{Kind: "blobs", Depth: depth})
		for _, it := range x.Items {
			if !it.IsNull {
				fn(WalkItem{Kind: "blobs", S: string(it.Data), Depth: depth + 1})
			}
		}
	case Bytes:
		if !x.IsNull {
			fn(WalkItem{Kind: "blobs", S: string(x.Data), Depth: depth})
		}
	case U32Array:
		fn(WalkItem{Kind: "u32arr", Depth: depth})
		for _, n := range x.Items {
			fn(WalkItem{Kind: "u32", Num: uint64(n), Depth: depth + 1})
		}
	case TypedU32Array:
		fn(WalkItem{Kind: "u32typed", S: x.Code, Depth: depth})
		for _, n := range x.Items {
			fn(WalkItem{Kind: "u32", Num: uint64(n), Depth: depth + 1})
		}
	case Bool:
		fn(WalkItem{Kind: "bool", Num: b2n(x.V), Depth: depth})
	case DateTime:
		fn(WalkItem{Kind: "datetime", Num: x.V, Depth: depth})
	}
}

// ArgsItems 把参数表里的项摊开（11/2 那种「参数表里就是槽位 + GUID」的请求最常用）
func ArgsItems(v Value) []Value {
	if a, ok := v.(Args); ok {
		return a.Items
	}
	return nil
}

func b2n(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// lowerHex 把 GUID 的十六进制统一成小写：MySQL objects.guid 存的是小写，
// 帧里解出来的也可能是大写，两边比之前先归一，省得「明明同一件却说不是」。
func lowerHex(h string) string {
	out := []byte(h)
	for i, c := range out {
		if c >= 'A' && c <= 'Z' {
			out[i] = c + ('a' - 'A')
		}
	}
	return string(out)
}

// hexEncode 字节转小写十六进制（hex.EncodeToString 的短别名）
func hexEncode(b []byte) string { return hex.EncodeToString(b) }
