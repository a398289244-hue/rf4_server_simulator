// Package gamecfg 管「游戏开机要的那 9 块配置」：1/4 下发配置块本体，1/5 回这 9 块的 sha1 版本号。
//
// 为什么必须有它：客户端开机时会主动问 1/4，回一帧「没这条 RPC」它就当场抛
// `Game bootstrapping failed: System.Exception: Unimplemented RPC 1:4` 然后断开
// （实机 2026-11-03 01:12 客户端日志就是这行）。所以这条不是「可选的小命令」，是开机必经的。
//
// 这 9 块配置是 LZMA 压缩包（块头 5d 00 00 40 00），我们不解压也不改，原样下发就行。
// 资产在凭据目录里，能直接看直接换：
//
//	data/config_wire/index.json   9 个版本号（sha1）+ 每块的字节数
//	data/config_wire/type_N.bin   第 N 块的配置本体（N = 1..9）
//
// 铁证一条：每块 type_N.bin 现算 sha1 都等于 index.json 里那个版本号（测试盯着这条），
// 所以「1/5 回的版本号」和「1/4 发的内容」是自洽的一对，不是凑出来的。
// 顺带把以前那个谜解了：1/5 那 9 段 40 字符的串不是什么会话指纹，就是配置块的 sha1。
package gamecfg

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"rf4server/internal/assetpaths"
	"rf4server/internal/wire"
)

// TypeCount 配置块数量。真服 1/5 就是回 9 段，客户端也是按 9 段读的。
const TypeCount = 9

// VersionChars 一个版本号多少字符（sha1 的十六进制形态）
const VersionChars = 40

// Library 装好的配置库：类型号 -> 配置块字节，外加那份版本号清单。
// 启动时装一次，之后只读，所以不用加锁。
type Library struct {
	// versions 9 个 sha1 版本号（顺序 = 类型 1..9），1/5 直接回这个
	versions []string
	// blobs 类型号 -> 配置块本体（已读进内存，总共 640KB 左右，不占什么）
	blobs map[int][]byte
	// dir 索引文件是从哪儿读的（日志要说清来源，别让人猜）
	dir string
}

// indexFile data/config_wire/index.json 的结构。
// types 里那两个字段（sha1 / bytes）只用来做一致性校验，以后它多加字段我们也不关心。
type indexFile struct {
	Versions []string `json:"versions"`
	Types    map[string]struct {
		SHA1  string `json:"sha1"`
		Bytes int    `json:"bytes"`
	} `json:"types"`
}

// Load 从凭据目录读那 9 块配置 + index.json。
// 少一块、或者哪块的 sha1 和 index 里对不上，就直接报错：
// 这玩意儿发错了客户端会当成「配置坏了」，宁可后端起不来时吵一句，也别默默发一份坏配置出去。
func Load() (*Library, error) {
	indexPath, err := assetpaths.File("config_wire/index.json")
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("读配置索引 %s 失败: %w", indexPath, err)
	}
	var idx indexFile
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("配置索引 %s 不是合法 JSON: %w", indexPath, err)
	}
	if len(idx.Versions) != TypeCount {
		return nil, fmt.Errorf("配置索引里版本号应该是 %d 个，实际 %d 个（%s）", TypeCount, len(idx.Versions), indexPath)
	}

	lib := &Library{
		versions: append([]string(nil), idx.Versions...),
		blobs:    map[int][]byte{},
		dir:      indexPath,
	}
	for typ := 1; typ <= TypeCount; typ++ {
		path, err := assetpaths.File("config_wire/type_" + strconv.Itoa(typ) + ".bin")
		if err != nil {
			return nil, err
		}
		blob, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("配置块 %d 读不到 %s: %w", typ, path, err)
		}
		if len(blob) == 0 {
			return nil, fmt.Errorf("配置块 %d 是空的（%s）", typ, path)
		}
		// 自证：块的 sha1 必须就是 1/5 要回的那个版本号，否则客户端缓存会认为配置坏了
		sum := sha1.Sum(blob)
		got := hex.EncodeToString(sum[:])
		want := idx.Versions[typ-1]
		if rec, ok := idx.Types[strconv.Itoa(typ)]; ok && rec.SHA1 != "" && rec.SHA1 != want {
			return nil, fmt.Errorf("配置索引自相矛盾：类型 %d 的 versions 是 %s，types 里写的却是 %s", typ, want, rec.SHA1)
		}
		if got != want {
			return nil, fmt.Errorf("配置块 %d 的 sha1 是 %s，和 1/5 要回的版本号 %s 对不上（%s）", typ, got, want, path)
		}
		lib.blobs[typ] = blob
	}
	return lib, nil
}

