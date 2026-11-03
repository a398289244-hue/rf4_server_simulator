package gamestore

// 制作配方静态表。它和物品定义、商店货架一样来自 MySQL，运行时不读快照。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Recipe 是 static_recipe 的一行。
type Recipe struct {
	RecipeID      string
	Name          string
	OutputID      string
	OutputQty     int64
	MaterialsJSON string
	DurationSec   int64
	Kind          string
}

// Recipe 按配方编号取配方；客户端偶尔会直接提交产出物编号，因此再按 output_id 兼容查一次。
func (m *MySQL) Recipe(ctx context.Context, key string) (Recipe, error) {
	var r Recipe
	if m == nil || m.db == nil {
		return r, errors.New("gamestore: 没接上 MySQL")
	}
	row := m.db.QueryRowContext(ctx, `
		SELECT recipe_id, COALESCE(name,''), COALESCE(output_id,''), COALESCE(output_qty,1),
		       COALESCE(materials_json,'[]'), COALESCE(duration_sec,0), COALESCE(kind,'')
		FROM static_recipe
		WHERE recipe_id = ? OR output_id = ? OR recipe_id = CONCAT('recipe_', ?)
		ORDER BY CASE WHEN recipe_id = ? THEN 0 WHEN output_id = ? THEN 1 ELSE 2 END
		LIMIT 1`, key, key, key, key, key)
	if err := row.Scan(&r.RecipeID, &r.Name, &r.OutputID, &r.OutputQty, &r.MaterialsJSON, &r.DurationSec, &r.Kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, fmt.Errorf("%w：制作配方 %s", ErrNotFound, key)
		}
		return r, fmt.Errorf("读取制作配方 %s 失败: %w", key, err)
	}
	return r, nil
}
