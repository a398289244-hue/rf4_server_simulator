// Command rf4dump 是「解剖刀」：把真服某条命令的应答帧拆成字段树打出来。
//
// 用途：搬一条新命令时，先看真服那帧里每个字段是什么（编号、类型、人话名字、值），
// 再照着写 Go 构造器。数据只读凭据（rpc_table_real.json + wire_tables.json），
// 只在开发时手动跑，运行期一行都不碰，所以不违反「运行时不发录制」那条规矩。
//
// 跑法（PowerShell）：
//
//	& D:\go\bin\go.exe run ./cmd/rf4dump -cmd 3/1
//	& D:\go\bin\go.exe run ./cmd/rf4dump -cmd 3/1 -out D:\rf4log\dump_3_1.txt
//	& D:\go\bin\go.exe run ./cmd/rf4dump -list
package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"rf4server/internal/assetpaths"
	"rf4server/internal/wire"
)

// answer 是 rpc_table_real.json 里一条命令的真服应答
type answer struct {
	Pay  string `json:"pay"`  // base64：完整应答帧（含 9 字节帧头）
	Name string `json:"name"` // 类名（有的话）
}

// entry 是 fallback 表里一条命令的记录
type entry struct {
	Responses []answer `json:"responses"`
}

func main() {
	cmdFlag := flag.String("cmd", "", "要拆的命令，形如 3/1")
	listFlag := flag.Bool("list", false, "列出凭据里所有有应答样本的命令")
	outFlag := flag.String("out", "", "结果写到这个文件（不填就打到屏幕）")
	hexFlag := flag.String("hexfile", "", "拆一个 hex 文本文件：整帧（应答或推送）自己贴，自动认帧头从哪儿开始拆")
	maxStr := flag.Int("maxstr", 64, "字符串最长打多少个字符，剩下打省略号")
	flag.Parse()

	// 输出目标：文件优先。走屏幕的话中文会被控制台代码页搞花，少爷看文件更清楚
	w := io.Writer(os.Stdout)
	if *outFlag != "" {
		f, err := os.Create(*outFlag)
		if err != nil {
			panic(err)
		}
		defer f.Close()
		w = f
	}

	tablePath, err := assetpaths.File("wire_tables.json")
	if err != nil {
		panic(err)
	}
	table, err := wire.LoadTable(tablePath)
	if err != nil {
		panic(err)
	}
	tabPath, err := assetpaths.File("rpc_table_real.json")
	if err != nil {
		panic(err)
	}
	raw, err := os.ReadFile(tabPath)
	if err != nil {
		panic(err)
	}
	var tab struct {
		Fallback map[string]entry `json:"fallback"`
	}
	if err := json.Unmarshal(raw, &tab); err != nil {
		panic(err)
	}

	// hex 模式：拆少爷从日志里贴出来的整帧（比如线上那条 273 字节的 2/1 玩家状态）。
	// 帧头自动认：marker=-2 是应答（跳 9 字节），marker=-1 是推送（跳 11 字节，多 main/sub 两字节）。
	if *hexFlag != "" {
		rawHex, err := os.ReadFile(*hexFlag)
		if err != nil {
			panic(err)
		}
		pay, err := hex.DecodeString(strings.Join(strings.Fields(string(rawHex)), ""))
		if err != nil {
			panic(err)
		}
		skip, kind, err := frameBodyOffset(pay)
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(w, "帧共 %d 字节，%s，帧头 hex=%s\n\n", len(pay), kind, hex.EncodeToString(pay[:skip]))
		v, off, err := table.DecodeValue(pay, skip)
		if err != nil {
			fmt.Fprintf(w, "解码失败：%v\n", err)
			return
		}
		if off != len(pay) {
			fmt.Fprintf(w, "警告：只解到 %d 字节，总共 %d 字节，尾巴还剩 %d 字节没解\n\n", off, len(pay), len(pay)-off)
		}
		printValue(w, v, 0, *maxStr)
		return
	}

	if *listFlag {
		keys := make([]string, 0, len(tab.Fallback))
		for k := range tab.Fallback {
			keys = append(keys, k)
		}
		sortCmds(keys)
		for _, k := range keys {
			rs := tab.Fallback[k].Responses
			sizes := make([]string, 0, len(rs))
			for _, r := range rs {
				b, _ := base64.StdEncoding.DecodeString(r.Pay)
				sizes = append(sizes, strconv.Itoa(len(b)))
			}
			fmt.Fprintf(w, "%-8s 应答 %d 条，长度 %s\n", k, len(rs), strings.Join(sizes, "/"))
		}
		return
	}
	if *cmdFlag == "" {
		fmt.Fprintln(w, "用法：-cmd 3/1 或 -list")
		return
	}

	rs := tab.Fallback[*cmdFlag].Responses
	if len(rs) == 0 {
		fmt.Fprintf(w, "凭据里没有 %s 的应答样本（可先跑 -list 看看有哪些）\n", *cmdFlag)
		return
	}
	pay, err := base64.StdEncoding.DecodeString(rs[0].Pay)
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(w, "命令 %s 应答 %d 字节\n帧头 hex=%s（9 字节：01 | marker(-2) | call_id）\n\n",
		*cmdFlag, len(pay), hex.EncodeToString(pay[:9]))
	if len(pay) <= 9 {
		fmt.Fprintln(w, "只有帧头，值是空的（这种命令直接回个空头就行）")
		return
	}
	v, off, err := table.DecodeValue(pay, 9)
	if err != nil {
		fmt.Fprintf(w, "解码失败：%v\n", err)
		return
	}
	if off != len(pay) {
		fmt.Fprintf(w, "警告：只解到 %d 字节，总共 %d 字节，剩下 %d 字节没解\n\n", off, len(pay), len(pay)-off)
	} else {
		fmt.Fprintf(w, "解码正好吃完 %d 字节，说明字段表认这条命令\n\n", off)
	}
	printValue(w, v, 0, *maxStr)
}

