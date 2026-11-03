package gamesess

import (
	"context"
	"strconv"
	"strings"
	"time"

	"rf4server/internal/baitdig"
	"rf4server/internal/craft"
	"rf4server/internal/entry"
	"rf4server/internal/fishing"
	"rf4server/internal/gamestore"
	"rf4server/internal/inventory"
	"rf4server/internal/itemops"
	"rf4server/internal/mapworld"
	"rf4server/internal/profile"
	"rf4server/internal/shortcut"
	"rf4server/internal/skilltree"
	"rf4server/internal/wire"
)

// Handler 一条业务命令的处理函数。
// 参数依次是：会话（能发帧、能读本号状态）、信封（主/次号 + call_id）、RC4 解密后的整帧正文。
// 规矩：谁处理谁负责回包，并且把 call_id 记进 s.answered，免得客户端重发时回两遍。
type Handler func(s *Session, env wire.Envelope, plain []byte)

// handlers 命令路由表，键是 "主命令/次命令"。
// 只有挂在这儿的命令有真应答，其它一律走 missing 打日志 —— 这张表就是「Go 版搬到哪了」的进度条：
//
//	5/1 5/2 5/3 5/4  技能          <- 已搬（学习还没搬，先只回本号的树）
//	3/1 1/51 2/1 3/2 进场/档案      <- 还没搬
//	4/5 4/4 4/6 4/7  背包           <- 还没搬
//	8/33 14/2        商店目录/购买   <- 还没搬
//
// handlers 是路由表：键是「主命令/子命令」，来自真服抓包的编号。
// 只列「已搬完、能给应答」的；没列出来的一律走 serveRequest 里的兜底策略，
// 这样日志就是「Go 版搬到哪了」的进度表。
//
//	1/1 1/2       版本握手/服务器时间      <- 已搬（逐字节对过真服）
//	4/1 4/4 4/5   仓库信息/物品编号/物品记录 <- 已搬（4/4、4/5 从 MySQL objects 现算）
//	5/1 5/2 5/3 5/4  技能树/学技能          <- 5/1 已搬；学习还没搬（先回原状清单）
//	9/5 19/6 23/1 24/12 24/3 28/2          <- 已搬：本地服没有的东西，如实回空表
//	3/1              我自己是谁（obj307）  <- 已搬（逐字节 == 真服那帧 262B）
//	0/1 2/1          服务器先开口的进场推送 + 状态补推 <- 已搬（在 arm 之后发，见 push.go）
//	14/1 14/2 14/4 14/5 抛竿/咬钩/起鱼/留鱼 <- 已搬（出鱼来自 static_species_weight + 水域名册）
var handlers = map[string]Handler{
	"1/1":   serverNameAndVersion,
	"1/2":   serverClock,
	"4/1":   storageInfo,
	"4/4":   bagObjectIDs,
	"4/5":   bagObjectList,
	"5/1":   skillTreeRequest,
	"5/2":   skillLearn,
	"5/3":   skillResetOne,
	"5/4":   skillResetWhole,
	"9/5":   emptyArgsReply("97"),
	"23/1":  emptyArgsReply("147"),
	"24/12": emptyArgsReply("524"),
	"19/6":  emptyGUIDList,
	"6/2":   compLoadNull,
	"1/9":   gameCheckNull,
	"1/18":  telemetryTrue,
	"24/3":  localChannelInfo,
	"28/2":  teamPanel,
	"3/1":   selfProfile,
	"1/4":   gameConfigs,
	"1/5":   sessionIDs,
	"1/12":  gamePlace,
	"1/13":  staticFrame,
	"28/1":  staticFrame,
	"4/6":   bagMove,
	"4/7":   bagDelete,
	"4/8":   bagUse,
	"9/1":   craftMake,
	"9/3":   craftMine2,
	"17/1":  rigNotMoved,
	"17/2":  rigMount,
	"17/3":  rigUnmount,
	"17/4":  rigUnmountAll,
	"17/6":  rigNotMoved,
	"21/1":  containerList,
	"21/5":  emptyArgsReply("145"),
	"22/1":  shopShelf,
	"22/2":  shopBuy,
	"22/5":  emptyArgsReply("149"),
	"22/6":  storeATMQueryFalse,
	"14/1":  castPrepare,
	"14/2":  castThrow,
	"14/4":  fishEnd,
	"14/5":  fishKeep,
	"14/7":  fishWaterTick,
	"14/11": fishHooked,
	"14/12": fishLeft,
	"11/1":  shortcutList,
	"11/2":  shortcutBind,
	"11/3":  shortcutUnbind,
}

