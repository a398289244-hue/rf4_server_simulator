package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestEconomyPriceDefinitionsMigrateAndOverride(t *testing.T) {
	db, err := OpenWithStatic(filepath.Join(t.TempDir(), "price-defs.db"), filepath.Join("..", "..", "server_data", "static.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var fishRows, shopRows int
	if err := db.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM fish_market_price_def`).Scan(&fishRows); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM shop_price_def`).Scan(&shopRows); err != nil {
		t.Fatal(err)
	}
	if fishRows != 1004 || shopRows != 25644 {
		t.Fatalf("迁移价格种子数量异常: fish=%d shop=%d", fishRows, shopRows)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}
	var rerunFishRows, rerunShopRows int
	if err := db.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM fish_market_price_def`).Scan(&rerunFishRows); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM shop_price_def`).Scan(&rerunShopRows); err != nil {
		t.Fatal(err)
	}
	if rerunFishRows != fishRows || rerunShopRows != shopRows {
		t.Fatalf("重复迁移重复写入价格种子: fish=%d/%d shop=%d/%d", rerunFishRows, fishRows, rerunShopRows, shopRows)
	}

	if err := db.UpsertFishMarketPriceDef(ctx, FishMarketPriceDef{
		MapKey: "price_test", FishKey: "test_fish", Grade: 2, WeightMinG: 0, WeightMaxG: 1000,
		SilverPerKg: 10, Source: "official_capture", SourceRef: "capture-a",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertFishMarketPriceDef(ctx, FishMarketPriceDef{
		MapKey: "price_test", FishKey: "test_fish", Grade: 2, WeightMinG: 1000,
		SilverPerKg: 20, Source: "official_capture", SourceRef: "capture-b",
	}); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := db.FishMarketPricePerKg(ctx, "price_test", "test_fish", 2, 999); err != nil || !ok || got != 10 {
		t.Fatalf("低重量段价格=%v found=%v err=%v", got, ok, err)
	}
	if got, ok, err := db.FishMarketPricePerKg(ctx, "price_test", "test_fish", 2, 1000); err != nil || !ok || got != 20 {
		t.Fatalf("高重量段价格=%v found=%v err=%v", got, ok, err)
	}
	if _, ok, err := db.FishMarketPricePerKg(ctx, "price_test", "test_fish", 3, 1000); err != nil || ok {
		t.Fatalf("不存在档位不应命中: found=%v err=%v", ok, err)
	}

	var storeID, itemID string
	if err := db.SQL.QueryRowContext(ctx, `SELECT store_id,item_id FROM shop_price_def ORDER BY store_id,item_id LIMIT 1`).Scan(&storeID, &itemID); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertShopPriceDef(ctx, ShopPriceDef{
		StoreID: storeID, ItemID: itemID, Silver: 123456, Gold: 7, Stock: 8,
		Source: "official_capture", SourceRef: "capture-shop",
	}); err != nil {
		t.Fatal(err)
	}
	price, err := db.ShopItemGet(ctx, storeID, itemID)
	if err != nil || price.Silver != 123456 || price.Gold != 7 || price.Stock != 8 {
		t.Fatalf("逐店表价未覆盖静态快照: price=%+v err=%v", price, err)
	}
}
