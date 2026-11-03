package gamecfg

// 这些测试是「1/4 发出去的东西能不能信」的证据。三条硬线：
//   1) 九块配置都能读到，且每块现算的 sha1 就是 index.json（= 1/5 要回）的那个版本号；
//   2) 1/4 应答帧形 = 9 字节应答头 + 0x08 字节块 marker + u32 长度 + LZMA 原文，一块字节都不多；
//   3) 请求里那个「要第几块」能从几种常见写法里都捞出来（抓包里没有 1/4 的样本，只能合成着验）。
//
// 凭据：data/config_wire/index.json + type_1..9.bin（从开源实现 glance522 那套搬来的资产，
// 版本正是客户端在跑的 4.0.25034）。

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"rf4server/internal/assetpaths"
	"rf4server/internal/wire"
)

// loadTableForTest 读协议字段表（解请求正文要用它）
func loadTableForTest(t *testing.T) *wire.Table {
	t.Helper()
	p, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatalf("找不到凭据目录: %v", err)
	}
	tbl, err := wire.LoadTable(p)
	if err != nil {
		t.Fatalf("读协议字段表失败: %v", err)
	}
	return tbl
}

// readIndex 直接读那份 index.json，拿来和 Load() 装好的东西对账（不靠被测代码自己证明自己）
func readIndex(t *testing.T) indexFile {
	t.Helper()
	p, err := assetpaths.File("config_wire/index.json")
	if err != nil {
		t.Fatalf("找不到凭据目录: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 index.json 失败: %v", err)
	}
	var idx indexFile
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatalf("index.json 解析失败: %v", err)
	}
	return idx
}

// TestLoadMatchesIndex 装好的配置必须和 index.json 一一对上：
// 版本号是 40 位十六进制、块大小对得上、现算 sha1 就等于版本号。
// 最后这条是关键：它证明「1/5 报的版本」和「1/4 发的内容」是同一对，客户端不会说配置坏了。
func TestLoadMatchesIndex(t *testing.T) {
	lib, err := Load()
	if err != nil {
		t.Fatalf("读配置块失败: %v", err)
	}
	if lib.Count() != TypeCount {
		t.Fatalf("装了 %d 块，要 %d 块", lib.Count(), TypeCount)
	}
	idx := readIndex(t)
	if len(idx.Versions) != TypeCount {
		t.Fatalf("index.json 里版本号 %d 个，要 %d 个", len(idx.Versions), TypeCount)
	}
	for typ := 1; typ <= TypeCount; typ++ {
		blob, ok := lib.Blob(typ)
		if !ok {
			t.Fatalf("第 %d 块没装上", typ)
		}
		want := idx.Versions[typ-1]
		if lib.Version(typ) != want {
			t.Fatalf("第 %d 块的版本号是 %s，index.json 写的是 %s", typ, lib.Version(typ), want)
		}
		sum := sha1.Sum(blob)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Fatalf("第 %d 块现算 sha1 是 %s，和要回的版本号 %s 对不上（发出去客户端校验必挂）", typ, got, want)
		}
		if rec, ok := idx.Types[strconv.Itoa(typ)]; ok && rec.Bytes != len(blob) {
			t.Fatalf("第 %d 块 %d 字节，index.json 说应该 %d 字节", typ, len(blob), rec.Bytes)
		}
		if _, err := hex.DecodeString(want); err != nil || len(want) != VersionChars {
			t.Fatalf("第 %d 块的版本号 %q 不是 %d 位十六进制", typ, want, VersionChars)
		}
	}
	if lib.Bytes() <= 0 {
		t.Fatalf("九块加起来 %d 字节，不像话", lib.Bytes())
	}
	t.Logf("配置库自证通过：%d 块 共 %d 字节，sha1 全部对上版本号", lib.Count(), lib.Bytes())
}

// TestReplyShapeIsBytesOnly 1/4 应答 = 应答头 + 0x08 字节块（u32 长度 + LZMA 原文）。
// 这是从开源实现学来的形状：里面不能再裹参数表，长度字段必须就是块的实际长度。
func TestReplyShapeIsBytesOnly(t *testing.T) {
	lib, err := Load()
	if err != nil {
		t.Fatalf("读配置块失败: %v", err)
	}
	const cid = uint32(0x11223344)
	for typ := 1; typ <= TypeCount; typ++ {
		frame, ok := lib.Reply(cid, typ)
		if !ok {
			t.Fatalf("第 %d 块拼不出应答", typ)
		}
		if len(frame) < 9+5 {
			t.Fatalf("应答只有 %d 字节，连帧头加长度字段都不够", len(frame))
		}
		if frame[0] != 0x01 {
			t.Fatalf("应答帧第一字节应是 0x01，实际 0x%02x", frame[0])
		}
		if m := int32(binary.LittleEndian.Uint32(frame[1:5])); m != -2 {
			t.Fatalf("marker 应是应答的 -2，实际 %d", m)
		}
		if got := binary.LittleEndian.Uint32(frame[5:9]); got != cid {
			t.Fatalf("call_id 是 %d，要 %d（客户端按这个号对号）", got, cid)
		}
		body := frame[9:]
		if body[0] != 0x08 {
			t.Fatalf("帧体开头应是字节块 marker 0x08，实际 0x%02x", body[0])
		}
		n := binary.LittleEndian.Uint32(body[1:5])
		blob, _ := lib.Blob(typ)
		if int(n) != len(blob) {
			t.Fatalf("长度字段写 %d，配置块实际 %d 字节", n, len(blob))
		}
		if len(body) != 5+int(n) {
			t.Fatalf("帧体 %d 字节，按 %d 字节块应是 %d 字节（多出来的字节客户端会当残帧）", len(body), n, 5+int(n))
		}
		// LZMA1 的块头：属性字节 5d + 两个保留 00 + 字典大小
		if blob[0] != 0x5d {
			t.Fatalf("第 %d 块开头 0x%02x，不像 LZMA1（真服的配置块都以 5d 00 00 40 00 开头）", typ, blob[0])
		}
	}
}

