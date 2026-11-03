package buy

// Package buy 管「买东西」这一条命令（22/2）：核对价格 -> 扣银币 -> 现拼物件记录入库 -> 回一帧购买回执。
//
// 回执的形状是把真服那帧逐字段拆出来的（D:\rf4log\dump_buy.txt）：
//
//	应答 = 01 feffffff call_id | obj804{
//	        #00 物品编号(短串)
//	        #01 参数表"61"{ 这次到手的物件记录 }
//	        #02 参数表"61"{ 钓组上的物件 }（一般是空）
//	        #03 GUID 数组（真服那条是空） }
//
// 物件记录本身由 internal/itemwire 按静态定义现拼（类别挑类、逐格填），
// 里面还嵌一格购买凭证 obj166（哪家店、买家 id、买后余额、成交价、买家名）。
// 全程没有一帧录制：字节全是这边拼的，考卷只活在测试里。
//
// 钱的事只认 MySQL：扣 users.silver_minor、往 objects 插一行、往 purchases 写流水，
// 少爷在库里能直接看到「买了一样、钱少了多少、货在不在」。

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"rf4server/internal/gamestore"
	"rf4server/internal/itemdef"
	"rf4server/internal/itemstack"
	"rf4server/internal/itemwire"
	"rf4server/internal/shop"
	"rf4server/internal/wire"
)

// PriceSource 货架价格来源（internal/shop.Service 正好符合这个接口）。
// 价格以表为准：客户端报的价只用来对照，对不上就以表为准并写一行日志。
type PriceSource interface {
	PriceOf(ctx context.Context, store, key string) (price, stock int64, ok bool, err error)
}

// PurchaseLog 购买流水（purchases 表）。写不上不影响买到手，只记一行日志。
type PurchaseLog interface {
	InsertPurchase(ctx context.Context, p gamestore.Purchase) error
}

// Service 购买服务。
type Service struct {
	tbl    *wire.Table        // 协议字段表：obj804 的四个字段写法从这儿来
	defs   *itemdef.Defs      // 静态定义（有没有编号、什么类别）
	iw     *itemwire.Service  // 物件记录现拼
	run    gamestore.Runtime  // 账号/钱包/背包
	prices PriceSource        // 货架价格
	log    PurchaseLog        // 购买流水
	stack  *itemstack.Service // 可堆叠物品累加到同一件真实物件
}

// New 造服务。缺件都在方法里明确报错，不会闷头回一帧「买到了」。
func New(t *wire.Table, defs *itemdef.Defs, iw *itemwire.Service, run gamestore.Runtime,
	prices PriceSource, plog PurchaseLog) *Service {
	s := &Service{tbl: t, defs: defs, iw: iw, run: run, prices: prices, log: plog}
	if st, ok := any(run).(itemstack.Store); ok {
		s.stack = itemstack.New(iw, st)
	}
	return s
}

// Result 一次购买的结果，字段全是给日志准备的
type Result struct {
	Store     string // 在哪家店
	Item      string // 买了哪件
	Price     int64  // 实扣（最小单位）
	Balance   int64  // 扣完余额
	GUID      string // 发出去的物件 GUID
	Class     string // 用的哪个 wire 类（出岔子先看图这格）
	TypeID    int    // 物件类型号
	SentID    int    // 记录里写给客户端的数字编号
	OK        bool   // 真买成了没有
	Reason    string // 没买成的原因
	ElemHex   string // 物件记录 hex（写进 purchases.payload，事后能直接拆）
	Delivered int    // 实际入包的物件记录数；礼包拆包时可能大于 1
}

type delivery struct {
	def   *itemdef.Def
	units uint64
}

