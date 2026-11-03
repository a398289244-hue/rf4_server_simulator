package gamesess

import (
	"encoding/hex"
	"strconv"

	"rf4server/internal/gamecfg"
	"rf4server/internal/wire"
)

// 这块管「游戏开机要的那 9 块配置」：1/4 下发配置块本体。
//
// 为什么单独一个文件：它是开机链路的第一道门，和背包/商城那类「进去之后的业务」不是一条线。
// 客户端开机问 1/4，要是收到一帧「没这条 RPC」，它当场抛
// `Game bootstrapping failed: System.Exception: Unimplemented RPC 1:4` 然后断开
// （2026-11-03 01:12 实机客户端日志就是这行）。所以这条必须给真东西，不能糊一帧错误过去。
//
// 内容从哪来：internal/gamecfg 从凭据目录读那 9 块 LZMA 配置（data/config_wire/），
// 一块都不改、原样下发；1/5 报的 9 个 sha1 就是这些块的校验值（见 handlers.go 的 sessionIDs）。
// 我们不解压也不改配置内容 —— 客户端自己解压、自己按 sha1 校验。

// reqHeadHex 读不出「要第几块」时，日志里打多少字节的请求原文。
// 我们没有真服 1/4 的请求样本（整场抓包一条都没问），所以第一次实机必须把 hex 打出来对形状。
const reqHeadHex = 40

// gameConfigs 1/4：按请求里那个类型号 N，回第 N 块配置（帧体 = 0x08 字节块 + u32 长度 + LZMA 原文）。
// 读不出类型号、或者那块配置没有，就回一帧空参数表"524"：
// 客户端这一步要的是「拿到配置」，给它错误回执它就崩，给它空 args 至少还能往下走。
func gameConfigs(s *Session, env wire.Envelope, plain []byte) {
	cfg := s.srv.Deps.GameCfg
	if cfg == nil {
		// 凭据目录里没 config_wire/：能起，但客户端开机多半过不去，所以日志要吵
		s.st.misses++
		sendBuilt(s, env, gamecfg.EmptyReply(env.CallID), nil,
			"游戏配置(1/4) 配置库没装",
			" 回空参数表凑数 ｜ 缺 data/config_wire/ 那 9 块，客户端会卡在开机（Unimplemented RPC 之外另一种死法：配置为空）")
		return
	}

	typ, ok := gamecfg.RequestType(s.srv.Deps.Table, plain)
	if !ok {
		s.st.misses++
		sendBuilt(s, env, gamecfg.EmptyReply(env.CallID), nil,
			"游戏配置(1/4) 读不出要第几块",
			" 请求前 "+strconv.Itoa(min(len(plain), reqHeadHex))+" 字节="+hex.EncodeToString(plain[:min(len(plain), reqHeadHex)])+
				" 回空参数表 ｜ 拿这行 hex 校准 1/4 的请求形状（抓包里一条 1/4 都没有，没法预先验）")
		return
	}

	frame, ok := cfg.Reply(env.CallID, typ)
	if !ok {
		s.st.misses++
		sendBuilt(s, env, gamecfg.EmptyReply(env.CallID), nil,
			"游戏配置(1/4) 没有第 "+strconv.Itoa(typ)+" 块",
			" 配置库只有 "+strconv.Itoa(cfg.Count())+" 块（类型 1.."+strconv.Itoa(gamecfg.TypeCount)+"） 回空参数表")
		return
	}

	ver := cfg.Version(typ)
	if s.cfgHits == nil {
		s.cfgHits = map[int]int{}
	}
	s.cfgHits[typ]++
	sendBuilt(s, env, frame, nil,
		"游戏配置(1/4) 第 "+strconv.Itoa(typ)+" 块",
		" "+strconv.Itoa(len(frame))+"B（LZMA 原样下发，sha1="+ver+"，本次连接第 "+strconv.Itoa(s.cfgHits[typ])+" 次要它）")
}
