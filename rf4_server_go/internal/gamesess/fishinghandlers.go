package gamesess

// 钓鱼这一片的处理器：14/1 抛竿准备、14/2 抛出去、14/4 起鱼结算、14/5 留鱼。
// 规矩和其它域一样：这层只读请求、叫 internal/fishing 去算、发出去、写一行能看懂的日志；
// 出什么鱼、多重多长、帧怎么拼，一律不在这里。
//
// 14/7 14/8 14/11 是客户端上报搏鱼过程的单帧，在 oneway 名单里（客户端不等回话），
// 所以不用挂处理器，serveRequest 会打「单向上报」那行日志。

import (
	"context"
	"math/rand"
	"strconv"
	"time"

	"rf4server/internal/fishing"
	"rf4server/internal/itemsync"
	"rf4server/internal/mapworld"
	"rf4server/internal/wire"
)

// fishPicker 造出这一竿使用的出鱼器：当前会话实际地图优先，命令行 --fish-loc 只做兜底。
// 不缓存对象，每次抛竿都按当下水域重新查名册，切图后不会继续出旧地区的鱼。
func (s *Session) fishPicker() *fishing.Picker {
	svc := s.srv.Deps.Fishing
	if svc == nil {
		return nil
	}
	loc := s.currentLocation()
	if loc == "" {
		loc = s.srv.Config.FishLoc
	}
	return svc.NewPicker(loc, s.srv.Config.FishKey)
}

// castPrepare 14/1：客户端把竿甩出去之前的「抛竿准备」，带着它手上这套钓具的 GUID。
// 回 obj158（允许抛距那格），然后排一次咬钩 —— 这两步少一步，水里就不会出鱼。
func castPrepare(s *Session, env wire.Envelope, plain []byte) {
	gear, ok := fishing.GearGUID(plain)
	if !ok {
		gear = "00000000000000000000000000000000"
		s.log(">> 抛竿(14/1) 没从请求里读到钓具 GUID（帧太短？），先回包，咬钩推送可能挂不上竿")
	}
	s.fishingState.SetGear(gear)
	svc := s.srv.Deps.Fishing
	if svc == nil {
		sendBuilt(s, env, notHere(env), nil, "抛竿(14/1) 钓鱼服务没装", " 多半是没连上 MySQL")
		return
	}
	frame, err := svc.CastPrepareFrame(env.CallID, gear)
	casts, bites, kept := s.fishingState.Stats()
	sendBuilt(s, env, frame, err, "抛竿(14/1) 钓具="+short(gear),
		" 累计 抛"+strconv.Itoa(casts)+" 咬"+strconv.Itoa(bites)+" 留"+strconv.Itoa(kept))
	if err != nil {
		return
	}
	s.scheduleBite("抛竿")
}

// castThrow 14/2：竿真的甩出去了（请求里带落点坐标和钓点名）。
// 真服这条回的就是空 obj800（29B），没什么可算的，照抄形状。
func castThrow(s *Session, env wire.Envelope, plain []byte) {
	// 14/2 有时也带当前 level_*，读到了就顺手校正；主来源仍是 1/12。
	if loc := mapworld.LevelFromRequest(s.srv.Deps.Table, plain); loc != "" {
		s.setCurrentLocation(loc)
		s.fishingState.SetPlace(loc)
	}
	frame, err := itemsync.EmptyFrame(s.srv.Deps.Table, env.CallID)
	if err != nil {
		s.log(">> 抛出(14/2) 空快照拼不出来：" + err.Error())
		return
	}
	if e := s.sendResponse(frame, env.CallID); e != nil {
		s.close("发抛出回执失败: " + friendlyErr(e))
		return
	}
	s.answered[env.CallID] = true
	s.log(">> 抛出(14/2) 回 29B 空快照（真服这条也是空快照） cid=" +
		strconv.FormatUint(uint64(env.CallID), 10))
}

