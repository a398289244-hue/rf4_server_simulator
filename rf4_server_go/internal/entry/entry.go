// Package entry 拼的是「进场链路上那些小帧」：客户端一连上就连着问的
// 版本、服务器时间、频道信息、空清单……单个都很小，但少回一条客户端就可能卡在那儿转圈。
//
// 铁律：这里每一帧都是现算出来的 —— 数据来自账号、系统时间或下面的常量，
// 运行期一帧录制都不发。录制只在两个地方当参照：_test.go 里逐字节对答案、
// cmd/rf4dump 里当解剖样本。
//
// 每个构造器旁边都写了凭据（哪条命令、真服那帧多少字节、字段是什么含义），
// 想改先回去看凭据，别凭感觉动。
package entry

import (
	"encoding/binary"
	"time"

	"rf4server/internal/wire"
)

// HeadLen 应答帧头长度：01 | marker(-2, i32) | call_id(u32)
const HeadLen = 9

// ticks1601to1970 换成 wire 层那个同名常量的别名：换算只有一份，测试里也照旧能用。
const ticks1601to1970 = wire.Ticks1601To1970

// Frame 给应答体挂上应答头（marker=-2，带 call_id）。
// body 是已经编好的字节：对象、参数表、单个标量都行。
func Frame(callID uint32, body []byte) []byte {
	return append(wire.ResponseHead(callID), body...)
}

// LocalServerPrefix 真服 1/1 的应答就是 "LocalServer/<客户端自己报的版本号>"。
// 凭据：1/1 应答 32 字节 = 9 帧头 + 0x14 短字符串(21) "LocalServer/4.0.25034"。
const LocalServerPrefix = "LocalServer/"

// ServerNameReply 1/1：客户端报版本号，我们回一个「我是谁/版本」。
// 版本号是从请求里读出来的，不写死 —— 客户端升级这条就不用改。
func ServerNameReply(callID uint32, version string) []byte {
	body, err := wire.Str{Marker: 0x14, S: LocalServerPrefix + version}.Encode()
	if err != nil {
		// 只有版本串长过 254 字节才会走到这儿，实际不可能；真走到了就回空串，
		// 让客户端按「服务端没报名字」处理，总比崩在这儿强。
		body = []byte{0x14, 0xff}
	}
	return Frame(callID, body)
}

// ClientVersion 从客户端 1/1 的请求里把它报的版本号读出来。
// 帧形：01 | marker(-1) | call_id | main | sub | 参数表"524"（里面第一项就是版本串）| 00。
// 读不到就返回空串（调用方自己决定回什么）。
func ClientVersion(t *wire.Table, plain []byte) string {
	if t == nil || len(plain) <= HeadLen+2 {
		return ""
	}
	v, _, err := t.DecodeValue(plain, HeadLen+2) // 跳过 main/sub，从参数表起步
	if err != nil {
		return ""
	}
	args, ok := v.(wire.Args)
	if !ok {
		return ""
	}
	for _, it := range args.Items {
		switch x := it.(type) {
		case wire.Str:
			if x.S != "" {
				return x.S
			}
		case wire.StrField:
			if !x.IsNull && x.S != "" {
				return x.S
			}
		case wire.Str16Field:
			if !x.IsNull && x.S != "" {
				return x.S
			}
		}
	}
	return ""
}

// Filetime 把 Go 的时间换算成 .NET FILETIME（1601 年起算的 100 纳秒间隔）。
// 真正的换算在 internal/wire/time.go，这边只是转一手：进场帧和玩家档案帧共用一套编码。
func Filetime(now time.Time) uint64 { return wire.Filetime(now) }

// ServerTimeReply 1/2：真服回的是一个 u64 的 FILETIME，也就是服务器当前时间。
// 凭据：1/2 应答 18 字节 = 9 帧头 + 0x11 + u64。
// 这条要是回错了，客户端的计时、超时、成就时间戳全会歪。
func ServerTimeReply(callID uint32, now time.Time) []byte {
	body, err := wire.U64(Filetime(now)).Encode()
	if err != nil {
		body = []byte{0x11, 0, 0, 0, 0, 0, 0, 0, 0}
	}
	return Frame(callID, body)
}

