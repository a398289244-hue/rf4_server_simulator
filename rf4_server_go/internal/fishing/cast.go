package fishing

import (
	"encoding/hex"

	"rf4server/internal/wire"
)

// 抛竿这一串命令的请求怎么读、应答怎么回。
//
// 请求形状（凭据：真服那次会话的原始帧，D:\rf4log\req_fishing.txt）：
//
//	14/1 抛竿准备 = 参数表"524"{ 钓具GUID }                       帧长 36B
//	14/2 抛出去   = 参数表"524"{ 钓具GUID, 落点坐标… }             帧长 106~113B
//	14/5 留鱼     = 参数表"524"{ 钓具GUID, 鱼GUID }                帧长 53B
//	14/4 / 14/8 / 14/11 = 都是"钓具GUID + 一堆坐标/姿态浮点"，
//	                     其中 14/8、14/11 在 oneway 名单里（客户端不等回话）
//
// 应答（凭据：cmd/rf4dump -cmd 14/1 那份解剖）：
//
//	14/1 -> obj158 { 钓具GUID, 允许抛距(float), 参数(float), 关联GUID(空) }  54B
//	14/2 -> 空 obj800                                                       29B

// gearOff / fishOff 请求里那两个 GUID 的字节位置。
// 不是猜的：14/1 那帧 36 字节，GUID 从第 19 字节开始（11 字节推送式壳 + 参数表名 + 条数 + GUID 标记）。
// 14/5 多一项，鱼 GUID 紧跟在钓具 GUID 后面：第 35 字节是第二项的标记(0x0c)，GUID 从第 36 开始。
const (
	gearOff = 19
	fishOff = 36
)

// GearGUID 从 14/1 / 14/2 / 14/4 的请求里取钓具 GUID（32 位十六进制，小写）。
// 帧不够长就返回 ok=false，调用方照样回包，只是日志里会说一声「GUID 没读着」。
func GearGUID(plain []byte) (string, bool) {
	if len(plain) < gearOff+16 {
		return "", false
	}
	return hex.EncodeToString(plain[gearOff : gearOff+16]), true
}

// KeepGUIDs 从 14/5（留鱼）请求里取「钓具 GUID + 鱼 GUID」。
// 位置算死的（见上面那条 53B 凭据），不合法就返回 ok=false，让调用方回一帧空快照而不是猜。
func KeepGUIDs(plain []byte) (gear, fish string, ok bool) {
	if len(plain) < fishOff+16 {
		return "", "", false
	}
	// 第 16 字节是参数表条数：留鱼这条真服固定 2 项，不是 2 就别按这个布局读
	if len(plain) > 16 && plain[16] != 2 {
		return "", "", false
	}
	gear = hex.EncodeToString(plain[gearOff : gearOff+16])
	fish = hex.EncodeToString(plain[fishOff : fishOff+16])
	if !isHexStr(gear) || !isHexStr(fish) {
		return "", "", false
	}
	return gear, fish, true
}

// FishStatusCode 读取 14/12 的鱼实体状态（obj24#00）。这条请求的正文结构会随
// 客户端版本变化，所以从参数树里取第一个 u32，不再写死字节偏移。
func FishStatusCode(t *wire.Table, plain []byte) (uint32, bool) {
	if t == nil || len(plain) <= 11 {
		return 0, false
	}
	v, _, err := t.DecodeValue(plain, 11)
	if err != nil {
		return 0, false
	}
	var out uint32
	found := false
	wire.Walk(v, func(it wire.WalkItem) {
		if !found && it.Kind == "u32" {
			out, found = uint32(it.Num), true
		}
	})
	return out, found
}

// isHexStr 整串是不是十六进制字符（GUID 读歪了通常是因为帧短了，这时别硬拼）
func isHexStr(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// CastReply 回 14/1：obj158。
//
//	#00 钓具 GUID（客户端给什么回什么）  #01 允许抛距 = 10.0
//	#02 参数 = 5.0                       #03 线末端鱼钩 GUID（没有时全零）
//
// 凭据：真服那条 54B（cmd/rf4dump -cmd 14/1），后两个 float 就是 10.0 和 5.0。
// 旧抓包样本 #03 是全零，所以 CastReply 保留这个兼容入口；实机走 CastReplyWithHook，
// 从当前账号 objects.elem 读取真实鱼钩 GUID。
func CastReply(t *wire.Table, callID uint32, gearGUID string) ([]byte, error) {
	return CastReplyWithHook(t, callID, gearGUID, "")
}

// CastReplyWithHook 和 CastReply 相同，但 #03 带当前线末端鱼钩实例 GUID。
// 最新协议实现和客户端行为都表明：有钩时必须回真实 GUID，客户端后面会用它校验 14/14#23。
func CastReplyWithHook(t *wire.Table, callID uint32, gearGUID, hookGUID string) ([]byte, error) {
	o, err := t.NewObj(158)
	if err != nil {
		return nil, err
	}
	if err := o.SetGUIDAt(0, gearGUID); err != nil {
		return nil, err
	}
	if err := o.SetU32At(1, wire.F32Bits(10)); err != nil { // 允许抛距（米）
		return nil, err
	}
	if err := o.SetU32At(2, wire.F32Bits(5)); err != nil { // 还没查实，真服这条是 5.0
		return nil, err
	}
	if err := o.SetGUIDAt(3, hookGUID); err != nil {
		return nil, err
	}
	body, err := o.Encode()
	if err != nil {
		return nil, err
	}
	return append(wire.ResponseHead(callID), body...), nil
}