// fishEnd 14/4：起鱼/收线。回 obj801，里面那个 obj307 是现取的档案（银币、技能点跟最新值）。
// 这次要是钩着鱼，就把那条鱼的 GUID 放进 #10，并把「这根竿 + 它的零件」现在这份记录放进 #08。
func fishEnd(s *Session, env wire.Envelope, plain []byte) {
	svc := s.srv.Deps.Fishing
	if svc == nil {
		sendBuilt(s, env, notHere(env), nil, "起鱼(14/4) 钓鱼服务没装", "")
		return
	}
	gear, _ := fishing.GearGUID(plain)
	d, src, err := s.profileData()
	if err != nil {
		s.log(">> 起鱼(14/4) 取不到档案，先回一帧「没这条 RPC」：" + err.Error())
		sendBuilt(s, env, notHere(env), nil, "起鱼(14/4) 没档案", "")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var guids []string
	hooked := "没鱼"
	if f, ok := s.fishingState.Pending(); ok {
		guids = []string{f.SetupGUID}
		hooked = f.Key + " " + strconv.FormatInt(f.Grams, 10) + "克/" +
			fishCm(f.LengthM) + "厘米"
	}
	recs, rerr := svc.GearRecords(ctx, s.userOrGuest(), gear)
	if rerr != nil {
		s.log(">> 起鱼(14/4) 装备记录没读到（4/22 和 #08 会缺这一截）：" + rerr.Error())
	}
	frame, err := svc.EndFrame(env.CallID, d, recs, guids)
	sendBuilt(s, env, frame, err, "起鱼(14/4) 钩着="+hooked,
		" 数据来自 "+src+" 装备记录 "+strconv.Itoa(len(recs))+" 件")
}

// fishKeep 14/5：把鱼留进鱼护。拼鱼卡片 -> 回包 -> 写 keepnets/fish_records -> 推 4/22。
// 库里写不进去只记日志，不把玩家踢下线：应答已经发出去了，界面是对得上的。
func fishKeep(s *Session, env wire.Envelope, plain []byte) {
	svc := s.srv.Deps.Fishing
	if svc == nil {
		sendBuilt(s, env, notHere(env), nil, "留鱼(14/5) 钓鱼服务没装", "")
		return
	}
	gear, fishGUID, ok := fishing.KeepGUIDs(plain)
	if !ok {
		s.log(">> 留鱼(14/5) 请求读不懂（该有钓具 GUID + 鱼 GUID 两项） cid=" +
			strconv.FormatUint(uint64(env.CallID), 10) + " 回一帧空快照")
		frame, err := itemsync.EmptyFrame(s.srv.Deps.Table, env.CallID)
		sendBuilt(s, env, frame, err, "留鱼(14/5) 空快照兜底", "")
		return
	}
	f, known := s.fishingState.Resolve(fishGUID)
	why := ""
	if !known {
		// 后端不记得这条鱼：多半是「抛竿后掉了线，重连才把 keep 补上来」。
		// 不能凭空编一条鱼出来，就按占位卡片发（鱼种名留空），并把原因写进日志。
		f = fishing.Fish{SetupGUID: fishGUID, Class: fishing.ClassMedium}
		why = " 后端不认这条鱼（多半是重连后才 keep 的），鱼卡片按占位值发"
	}
	in := fishing.KeepInput{
		CallID:   env.CallID,
		Username: s.userOrGuest(),
		GearGUID: gear,
		FishGUID: fishGUID,
		Fish:     f,
		Place:    s.fishPlace(),
		Bait:     s.srv.Config.FishBait,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := svc.Keep(ctx, in)
	if err != nil {
		s.log(">> 留鱼(14/5) 鱼卡片拼不出来：" + err.Error() + " 鱼=" + fishGUID)
		sendBuilt(s, env, notHere(env), nil, "留鱼(14/5) 拼帧失败", "")
		return
	}
	sendBuilt(s, env, res.Reply, nil, "留鱼(14/5) "+describeCatch(f),
		" 入库="+switchStr(res.Saved, "成了", "没成")+" 记录"+strconv.Itoa(len(res.CardHex)/2)+"B"+why)
	if res.SaveErr != "" {
		s.log(">> 留鱼(14/5) " + res.SaveErr + "（不影响这一局，重启后鱼护里会少这一条）")
	}
	s.fishingState.MarkKept()
	s.fishingState.ClearPending()

	if len(res.Push422) > 0 {
		if e := s.sendPush(res.Push422); e != nil {
			s.log(">> 装备更新(4/22) 推不出去：" + friendlyErr(e))
		} else {
			s.log(">> 装备更新(4/22) 补推 " + strconv.Itoa(len(res.Push422)) + "B（真服留鱼后也推这条）")
		}
	}
	// 银币/经验以后会从卖鱼那步变，所以这里顺手补一帧状态；现在没变化也无害。
	s.pushState("留鱼")
}

// scheduleBite 排一次咬钩：延迟到某个随机时刻，然后把 14/14 推出去。
// 为什么是异步的：真服也不是抛完立刻咬钩（实测 4~16 秒不等），
// 而且这段时间客户端还在发 14/8 上报搏鱼姿态，我们不能卡在帧循环里睡。
func (s *Session) scheduleBite(reason string) {
	svc := s.srv.Deps.Fishing
	if svc == nil {
		return
	}
	if !s.srv.Config.AutoBite {
		s.log(">> 咬钩 已按 --auto-bite=false 关掉，这次抛竿不会出鱼")
		return
	}
	p := s.fishPicker()
	if p == nil {
		s.log(">> 咬钩 出鱼器没起来（鱼种表没装上？），这次抛竿不会出鱼")
		return
	}
	delay := fishing.BiteDelay(rand.New(rand.NewSource(time.Now().UnixNano())),
		s.srv.Config.BiteMin, s.srv.Config.BiteMax)
	s.log(">> 咬钩 已排[" + reason + "] " + delay.String() + " 后派鱼 水域=" + switchStr(p.Loc() != "", p.Loc(), "没配") +
		" 指定鱼种=" + switchStr(s.srv.Config.FishKey != "", s.srv.Config.FishKey, "按名册随机"))

	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-s.done:
			return // 连接都关了，就别往断掉的 socket 上推了
		case <-timer.C:
		}
		f, ok := p.Pick()
		if !ok {
			s.log(">> 咬钩 配不出鱼（static_species_weight / static_map_fish_roster 里没找到能用的），这次不派")
			return
		}
		gear := s.fishingState.Gear()
		if gear == "" {
			s.log(">> 咬钩 没记住钓具 GUID，这条鱼只能盲发（客户端可能挂不上竿）")
		}
		s.fishingState.Arm(f)
		frame, err := svc.BiteFrame(gear, f)
		if err != nil {
			s.log(">> 咬钩 14/14 拼帧失败: " + err.Error())
			return
		}
		if e := s.sendPush(frame); e != nil {
			s.log(">> 咬钩 14/14 推不出去：" + friendlyErr(e))
			return
		}
		s.log(">> 咬钩(14/14) 推 " + strconv.Itoa(len(frame)) + "B " + describeCatch(f) +
			" 实例=" + short(f.SetupGUID) + " 延迟 " + delay.String() +
			" 搏鱼参数按 ruffe 那组常量（待按鱼种校准）")
	}()
}