// deliveryPlan 把普通商品变成一项，把类别55礼包递归展开成真实内容。
// 数量设上限是为了防止静态表误填后一次购买生成几十万行 objects。
func (s *Service) deliveryPlan(def *itemdef.Def, seen map[string]bool, depth int, multiplier uint64) ([]delivery, error) {
	const maxUnits = 10000
	if def == nil {
		return nil, errors.New("没有这件货的静态定义，先不卖")
	}
	if def.ID == 0 {
		return nil, fmt.Errorf("%s 没有服务器编号，客户端认不出来", def.Key)
	}
	if multiplier == 0 || multiplier > maxUnits {
		return nil, fmt.Errorf("%s 的礼包数量 %d 不合理", def.Key, multiplier)
	}
	if def.Cat != 55 {
		if _, ok := s.tbl.BuildClassFor(def.Cat); !ok {
			return nil, fmt.Errorf("类别 %d 没有 wire 类，%s 发不出去", def.Cat, def.Key)
		}
		return []delivery{{def: def, units: multiplier}}, nil
	}
	if depth >= 4 || seen[def.Key] {
		return nil, fmt.Errorf("礼包 %s 内容循环引用", def.Key)
	}
	items, ok := def.BundleItems()
	if !ok {
		return nil, fmt.Errorf("类别 55 礼包 %s 没有有效内容清单", def.Key)
	}
	seen[def.Key] = true
	defer delete(seen, def.Key)
	var out []delivery
	for _, item := range items {
		if item.Quantity > maxUnits/multiplier {
			return nil, fmt.Errorf("礼包 %s 的内容 %s 数量过大", def.Key, item.Key)
		}
		child, found := s.defs.Get(item.Key)
		if !found {
			return nil, fmt.Errorf("礼包 %s 的内容 %s 没有静态定义", def.Key, item.Key)
		}
		part, err := s.deliveryPlan(child, seen, depth+1, multiplier*item.Quantity)
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("礼包 %s 没有可发内容", def.Key)
	}
	return out, nil
}

func chooseSource(unboxed bool) string {
	if unboxed {
		return "go-buy-unboxed"
	}
	return "go-buy"
}

// BuyFrame 处理一条 22/2。永远回一帧（哪怕没买成），客户端才不会干等；
// 没买成时那帧的回执里物件记录是空的，界面上不会多东西。
func (s *Service) BuyFrame(ctx context.Context, user string, cid uint32, plain []byte) ([]byte, Result, error) {
	res := Result{}
	if s == nil || s.tbl == nil {
		return nil, res, errors.New("buy: 协议字段表没载入，回执拼不出来")
	}
	req, ok := shop.RequestPurchase(s.tbl, plain)
	res.Store, res.Item = req.Store, req.Item
	if !ok {
		res.Reason = "购买请求读不懂（该是 参数表{ 店名, 物品名, 报价 }）"
		frame, err := s.reply(cid, res.Item, nil)
		return frame, res, err
	}

	// 1. 这件货到底能不能发出去（没定义/没编号/类别没类，买回去都是麻烦）
	def, ok := s.defs.Get(req.Item)
	if !ok {
		res.Reason = "没有这件货的静态定义，先不卖"
		frame, err := s.reply(cid, req.Item, nil)
		return frame, res, err
	}
	plan, err := s.deliveryPlan(def, map[string]bool{}, 0, 1)
	if err != nil {
		res.Reason = err.Error()
		frame, err := s.reply(cid, req.Item, nil)
		return frame, res, err
	}
	res.SentID = def.ID

	// 2. 价格以货架表为准
	price := req.PriceClient
	if s.prices != nil {
		if tblPrice, _, found, err := s.prices.PriceOf(ctx, req.Store, req.Item); err == nil && found {
			if price != 0 && price != tblPrice {
				res.Reason = fmt.Sprintf("客户端报价 %d 和货架表 %d 不一样，按表里的扣", price, tblPrice)
			}
			price = tblPrice
		}
	}
	res.Price = price

	if s.run == nil {
		res.Reason = "没接上 MySQL，卖不了"
		frame, err := s.reply(cid, req.Item, nil)
		return frame, res, err
	}

	// 3. 钱包：先看清够不够，够就扣（AddWallet 自己带事务）
	acct, err := s.run.Account(ctx, user)
	if err != nil {
		res.Reason = "读账号失败: " + err.Error()
		frame, e := s.reply(cid, req.Item, nil)
		return frame, res, e
	}
	if price > 0 && acct.SilverMinor < price {
		res.Reason = fmt.Sprintf("银币不够：要 %d，只有 %d", price, acct.SilverMinor)
		frame, e := s.reply(cid, req.Item, nil)
		return frame, res, e
	}
	paid := acct
	if price > 0 {
		paid, err = s.run.AddWallet(ctx, user, -price, 0)
		if err != nil {
			res.Reason = "扣银币失败: " + err.Error()
			frame, e := s.reply(cid, req.Item, nil)
			return frame, res, e
		}
	}
	res.Balance = paid.SilverMinor

	// 4/5. 现拼并入背包。类别55不是一件可下发的普通物品，而是礼包定义；
	// 这里按 nbkhfhenbag 拆成真实内容入 objects，购买回包也只发这些真实记录。
	records := make([][]byte, 0, len(plan))
	for i, give := range plan {
		copies := uint64(1)
		units := give.units
		if !s.tbl.IsCountable(give.def.Cat) {
			copies, units = give.units, 1
		}
		for copyNo := uint64(0); copyNo < copies; copyNo++ {
			receiptPrice := int64(0)
			if i == 0 && copyNo == 0 {
				receiptPrice = price
			}
			built, buildErr := s.iw.Build(itemwire.Input{
				Key: give.def.Key, Units: int(units),
				Receipt: &itemwire.Receipt{Store: req.Store, UserID: paid.UserID, Balance: res.Balance, Price: receiptPrice, User: buyerName(paid)},
			})
			if buildErr != nil {
				res.Reason = "礼包内容 " + give.def.Key + " 的物件记录拼不出来: " + buildErr.Error()
				frame, e := s.reply(cid, req.Item, nil)
				return frame, res, e
			}
			elemHex := hex.EncodeToString(built.Frame)
			objPrice := int64(0)
			if len(records) == 0 {
				objPrice = price
			}
			obj := gamestore.Object{
				GUID: built.GUID, Username: user, ItemKey: give.def.Key, ItemID: give.def.ID,
				PriceMinor: objPrice, How: "商城买入 " + req.Store,
				Source: chooseSource(def.Cat == 55), Elem: elemHex, CreatedAt: time.Now().UnixMilli(),
			}
			finalGUID, finalRecord := built.GUID, built.Frame
			if s.stack != nil && s.tbl.IsCountable(give.def.Cat) {
				stacked, e := s.stack.Add(ctx, user, obj)
				if e != nil {
					res.Reason = "写 objects 失败: " + e.Error() + "（钱已扣，GUID " + built.GUID + "，日志里照着补就行）"
					frame, re := s.reply(cid, req.Item, nil)
					return frame, res, re
				}
				finalGUID, finalRecord = stacked.GUID, stacked.Record
				if stacked.Existing && len(plan) == 1 {
					res.Reason = "已累加到现有堆，数量 " + strconv.FormatUint(stacked.Quantity, 10)
				}
			} else if err := s.run.InsertObject(ctx, obj); err != nil {
				res.Reason = "写 objects 失败: " + err.Error() + "（钱已扣，GUID " + built.GUID + "，日志里照着补就行）"
				frame, e := s.reply(cid, req.Item, nil)
				return frame, res, e
			}
			if len(records) == 0 {
				res.GUID, res.Class, res.TypeID = finalGUID, built.Class, built.TypeID
				res.SentID = give.def.ID
				res.ElemHex = hex.EncodeToString(finalRecord)
			}
			records = append(records, finalRecord)
		}
	}
	res.Delivered = len(records)

	// 6. 流水（写不上不算失败）
	if s.log != nil {
		_ = s.log.InsertPurchase(ctx, gamestore.Purchase{
			Username: user, UserID: paid.UserID, Store: req.Store, ItemKey: def.Key,
			ItemID: def.ID, Price: price, GUID: res.GUID, At: time.Now().UnixMilli(),
		})
	}

	res.OK = true
	frame, err := s.reply(cid, req.Item, records)
	return frame, res, err
}

