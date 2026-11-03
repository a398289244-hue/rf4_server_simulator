// Command rf4def 是「解剖刀的第二把」：把真服某条应答帧抽成**结构化定义 JSON**（data/*.json）。
//
// 为什么要抽：1/13（地图+天气）、28/1（大区浏览器）这种帧里全是静态定义，
// 但规矩是运行期不发录制。把真服那帧抽成 JSON 定义之后，运行期由 internal/wiredef 按类表现拼，
// 发出去的每个字节都是我们自己拼的，而且那份 JSON 在 data/ 里能直接改。
//
// 跑法（PowerShell）：
//
//	& D:\go\bin\go.exe run ./cmd/rf4def -cmd 1/13 -out ..\rf4_server\data\map_weather_1_13.json
//	& D:\go\bin\go.exe run ./cmd/rf4def -cmd 28/1 -out ..\rf4_server\data\region_explorer_28_1.json
//
// 抽完立刻跑 go test ./internal/wiredef，那边会把定义拼回去和真服原文逐字节比，
// 对不上就是抽取或拼帧跑偏了。
package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"rf4server/internal/assetpaths"
	"rf4server/internal/wire"
	"rf4server/internal/wiredef"
)

func main() {
	cmdFlag := flag.String("cmd", "", "要抽的命令，形如 1/13")
	outFlag := flag.String("out", "", "定义 JSON 写到哪儿（必填）")
	flag.Parse()

	if *cmdFlag == "" || *outFlag == "" {
		fmt.Println("用法：-cmd 1/13 -out data\\map_weather_1_13.json")
		return
	}

	tp, err := assetpaths.File("wire_tables.json")
	if err != nil {
		panic(err)
	}
	table, err := wire.LoadTable(tp)
	if err != nil {
		panic(err)
	}
	pay, err := assetpaths.RealAnswer(*cmdFlag)
	if err != nil {
		panic(err)
	}
	off, err := bodyOffset(pay)
	if err != nil {
		panic(err)
	}
	v, used, err := table.DecodeValue(pay, off)
	if err != nil {
		panic(err)
	}
	if used != len(pay) {
		panic(fmt.Errorf("只解到 %d 字节，整帧 %d 字节，剩下 %d 字节解不动 —— 类表得先补", used, len(pay), len(pay)-used))
	}

	def, err := toDef(v)
	if err != nil {
		panic(err)
	}
	// 抽完先自证一遍：拼回去必须和原文一模一样，否则这份定义不能拿去用
	back, err := wiredef.Build(table, def)
	if err != nil {
		panic(err)
	}
	if hex.EncodeToString(back) != hex.EncodeToString(pay[off:]) {
		panic(fmt.Errorf("定义拼回去和原文不一样：拼出来 %d 字节，原文 %d 字节", len(back), len(pay)-off))
	}

	b, err := json.MarshalIndent(def, "", " ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(*outFlag, b, 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("%s 抽好，%d 字节定义，回拼逐字节 == 真服原文（%d 字节）\n", *outFlag, len(b), len(pay)-off)
}

// bodyOffset 认帧头：应答跳 9 字节，推送跳 11 字节
func bodyOffset(pay []byte) (int, error) {
	if len(pay) < 12 {
		return 0, fmt.Errorf("帧太短：%d 字节", len(pay))
	}
	switch int32(le32(pay[1:5])) {
	case int32(wire.MarkerResponse):
		return 9, nil
	case int32(wire.MarkerPush):
		return 11, nil
	}
	return 0, fmt.Errorf("marker 不认识：%x", pay[1:5])
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// toDef 把解码出来的值树翻成定义树。翻不动的类型直接报错，别默默丢东西。
func toDef(v wire.Value) (wiredef.Node, error) {
	switch x := v.(type) {
	case wire.Null:
		return wiredef.Node{Kind: "null"}, nil
	case wire.Num32:
		return wiredef.Node{Kind: "u32", N: uint64(x.V)}, nil
	case wire.Num64:
		return wiredef.Node{Kind: "u64", N: x.V}, nil
	case wire.OptByte:
		return wiredef.Node{Kind: "byte", N: uint64(x.V)}, nil
	case wire.U16Field:
		if x.IsNull {
			return wiredef.Node{Kind: "null"}, nil
		}
		return wiredef.Node{Kind: "u16", N: uint64(x.V)}, nil
	case wire.StrField:
		if x.IsNull {
			return wiredef.Node{Kind: "null"}, nil
		}
		return wiredef.Node{Kind: "str", S: x.S}, nil
	case wire.Str16Field:
		if x.IsNull {
			return wiredef.Node{Kind: "null"}, nil
		}
		return wiredef.Node{Kind: "str16", S: x.S}, nil
	case wire.Str32Field:
		if x.IsNull {
			return wiredef.Node{Kind: "null"}, nil
		}
		return wiredef.Node{Kind: "str", S: x.S}, nil
	case wire.Str:
		return wiredef.Node{Kind: "str", S: x.S}, nil
	case wire.GUID:
		return wiredef.Node{Kind: "guid", S: x.Hex}, nil
	case *wire.Obj:
		if x.Unknown {
			return wiredef.Node{}, fmt.Errorf("typeID %d 类表里没有，抽不成定义（先补类表）", x.TypeID)
		}
		out := wiredef.Node{Kind: "obj", TypeID: int(x.TypeID)}
		for _, f := range x.Fields {
			n, err := toDef(f.Value)
			if err != nil {
				return out, fmt.Errorf("obj %d 第 %d 个字段 %s: %w", x.TypeID, f.Idx, f.Name, err)
			}
			out.Fields = append(out.Fields, wiredef.FieldNode{Idx: f.Idx, Val: n})
		}
		return out, nil
	case wire.Args:
		out := wiredef.Node{Kind: "args", Code: x.Code, CodeNull: x.CodeNull}
		for i, it := range x.Items {
			n, err := toDef(it)
			if err != nil {
				return out, fmt.Errorf("参数表 %q 第 %d 项: %w", x.Code, i, err)
			}
			out.Items = append(out.Items, n)
		}
		return out, nil
	case wire.ElemNull:
		return wiredef.Node{Kind: "elemnull"}, nil
	case wire.Bool:
		return wiredef.Node{Kind: "bool", B: x.V}, nil
	case wire.DateTime:
		return wiredef.Node{Kind: "datetime", N: x.V}, nil
	case wire.Bytes:
		return wiredef.Node{Kind: "bytes", S: string(x.Data), IsNull: x.IsNull}, nil
	case wire.U32Array:
		return wiredef.Node{Kind: "u32arr", Nums: x.Items}, nil
	case wire.TypedU32Array:
		return wiredef.Node{Kind: "u32typed", Code: x.Code, CodeNull: x.CodeNull, Nums: x.Items}, nil
	case wire.GUIDArray:
		out := wiredef.Node{Kind: "guidarr"}
		for _, g := range x.Items {
			out.Hexes = append(out.Hexes, hex.EncodeToString(g))
		}
		return out, nil
	case wire.BytesArray:
		out := wiredef.Node{Kind: "blobs"}
		for _, it := range x.Items {
			out.Items = append(out.Items, wiredef.Node{Kind: "bytes", S: string(it.Data), IsNull: it.IsNull})
		}
		return out, nil
	}
	return wiredef.Node{}, fmt.Errorf("值类型 %T 还没法抽成定义（要先在 wiredef 里加一种 kind）", v)
}