// EmptyArgsReply 回一个「参数表里啥也没有」的应答。
// 凭据（全是 9 帧头 + 参数表本体）：
//
//	9/5   -> code "97"  共 15 字节
//	23/1  -> code "147" 共 16 字节
//	24/12 -> code "524" 共 16 字节
//
// 这几条真服当时就是空清单：本地服没有公会战队、没有活动页，照实回空最诚实。
func EmptyArgsReply(callID uint32, code string) []byte {
	body, err := wire.Args{Code: code}.Encode()
	if err != nil {
		body = []byte{0x03, 0xff, 0x00, 0x00}
	}
	return Frame(callID, body)
}

// EmptyGUIDsReply 19/6：真服回的是「空 GUID 数组」，共 12 字节（9 帧头 + 06 + 数量 u16=0）。
// 这条是「别人名下的物品」一类的清单，本地服现在就主人一个，空数组是对的。
func EmptyGUIDsReply(callID uint32) []byte {
	body, err := wire.GUIDArray{}.Encode()
	if err != nil {
		body = []byte{0x06, 0x00, 0x00}
	}
	return Frame(callID, body)
}

// TypeStorageInfo 4/1 的应答对象类型号：133。
// 三个 u32：仓库/占用数量、仓库格位（观察 0/30/100）、角色格位（观察 0/45/150）。
const TypeStorageInfo = 133

// StorageInfoReply 4/1：仓库/格位信息。现在三个数都回 0（= 没有扩展仓库、没占格）。
// 凭据：4/1 应答 26 字节 = 9 帧头 + 01 | 133(u32) | 00000000 00000000 00000000。
// 以后要把格位数按 MySQL 里仓库表统计出来，改这一个函数就够了。
func StorageInfoReply(t *wire.Table, callID uint32) ([]byte, error) {
	return StorageInfoReplyWithCapacity(t, callID, 0, 0, 0)
}

// StorageInfoReplyWithCapacity 回 4/1 items/info 的三项容量状态。
// #00 是当前占用，#01 是条数上限（普通账号 100），#02 是扩展/重量档（对应 150）。
// 旧抓包恰好三项全 0，但实机证明 0 上限会让 HUD 退回本地缓存计数，出现“鱼护空却显示 17/0”。
func StorageInfoReplyWithCapacity(t *wire.Table, callID uint32, occupied, capacity, extended uint32) ([]byte, error) {
	o, err := makeObjByType(t, TypeStorageInfo)
	if err != nil {
		return nil, err
	}
	if err := setNum32(o, 0, occupied); err != nil {
		return nil, err
	}
	if err := setNum32(o, 1, capacity); err != nil {
		return nil, err
	}
	if err := setNum32(o, 2, extended); err != nil {
		return nil, err
	}
	b, err := o.Encode()
	if err != nil {
		return nil, err
	}
	return Frame(callID, b), nil
}

// TypeChannelInfo 24/3 的应答对象类型号：402（服务器/频道信息）
const TypeChannelInfo = 402

// LocalChannelID 本地频道的稳定 id。真服那帧这位就是 "rf4-local-global"，照用。
const LocalChannelID = "rf4-local-global"

// LocalChannelName 本地频道的显示名。真服那帧里这位是录制当时那位老号自己起的名字
// （「大哥带带我」），本地服没必要照抄，写成一眼能看出是谁家的。
const LocalChannelName = "少爷的本地服"

// LocalChannelRegion 大区短码。真服那帧是 "GL"（全球），观察到的另一种是 "CN"。
const LocalChannelRegion = "GL"

// ServerInfoReply 24/3：本地频道信息。字段照真服那帧（64 字节）一个个对上：
//
//	#00 STR  id       = "rf4-local-global"
//	#01 STR  name     = 本地频道名（这条是唯一自定的，见 LocalChannelName）
//	#02 STR  region   = "GL"
//	#03 U32  频道序号尾码 = 0xffffffff
//	#04 BYTE 频道可见性   = 1（Private，private 才对得上本地私服）
//	#05 OPT  观察值 0/1   = 0
//	#06 VAL  成员 uid 列表 = 空 u32 数组（字段说明里特别标了「不能为 null」）
//	#07 U32  频道子 id    = 0（观察 >512 才会集群）
//	#08 OPT  bool         = 0
func ServerInfoReply(t *wire.Table, callID uint32) ([]byte, error) {
	return serverInfoReplyWithName(t, callID, LocalChannelName)
}

