package gamesess

import (
	"strconv"
	"time"

	"rf4server/internal/profile"
)

// 这两条推送是「客户端愿不愿意开始说话」的开关，命令号一个都不能改：
//
//	0/1  进场第一条，正文是空参数表"524"，真服原文 19 字节
//	2/1  玩家状态（里面就是档案 obj307），真服原文 272 字节
//
// 凭据 + 实机对照（2026-11-02）：
//   22:51 那次 Go 版装完弹一句没说，客户端一条请求都不发，界面停在「载入个人资料」；
//   Node 版成功那场是 .570 发 0/1、.583 发 2/1，客户端 .676 才开始问 1/1 -> 1/2 -> …。
//   所以这两条必须服务器先手发，等客户端问是等不来的。
// 挂机补推的节奏照 Node 版那个 12500ms（真服挂机时也是十几秒一条），命令行 --state-push-ms 可调。

// speakFirst 装完弹就替服务器先开口：先 0/1、再 2/1，然后起个定时器按点补推 2/1。
// 0/1、2/1 是在握手那儿同步发的（就是要抢在客户端第一问之前），只有补推循环放后台。
func (s *Session) speakFirst() {
	if err := s.sendPush(profile.HelloPush()); err != nil {
		s.log(">> 进场推送 0/1 发不出去：" + friendlyErr(err) + "，状态补推也就不起了")
		return
	}
	s.log(">> 进场推送 0/1 19B（服务器先开口，客户端这才开始问东西）")

	if !s.pushState("进场") {
		return // 第一帧状态就发不动，多半是账号没对上或者连接已经断了，别起循环
	}
	go s.statePushLoop()
}

// pushState 拼一条 2/1 玩家状态发出去，成功返回 true。
// 数据是现取的（银币、技能点随时在变），所以每一帧都可能和上一帧不一样，这是对的。
// 发不出去只记一行日志，不断线：推送丢了客户端顶多界面晚点刷新，不至于把会话踢掉。
func (s *Session) pushState(why string) bool {
	d, src, err := s.profileData()
	if err != nil {
		s.log(">> 玩家状态 2/1 没发（" + why + "）：" + err.Error())
		return false
	}
	frame, err := profile.StatePush(s.srv.Deps.Table, d)
	if err != nil {
		s.log(">> 玩家状态 2/1 拼帧失败（" + why + "）：" + err.Error())
		return false
	}
	if err := s.sendPush(frame); err != nil {
		s.log(">> 玩家状态 2/1 发不出去（" + why + "）：" + friendlyErr(err))
		return false
	}
	s.log(">> 玩家状态 2/1 " + strconv.Itoa(len(frame)) + "B 数据来自 " + src +
		" ｜ " + d.Summary() + " ｜ 原因=" + why)
	return true
}

// statePushLoop 按 --state-push-ms 补推 2/1。间隔填 0 就是不补推（只想看一次性推送时用）。
func (s *Session) statePushLoop() {
	interval := s.srv.Config.StatePush
	if interval <= 0 {
		return
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-s.done:
			return // 连接关了就不再多发
		case <-tick.C:
			s.pushState("挂机补推")
		}
	}
}
