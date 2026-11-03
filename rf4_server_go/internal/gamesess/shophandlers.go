package gamesess

// 商城和仓库这一片的处理器：22/1 开商店、22/2 买东西、21/1 开容器。
// 每个处理器只做三件事：读请求 -> 叫对应包去算 -> 发出去并写一行「人话日志」。
// 业务规则全在 internal/shop、internal/buy、internal/container 里，这层一律不掺和。

import (
	"context"
	"strconv"

	"rf4server/internal/profile"
	"rf4server/internal/shop"
	"rf4server/internal/wire"
)

// shopShelf 22/1：客户端点开一家店。货架现读 MySQL static_shop_item 现拼。
// 客户端就靠这份清单判定「有没有货」，所以三个数都要进日志：库里多少、挂上多少、藏了多少、为什么藏。
func shopShelf(s *Session, env wire.Envelope, plain []byte) {
	svc := s.srv.Deps.Shop
	name := shop.RequestName(plain)
	if svc == nil {
		s.st.misses++
		s.log(">> 货架(22/1) 商城服务没装（多半是没接上 MySQL），按「没实现」处理 商店=" + name)
		sendBuilt(s, env, notHere(env), nil, "货架(22/1) 商城没装", " 商店="+name)
		return
	}
	frame, st, err := svc.ShelfFrame(context.Background(), name, env.CallID)
	if err != nil {
		s.st.misses++
		s.log(">> 货架(22/1) 拼不出来：" + err.Error() + " 先回一帧空货架，免得客户端一直转")
		empty, e := svc.EmptyFrame(env.CallID)
		sendBuilt(s, env, empty, e, "货架(22/1) 空货架兜底", " 商店="+name)
		return
	}
	hide := ""
	if st.NoDef+st.NoID+st.NoClass > 0 {
		hide = " 藏起来 无定义" + strconv.Itoa(st.NoDef) + " 无编号" + strconv.Itoa(st.NoID) +
			" 类别发不出" + strconv.Itoa(st.NoClass)
	}
	sendBuilt(s, env, frame, nil, "货架(22/1) 商店="+name,
		" 库里 "+strconv.Itoa(st.Rows)+" 件，挂上 "+strconv.Itoa(st.Listed)+" 件"+hide+
			"（价格现读 MySQL static_shop_item，库里改完重开商店就生效）")
}

// shopBuy 22/2：买东西。价格以货架表为准，扣 users.silver_minor，往 objects 插一行，
// 回一帧购买回执（里面带现拼的物件记录）。成没成都得回帧，客户端才不会干等。
func shopBuy(s *Session, env wire.Envelope, plain []byte) {
	svc := s.srv.Deps.Buy
	if svc == nil {
		s.st.misses++
		s.log(">> 购买(22/2) 商城服务没装（多半是没接上 MySQL）")
		sendBuilt(s, env, notHere(env), nil, "购买(22/2) 商城没装", "")
		return
	}
	frame, res, err := svc.BuyFrame(context.Background(), s.userOrGuest(), env.CallID, plain)
	if err != nil {
		s.st.misses++
		s.log(">> 购买(22/2) 回执拼不出来：" + err.Error() + " " + res.Store + " " + res.Item)
		return
	}
	note := " 扣 " + profile.Money(res.Price) + " 买后余额 " + profile.Money(res.Balance)
	if res.OK {
		note += " 记录现拼 " + res.Class + "(" + strconv.Itoa(res.TypeID) + ") 编号 " + strconv.Itoa(res.SentID) +
			" GUID " + res.GUID + " 实际入包 " + strconv.Itoa(res.Delivered) + " 条，已写进 MySQL objects+purchases"
	} else {
		s.st.misses++
		note += " 原因：" + choose(res.Reason != "", res.Reason, "没写成")
	}
	sendBuilt(s, env, frame, nil, "购买(22/2) "+choose(res.OK, "成了", "没成")+" "+res.Store+" "+res.Item, note)
}

// containerList 21/1：客户端点开工坊/仓库那类界面。数据只认 MySQL containers + container_items。
// 这个号一格货都没有也照样回一帧空列表（语法借同名容器别人那一行），不回它就一直转。
func containerList(s *Session, env wire.Envelope, plain []byte) {
	svc := s.srv.Deps.Container
	name := shop.RequestName(plain)
	if svc == nil {
		s.st.misses++
		s.log(">> 容器(21/1) 容器服务没装（多半是没接上 MySQL） 容器=" + name)
		sendBuilt(s, env, notHere(env), nil, "容器(21/1) 容器没装", " 容器="+name)
		return
	}
	frame, st, err := svc.Frame(context.Background(), s.userOrGuest(), name, env.CallID)
	if err != nil {
		s.st.misses++
		s.log(">> 容器(21/1) 拼不出来：" + err.Error() + " 容器=" + name + " 先回一帧「没这条 RPC」，免得客户端解歪")
		sendBuilt(s, env, notHere(env), nil, "容器(21/1) 兜底", " 容器="+name)
		return
	}
	other := ""
	if st.SrcUser != "" && st.SrcUser != s.userOrGuest() {
		other = " 语法来源账号 " + st.SrcUser
	}
	sendBuilt(s, env, frame, nil, "容器(21/1) "+name,
		" 回 "+strconv.Itoa(st.Items)+" 件 语法 "+st.Syntax+other+
			choose(st.Reason != "", " 说明："+st.Reason, ""))
}
