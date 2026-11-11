package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

var errInvalidPriceDefinition = errors.New("无效价格定义")

// ShopPriceDef 是一个店铺 SKU 的可追溯价格定义。
type ShopPriceDef struct {
	StoreID   string
	ItemID    string
	Silver    int64
	Gold      int64
	Stock     int64
	Source    string
	SourceRef string
}

// UpsertShopPriceDef 写入一个逐店 SKU 价格，供官服抓包值覆盖静态快照。
func (d *DB) UpsertShopPriceDef(ctx context.Context, p ShopPriceDef) error {
	p.StoreID = strings.TrimSpace(p.StoreID)
	p.ItemID = strings.TrimSpace(p.ItemID)
	p.Source = strings.TrimSpace(p.Source)
	p.SourceRef = strings.TrimSpace(p.SourceRef)
	if p.StoreID == "" || p.ItemID == "" || p.Silver < 0 || p.Gold < 0 || p.Stock < 0 || p.Source == "" {
		return errInvalidPriceDefinition
	}
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO shop_price_def(store_id,item_id,silver,gold,stock,source,source_ref)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(store_id,item_id) DO UPDATE SET
			silver=excluded.silver, gold=excluded.gold, stock=excluded.stock,
			source=excluded.source, source_ref=excluded.source_ref`,
		p.StoreID, p.ItemID, p.Silver, p.Gold, p.Stock, p.Source, p.SourceRef)
	return err
}

// FishMarketPriceDef 是鱼市每公斤价格定义。WeightMaxG 为 0 表示不设上限。
type FishMarketPriceDef struct {
	MapKey      string
	FishKey     string
	Grade       int64
	WeightMinG  int64
	WeightMaxG  int64
	SilverPerKg float64
	Source      string
	SourceRef   string
}

// FishMarketPricePerKg 返回当前重量命中的最精确鱼市价格。
// 相同地图/鱼种/档位下，重量下限更高的记录优先；未命中返回 found=false。
func (d *DB) FishMarketPricePerKg(ctx context.Context, mapKey, fishKey string, grade, weightG int64) (price float64, found bool, err error) {
	mapKey = strings.TrimSpace(mapKey)
	fishKey = strings.TrimSpace(fishKey)
	if mapKey == "" || fishKey == "" || grade < 1 {
		return 0, false, nil
	}
	if weightG < 1 {
		weightG = 1
	}
	err = d.SQL.QueryRowContext(ctx, `
		SELECT silver_per_kg
		  FROM fish_market_price_def
		 WHERE map_key=? AND fish_key=? AND grade=?
		   AND weight_min_g<=?
		   AND (weight_max_g=0 OR ?<weight_max_g)
		 ORDER BY weight_min_g DESC,
		          CASE WHEN weight_max_g=0 THEN 1 ELSE 0 END,
		          weight_max_g ASC
		 LIMIT 1`, mapKey, fishKey, grade, weightG, weightG).Scan(&price)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return price, price > 0, nil
}

// UpsertFishMarketPriceDef 写入可追溯鱼市价格定义，供官服抓包值覆盖社区样本。
func (d *DB) UpsertFishMarketPriceDef(ctx context.Context, p FishMarketPriceDef) error {
	p.MapKey = strings.TrimSpace(p.MapKey)
	p.FishKey = strings.TrimSpace(p.FishKey)
	p.Source = strings.TrimSpace(p.Source)
	p.SourceRef = strings.TrimSpace(p.SourceRef)
	if p.MapKey == "" || p.FishKey == "" || p.Grade < 1 || p.WeightMinG < 0 ||
		p.WeightMaxG < 0 || (p.WeightMaxG > 0 && p.WeightMaxG <= p.WeightMinG) ||
		p.SilverPerKg <= 0 || p.Source == "" {
		return errInvalidPriceDefinition
	}
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO fish_market_price_def(map_key,fish_key,grade,weight_min_g,weight_max_g,silver_per_kg,source,source_ref)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(map_key,fish_key,grade,weight_min_g) DO UPDATE SET
			weight_max_g=excluded.weight_max_g, silver_per_kg=excluded.silver_per_kg,
			source=excluded.source, source_ref=excluded.source_ref`,
		p.MapKey, p.FishKey, p.Grade, p.WeightMinG, p.WeightMaxG, p.SilverPerKg, p.Source, p.SourceRef)
	return err
}