// craftMake 9/1：按 MySQL 配方制作鱼饵，每次固定产出 30 个并堆入真实背包。
func craftMake(s *Session, env wire.Envelope, plain []byte) {
	if s.srv.Deps.Craft == nil || s.srv.Deps.Run == nil {
		frame, err := craft.ResultReply(s.srv.Deps.Table, env.CallID, false)
		sendBuilt(s, env, frame, err, "制作鱼饵(9/1)", " 制作服务或 MySQL 没接上")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := s.srv.Deps.Craft.Make(ctx, s.userOrGuest(), plain)
	if err != nil {
		frame, ferr := craft.ResultReply(s.srv.Deps.Table, env.CallID, false)
		sendBuilt(s, env, frame, ferr, "制作鱼饵(9/1)", " 失败："+err.Error())
		return
	}
	frame, ferr := craft.ProducedReply(s.srv.Deps.Table, env.CallID, res.Records,
		craft.SkillCaptureBait, res.MasteryAfter-res.MasteryBefore)
	sendBuilt(s, env, frame, ferr, "制作鱼饵(9/1)",
		" 配方="+res.Recipe+" 产物="+res.OutputKey+" x"+strconv.Itoa(res.Quantity)+
			" 当前堆="+strconv.FormatUint(res.StackQuantity, 10)+" 捕获鱼饵 "+
			strconv.FormatFloat(res.MasteryBefore, 'f', 1, 64)+"% -> "+
			strconv.FormatFloat(res.MasteryAfter, 'f', 1, 64)+"%"+
			choose(res.Warning != "", " ｜ "+res.Warning, ""))
	if ferr != nil || len(res.Records) == 0 {
		return
	}
	s.loadSkillsAtLogin()
	s.pushSkillTree("制作鱼饵 " + res.OutputKey)
	push, perr := fishing.KeepPush422(s.srv.Deps.Table, res.Records)
	if perr != nil {
		s.log(">> 制作鱼饵(9/1) 背包更新拼帧失败：" + perr.Error())
		return
	}
	if len(push) > 0 {
		if err := s.sendPush(push); err != nil {
			s.log(">> 制作鱼饵(9/1) 背包更新(4/22) 推送失败：" + friendlyErr(err))
		} else {
			s.log(">> 制作鱼饵(9/1) 背包更新(4/22) 补推 " + strconv.Itoa(len(push)) + "B")
		}
	}
}

// gamePlace 1/12：客户端给一个 level_* 水域名，我们回该水域的 obj205 地图树。
// 地图内容来自结构化定义，天气日号/小时每次按当前时间重锚；未知水域退回出生水域。
func gamePlace(s *Session, env wire.Envelope, plain []byte) {
	if s.srv.Deps.MapWorld == nil {
		sendBuilt(s, env, entry.UnimplementedReply(env.CallID, env.Main, env.Sub), nil,
			"水域定义(1/12) 没装", " map_defs.json 未载入")
		return
	}
	requested := mapworld.LevelFromRequest(s.srv.Deps.Table, plain)
	frame, used, err := s.srv.Deps.MapWorld.Reply(requested, env.CallID, time.Now())
	if err == nil {
		s.setCurrentLocation(used)
	}
	detail := " 请求=" + choose(requested != "", requested, "(没读出水域名)") + " 实际=" + used + "（已用于本地出鱼名册）"
	if requested != "" && requested != used {
		detail += "（未知水域，已退回出生水域）"
	}
	sendBuilt(s, env, frame, err, "水域定义(1/12)", detail)
}

// serveRequest 按主/次号查表分发；查不到就记一笔「还没实现」，让少爷在 IDEA 里一眼看到缺哪条。
// serveRequest 按主/子号分发。查不到不能干等，分两种收尾：
//
//	单向上报（客户端压根不等回话）-> 只记一行日志；
//	其余（客户端在等回执）        -> 回一帧「没这条 RPC」，它立刻死心，不会干到超时断线。
//
// 「还没实现」那行日志照旧打：它就是 Go 版的搬迁进度表，少爷一眼能看到下一条该搬什么。
func (s *Session) serveRequest(env wire.Envelope, plain []byte) {
	key := entry.Key(env.Main, env.Sub)
	s.noteRequest() // 重新开始「客户端有没有卡住」的计时
	if h, ok := handlers[key]; ok {
		h(s, env, plain)
		return
	}
	s.st.misses++
	if entry.IsOneWay(env.Main, env.Sub) {
		s.noteQuiet(key) // 记下「最后一条没回话」，20 秒后如果客户端没动静就预警
		s.log("<< 单向上报，不用回话 " + s.userOrGuest() + " main=" + strconv.Itoa(env.Main) +
			" sub=" + strconv.Itoa(env.Sub) + " len=" + strconv.Itoa(len(plain)) +
			" cid=" + strconv.FormatUint(uint64(env.CallID), 10) + "（抓包 oneway 名单里）")
		return
	}
	sendBuilt(s, env, entry.UnimplementedReply(env.CallID, env.Main, env.Sub), nil,
		"还没实现 "+key, " 先回一帧「没这条 RPC」，省得客户端干等")
}

// bag 背包清单服务。服务本身没状态，现取现用；真正的数据入口还是 Deps.Run（MySQL）。
func (s *Session) bag() *inventory.Service { return inventory.New(s.srv.Deps.Run) }

// 容器号来自 4/1、4/4、4/5 请求里的 args("524")[0]。0=背包，5=鱼护；
// 客户端会分别问这两条，服务端不能把两种记录混在一帧里，否则 HUD 会拿装备数量冒充鱼护数量。
const (
	containerBackpack = uint32(0)
	containerKeepnet  = uint32(5)
)

// requestContainer 从 4/x 请求明文里解出第一个 u32 容器号。
func requestContainer(s *Session, plain []byte) (uint32, bool) {
	v, _, err := s.srv.Deps.Table.DecodeValue(plain, 11)
	if err != nil {
		return 0, false
	}
	args, ok := v.(wire.Args)
	if !ok || len(args.Items) == 0 {
		return 0, false
	}
	switch n := args.Items[0].(type) {
	case wire.Num32:
		return n.V, true
	case wire.Num64:
		return uint32(n.V), true
	default:
		return 0, false
	}
}

// containerLabel 只用于日志，把 0/5 翻成人话。
func containerLabel(container uint32) string {
	switch container {
	case containerBackpack:
		return "背包"
	case containerKeepnet:
		return "鱼护"
	default:
		return "容器" + strconv.FormatUint(uint64(container), 10)
	}
}

// bagObjectList 4/5：按客户端指定的容器下发物件记录。
// 容器 0 只回 MySQL objects 里的装备；容器 5 只回 MySQL keepnets 里的鱼卡。
// 两套记录绝不能混发：鱼护拿到装备 GUID 会让 HUD 把旧缓存数量继续当成鱼。
func bagObjectList(s *Session, env wire.Envelope, plain []byte) {
	container, ok := requestContainer(s, plain)
	if !ok {
		s.log(">> 物品清单(4/5) 请求里读不到容器号，按背包处理")
		container = containerBackpack
	}
	res := inventory.Result{}
	fishCount := 0
	fishRequested := []string(nil)
	fishMissing := []string(nil)

	switch container {
	case containerKeepnet:
		if s.srv.Deps.Fishing == nil {
			s.log(">> 鱼护清单(4/5) 钓鱼服务没接上，按空鱼护回")
			break
		}
		fish, requested, missing, fishErr := s.srv.Deps.Fishing.KeepnetRecords(context.Background(), s.user, plain)
		if fishErr != nil {
			s.log(">> 鱼护清单(4/5) 查不出来：" + fishErr.Error())
			return
		}
		fishCount, fishRequested, fishMissing = len(fish), requested, missing
		res.Elems = fish
	case containerBackpack:
		var err error
		res, err = s.bag().Need(context.Background(), s.user, plain)
		if err != nil {
			s.st.misses++
			s.log(">> 背包清单(4/5) 发不出来：" + err.Error())
			return
		}
	default:
		s.log(">> 物品清单(4/5) 未知容器 " + strconv.FormatUint(uint64(container), 10) + "，回空列表")
	}

	frame := inventory.BuildFrame(env.CallID, res.Elems)
	if err := s.sendResponse(frame, env.CallID); err != nil {
		s.close("发物品清单失败: " + friendlyErr(err))
		return
	}
	s.answered[env.CallID] = true
	s.log(">> 物品清单(4/5) 容器=" + containerLabel(container) + " 回 " + strconv.Itoa(len(res.Elems)) + " 件 / " + strconv.Itoa(len(frame)) +
		"B cid=" + strconv.FormatUint(uint64(env.CallID), 10) +
		choose(container == containerKeepnet, " 鱼护 "+strconv.Itoa(fishCount)+" 条", " 装备 "+strconv.Itoa(len(res.Elems))+" 件") +
		choose(len(res.Requested) > 0, " 客户端点名下载: "+strings.Join(res.Requested, ","), "") +
		choose(len(fishRequested) > 0, " 点名鱼: "+strings.Join(fishRequested, ","), "") +
		choose(len(res.Deferred) > 0, " 容器暂不发: "+strings.Join(res.Deferred, ","), "") +
		choose(len(res.Missing) > 0, " 缺记录: "+strings.Join(res.Missing, ","), "") +
		choose(len(fishMissing) > 0, " 鱼护缺记录: "+strings.Join(fishMissing, ","), ""))
}

// skillTreeRequest 5/1：客户端打开技能面板，要这一棵树。
// 回包两步走，缺一不可：
//  1. 一条 call_id 对号的应答（里面就是 obj311）；
//  2. 再补一条 2/4 全量技能树推送 —— 实测客户端面板第一眼读的是推送，只回应答它会显示不出来。
func skillTreeRequest(s *Session, env wire.Envelope, plain []byte) {
	st := s.skillState()
	avail := st.Available()
	pay, err := skilltree.ReplyTree(s.srv.Deps.Table, s.srv.Deps.Defs, st, env.CallID)
	if err != nil {
		s.log("技能(5/1) 造包失败: " + err.Error())
		return
	}
	if err := s.sendResponse(pay, env.CallID); err != nil {
		s.close("回技能树失败: " + friendlyErr(err))
		return
	}
	s.answered[env.CallID] = true
	s.log(">> 技能(5/1) 回本号自己的技能树（数据来自 " + s.skillSrc + "）：等级 " + strconv.Itoa(st.Level) +
		" 可用技能点 " + strconv.FormatUint(uint64(avail), 10) +
		" 熟练度 " + strconv.Itoa(len(st.Cats)) + " 个大类 已学 " + strconv.Itoa(len(st.Nodes)) + " 个节点" +
		" 回包 " + strconv.Itoa(len(pay)) + "B cid=" + strconv.FormatUint(uint64(env.CallID), 10))
	push, err := skilltree.PushTree(s.srv.Deps.Table, s.srv.Deps.Defs, st)
	if err != nil {
		s.log("技能(2/4) 推送造包失败: " + err.Error())
		return
	}
	if err := s.sendPush(push); err != nil {
		s.close("推技能树失败: " + friendlyErr(err))
		return
	}
	s.log(">> 技能(2/4) 刚回完 5/1，补一条真服形状的全量技能树 " + strconv.Itoa(len(push)) +
		"B（可用点 " + strconv.FormatUint(uint64(avail), 10) + "）")
}

// skillLearn 5/2：学一级。判定（skilltree 包）→ 写 MySQL → 回本号的新技能树。
// 只回应答、不主动推 2/6：实测一推 2/6 客户端十几毫秒就把连接断了（Node 版那个开关默认也是关的）。
func skillLearn(s *Session, env wire.Envelope, plain []byte) { s.skillChange(env, plain, 2) }

// skillResetOne 5/3：退掉一个节点，投进去的点回到「剩余点数」里
func skillResetOne(s *Session, env wire.Envelope, plain []byte) { s.skillChange(env, plain, 3) }

// skillResetWhole 5/4：这个号已学节点全清（发放点数、熟练度都不动）
func skillResetWhole(s *Session, env wire.Envelope, plain []byte) { s.skillChange(env, plain, 4) }

// skillChange 5/2 5/3 5/4 的共同流程：
//
//	1 现查一遍技能状态（少爷直接在库里改过也要认）；
//	2 从请求里捞整数，挑出（大类, 节点）；
//	3 按规则改内存那份（规则在 skilltree.Learn/ResetNode/ResetAll，能单测）；
//	4 写 MySQL：写成才算成，写不回去就回滚并照实说；
//	5 不管成没成都回一帧本号的树 —— 客户端要点一下才重画，不回就一直转。
func (s *Session) skillChange(env wire.Envelope, plain []byte, sub int) {
	st, src := s.freshSkillState()
	ints := skilltree.IntsIn(s.srv.Deps.Table, plain, skilltree.ArgOff)
	before := map[int]int{} // 动过的节点 -> 改动前的等级，写库失败时照这个退回去

	cat, node := 0, 0
	if sub != 4 {
		cat2, node2, have := skilltree.PickCatNode(s.srv.Deps.Defs, ints)
		if !have {
			s.sendSkillTree(env, st, "参数没读出来 整数="+intsToString(ints), src)
			return
		}
		cat, node = cat2, node2
		if sub == 3 && len(ints) >= 2 {
			// 5/3 只给节点号：这一对里第 2 个整数才是节点（第 1 个是大类）
			node = int(ints[1])
		}
		before[node] = st.Nodes[node]
	} else {
		for n, lv := range st.Nodes {
			before[n] = lv // 全清：先记下改动前的等级，写一半断了照着退
		}
	}

	var res skilltree.Result
	switch sub {
	case 2:
		res = skilltree.Learn(s.srv.Deps.Defs, &st, cat, node, false)
	case 3:
		res = skilltree.ResetNode(s.srv.Deps.Defs, &st, node)
	case 4:
		res = skilltree.ResetAll(&st)
	}
	res.Cat, res.Node = cat, node

	if res.OK {
		if why := s.persistSkillChange(sub, &st, before); why != "" {
			res.OK, res.Reason = false, why
		}
	}
	if !res.OK && len(before) > 0 {
		s.revertSkillNodes(st, before) // 库里没写成，内存这份也退回去，界面和库就不会对不上
	} else if res.OK {
		s.storeSkills(st, src)
	}

	label := "学一级"
	note := ""
	if res.OK {
		if sub == 2 {
			label = "学 大类 " + strconv.Itoa(res.Cat) + " 节点 " + strconv.Itoa(res.Node)
			note = " 现在 " + strconv.Itoa(res.Level) + "/" + strconv.Itoa(res.Max) + " 级"
		} else if sub == 3 {
			label = "重置节点 " + strconv.Itoa(res.Node)
		} else {
			label = "全部重置"
		}
		note += " 清掉 " + strconv.Itoa(res.Cleared) + " 个节点"
		if sub != 4 && res.Cleared == 0 {
			note = ""
		}
		note += " 剩技能点 " + strconv.FormatUint(uint64(res.Avail), 10) + " 已写进 MySQL skill_node"
	} else {
		label = "拒绝"
		note = ": " + res.Reason
	}
	s.sendSkillTree(env, st, label+note+" 整数="+intsToString(ints), src)
}

// persistSkillChange 把刚才改在内存里的那一笔落到 MySQL。
// 空串 = 写成了；否则是「为什么没写成」，调用方会把内存退回去。
func (s *Session) persistSkillChange(sub int, st *skilltree.State, before map[int]int) string {
	if s.srv.Deps.Run == nil {
		return "没接上 MySQL，技能点存不住"
	}
	if sub != 4 {
		for n := range before {
			return s.setSkillNodeInDB(n, st.Nodes[n])
		}
		return ""
	}
	levels := make(map[int]int, len(before))
	for n := range before {
		levels[n] = 0
	}
	if len(levels) == 0 {
		return "" // 本来就没学过，没东西要写
	}
	done, why := s.setSkillNodesInDB(levels)
	if why != "" {
		back := make(map[int]int, len(done))
		for _, n := range done {
			back[n] = before[n]
		}
		s.rollbackDoneNodes(back)
	}
	return why
}

// rollbackDoneNodes 5/4 写到一半断了：把已经写进去的那几行改回原来的等级
func (s *Session) rollbackDoneNodes(levels map[int]int) {
	for n, lv := range levels {
		if why := s.setSkillNodeInDB(n, lv); why != "" {
			s.log(">> 技能(5/4) 回滚节点 " + strconv.Itoa(n) + " 也没成：" + why)
		}
	}
}

// sendSkillTree 回一帧本号的技能树（5/1 到 5/4 回的都是这一个东西）。
// extra 拼在日志尾巴上；来源（MySQL / db.json）一定带上 —— 「这数到底哪来的」是最容易查的一件事。
func (s *Session) sendSkillTree(env wire.Envelope, st skilltree.State, extra, src string) {
	frame, err := skilltree.ReplyTree(s.srv.Deps.Table, s.srv.Deps.Defs, st, env.CallID)
	what := "技能(" + strconv.Itoa(env.Main) + "/" + strconv.Itoa(env.Sub) + ") " + extra
	detail := " 可用点 " + strconv.FormatUint(uint64(st.Available()), 10) + " 数据来自 " + src
	sendBuilt(s, env, frame, err, what, detail)
}

// intsToString 把请求里捞到的整数拼成一行，只为日志：出问题要说清客户端到底发了啥
func intsToString(ints []uint64) string {
	parts := make([]string, 0, len(ints))
	for _, n := range ints {
		parts = append(parts, strconv.FormatUint(n, 10))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// userOrGuest 日志里显示账号名；没对上令牌就写个「未登录」，免得日志出现空字段
func (s *Session) userOrGuest() string {
	if s.user == "" {
		return "未登录"
	}
	return s.user
}

// sendBuilt 把「拼好的一帧 → 发出去 → 记账 → 写日志」收成一处，
// 各处理器只管两件事：这帧怎么拼、日志怎么说人话。
// detail 是拼在日志尾巴上的补充说明（比如「回了几件」「为什么是空表」）。
func sendBuilt(s *Session, env wire.Envelope, frame []byte, err error, what, detail string) {
	if err != nil {
		s.st.misses++
		s.log(">> " + what + " 拼帧失败: " + err.Error() +
			" cid=" + strconv.FormatUint(uint64(env.CallID), 10))
		return
	}
	if e := s.sendResponse(frame, env.CallID); e != nil {
		s.close("发" + what + "失败: " + friendlyErr(e))
		return
	}
	s.answered[env.CallID] = true
	s.log(">> " + what + " 回" + strconv.Itoa(len(frame)) + "B" + detail +
		" cid=" + strconv.FormatUint(uint64(env.CallID), 10))
}

// serverNameAndVersion 1/1：客户端报上版本号，我们回 "LocalServer/<版本号>"。
// 版本号是从请求里读出来的（不写死），读不到就回个不带版本的，并在日志里说一声。
func serverNameAndVersion(s *Session, env wire.Envelope, plain []byte) {
	ver := entry.ClientVersion(s.srv.Deps.Table, plain)
	if ver == "" {
		s.log("<< 客户端 1/1 里没读到版本号，回包也不带版本（帧尾会少一串数字）")
	}
	sendBuilt(s, env, entry.ServerNameReply(env.CallID, ver), nil,
		"握手(1/1) 报名字+版本", " 版本="+choose(ver != "", ver, "(没报)"))
}

// serverClock 1/2：回服务器当前时间（FILETIME）。
// 客户端的计时、成就时间戳都以这条为准，所以必须现算，不能塞录制里那个旧数。
func serverClock(s *Session, env wire.Envelope, plain []byte) {
	now := time.Now().UTC()
	sendBuilt(s, env, entry.ServerTimeReply(env.CallID, now), nil,
		"握手(1/2) 服务器时间", " 北京时间 "+now.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05"))
}

// storageInfo 4/1：按客户端指定的容器回占用/容量信息。
// 容器 0=背包（占用数来自 MySQL objects）；容器 5=鱼护（占用数来自 MySQL keepnets，容量 100/150）。
func storageInfo(s *Session, env wire.Envelope, plain []byte) {
	container, ok := requestContainer(s, plain)
	if !ok {
		s.log(">> 物品容量(4/1) 请求里读不到容器号，按背包处理")
		container = containerBackpack
	}
	occupied := uint32(0)
	capacity := uint32(0)
	extended := uint32(0)
	switch container {
	case containerBackpack:
		guids, err := s.bag().GUIDs(context.Background(), s.user)
		if err != nil {
			s.log(">> 背包容量(4/1) 读取占用数失败，先按 0 件回：" + err.Error())
		} else {
			occupied = uint32(len(guids))
		}
	case containerKeepnet:
		capacity, extended = 100, 150
		if s.srv.Deps.Fishing != nil {
			ids, err := s.srv.Deps.Fishing.KeepnetGUIDs(context.Background(), s.user)
			if err != nil {
				s.log(">> 鱼护容量(4/1) 读取占用数失败，先按 0 条回：" + err.Error())
			} else {
				occupied = uint32(len(ids))
			}
		}
	default:
		s.log(">> 物品容量(4/1) 未知容器 " + strconv.FormatUint(uint64(container), 10) + "，回 0/0/0")
	}
	frame, err := entry.StorageInfoReplyWithCapacity(s.srv.Deps.Table, env.CallID, occupied, capacity, extended)
	sendBuilt(s, env, frame, err, "物品容量(4/1)", " 容器="+containerLabel(container)+" 当前 "+strconv.FormatUint(uint64(occupied), 10)+"/"+strconv.FormatUint(uint64(capacity), 10)+"，扩展档 "+strconv.FormatUint(uint64(extended), 10))
}

// bagObjectIDs 4/4：按客户端指定的容器回 GUID 表。
// 容器 0 查 MySQL objects；容器 5 查 MySQL keepnets，不能把两种 ID 合并。
func bagObjectIDs(s *Session, env wire.Envelope, plain []byte) {
	container, ok := requestContainer(s, plain)
	if !ok {
		s.log(">> 物品编号表(4/4) 请求里读不到容器号，按背包处理")
		container = containerBackpack
	}

	var guids [][]byte
	switch container {
	case containerBackpack:
		var err error
		guids, err = s.bag().GUIDs(context.Background(), s.user)
		if err != nil {
			s.st.misses++
			s.log(">> 背包编号表(4/4) 查不出来: " + err.Error())
			return
		}
	case containerKeepnet:
		if s.srv.Deps.Fishing != nil {
			var fishErr error
			guids, fishErr = s.srv.Deps.Fishing.KeepnetGUIDs(context.Background(), s.user)
			if fishErr != nil {
				s.st.misses++
				s.log(">> 鱼护编号表(4/4) 查不出来: " + fishErr.Error())
				return
			}
		}
	default:
		s.log(">> 物品编号表(4/4) 未知容器 " + strconv.FormatUint(uint64(container), 10) + "，回空列表")
	}

	sendBuilt(s, env, inventory.BuildGUIDFrame(env.CallID, guids), nil,
		"物品编号表(4/4)", " 容器="+containerLabel(container)+" 共 "+strconv.Itoa(len(guids))+" 件（只算 "+s.userOrGuest()+" 名下的）")
}

// emptyArgsReply 造一个「回空参数表」的处理器。
// 9/5、23/1、24/12 这三条真服当年就是空清单（本地服也确实没有这些东西），
// 如实回空最诚实 —— 但必须回，客户端在等回执。
func emptyArgsReply(code string) Handler {
	return func(s *Session, env wire.Envelope, plain []byte) {
		sendBuilt(s, env, entry.EmptyArgsReply(env.CallID, code), nil,
			"空清单("+entry.Key(env.Main, env.Sub)+")", " 参数表 \""+code+"\" 空着回")
	}
}

// emptyGUIDList 19/6：回一帧空 GUID 数组（本地服没有别人名下的物件）
func emptyGUIDList(s *Session, env wire.Envelope, plain []byte) {
	sendBuilt(s, env, entry.EmptyGUIDsReply(env.CallID), nil,
		"空清单(19/6)", " GUID 数组 0 个")
}

// compLoadNull 6/2 comp/load：本地服没有竞赛可载入，回一帧 null（整帧 10 字节）。
//
// 这条差点被「真服没答过」骗过去：抓包 45 秒里服务端确实一声不吭，于是它进了单向名单。
// 但实机 2026-11-03 01:27 证明了——客户端发完 6/2 就一条都不再发，卡「载入个人资料」，
// 120 秒后抛 `HermesError code:110 desc:6:2` + `Game bootstrapping failed` 然后断开。
// 开源实现的回法一模一样：`d.Register(6, 2, hNull) // comp/load 无竞赛 -> null`。
// 教训写进 policy.go：**抓包只能证明「服务端当时没答」，证明不了「客户端不要回执」**。
func compLoadNull(s *Session, env wire.Envelope, plain []byte) {
	sendBuilt(s, env, entry.NullReply(env.CallID), nil,
		"竞赛载入(6/2)", " 本地服没有竞赛 回 null（客户端就在等这一帧，不给它就一直卡载入）")
}

// gameCheckNull 1/9 game/check：合同规定返回 null。动作完成后客户端会补问这一帧，
// 不回就会记成「未实现」甚至打断后续流程。
func gameCheckNull(s *Session, env wire.Envelope, plain []byte) {
	sendBuilt(s, env, entry.NullReply(env.CallID), nil,
		"游戏检查(1/9)", " 合同要求 null，回一帧让客户端收尾")
}

// storeATMQueryFalse 22/6：ATM/商店查询固定 false，不改货币。
func storeATMQueryFalse(s *Session, env wire.Envelope, plain []byte) {
	sendBuilt(s, env, entry.BoolReply(env.CallID, false), nil,
		"商店查询(22/6)", " 固定回 false")
}

// telemetryTrue 1/18：客户端开机上报一大包自己的设置/设备指纹（抓包那帧 1654B）。
// 真服那次没答，开源实现回 bool true，我们跟着回 true —— 万一这个客户端也要回执，我们不会卡。
func telemetryTrue(s *Session, env wire.Envelope, plain []byte) {
	sendBuilt(s, env, entry.BoolReply(env.CallID, true), nil,
		"客户端上报(1/18)", " 回 true（开源实现同款回法，抓包那次服务端没答）")
}

// localChannelInfo 24/3：本地频道信息（名字、大区、私有频道），字段值见 internal/entry
func localChannelInfo(s *Session, env wire.Envelope, plain []byte) {
	frame, err := entry.ServerInfoReply(s.srv.Deps.Table, env.CallID)
	sendBuilt(s, env, frame, err, "本地频道(24/3)", " 频道名 "+entry.LocalChannelName)
}

// teamPanel 28/2：战队/工会面板，本地服没有战队 -> 照真服回三层空表
func teamPanel(s *Session, env wire.Envelope, plain []byte) {
	frame, err := entry.ChannelListReply(s.srv.Deps.Table, env.CallID)
	sendBuilt(s, env, frame, err, "战队面板(28/2)", " 战队/工会/编队都是空表")
}

// selfProfile 3/1：客户端问「我是谁」，回 obj307 玩家档案。
// 内容和 2/1 状态推送里那份一模一样，只是外面套的壳不同（应答头 vs 推送头）。
// 这条不回，界面就一直停在「载入个人资料」—— 客户端就是攥着这一帧才往下走的。
func selfProfile(s *Session, env wire.Envelope, plain []byte) {
	d, src, err := s.profileData()
	if err != nil {
		s.st.misses++
		s.log(">> 档案(3/1) 发不出来：" + err.Error() +
			" cid=" + strconv.FormatUint(uint64(env.CallID), 10))
		return
	}
	frame, err := profile.Reply(s.srv.Deps.Table, env.CallID, d)
	sendBuilt(s, env, frame, err, "档案(3/1) 我自己是谁",
		" 数据来自 "+src+" ｜ "+d.Summary())
}

// sessionIDs 1/5：回「游戏开机那 9 块配置的 sha1 版本号」（整帧 408B，形状和真服一致）。
// 这 9 串不是会话指纹 —— 开源实现里这条就叫 game/configs_version，而且它 index.json 里的
// 9 个 sha1 有 7 个和录制那帧逐段相同。装了配置库（internal/gamecfg）就回它那 9 个版本号，
// 这样 1/5 报的版本和 1/4 发的内容是同一对；没装才退回按账号现编的占位串。
func sessionIDs(s *Session, env wire.Envelope, plain []byte) {
	if cfg := s.srv.Deps.GameCfg; cfg != nil {
		if frame := entry.SessionIDsReplyList(env.CallID, cfg.Versions()); frame != nil {
			sendBuilt(s, env, frame, nil, "配置版本表(1/5)",
				" 回 "+strconv.Itoa(entry.SessionIDCount)+" 个配置块 sha1（和 1/4 发的内容是一对）")
			return
		}
	}
	sendBuilt(s, env, entry.SessionIDsReply(env.CallID, s.user), nil,
		"配置版本表(1/5)", " 配置库没装，按账号现编 "+strconv.Itoa(entry.SessionIDCount)+" 段占位")
}

// staticFrame 整帧都是静态定义的应答（现在挂了 1/13 地图+天气、28/1 大区浏览器）。
// 拼帧全靠 internal/staticdef + internal/wiredef，这边只管发和记账：
// 以后要加一条「整帧都是定义」的命令，只要在路由上多挂一行 + 多准备一份定义 JSON。
func staticFrame(s *Session, env wire.Envelope, plain []byte) {
	key := entry.Key(env.Main, env.Sub)
	if !s.srv.Deps.Static.Has(key) {
		// 定义还没准备：照样得回一帧，客户端才不会在那条上干等到超时
		s.st.misses++
		sendBuilt(s, env, entry.UnimplementedReply(env.CallID, env.Main, env.Sub), nil,
			"还没实现 "+key, " 静态定义 JSON 还没准备")
		return
	}
	frame, err := s.srv.Deps.Static.Reply(key, env.CallID)
	if err != nil {
		s.st.misses++
		s.log(">> 静态定义帧(" + key + ") 发不出来：" + err.Error() +
			" cid=" + strconv.FormatUint(uint64(env.CallID), 10))
		return
	}
	sendBuilt(s, env, frame, nil, "静态定义帧("+key+")", " 内容来自定义 JSON，按字段表现拼，不是录制原文")
}

// shortcutService 快捷栏服务。和背包那个一样：服务本身没状态，现取现用，真数据在 MySQL。
func (s *Session) shortcuts() *shortcut.Service {
	return shortcut.New(s.srv.Deps.Table, s.srv.Deps.Run)
}

// shortcutList 11/1：下发这个号自己的快捷键绑定（数据在 MySQL shortcuts 表）。
// 一格都没绑也照样回空列表，客户端才会把快捷栏画出来而不是干等。
func shortcutList(s *Session, env wire.Envelope, plain []byte) {
	frame, rows, err := s.shortcuts().ListFrame(context.Background(), s.user, env.CallID)
	if err != nil {
		s.st.misses++
		s.log(">> 快捷栏(11/1) 发不出来：" + err.Error() +
			" cid=" + strconv.FormatUint(uint64(env.CallID), 10))
		return
	}
	sendBuilt(s, env, frame, nil, "快捷栏(11/1) 绑定清单",
		" 共 "+strconv.Itoa(len(rows))+" 格："+shortcut.Describe(rows))
}

// shortcutBind 11/2：绑一格。先验这件东西是不是这个号名下的（影子物品就是栽在这儿），
// 验不过也照样回帧（GUID 写零），客户端才会把这一格显示成空的。
func shortcutBind(s *Session, env wire.Envelope, plain []byte) {
	frame, res, err := s.shortcuts().BindFrame(context.Background(), s.user, env.CallID, plain)
	if err != nil {
		s.st.misses++
		s.log(">> 快捷栏(11/2) 请求读不懂：" + err.Error() + " 先回一帧「没这条 RPC」")
		sendBuilt(s, env, entry.UnimplementedReply(env.CallID, env.Main, env.Sub), nil,
			"快捷栏(11/2) 读不懂", " 免得客户端干等")
		return
	}
	sendBuilt(s, env, frame, nil, "快捷栏(11/2) 绑定",
		" 槽"+strconv.Itoa(res.Slot)+" <- "+choose(res.OK,
			res.ItemKey+"("+res.GUID+")", "拒绝："+res.Reason))
}

// shortcutUnbind 11/3：解掉一格（库里这一行 guid 写空），应答照真服那样回 obj136{槽位, 零 GUID}
func shortcutUnbind(s *Session, env wire.Envelope, plain []byte) {
	frame, res, err := s.shortcuts().UnbindFrame(context.Background(), s.user, env.CallID, plain)
	if err != nil {
		s.st.misses++
		s.log(">> 快捷栏(11/3) 请求读不懂：" + err.Error() + " 先回一帧「没这条 RPC」")
		sendBuilt(s, env, entry.UnimplementedReply(env.CallID, env.Main, env.Sub), nil,
			"快捷栏(11/3) 读不懂", " 免得客户端干等")
		return
	}
	sendBuilt(s, env, frame, nil, "快捷栏(11/3) 解绑",
		" 槽"+strconv.Itoa(res.Slot)+" "+choose(res.OK, "已清空（MySQL shortcuts 那行也清了）", "失败："+res.Reason))
}

// craftMine2 9/3：拿铲子/红虫长柄勺挖饵。命令名虽然叫 crafting/mine2，
// 实机用铲子时发的就是它；绝不能把请求里的工具 GUID 当分解物品删掉。
func craftMine2(s *Session, env wire.Envelope, plain []byte) {
	guids := s.ops().Involved(plain)
	detail := " 请求里没读出工具 GUID"
	var obj gamestore.Object
	if len(guids) > 0 && s.srv.Deps.Run != nil && s.user != "" && s.srv.Deps.BaitDig != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, guid := range guids {
			o, err := s.srv.Deps.Run.Object(ctx, guid)
			if err != nil || !strings.EqualFold(o.Username, s.user) {
				continue
			}
			if s.srv.Deps.BaitDig.Handles(o.ItemKey) {
				obj = o
				break
			}
		}
		if obj.GUID == "" {
			detail = " 点到的物品里没有本号可用的铲子/红虫长柄勺"
		} else {
			res, handled, err := s.srv.Deps.BaitDig.Dig(ctx, s.user, obj.ItemKey)
			if err != nil || !handled {
				detail = " 挖饵失败：" + choose(err != nil, err.Error(), "该物品不走挖饵")
				s.log(">> 挖饵(9/3) 工具=" + obj.ItemKey + detail)
			} else {
				frame, ferr := craft.DigResultReply(s.srv.Deps.Table, env.CallID, res.Records,
					baitdig.SkillCaptureBait, res.MasteryAfter-res.MasteryBefore)
				sendBuilt(s, env, frame, ferr, "挖饵(9/3)",
					" 工具="+obj.ItemKey+" 获得 "+res.BaitKey+" x"+strconv.Itoa(res.Quantity)+
						" 当前堆 "+strconv.FormatUint(res.StackQuantity, 10)+" 捕获鱼饵 "+
						strconv.FormatFloat(res.MasteryBefore, 'f', 1, 64)+"% -> "+
						strconv.FormatFloat(res.MasteryAfter, 'f', 1, 64)+"%"+
						choose(res.Warning != "", " ｜ "+res.Warning, ""))
				s.loadSkillsAtLogin()
				s.pushSkillTree("挖饵 " + res.BaitKey)
				if len(res.Records) > 0 {
					push, perr := fishing.KeepPush422(s.srv.Deps.Table, res.Records)
					if perr != nil {
						s.log(">> 挖饵(9/3) 背包更新拼帧失败：" + perr.Error())
					} else if len(push) > 0 {
						if e := s.sendPush(push); e != nil {
							s.log(">> 挖饵(9/3) 背包更新(4/22) 推送失败：" + friendlyErr(e))
						} else {
							s.log(">> 挖饵(9/3) 背包更新(4/22) 补推 " + strconv.Itoa(len(push)) + "B")
						}
					}
				}
				return
			}
		}
	}
	frame, err := craft.ResultReply(s.srv.Deps.Table, env.CallID, false)
	sendBuilt(s, env, frame, err, "挖饵(9/3)", detail)
}

// itemOps 背包操作服务（4/6 挪、4/7 丢；4/8 用它的 GUID 解析）
func (s *Session) ops() *itemops.Service {
	return itemops.New(s.srv.Deps.Table, s.srv.Deps.Run)
}

// bagDelete 4/7：丢掉几件。回的不是「删了哪个 GUID」那么省事，
// 而是「删完还剩的整份清单 + 删掉的那些 GUID」（800 快照）——
// 只回 GUID 的话界面不会实时刷新，少爷之前就为此回来报过一回。
func bagDelete(s *Session, env wire.Envelope, plain []byte) {
	frame, res, err := s.ops().DeleteFrame(context.Background(), s.user, env.CallID, plain)
	if err != nil {
		s.st.misses++
		s.log(">> 背包删除(4/7) 出岔子：" + err.Error() + " 先回一帧空快照让客户端收尾")
		if empty, e := s.ops().AckFrame(env.CallID); e == nil {
			sendBuilt(s, env, empty, nil, "背包删除(4/7) 兜底空快照", " 真因见上一行")
		}
		return
	}
	sendBuilt(s, env, frame, nil, "背包删除(4/7)",
		" 删 "+strconv.Itoa(len(res.Deleted))+" 件 "+strings.Join(res.Deleted, ",")+
			" 还剩 "+strconv.Itoa(res.Left)+" 件"+
			choose(len(res.Deferred) > 0, " 容器暂不发: "+strings.Join(res.Deferred, ","), "")+
			choose(res.Reason != "", " 说明："+res.Reason, ""))
}

// bagMove 4/6：挪位置（背包↔帐篷↔车↔房子…）。客户端自己已经把东西搬好了，
// 服务端这边先认账（回空 800）。要把挪动结果写进 objects.parent_guid，
// 加一个 Runtime.SetParent 就行，改的只有 itemops 那一处。
func bagMove(s *Session, env wire.Envelope, plain []byte) {
	frame, err := s.ops().AckFrame(env.CallID)
	if err != nil {
		s.st.misses++
		s.log(">> 背包挪位(4/6) 空快照拼不出来：" + err.Error())
		return
	}
	guids := s.ops().Involved(plain)
	sendBuilt(s, env, frame, nil, "背包挪位(4/6) 认账（回空 800）",
		" 点了 "+strconv.Itoa(len(guids))+" 件 "+strings.Join(guids, ","))
}

// bagUse 4/8：使用物品。真服这条回 bool；铲子和红虫长柄勺在这里执行挖饵，
// 产物写 objects 后按 4/22 推回背包。普通物品仍按成功认账，免得客户端干等。
func bagUse(s *Session, env wire.Envelope, plain []byte) {
	guids := s.ops().Involved(plain)
	if len(guids) == 0 {
		sendBuilt(s, env, entry.BoolReply(env.CallID, false), nil,
			"背包使用(4/8)", " 请求里没读出 GUID")
		return
	}
	svc := s.srv.Deps.BaitDig
	if svc == nil || s.srv.Deps.Run == nil {
		sendBuilt(s, env, entry.BoolReply(env.CallID, false), nil,
			"背包使用(4/8)", " 挖饵服务或 MySQL 没接上")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	username := s.userOrGuest()
	var records [][]byte
	var dug []string
	ok := true
	for _, guid := range guids {
		obj, err := s.srv.Deps.Run.Object(ctx, guid)
		if err != nil {
			ok = false
			s.log(">> 背包使用(4/8) 找不到物品 " + guid + "：" + err.Error())
			continue
		}
		if !strings.EqualFold(obj.Username, username) {
			ok = false
			s.log(">> 背包使用(4/8) 拒绝跨账号物品 " + guid + "（归属 " + obj.Username + "）")
			continue
		}
		res, handled, err := svc.Dig(ctx, username, obj.ItemKey)
		if err != nil {
			ok = false
			s.log(">> 挖饵(4/8) 工具=" + obj.ItemKey + " 失败：" + err.Error())
			continue
		}
		if !handled {
			s.log(">> 背包使用(4/8) 普通物品=" + obj.ItemKey + "，按成功认账")
			continue
		}
		records = append(records, res.Records...)
		dug = append(dug, res.BaitKey+" x"+strconv.Itoa(res.Quantity))
		s.log(">> 挖饵(4/8) 工具=" + obj.ItemKey + " 获得 " + res.BaitKey + " x" +
			strconv.Itoa(res.Quantity) + " 当前堆 " + strconv.FormatUint(res.StackQuantity, 10) + " 捕获鱼饵 " +
			strconv.FormatFloat(res.MasteryBefore, 'f', 1, 64) + "% -> " +
			strconv.FormatFloat(res.MasteryAfter, 'f', 1, 64) + "%" +
			choose(res.Warning != "", " ｜ "+res.Warning, ""))
	}

	sendBuilt(s, env, entry.BoolReply(env.CallID, ok), nil, "背包使用(4/8)",
		" 使用 "+strconv.Itoa(len(guids))+" 件，挖到 "+choose(len(dug) > 0, strings.Join(dug, ","), "无饵"))

	if len(records) == 0 {
		return
	}
	s.loadSkillsAtLogin()
	s.pushSkillTree("使用工具挖饵")
	push, err := fishing.KeepPush422(s.srv.Deps.Table, records)
	if err != nil {
		s.log(">> 挖饵(4/8) 物品推送拼帧失败：" + err.Error())
		return
	}
	if len(push) > 0 {
		if err := s.sendPush(push); err != nil {
			s.log(">> 挖饵(4/8) 物品推送失败：" + friendlyErr(err))
		} else {
			s.log(">> 挖饵(4/8) 背包更新(4/22) 补推 " + strconv.Itoa(len(push)) + "B")
		}
	}
}
