package gamesess

import (
	"rf4server/internal/entry"
	"rf4server/internal/wire"
)

// notHere 回一帧「没这条 RPC」（marker=-3 + LocalServer.UnsupportedOperation）。
// 兜底回什么都走这一个函数：哪天要改成别的写法，只改这一处。
func notHere(env wire.Envelope) []byte {
	return entry.UnimplementedReply(env.CallID, env.Main, env.Sub)
}
