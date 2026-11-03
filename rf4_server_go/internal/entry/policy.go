// Package entry 里这份名单管「哪些命令我们干脆不回话」。
//
// 规矩的来头（两条独立证据，别混着看）：
//  1. 真服抓包（data/rpc_table_real.json 的 oneway 段，11 条）：这些命令只见到客户端单向发，服务端一声不吭；
//  2. 实机反证（2026-11-03 01:27）：真服虽然没答 6/2，**我们这个客户端却在等**——
//     它发完 6/2 就一条都不再发，干等 120 秒后抛
//     `HermesError code:110 desc:6:2` + `Game bootstrapping failed`，然后断开。
//     开源实现（gh_refs/rf4_server_simulator/internal/gameserver/timeline.go）也是这么处理的：
//     `d.Register(6, 2, hNull)`（comp/load 无竞赛 → null）、`d.Register(1, 18, hBoolTrue)`。
//
// 所以名单拆成两张表：真服沉默的那 11 条是凭据，客户端确实还在等的几条单独列出来当例外，
// 例外会从「不回话」名单里剔掉（改成老老实实回一帧最小的合法值）。
// 测试 TestOnewayListMatchesCredential 会盯着：名单只能等于「凭据减去例外」，谁偷偷加回去谁露馅。
package entry

// realServerSilent 真服抓包里服务端没答过的命令（凭据，11 条）
var realServerSilent = map[string]bool{
	"1/7":   true, // 客户端上报自己的区域/语言（zh-CN）
	"1/15":  true, // 进图通知（game/enter_place_notify）
	"1/18":  true, // 上报一大包客户端设置/设备指纹（抓包 1654 字节）
	"3/9":   true, // 钓鱼过程上报
	"3/10":  true, // 用船上报（boat_use）
	"3/18":  true, // 抓包这条服务端偶尔回「没实现」，客户端两种都能吃
	"6/2":   true, // comp/load：没有竞赛可载入
	"14/7":  true, // 搏鱼过程上报（抓包 1072 条，真服一条没答）
	"14/8":  true, // 搏鱼负载上报
	"14/11": true, // 咬钩上报
	"17/7":  true, // 取竿到手（客户端只是通知服务端「我手上是哪根」）
}

// clientAwaitsReply 真服虽然没答，但实测我们这个客户端会等回执（等不到就 120 秒超时断线）。
// 这几条不留在沉默名单里，改由路由表回一帧最小的合法值。键后面必须挂上路由，否则等于没写。
var clientAwaitsReply = map[string]bool{
	"6/2":  true, // 实机 2026-11-03 01:27：发完 6/2 就卡住，120 秒后 HermesError code:110 desc:6:2
	"1/18": true, // 开源实现回 bool true（设备指纹上报的回执）；真服没答是它那边不用回执
	"3/18": true, // 实机 2026-11-03 12:14：客户端等满 120 秒后 HermesError code:110 desc:3:18 并主动断线
}

// oneWayCommands 我们真的不回话的 = 真服沉默名单 - 客户端还在等的例外
var oneWayCommands = func() map[string]bool {
	m := make(map[string]bool, len(realServerSilent))
	for k := range realServerSilent {
		if !clientAwaitsReply[k] {
			m[k] = true
		}
	}
	return m
}()

// RealServerSilent 拷贝一份真服沉默凭据（测试要对账，别让它改到内部表）
func RealServerSilent() map[string]bool {
	m := make(map[string]bool, len(realServerSilent))
	for k, v := range realServerSilent {
		m[k] = v
	}
	return m
}

// ClientAwaitsReply 拷贝一份「真服没答但客户端在等」的例外表
func ClientAwaitsReply() map[string]bool {
	m := make(map[string]bool, len(clientAwaitsReply))
	for k, v := range clientAwaitsReply {
		m[k] = v
	}
	return m
}

// NeedsReply 这条命令要不要回话。
// 名单里的不回；其余一律要回 —— 没实现的也得回一帧「Unimplemented RPC」（见 UnimplementedReply），
// 否则客户端会一直等到自己超时才断线（实测 120 秒）。
func NeedsReply(main, sub int) bool {
	return !oneWayCommands[Key(main, sub)]
}

// Key 「主/子」拼成路由表的键，全项目就用这一个写法，别各处自己拼
func Key(main, sub int) string { return itoa(main) + "/" + itoa(sub) }

// IsOneWayKey 拿 "主/子" 字符串直接问（日志、测试、路由自检都用这个）
func IsOneWayKey(key string) bool { return oneWayCommands[key] }

// IsOneWay 只问「是不是单向上报」，给日志用（日志要说清为啥没回话）
func IsOneWay(main, sub int) bool { return oneWayCommands[Key(main, sub)] }