// reply 拼一帧购买回执 obj804。records 为 nil 就是「这次没到手东西」的空回执。
func (s *Service) reply(cid uint32, itemKey string, records [][]byte) ([]byte, error) {
	cls, ok := s.tbl.Class(itemwire.ClsPurchase)
	if !ok {
		return nil, fmt.Errorf("buy: 字段表里没有购买回执类 %s", itemwire.ClsPurchase)
	}
	o, err := s.tbl.NewObj(cls.TypeID)
	if err != nil {
		return nil, err
	}
	if err := o.SetShortStrAt(itemwire.P804Item, itemKey); err != nil {
		return nil, err
	}
	items := make([]wire.Value, 0, len(records))
	for _, record := range records {
		if len(record) > 0 {
			items = append(items, wire.Raw{Data: record})
		}
	}
	if err := o.SetValAt(itemwire.P804Objects, wire.Args{Code: "61", Items: items}); err != nil {
		return nil, err
	}
	if err := o.SetValAt(itemwire.P804Rig, wire.Args{Code: "61", Items: nil}); err != nil {
		return nil, err
	}
	if err := o.SetValAt(itemwire.P804GUIDs, wire.GUIDArray{}); err != nil {
		return nil, err
	}
	body, err := o.Encode()
	if err != nil {
		return nil, fmt.Errorf("buy: 购买回执拼不出来: %w", err)
	}
	return append(wire.ResponseHead(cid), body...), nil
}

// buyerName 凭证里写哪个名字：有昵称用昵称，没有用登录名
func buyerName(a gamestore.Account) string {
	if a.Nickname != "" {
		return a.Nickname
	}
	return a.Username
}