// serverInfoReplyWithName 把频道名单拎出来，测试时塞真服那个名字进去就能逐字节对答案。
func serverInfoReplyWithName(t *wire.Table, callID uint32, name string) ([]byte, error) {
	o, err := makeObjByType(t, TypeChannelInfo)
	if err != nil {
		return nil, err
	}
	if err := setStr(o, 0, LocalChannelID); err != nil {
		return nil, err
	}
	if err := setStr(o, 1, name); err != nil {
		return nil, err
	}
	if err := setStr(o, 2, LocalChannelRegion); err != nil {
		return nil, err
	}
	if err := setNum32(o, 3, 0xffffffff); err != nil {
		return nil, err
	}
	if err := setByte(o, 4, 1); err != nil { // 1 = Private
		return nil, err
	}
	if err := setByte(o, 5, 0); err != nil {
		return nil, err
	}
	if err := setVal(o, 6, wire.U32Array{}); err != nil { // 空数组，但不能是 null
		return nil, err
	}
	if err := setNum32(o, 7, 0); err != nil {
		return nil, err
	}
	if err := setByte(o, 8, 0); err != nil {
		return nil, err
	}
	b, err := o.Encode()
	if err != nil {
		return nil, err
	}
	return Frame(callID, b), nil
}

// TypeTeamPanel 28/2 的应答对象类型号：5003（战队/工会 + 编队 + 占位数据）
const (
	TypeTeamPanel = 5003
	// TypeFlagEnum 5003 第一个字段套的那层枚举对象
	TypeFlagEnum = 5012
)

// ChannelListReply 28/2：战队/工会面板。本地服没有战队，照真服那样回「三层空表」：
//
//	5003#00 = 5012{ #00 = 参数表"210"（空） }
//	5003#01 = 参数表"202"（空）  —— 战队列表
//	5003#02 = 参数表"201"（空）  —— 编队/占位数据
//
// 凭据：28/2 应答 40 字节，逐字节和这个结构一模一样。
func ChannelListReply(t *wire.Table, callID uint32) ([]byte, error) {
	flag, err := makeObjByType(t, TypeFlagEnum)
	if err != nil {
		return nil, err
	}
	if err := setVal(flag, 0, wire.Args{Code: "210"}); err != nil {
		return nil, err
	}
	panel, err := makeObjByType(t, TypeTeamPanel)
	if err != nil {
		return nil, err
	}
	if err := setVal(panel, 0, flag); err != nil {
		return nil, err
	}
	if err := setVal(panel, 1, wire.Args{Code: "202"}); err != nil {
		return nil, err
	}
	if err := setVal(panel, 2, wire.Args{Code: "201"}); err != nil {
		return nil, err
	}
	b, err := panel.Encode()
	if err != nil {
		return nil, err
	}
	return Frame(callID, b), nil
}

// unimplementedClass 真服回「这条 RPC 没实现」时挂的类名（latin1 原文，别翻译）。
// 凭据（2026-11-02 本地实测那帧，71 字节）：
//
//	01 fdffffff <cid u32> | 20000000 "LocalServer.UnsupportedOperation" | 16000000 "Unimplemented RPC 3:18"
const unimplementedClass = "LocalServer.UnsupportedOperation"

