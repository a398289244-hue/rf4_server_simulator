package entry

import (
	"crypto/sha1"
	"encoding/hex"
	"io"
	"strconv"

	"rf4server/internal/wire"
)

// 1/5 真服回的是「9 段 40 字节的串」（整帧 408 字节，物件写法是字节块数组）。
//
// 这 9 串是什么，已经查实了（2026-11-03）：它们就是「游戏开机那 9 块配置」的 sha1 版本号，
// 不是以前猜的会话指纹。两条硬证据：
//  1. 开源实现（gh_refs/rf4_server_simulator）里 1/5 的处理函数就叫 hGameConfigsVersion，
//     回的是 assets/config_wire/index.json 里那 9 个 sha1；
//  2. 把那份 index.json 和录制里真服那帧 1/5 逐段比对，第 3~9 段一模一样，
//     只有第 1、2 段不同 —— 那是真服当时正好更新了 1、2 两块配置，版本号自然换了。
//
// 所以正确的做法是：谁装了配置块（internal/gamecfg），1/5 就回它那 9 个版本号，
// 这样「1/5 报的版本」和「1/4 发的内容」是同一对，客户端不会说配置坏了。
// 没装配置库时才退回按账号现生成的指纹串（形状一样，只是版本号是编的），保持能起。
const (
	// SessionIDCount 真服那帧是 9 段
	SessionIDCount = 9
	// SessionFrameLen 1/5 应答整帧应有的字节数：9 帧头 + 3 数组头 + 9*(4 长度 + 40 内容)
	SessionFrameLen = 9 + 3 + SessionIDCount*(4+SessionIDLenChars)
	// SessionIDLenChars 一串版本号多少字符（sha1 的十六进制形态）
	SessionIDLenChars = 40
)

// SessionIDsReply 1/5 的兜底回法：按账号现生成 9 段串（sha1("rf4-local:<用户名>:<序号>")）。
// 只在「没装配置块」时用 —— 形状和真服一致，客户端不会崩，但 1/4 也没东西可发。
// username 传空串（没对上账号）也能回，只是 9 串都按空用户名算，形状不变。
func SessionIDsReply(callID uint32, username string) []byte {
	arr := wire.BytesArray{}
	for i := 0; i < SessionIDCount; i++ {
		h := sha1.New()
		// sha1 的 Write 不会返回错误（hash 实现吞掉所有写入），所以这里不接 err
		_, _ = io.WriteString(h, "rf4-local:"+username+":"+strconv.Itoa(i))
		arr.Items = append(arr.Items, wire.BlobItem{Data: []byte(hex.EncodeToString(h.Sum(nil)))})
	}
	return sessionFrame(callID, arr)
}

// SessionIDsReplyList 1/5 的正式回法：把配置块的 9 个 sha1 版本号原样发回去。
// versions 顺序就是配置类型 1..9（和 1/4 按类型下发的 blob 一一对应）。
// 传进来不足 9 个就退回兜底回法：宁可不给真版本号，也别发一帧形状不对的出去。
func SessionIDsReplyList(callID uint32, versions []string) []byte {
	if len(versions) != SessionIDCount {
		return nil
	}
	arr := wire.BytesArray{}
	for _, v := range versions {
		arr.Items = append(arr.Items, wire.BlobItem{Data: []byte(v)})
	}
	return sessionFrame(callID, arr)
}

// sessionFrame 把 9 串打包成 1/5 那帧应答（0x04 字节块数组 + 9 字节应答头）
func sessionFrame(callID uint32, arr wire.BytesArray) []byte {
	out, err := arr.Encode()
	if err != nil {
		// 只有条数溢出 u16 才会失败，这边写死 9 条，不可能走到
		out = []byte{0x04, 0x00, 0x00}
	}
	return append(wire.ResponseHead(callID), out...)
}
