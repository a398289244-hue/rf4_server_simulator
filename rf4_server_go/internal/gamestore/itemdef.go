package gamestore

// 物品静态定义这一层：一件货的数字编号、类别、还有那一堆属性。
// 它和 Runtime 那批「这个号会变的数据」不是一回事（这是全游戏共用的定义表，
// 改它等于改版本，不是改玩家存档），所以单独放一个文件、单独一条查询。
//
// 数据就在 MySQL static_item_def 里（14826 行），少爷要直接看直接改都行：
//
//	SELECT item_id, numeric_id, category, kind, brand, attrs_json FROM static_item_def LIMIT 5;

import (
	"context"
	"errors"
	"time"
)

// ItemDefRow static_item_def 的一行。AttrsJSON 是那堆属性的原文（字段名是逆推出来的混淆名，
// 浮点在里面存的是 IEEE754 位模式整数），交给 internal/itemdef 去解析成本地能用的结构。
type ItemDefRow struct {
	Key       string // 物品编号，如 tele_10112_4，static_item_def.item_id
	NumericID int    // 服务器数字编号（客户端就按这个认货），static_item_def.numeric_id
	Category  int    // 类别号（3=竿 6=渔轮 11=鱼线 12=钩 27=饵…），static_item_def.category
	Kind      string // 系列名（kama/express_fishing…），只为日志和排查用
	Brand     string // 品牌，同上
	AttrsJSON string // 属性原文，static_item_def.attrs_json
}

// ItemDefRows 把整张 static_item_def 拉回来（一万多行，开机拉一次就够）。
//
// 为什么一次拉全而不是一条条查：item_id 是 text 列、没索引，
// 一件一趟就是全表扫；商城货架、装配、买货都要按编号查定义，一次拉完存内存最划算。
func (m *MySQL) ItemDefRows(ctx context.Context) ([]ItemDefRow, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("gamestore: 没接上 MySQL")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rows, err := m.db.QueryContext(ctx,
		// kind/brand/item_id/category 这几列可空（库里就有 NULL），一律 COALESCE 成空值再扫，
		// 不然整表读一半就报「converting NULL to string」。
		`SELECT COALESCE(item_id, ''), COALESCE(numeric_id, 0), COALESCE(category, 0),
		       COALESCE(kind, ''), COALESCE(brand, ''), COALESCE(attrs_json, '')
           FROM static_item_def`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ItemDefRow
	for rows.Next() {
		var (
			key       string
			numericID int64
			category  int64
			kind      string
			brand     string
			attrsJSON string
		)
		if err := rows.Scan(&key, &numericID, &category, &kind, &brand, &attrsJSON); err != nil {
			return nil, err
		}
		out = append(out, ItemDefRow{
			Key:       key,
			NumericID: int(numericID),
			Category:  int(category),
			Kind:      kind,
			Brand:     brand,
			AttrsJSON: attrsJSON,
		})
	}
	return out, rows.Err()
}