// UnimplementedReply 给一条还没实现的命令回一帧「没这条 RPC」（marker=-3）。
// 为什么不能干脆不回话：客户端会一直等这条 RPC 的回执，等到自己超时才断开
// （Node 版实测 3/18 能等 120 秒）。回一帧未实现，它立刻就知道不用等了。
func UnimplementedReply(callID uint32, main, sub int) []byte {
	msg := "Unimplemented RPC " + itoa(main) + ":" + itoa(sub)
	out := make([]byte, HeadLen+4+len(unimplementedClass)+4+len(msg))
	out[0] = 0x01
	// marker 是负数，必须先落到 int32 变量再转 uint32：
	// Go 不允许把负的常量直接转成无符号（会报 constant -3 overflows uint32），
	// 变量转换才会按补码搬位。背包那帧拼帧头也是同一个写法，别嫌啰嗦。
	var errMarker int32 = wire.MarkerAck // -3：错误/回执那一族
	binary.LittleEndian.PutUint32(out[1:], uint32(errMarker))
	binary.LittleEndian.PutUint32(out[5:], callID)
	o := HeadLen
	binary.LittleEndian.PutUint32(out[o:], uint32(len(unimplementedClass)))
	o += 4
	copy(out[o:], unimplementedClass)
	o += len(unimplementedClass)
	binary.LittleEndian.PutUint32(out[o:], uint32(len(msg)))
	o += 4
	copy(out[o:], msg)
	return out
}

// NullReply 回一帧「值是 null」的应答：帧体就一个 0x00，整帧 10 字节。
// 用在「服务端确实没有这个内容」的命令上（开源实现同一做法：comp/load 无竞赛 -> null）。
// 重点是「得回一帧」：客户端在等回执，不给它就干等 120 秒超时（6/2 实机就是这么死的）。
func NullReply(callID uint32) []byte {
	b, err := wire.Null{}.Encode()
	if err != nil {
		b = []byte{0x00} // Null 的编码不可能失败，兜底也拼得出同样的字节
	}
	return append(wire.ResponseHead(callID), b...)
}

// BoolReply 回一帧布尔（true=0x0e / false=0x0f，各一个字节）。
// 1/18 设备指纹上报要的就是一个 bool：开源实现回 true，我们跟着回 true。
func BoolReply(callID uint32, v bool) []byte {
	b, err := wire.Bool{V: v}.Encode()
	if err != nil {
		b = []byte{0x0f}
	}
	return append(wire.ResponseHead(callID), b...)
}

// makeObjByType 按类型号从字段表里造一个「骨架对象」（每个字段先填默认零值）。
// 用类型号而不是那串乱码类名，是为了以后字段表重新生成也不会一下全断。
func makeObjByType(t *wire.Table, typeID int) (*wire.Obj, error) {
	if t == nil {
		return nil, errNoTable
	}
	_, class, ok := t.ClassByType(typeID)
	if !ok {
		return nil, errNoClass
	}
	o, err := wire.MakeObj(t, class)
	if err != nil {
		return nil, err
	}
	return o, nil
}

// setNum32 按序号写一个 u32 字段。序号写错、字段类型不对都会报错，不会静默写歪。
func setNum32(o *wire.Obj, idx int, v uint32) error {
	f, ok := fieldAt(o, idx, "U32")
	if !ok {
		return badField(o, idx, "U32")
	}
	f.Value = wire.U32(v)
	return nil
}

// setByte 按序号写一个单字节字段（op=BYTE 或 OPT）
func setByte(o *wire.Obj, idx int, v uint8) error {
	f, ok := fieldAt(o, idx, "BYTE", "OPT")
	if !ok {
		return badField(o, idx, "BYTE/OPT")
	}
	f.Value = wire.OptByte{V: v}
	return nil
}

// setStr 按序号写一个短字符串字段（op=STR）
func setStr(o *wire.Obj, idx int, s string) error {
	f, ok := fieldAt(o, idx, "STR")
	if !ok {
		return badField(o, idx, "STR")
	}
	f.Value = wire.StrField{S: s}
	return nil
}

// setVal 按序号写一个「值」字段（op=VAL/ARGLIST/ARRAY/U32ARR 这些）
func setVal(o *wire.Obj, idx int, v wire.Value) error {
	f, ok := fieldAt(o, idx, "VAL", "ARGLIST", "ARRAY", "U32ARR", "DICT", "STR3", "KV")
	if !ok {
		return badField(o, idx, "VAL 家族")
	}
	f.Value = v
	return nil
}

// fieldAt 找第 idx 个字段，并确认它的写入法在 want 里
func fieldAt(o *wire.Obj, idx int, want ...string) (*wire.Field, bool) {
	if idx < 0 || idx >= len(o.Fields) {
		return nil, false
	}
	f := &o.Fields[idx]
	for _, w := range want {
		if f.Op == w {
			return f, true
		}
	}
	return f, false
}
