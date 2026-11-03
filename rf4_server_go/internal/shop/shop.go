// Package shop 管商城货架（22/1）：按店名现拼一份货架清单回给客户端。
//
// 一帧的形状不是猜的，是把真服那条 22/1 应答逐字段拆出来的（D:\rf4log\dump_2_1... 见 dump_22_1.txt）：
//
//	应答 = 01 feffffff call_id | 参数表 code="140"{ obj501 × N }
//	obj501 = #00 物品编号(短串) | #01 银币价(u64) | #02 金币价(u64) | #03 库存数量(u32)
//
// 也就是说它就是一个普通参数表，用协议字段表现拼就行 —— 不用碰录制。
//
// 客户端拿这份清单判定「有没有货」：清单里没有的编号，界面上就是「在库房没有」。
// 上架前只挡两类硬错误：没有静态定义、没有服务器数字编号。
// 类别没有专属 wire 类时回退到通用物品类 140，避免工具店和普通商品整批显示没库存。
package shop

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"rf4server/internal/gamestore"
	"rf4server/internal/itemdef"
	"rf4server/internal/wire"
)

// 货架元素的物件类与字段名（凭据：真服那条 22/1 应答）
const (
	elemClass = "ifdefblccde" // obj501：一件货架商品
	fKey      = "nhjofmmmngl" // #00 物品编号
	fPrice    = "obcephkkfhi" // #01 银币价（u64，最小单位）
	fGold     = "hklkfdfoloi" // #02 金币价（u64，最小单位）
	fStock    = "aihahcigili" // #03 库存数量（u32）
)

// argCodeShop 货架那坨参数表的名字（真服就是 "140"）
const argCodeShop = "140"

// ItemSource 货架数据来源（MySQL static_shop_item）。只依赖这一个方法，
// 单测想塞几家店随便造，不用起库。
type ItemSource interface {
	ShelfRows(ctx context.Context, store string) ([]gamestore.ShopRow, error)
}

// Stats 一次货架拼装的统计，日志里每一类都要报出来：
// 「这家店库里 8269 件、真挂上去 7912 件、藏了哪些、为什么藏」一目了然。
type Stats struct {
	Store   string // 哪家店
	Rows    int    // 库里这家店几行
	Listed  int    // 真挂上架几件
	NoDef   int    // 没静态定义
	NoID    int    // 没服务器数字编号（客户端认不出）
	NoClass int    // 连通用回退类都没有（正常协议表下应为 0）
	NoArgs  int    // 库里这家店一行都没有
	Bytes   int    // 回包字节数
}

// Service 货架服务。不带可变状态：每次开商店都现读一遍库（少爷在库里改完价就能看见）。
type Service struct {
	tbl  *wire.Table   // 协议字段表：obj501 的字段写法从这儿来
	defs *itemdef.Defs // 静态定义（有没有编号、是什么类别）
	src  ItemSource    // 货架数据来源
}

// New 造服务。src 为 nil 时方法明确报错，不会回一帧空货架假装没事。
func New(t *wire.Table, defs *itemdef.Defs, src ItemSource) *Service {
	return &Service{tbl: t, defs: defs, src: src}
}

// Sellable 闸门：这件货能不能上架。能上架返回 true；否则第二个值是「为什么不能」（日志直接用）。
func (s *Service) Sellable(def *itemdef.Def) (bool, string) {
	return s.sellable(def, map[string]bool{}, 0)
}

func (s *Service) sellable(def *itemdef.Def, seen map[string]bool, depth int) (bool, string) {
	if def == nil {
		return false, "没有这件货的静态定义"
	}
	if def.ID == 0 {
		return false, "没有服务器编号，客户端会显示成别的货"
	}
	// 类别 55 是包装/礼包，不能拿通用 type163 当成普通物件塞进背包。
	// 它可以上架，但前提是内容清单完整，且每项真实物品都能下发；购买时 buy 服务会拆包。
	if def.Cat == 55 {
		if depth >= 4 || seen[def.Key] {
			return false, "礼包内容循环引用"
		}
		items, ok := def.BundleItems()
		if !ok {
			return false, "类别 55 礼包没有有效内容清单"
		}
		seen[def.Key] = true
		defer delete(seen, def.Key)
		for _, item := range items {
			child, found := s.defs.Get(item.Key)
			if !found {
				return false, "礼包内容 " + item.Key + " 没有静态定义"
			}
			if ok, why := s.sellable(child, seen, depth+1); !ok {
				return false, "礼包内容 " + item.Key + " 发不出去：" + why
			}
		}
		return true, ""
	}
	if _, ok := s.tbl.BuildClassFor(def.Cat); !ok {
		return false, "类别 " + strconv.Itoa(def.Cat) + " 没有 wire 类，买回去背包装不下"
	}
	return true, ""
}

