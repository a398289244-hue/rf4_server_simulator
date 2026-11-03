package gamesess

// 装配这一片的处理器：17/2 装上、17/3 拆一件、17/4 全拆、17/1 和 17/6 先认账。
// 规则全在 internal/rig 里，这边只管叫它、发帧、把「装到哪一格/进了哪个槽」写成人话日志。

import (
	"context"
	"strconv"

	"rf4server/internal/rig"
	"rf4server/internal/wire"
)

// rigMount 17/2：把一件东西装到竿上。回的是 800 快照：先物品记录、后竿记录。
func rigMount(s *Session, env wire.Envelope, plain []byte) {
	svc := s.srv.Deps.Rig
	if svc == nil {
		s.rigMissing(env, "17/2")
		return
	}
	frame, res, err := svc.MountFrame(context.Background(), s.userOrGuest(), env.CallID, plain)
	if err != nil {
		s.st.misses++
		s.log(">> 装配(17/2) 回包拼不出来：" + err.Error())
		return
	}
	if !res.OK {
		s.st.misses++
		sendBuilt(s, env, frame, nil, "装配(17/2) 拒绝",
			" 竿 "+short(res.RodGUID)+" 物品 "+short(res.ItemGUID)+"："+choose(res.Reason != "", res.Reason, "没写成"))
		return
	}
	where := "第 " + strconv.Itoa(res.Slot) + " 格"
	if res.Kind == "part" {
		where = "钓组零件表 品类 " + strconv.Itoa(res.Cat) + " 槽索引 " + strconv.Itoa(res.PartIndex) +
			"（钓组类型 " + strconv.Itoa(res.RigType) + "）"
	}
	if res.Prev != "" {
		where += "（顶掉原来的 " + short(res.Prev) + "）"
	}
	sendBuilt(s, env, frame, nil, "装配(17/2) 装上",
		" "+short(res.ItemGUID)+" -> 竿 "+short(res.RodGUID)+" 进 "+where+
			" 回 "+strconv.Itoa(res.Records)+" 条记录（objects.elem 已更新，MySQL rig/rig_component 已同步）")
}

// rigUnmount 17/3：拆一件下来（拆下来的会退回背包）
func rigUnmount(s *Session, env wire.Envelope, plain []byte) {
	svc := s.srv.Deps.Rig
	if svc == nil {
		s.rigMissing(env, "17/3")
		return
	}
	frame, res, err := svc.UnmountFrame(context.Background(), s.userOrGuest(), env.CallID, plain)
	if err != nil {
		s.st.misses++
		s.log(">> 装配(17/3) 回包拼不出来：" + err.Error())
		return
	}
	if !res.OK {
		s.st.misses++
		sendBuilt(s, env, frame, nil, "装配(17/3) 没拆成",
			" 竿 "+short(res.RodGUID)+"："+choose(res.Reason != "", res.Reason, "没写成"))
		return
	}
	sendBuilt(s, env, frame, nil, "装配(17/3) 拆下",
		" 竿 "+short(res.RodGUID)+" 拆 "+strconv.Itoa(len(res.Removed))+" 件 "+rig.Describe(res.Removed)+
			" 回 "+strconv.Itoa(res.Records)+" 条记录")
}

// rigUnmountAll 17/4：这根竿全拆。没装东西也回一帧空快照（客户端不转圈）
func rigUnmountAll(s *Session, env wire.Envelope, plain []byte) {
	svc := s.srv.Deps.Rig
	if svc == nil {
		s.rigMissing(env, "17/4")
		return
	}
	frame, res, err := svc.UnmountAllFrame(context.Background(), s.userOrGuest(), env.CallID, plain)
	if err != nil {
		s.st.misses++
		s.log(">> 装配(17/4) 回包拼不出来：" + err.Error())
		return
	}
	what := "装配(17/4) 全拆 " + strconv.Itoa(len(res.Removed)) + " 件"
	if !res.OK {
		s.st.misses++
		what = "装配(17/4) 本来就没装东西"
	}
	sendBuilt(s, env, frame, nil, what, " 竿 "+short(res.RodGUID))
}

// rigNotMoved 17/1（选钓组类型）、17/6（挂饵方式）：规则还没搬，先认账回空 800，别把客户端晾着
func rigNotMoved(s *Session, env wire.Envelope, plain []byte) {
	svc := s.srv.Deps.Rig
	if svc == nil {
		s.rigMissing(env, "17/"+strconv.Itoa(env.Sub))
		return
	}
	frame, err := svc.EmptyFrame(env.CallID)
	sendBuilt(s, env, frame, err, "装配(17/"+strconv.Itoa(env.Sub)+") 先认账（钓组类型/挂饵方式的规则还没搬）", "")
}

// rigMissing 装配服务没装（多半是没接上 MySQL）：按「没实现」处理，日志说清为什么
func (s *Session) rigMissing(env wire.Envelope, what string) {
	s.st.misses++
	s.log(">> 装配(" + what + ") 装配服务没装（多半是没接上 MySQL）")
	sendBuilt(s, env, notHere(env), nil, "装配("+what+") 装配没装", "")
}

// short GUID 前 8 位，日志里够认就行
func short(guid string) string {
	if len(guid) <= 8 {
		return guid
	}
	return guid[:8]
}
