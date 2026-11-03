package gamesess

// 开服首包那 36 字节是「服务器身份 ID」，不是玩家数据，也不是剧本帧。
// 客户端在 connect 阶段就会读它，对不上直接报
// `HermesTcpClient::connect: invalid response data`（2026-11-02 22:42 实测：
// Go 版发随机 UUID 就是这个错，Node 版发下面这个固定值则一路通畅）。
//
// 这个值是 2026-10-31 抓真服包时服务器发的会话 ID，同一台服、同一个实例就是它。
// 它是「服务器的身份证号」，和「别把录制里的玩家快照当真相」那条规矩不冲突：
// 照抄的是身份常量，不是任何人的背包、等级或银币。
// 想做对照实验就启动时加 --session-guid random，想换一份就 --session-guid 填 36 字节。
const ServerSessionGUID = "DAB12FBC-828D-4E00-B222-FB0D6EE7BF23"

// firstHello 连上第一条要发的东西：36 字节服务器身份 ID（ASCII，不带长度前缀、不走 RC4）。
// 配了 --session-guid 就用配的那份；填 random 每次随机（只为对照，客户端会拒，别日常用）。
func (s *Session) firstHello() []byte {
	switch s.srv.Config.SessionGUID {
	case "":
		return []byte(ServerSessionGUID)
	case "random", "随机":
		return randomSessionGUID()
	default:
		return []byte(s.srv.Config.SessionGUID)
	}
}
