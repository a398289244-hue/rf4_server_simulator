package wire

import "encoding/binary"

// 帧这一层的字节。整条 TCP 流外面还有一层「4 字节长度 + 2 字节序号 + RC4」的壳，
// 那层等接 socket 时再搬；这里只管壳里面那段 payload。

// Marker 三种值，写在 payload 第 1~4 字节（小端有符号）：
const (
	MarkerPush     int32 = -1 // 服务端主动推（客户端请求也是这个值，方向靠连接分）
	MarkerResponse int32 = -2 // 对某个 call_id 的应答
	MarkerAck      int32 = -3 // 确认（录制里 main=32 sub=0 那条见过）
)

// PushTrailer 真服每条推送 payload 末尾都带 1 个 0x01。
// 凭据：录制里 0/1、2/1、2/4、4/21、4/22、14/14 六条推送最后一个字节全是 01；
// 而客户端的 5/1 请求最后一个字节是 00。这个字节自己拼帧时不能漏，漏了客户端解不动。
const PushTrailer byte = 0x01

// RequestTrailer 客户端请求 payload 末尾那个字节，收请求时要按这个跳掉。
const RequestTrailer byte = 0x00

// ResponseHead 应答帧头，9 字节：01 | marker(-2, i32) | call_id(u32)。
// 应答不写 main/sub，客户端拿 call_id 去对号（真服那条 311 应答就是这么回的）。
func ResponseHead(callID uint32) []byte {
	b := make([]byte, 9)
	b[0] = 0x01
	m := MarkerResponse // 先落到变量再转：常量直接转 uint32 编译器会说负数装不下
	binary.LittleEndian.PutUint32(b[1:], uint32(m))
	binary.LittleEndian.PutUint32(b[5:], callID)
	return b
}

// PushHead 推送帧头，11 字节：01 | marker(-1, i32) | 序号(u32) | main | sub。
// 序号这条流上是递增的，但真服推送这条位置常年写 1（录制里 2/1、2/4、14/14 都是 1），
// 我们照录制写 1，别自作聪明递增，客户端对不上会丢帧。
func PushHead(seq uint32, main, sub byte) []byte {
	b := make([]byte, 11)
	b[0] = 0x01
	m := MarkerPush // 同上，负数先落到变量
	binary.LittleEndian.PutUint32(b[1:], uint32(m))
	binary.LittleEndian.PutUint32(b[5:], seq)
	b[9] = main
	b[10] = sub
	return b
}

// WrapArgs524 把已经编好的对象字节列表包成真服推送那个外壳：
// 03 | 短串"524" | 条数(u16) | 各项 | 尾巴 01
// 技能树推送（2/4）、档案推送（2/1）外面都是这层，客户端先看编码名"524"再解内容。
func WrapArgs524(items [][]byte) []byte {
	a := Args{Code: "524"}
	for _, it := range items {
		// 这里的 item 已经是编好的对象字节，直接当裸字节塞，不再走 marker 编码
		a.Items = append(a.Items, Raw{Data: it})
	}
	out, err := a.Encode()
	if err != nil {
		// 只可能是条数溢出（u16），推送最多一个对象，正常不会走到
		out = nil
	}
	return append(out, PushTrailer)
}

// Raw 一块已经编好的字节，塞进参数表时用；它自己不带 marker（物件记录自带 01+typeID）。
// 背包清单（4/5）、800 快照（4/7、17/2）、货架（22/1）都要把「已经算好的记录字节」原样排进
// 参数表，所以这块导出给上层用 —— 别再各拼各的，拼出来的顺序差一字节客户端就解歪。
type Raw struct{ Data []byte }

// Encode 原样返回
func (r Raw) Encode() ([]byte, error) { return r.Data, nil }

// RawItems 把一串已经编好的字节变成参数表能收的项
func RawItems(list [][]byte) []Value {
	out := make([]Value, 0, len(list))
	for _, b := range list {
		out = append(out, Raw{Data: b})
	}
	return out
}