// fishPlace 鱼卡片上那个钓点名（obj25#10）。真服那格填的是水域 id，
// 优先用会话当前实际地图，其次命令行兜底，最后才用 14/2 读到的钓点名。
func (s *Session) fishPlace() string {
	if loc := s.currentLocation(); loc != "" {
		return loc
	}
	if s.srv.Config.FishLoc != "" {
		return s.srv.Config.FishLoc
	}
	return s.fishingState.Place()
}

// describeCatch 一条鱼怎么用人话说一行：日志里到处要用，统一在这儿。
func describeCatch(f fishing.Fish) string {
	if f.Key == "" {
		return "鱼=未知(占位)"
	}
	return "鱼=" + f.Key + " " + strconv.FormatInt(f.Grams, 10) + "克/" + fishCm(f.LengthM) + "厘米/" +
		ClassName(f.Class)
}

// fishCm 体长（米）换成人看的厘米，保留一位小数
func fishCm(m float64) string {
	return strconv.FormatFloat(m*100, 'f', 1, 64)
}

// ClassName 大小档的人话名
func ClassName(class byte) string {
	switch class {
	case fishing.ClassSmall:
		return "小"
	case fishing.ClassMedium:
		return "中"
	case fishing.ClassBig:
		return "大"
	case fishing.ClassTrophy:
		return "特大"
	}
	return "档" + strconv.Itoa(int(class))
}

// switchStr 小 helper：条件成立取 a 否则取 b（日志里那种「有就显示、没有就显示占位」）
func switchStr(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}