// TestUnknownType 类型号越界：Reply 说不行，让调用方去回空参数表；别拿 nil 帧糊客户端。
func TestUnknownType(t *testing.T) {
	lib, err := Load()
	if err != nil {
		t.Fatalf("读配置块失败: %v", err)
	}
	for _, typ := range []int{0, -1, TypeCount + 1, 999} {
		if frame, ok := lib.Reply(1, typ); ok {
			t.Fatalf("类型 %d 不该有配置，结果回了 %d 字节", typ, len(frame))
		}
		if v := lib.Version(typ); v != "" {
			t.Fatalf("类型 %d 不该有版本号，结果给了 %q", typ, v)
		}
	}
	// 空参数表那帧必须是合法的 args"524"：客户端解得开，只是拿不到配置
	empty := EmptyReply(7)
	want := append(wire.ResponseHead(7), 0x03, 0x03, '5', '2', '4', 0x00, 0x00)
	if len(empty) != len(want) {
		t.Fatalf("空应答 %d 字节，应是 %d 字节", len(empty), len(want))
	}
	for i := range want {
		if empty[i] != want[i] {
			t.Fatalf("空应答第 %d 字节 0x%02x，应是 0x%02x", i, empty[i], want[i])
		}
	}
}

// TestNilLibrarySafe 配置库没装（Load 失败）时，所有方法都要安静地给「没有」，不能 panic。
func TestNilLibrarySafe(t *testing.T) {
	var lib *Library
	if lib.Count() != 0 || lib.Bytes() != 0 || lib.Versions() != nil {
		t.Fatal("空库的计数没归零")
	}
	if lib.Source() != "没装" {
		t.Fatalf("空库的来源应说「没装」，实际 %q", lib.Source())
	}
	if _, ok := lib.Blob(1); ok {
		t.Fatal("空库不该有配置块")
	}
	if _, ok := lib.Reply(1, 1); ok {
		t.Fatal("空库拼不出应答才对")
	}
	if lib.Version(3) != "" {
		t.Fatal("空库没有版本号")
	}
}

// reqFrame 按客户端请求的真实帧形拼一条 1/4 请求：11 字节壳 + 正文 + 尾巴 00
func reqFrame(t *testing.T, body wire.Value) []byte {
	t.Helper()
	raw, err := body.Encode()
	if err != nil {
		t.Fatalf("拼请求失败: %v", err)
	}
	return append(append(wire.PushHead(5, 1, 4), raw...), wire.RequestTrailer)
}

// TestRequestTypeReadsTypeNumber 请求里那个「要第几块」有几种可能写法，都得捞得到。
// 抓包里一条 1/4 都没有，所以这里按客户端惯用写法合成：参数表裹整数、裸整数、byte 形态的整数。
func TestRequestTypeReadsTypeNumber(t *testing.T) {
	tbl := loadTableForTest(t)
	cases := []struct {
		name string
		body wire.Value
		want int
		ok   bool
	}{
		{"args 524 裹 u32", wire.Args{Code: "524", Items: []wire.Value{wire.U32(3)}}, 3, true},
		{"args 524 裹 byte 形态", wire.Args{Code: "524", Items: []wire.Value{wire.Num32{Marker: 0x0d, V: 9}}}, 9, true},
		{"args 名字未知也认", wire.Args{Code: "531", Items: []wire.Value{wire.U32(7)}}, 7, true},
		{"没有参数表，正文就是个整数", wire.U32(2), 2, true},
		{"空参数表（客户端没给类型）", wire.Args{Code: "524"}, 0, false},
	}
	for _, c := range cases {
		got, ok := RequestType(tbl, reqFrame(t, c.body))
		if ok != c.ok {
			t.Fatalf("%s: ok 是 %v，要 %v", c.name, ok, c.ok)
		}
		if c.ok && got != c.want {
			t.Fatalf("%s: 读出类型 %d，要 %d", c.name, got, c.want)
		}
	}
	// 帧太短 / 没字段表：都不能 panic，直接说读不出来
	if _, ok := RequestType(tbl, []byte{0x01, 0xff, 0xff, 0xff, 0xff}); ok {
		t.Fatal("四个字节不该读出类型号")
	}
	if _, ok := RequestType(nil, reqFrame(t, wire.Args{Code: "524", Items: []wire.Value{wire.U32(1)}})); ok {
		t.Fatal("没给字段表就该说读不出来")
	}
}

// TestVersionsOrderIsTypeOrder 1/5 那 9 段的顺序就是类型 1..9，客户端按下标读，顺序错一位就全错。
func TestVersionsOrderIsTypeOrder(t *testing.T) {
	lib, err := Load()
	if err != nil {
		t.Fatalf("读配置块失败: %v", err)
	}
	idx := readIndex(t)
	vs := lib.Versions()
	if len(vs) != len(idx.Versions) {
		t.Fatalf("给了 %d 个版本号，index.json 有 %d 个", len(vs), len(idx.Versions))
	}
	for i := range vs {
		if vs[i] != idx.Versions[i] {
			t.Fatalf("第 %d 段（类型 %d）版本号不一样：给的是 %s，应是 %s", i, i+1, vs[i], idx.Versions[i])
		}
	}
	// 返回的必须是副本：调用方改了不能把库里的版本带跑
	vs[0] = "deadbeef"
	if lib.Version(1) == "deadbeef" {
		t.Fatal("Versions() 给的是内部切片，外面一改就把库改了")
	}
}