// Versions 9 个版本号（1/5 回的就是它，顺序就是类型 1..9）
func (l *Library) Versions() []string {
	if l == nil {
		return nil
	}
	return append([]string(nil), l.versions...)
}

// Count 装了几块（开机日志用）
func (l *Library) Count() int {
	if l == nil {
		return 0
	}
	return len(l.blobs)
}

// Bytes 九块加起来多大（开机日志用）
func (l *Library) Bytes() int {
	if l == nil {
		return 0
	}
	n := 0
	for _, b := range l.blobs {
		n += len(b)
	}
	return n
}

// Source 索引文件从哪儿读的（日志用）
func (l *Library) Source() string {
	if l == nil {
		return "没装"
	}
	return l.dir
}

// Blob 按类型号取配置块。类型号不在 1..9 就返回 ok=false。
func (l *Library) Blob(typ int) ([]byte, bool) {
	if l == nil {
		return nil, false
	}
	b, ok := l.blobs[typ]
	return b, ok
}

// Version 按类型号取它对应的 sha1 版本号（日志用：发出去的是哪一版配置要说得清）。
// 类型号不认识就回空串，调用方自己决定怎么打。
func (l *Library) Version(typ int) string {
	if l == nil || typ < 1 || typ > len(l.versions) {
		return ""
	}
	return l.versions[typ-1]
}

// Reply 拼 1/4 的应答：01 | marker(-2) | call_id + 一个 bytes 字段（0x08 + u32 长度 + 配置块本体）。
// 这个形状是从 glance522 那套服务器模拟器学来的（它就是 0x08 + u32 + blob），
// 类型号不认识或者没装配置库就返回 ok=false，调用方回一份空参数表并写日志。
func (l *Library) Reply(callID uint32, typ int) ([]byte, bool) {
	blob, ok := l.Blob(typ)
	if !ok {
		return nil, false
	}
	body, err := wire.Bytes{Data: blob}.Encode()
	if err != nil {
		return nil, false
	}
	return append(wire.ResponseHead(callID), body...), true
}

// EmptyReply 类型号不认识时回的那一帧：空参数表"524"。
// 别回「没这条 RPC」—— 客户端那一步要的是「拿到配置」，给它错误回执它就崩（开机那次实机测过）。
func EmptyReply(callID uint32) []byte {
	body, err := wire.Args{Code: "524"}.Encode()
	if err != nil {
		body = []byte{0x03, 0x03, '5', '2', '4', 0x00, 0x00}
	}
	return append(wire.ResponseHead(callID), body...)
}

// RequestType 从 1/4 的请求里读「要第几块配置」（那个类型号 N，1..9）。
//
// 抓包里一条 1/4 都没有，所以请求形状没法预先验死。做法是不猜死偏移、也不认死参数表名：
// 从第 11 字节（11 字节请求壳之后）起，用协议字段表一路往下解，
// 解出来是参数表就钻进它的项，抓到第一个整数就用。
// 客户端常见写法是 args"524"{ 一个小整数 }，写成 u32/byte/short 都能捞到；
// 真的一条都解不出来才返回 ok=false，调用方兜底回空参数表并把 hex 打进日志校准。
func RequestType(t *wire.Table, plain []byte) (int, bool) {
	if t == nil || len(plain) <= argOff {
		return 0, false
	}
	for off := argOff; off < len(plain); {
		v, next, err := t.DecodeValue(plain, off)
		if err != nil {
			break // 解不动了就到此为止，别硬编偏移瞎猜
		}
		if n, ok := firstInt(v); ok {
			return n, true
		}
		if next <= off {
			break // 没往前走的解法不算进度，防死循环
		}
		off = next
	}
	return 0, false
}

// argOff 客户端请求里参数表的起点：11 字节壳（含 main/sub）之后的那个字节
const argOff = 11

// firstInt 深度优先找第一个整数：值本身就是整数就算它，是参数表就往下看它的项
func firstInt(v wire.Value) (int, bool) {
	if n, ok := asInt(v); ok {
		return n, true
	}
	for _, it := range wire.ArgsItems(v) {
		if n, ok := firstInt(it); ok {
			return n, true
		}
	}
	return 0, false
}

// asInt 客户端那个「类型号」可能按 BYTE/OPT 发、也可能按 U32 发，两种都认一下
func asInt(v wire.Value) (int, bool) {
	switch x := v.(type) {
	case wire.OptByte:
		return int(x.V), true
	case wire.Num32: // marker 0x0d/0x10/0x13 都归到这里，客户端发 byte 还是 u32 都认
		return int(x.V), true
	case wire.Num64:
		return int(x.V), true
	case wire.U16Field:
		return int(x.V), true
	}
	return 0, false
}