// ShelfFrame 拼一家店的货架应答。store 是客户端报上来的店名（可能压根没这家店）。
func (s *Service) ShelfFrame(ctx context.Context, store string, cid uint32) ([]byte, Stats, error) {
	st := Stats{Store: store}
	if s == nil || s.tbl == nil {
		return nil, st, errors.New("shop: 协议字段表没载入，货架拼不出来")
	}
	if s.src == nil {
		return nil, st, errors.New("shop: 没接上货架数据（static_shop_item），先回「这家店没货」")
	}
	rows, err := s.src.ShelfRows(ctx, store)
	if err != nil {
		return nil, st, fmt.Errorf("读 %s 的货架失败: %w", store, err)
	}
	st.Rows = len(rows)

	elems := make([]wire.Value, 0, len(rows))
	for _, r := range rows {
		def, ok := s.defs.Get(r.ItemID)
		if !ok {
			st.NoDef++
			continue
		}
		if ok, _ := s.Sellable(def); !ok {
			switch {
			case def.ID == 0:
				st.NoID++
			default:
				st.NoClass++
			}
			continue
		}
		el, err := s.element(r.ItemID, r.Price, r.Gold, r.Stock)
		if err != nil {
			return nil, st, err
		}
		elems = append(elems, el)
	}
	st.Listed = len(elems)
	if st.Rows == 0 {
		st.NoArgs = 1
	}

	frame, err := s.encode(cid, elems)
	if err != nil {
		return nil, st, err
	}
	st.Bytes = len(frame)
	return frame, st, nil
}

// element 拼一件货架商品（obj501）
func (s *Service) element(key string, price, gold, stock int64) (wire.Value, error) {
	cls, ok := s.tbl.Class(elemClass)
	if !ok {
		return nil, fmt.Errorf("shop: 字段表里没有货架元素类 %s", elemClass)
	}
	o, err := s.tbl.NewObj(cls.TypeID)
	if err != nil {
		return nil, err
	}
	if err := o.Set(fKey, wire.StrField{S: key}); err != nil {
		return nil, err
	}
	if err := o.Set(fPrice, wire.U64(uint64(price))); err != nil {
		return nil, err
	}
	if err := o.Set(fGold, wire.U64(uint64(gold))); err != nil {
		return nil, err
	}
	if err := o.Set(fStock, wire.U32(uint32(stock))); err != nil {
		return nil, err
	}
	return o, nil
}

// encode 把商品列表包成应答：01 feffffff cid | 参数表"140"{...}
func (s *Service) encode(cid uint32, elems []wire.Value) ([]byte, error) {
	body, err := wire.Args{Code: argCodeShop, Items: elems}.Encode()
	if err != nil {
		return nil, fmt.Errorf("shop: 货架参数表拼不出来: %w", err)
	}
	return append(wire.ResponseHead(cid), body...), nil
}

// EmptyFrame 没这家店 / 没接上库时回一帧空货架。
// 客户端要的是「一个合法答案」，不回它就一直转圈。
func (s *Service) EmptyFrame(cid uint32) ([]byte, error) {
	if s == nil || s.tbl == nil {
		return nil, errors.New("shop: 协议字段表没载入")
	}
	return s.encode(cid, nil)
}

// PriceOf 这件货在这家店卖多少钱（购买时以表为准，客户端报的价只当参考）。
// 库里没有这件 / 这家店没上架 -> ok=false。
func (s *Service) PriceOf(ctx context.Context, store, key string) (price, stock int64, ok bool, err error) {
	if s == nil || s.src == nil {
		return 0, 0, false, errors.New("shop: 没接上货架数据")
	}
	rows, err := s.src.ShelfRows(ctx, store)
	if err != nil {
		return 0, 0, false, err
	}
	for _, r := range rows {
		if r.ItemID == key {
			return r.Price, r.Stock, true, nil
		}
	}
	return 0, 0, false, nil
}
