package wire

import (
	"encoding/binary"
	"errors"
	"io"
)

// 传输层：TCP 上的一条条帧 + RC4。
// 帧格式（2026-10-31 抓包实测，1012/1012 条 payload 都对得上）：
//
//	u32 长度 | u8 类型 | u64 线序号(wid) | payload(走 RC4，帧头是明文)
//	长度 = 9 + len(payload)；类型 0=数据帧、1=传输 ack（payload 为空）；长度=0（4 字节全零）是心跳。
//
// 加密：只有 payload 过 RC4，钥匙就是登录时下发的 32 位 ASCII token 本身（不是 hex 解码）。
// 收发两条独立密钥流，各自从本方向第一个被加密的 payload 开始连续走。

// RC4 一条方向的 RC4 密钥流（状态必须连续，不能每帧重置）
type RC4 struct {
	s [256]byte // S 盒，先排成 0..255
	i byte      // 密钥流下标
	j byte      // 交换下标
}

// NewRC4 用 key（这里是 token 的 ASCII 字节）初始化一条密钥流
func NewRC4(key []byte) *RC4 {
	r := &RC4{}
	for i := range r.s {
		r.s[i] = byte(i)
	}
	var j byte
	for i := 0; i < 256; i++ {
		j = j + r.s[i] + key[i%len(key)]
		r.s[i], r.s[j] = r.s[j], r.s[i]
	}
	return r
}

// Crypt 就地加/解密（RC4 是对称的，加解密同一个函数）。状态保留，下一帧接着走。
func (r *RC4) Crypt(b []byte) []byte {
	out := make([]byte, len(b))
	for k := range b {
		r.i++
		r.j += r.s[r.i]
		r.s[r.i], r.s[r.j] = r.s[r.j], r.s[r.i]
		out[k] = b[k] ^ r.s[r.s[r.i]+r.s[r.j]]
	}
	return out
}

// ErrFrameTooBig 长度字段不合理，多半是流错位了
var ErrFrameTooBig = errors.New("wire: 帧长度不对劲，字节流错位")

// MaxFrameBody 单帧 payload 上限：8MB，超过就认定错位，别再往下读
const MaxFrameBody = 8 * 1024 * 1024

// ZeroFrame 心跳帧：4 个 0（长度=0）。真服大约每 3.4 秒发一条，客户端靠它判断连接还活着。
var ZeroFrame = []byte{0, 0, 0, 0}

// BuildFrame 组一帧：u32(9+len(payload)) | u8 类型 | u64 wid | payload。
// payload 传进来时必须已经 RC4 加过密（帧头保持明文是这套协议的关键）。
func BuildFrame(typ byte, wid uint64, payload []byte) []byte {
	b := make([]byte, 13+len(payload))
	binary.LittleEndian.PutUint32(b[0:], uint32(9+len(payload)))
	b[4] = typ
	binary.LittleEndian.PutUint64(b[5:], wid)
	copy(b[13:], payload)
	return b
}

// AckFrame 传输层确认帧：类型 1、payload 空、wid 回应对端的 wid。
// 规矩：收到每一条数据帧都要回一条 ack，不然客户端会重发甚至断开。
func AckFrame(wid uint64) []byte { return BuildFrame(1, wid, nil) }

// Frame 一次读到的完整帧
type Frame struct {
	Type    byte   // 0=数据 1=传输 ack
	Wid     uint64 // 线序号（对端的计数器，回 ack 时要用）
	Payload []byte // 密文（还没 RC4 解）；心跳/ack 时为空
	Zero    bool   // true = 4 字节全零的心跳帧
}

// FrameReader 从连接里增量切帧。内部自己缓冲，调用方只管 Next()。
type FrameReader struct {
	rd  io.Reader // 底层连接
	buf []byte    // 还没切完的字节
}

// NewFrameReader 建一个分帧器
func NewFrameReader(rd io.Reader) *FrameReader {
	return &FrameReader{rd: rd, buf: make([]byte, 0, 16*1024)}
}

// Next 读一帧。连接正常关闭返回 io.EOF；字节流错位返回 ErrFrameTooBig。
func (fr *FrameReader) Next() (Frame, error) {
	for {
		// 先够 4 字节看长度
		for len(fr.buf) < 4 {
			if err := fr.fill(); err != nil {
				return Frame{}, err
			}
		}
		bl := binary.LittleEndian.Uint32(fr.buf[0:])
		if bl == 0 { // 心跳
			fr.buf = fr.buf[4:]
			return Frame{Zero: true}, nil
		}
		if bl == 1 { // 录制里见过 5 字节的极短帧：长度 1 + 1 字节类型
			for len(fr.buf) < 5 {
				if err := fr.fill(); err != nil {
					return Frame{}, err
				}
			}
			f := Frame{Type: fr.buf[4]}
			fr.buf = fr.buf[5:]
			return f, nil
		}
		if bl < 9 || int(bl) > MaxFrameBody {
			return Frame{}, ErrFrameTooBig
		}
		// 等整帧到齐
		for len(fr.buf) < 4+int(bl) {
			if err := fr.fill(); err != nil {
				return Frame{}, err
			}
		}
		body := fr.buf[4 : 4+int(bl)]
		f := Frame{Type: body[0], Wid: binary.LittleEndian.Uint64(body[1:9])}
		if len(body) > 9 {
			f.Payload = append([]byte(nil), body[9:]...) // 拷贝走，fr.buf 后面会被覆盖
		}
		fr.buf = fr.buf[4+int(bl):]
		return f, nil
	}
}

// fill 从连接再读一截进缓冲
func (fr *FrameReader) fill() error {
	tmp := make([]byte, 64*1024)
	n, err := fr.rd.Read(tmp)
	if n > 0 {
		fr.buf = append(fr.buf, tmp[:n]...)
		return nil
	}
	if err != nil {
		return err
	}
	return io.ErrNoProgress
}

// Envelope 帧里 payload 的信封：u8 0x01 | i32 marker | u32 call_id |（marker=-1 才有）u8 main, u8 sub
type Envelope struct {
	Marker int32  // -1=请求或推送，-2=应答
	CallID uint32 // 调用号：应答必须原样带回
	Main   int    // 主命令号；marker=-2 时为 -1（没有这一栏）
	Sub    int    // 副命令号；同上
	ArgsAt int    // 参数从第几个字节开始（请求 11，应答 9）
}

// ReadEnvelope 解信封。首字节不是 0x01 或长度不够就返回 false。
func ReadEnvelope(p []byte) (Envelope, bool) {
	if len(p) < 9 || p[0] != 0x01 {
		return Envelope{}, false
	}
	marker := int32(binary.LittleEndian.Uint32(p[1:]))
	cid := binary.LittleEndian.Uint32(p[5:])
	if marker == -1 && len(p) > 10 {
		return Envelope{Marker: marker, CallID: cid, Main: int(p[9]), Sub: int(p[10]), ArgsAt: 11}, true
	}
	return Envelope{Marker: marker, CallID: cid, Main: -1, Sub: -1, ArgsAt: 9}, true
}

// SetCallID 把应答帧的 call_id 改成要应答的那个请求的号（位置固定在第 5~8 字节）
func SetCallID(p []byte, cid uint32) []byte {
	if len(p) >= 9 {
		binary.LittleEndian.PutUint32(p[5:], cid)
	}
	return p
}