// sortCmds 按「主命令、子命令」的数字大小排序，不是一串字典序
func sortCmds(keys []string) {
	sort.Slice(keys, func(i, j int) bool {
		a := strings.Split(keys[i], "/")
		b := strings.Split(keys[j], "/")
		an, _ := strconv.Atoi(a[0])
		aj, _ := strconv.Atoi(a[1])
		bn, _ := strconv.Atoi(b[0])
		bj, _ := strconv.Atoi(b[1])
		if an != bn {
			return an < bn
		}
		return aj < bj
	})
}

// printValue 递归打印一个值。缩进两层 = 一个层级，方便照着写构造器
func printValue(w io.Writer, v wire.Value, depth int, maxStr int) {
	pad := strings.Repeat("  ", depth)
	switch x := v.(type) {
	case *wire.Obj:
		fmt.Fprintf(w, "%s对象 %s (typeID %d) 字段 %d 个", pad, x.Class, x.TypeID, len(x.Fields))
		if x.Unknown {
			fmt.Fprintf(w, "  [字段表里没有这个类型，原样保留 %d 字节]", len(x.Raw))
		}
		fmt.Fprintln(w)
		for _, f := range x.Fields {
			zh := ""
			if f.Zh != "" {
				zh = "  // " + f.Zh
			}
			fmt.Fprintf(w, "%s  #%02d %-8s %-22s%s\n", pad, f.Idx, f.Op, f.Name, zh)
			printValue(w, f.Value, depth+2, maxStr)
		}
	case wire.Args:
		fmt.Fprintf(w, "%s参数表 code=%q，共 %d 项\n", pad, x.Code, len(x.Items))
		for i, it := range x.Items {
			fmt.Fprintf(w, "%s  [%d]\n", pad, i)
			printValue(w, it, depth+2, maxStr)
		}
	case wire.Dict:
		fmt.Fprintf(w, "%s字典 type=%q，共 %d 对\n", pad, x.TypeName, len(x.Pairs))
		for i, p := range x.Pairs {
			fmt.Fprintf(w, "%s  第 %d 对 key=%q\n", pad, i, p.Key)
			printValue(w, p.Val, depth+2, maxStr)
		}
	case wire.ValList:
		fmt.Fprintf(w, "%s列表，共 %d 项\n", pad, len(x.Items))
		for i, it := range x.Items {
			fmt.Fprintf(w, "%s  [%d]\n", pad, i)
			printValue(w, it, depth+2, maxStr)
		}
	case wire.KV:
		fmt.Fprintf(w, "%s短字符串键值对 %q = %q\n", pad, x.K.S, x.V.S)
	case wire.Str3:
		fmt.Fprintf(w, "%s三个短字符串 %v\n", pad, x.Items)
	case wire.GUIDArray:
		fmt.Fprintf(w, "%sGUID 数组，共 %d 个\n", pad, len(x.Items))
		for i, g := range x.Items {
			fmt.Fprintf(w, "%s  [%d] %x\n", pad, i, g)
		}
	case wire.BytesArray:
		fmt.Fprintf(w, "%s字节块数组，共 %d 块\n", pad, len(x.Items))
		for i, b := range x.Items {
			s := hex.EncodeToString(b.Data)
			if len(s) > maxStr*2 {
				s = s[:maxStr*2] + "…"
			}
			fmt.Fprintf(w, "%s  [%d] %d 字节 %s\n", pad, i, len(b.Data), s)
		}
	case wire.U32Array:
		fmt.Fprintf(w, "%s u32 数组 %v\n", pad, x.Items)
	case wire.TypedU32Array:
		fmt.Fprintf(w, "%s 带类型 u32 数组 code=%q %v\n", pad, x.Code, x.Items)
	case wire.Num32:
		fmt.Fprintf(w, "%s数字 %d (0x%x)；当浮点看 = %g\n", pad, x.V, x.V, wire.BitsF32(x.V))
	case wire.Num64:
		fmt.Fprintf(w, "%s长整数 %d (0x%x)\n", pad, x.V, x.V)
	case wire.DateTime:
		fmt.Fprintf(w, "%s日期时间 %d\n", pad, x.V)
	case wire.GUID:
		fmt.Fprintf(w, "%sGUID %s\n", pad, x.Hex)
	case wire.OptGUIDField:
		if x.Present {
			fmt.Fprintf(w, "%sGUID（有）%s\n", pad, x.Hex)
		} else {
			fmt.Fprintf(w, "%sGUID（空）\n", pad)
		}
	case wire.Bytes:
		s := hex.EncodeToString(x.Data)
		if len(s) > maxStr*2 {
			s = s[:maxStr*2] + "…"
		}
		if x.IsNull {
			fmt.Fprintln(w, pad+"字节块（null）")
		} else {
			fmt.Fprintf(w, "%s字节块 %d 字节 %s\n", pad, len(x.Data), s)
		}
	case wire.Str:
		fmt.Fprintf(w, "%s字符串 %q\n", pad, clip(x.S, maxStr))
	case wire.StrField:
		fmt.Fprintf(w, "%s短字符串 %q\n", pad, clip(x.S, maxStr))
	case wire.Str16Field:
		fmt.Fprintf(w, "%s短字符串(16 位长) %q\n", pad, clip(x.S, maxStr))
	case wire.Str32Field:
		fmt.Fprintf(w, "%s短字符串(32 位长) %q\n", pad, clip(x.S, maxStr))
	case wire.OptByte:
		fmt.Fprintf(w, "%s单字节 %d (0x%02x)\n", pad, x.V, x.V)
	case wire.U16Field:
		fmt.Fprintf(w, "%s双字节 %d (0x%04x)\n", pad, x.V, x.V)
	case wire.Bool:
		fmt.Fprintf(w, "%s布尔 %v\n", pad, x.V)
	case wire.Null:
		fmt.Fprintln(w, pad+"null")
	case wire.ElemNull:
		fmt.Fprintln(w, pad+"数组元素位 null")
	default:
		if b, err := x.Encode(); err == nil {
			fmt.Fprintf(w, "%s未知类型 %T：%s\n", pad, x, hex.EncodeToString(b))
		} else {
			fmt.Fprintf(w, "%s未知类型 %T（编码也失败：%v）\n", pad, x, err)
		}
	}
}

// clip 字符串太长就截断，别把屏幕刷屏
func clip(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// frameBodyOffset 认出这帧是应答还是推送，返回「帧体从第几字节开始」和人话说明。
// 应答帧：01 | marker(-2) | call_id            -> 9 字节头
// 推送帧：01 | marker(-1) | 序号 | main | sub  -> 11 字节头
func frameBodyOffset(pay []byte) (int, string, error) {
	if len(pay) < 12 {
		return 0, "", fmt.Errorf("帧太短（%d 字节），连帧头都放不下", len(pay))
	}
	switch int32(binary.LittleEndian.Uint32(pay[1:5])) {
	case int32(wire.MarkerResponse):
		return 9, "应答帧（marker=-2，带 call_id）", nil
	case int32(wire.MarkerPush):
		return 11, fmt.Sprintf("推送帧（marker=-1，主命令 %d/子命令 %d）", pay[9], pay[10]), nil
	}
	return 0, "", fmt.Errorf("第 2~5 字节的 marker 不认识：%x", pay[1:5])
}
