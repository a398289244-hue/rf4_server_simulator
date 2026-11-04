package store

import (
	"context"
	"time"
)

// ReelPerformance 保存渔线轮当前改装状态对应的服务端物理参数。
type ReelPerformance struct {
	ReelGUID          string
	UID               int64
	BaseDragKg        float64
	LockedDragKg      float64
	Durability        float64
	BearingItemID     string
	BearingQuality    float64
	BearingFactor     float64
	OilItemID         string
	OilQuality        float64
	OilWearFactor     float64
	CastFriction      float64
	CastEfficiency    float64
	WearProgress      float64
	SpoolDurability   float64
	DragDurability    float64
	BearingDurability float64
	OilDurability     float64
	GearDurability    float64
	LineOutM          float64
	RetrieveM         float64
	DragWork          float64
	UpdatedTS         float64
}

// UpsertReelPerformance 写入或刷新一只渔线轮的改装后参数。
func (d *DB) UpsertReelPerformance(ctx context.Context, v ReelPerformance) error {
	if v.UpdatedTS <= 0 {
		v.UpdatedTS = float64(time.Now().Unix())
	}
	_, err := d.SQL.ExecContext(ctx, `
		INSERT INTO reel_performance(
			reel_guid, uid, base_drag_kg, locked_drag_kg, durability,
			bearing_item_id, bearing_quality, bearing_factor,
			oil_item_id, oil_quality, oil_wear_factor,
			cast_friction, cast_efficiency, wear_progress,
			spool_durability, drag_durability, bearing_durability, oil_durability, gear_durability,
			line_out_m, retrieve_m, drag_work, updated_ts
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(reel_guid) DO UPDATE SET
			uid=excluded.uid,
			base_drag_kg=excluded.base_drag_kg,
			locked_drag_kg=excluded.locked_drag_kg,
			durability=excluded.durability,
			bearing_item_id=excluded.bearing_item_id,
			bearing_quality=excluded.bearing_quality,
			bearing_factor=excluded.bearing_factor,
			oil_item_id=excluded.oil_item_id,
			oil_quality=excluded.oil_quality,
			oil_wear_factor=excluded.oil_wear_factor,
			cast_friction=excluded.cast_friction,
			cast_efficiency=excluded.cast_efficiency,
			wear_progress=excluded.wear_progress,
			spool_durability=excluded.spool_durability,
			drag_durability=excluded.drag_durability,
			bearing_durability=excluded.bearing_durability,
			oil_durability=excluded.oil_durability,
			gear_durability=excluded.gear_durability,
			line_out_m=excluded.line_out_m,
			retrieve_m=excluded.retrieve_m,
			drag_work=excluded.drag_work,
			updated_ts=excluded.updated_ts`,
		v.ReelGUID, v.UID, v.BaseDragKg, v.LockedDragKg, v.Durability,
		v.BearingItemID, v.BearingQuality, v.BearingFactor,
		v.OilItemID, v.OilQuality, v.OilWearFactor,
		v.CastFriction, v.CastEfficiency, v.WearProgress,
		v.SpoolDurability, v.DragDurability, v.BearingDurability, v.OilDurability, v.GearDurability,
		v.LineOutM, v.RetrieveM, v.DragWork, v.UpdatedTS)
	return err
}

// GetReelPerformance 读取一只渔线轮最近一次计算出的物理参数。
func (d *DB) GetReelPerformance(ctx context.Context, reelGUID string) (*ReelPerformance, error) {
	var v ReelPerformance
	err := d.SQL.QueryRowContext(ctx, `
		SELECT reel_guid, uid, base_drag_kg, locked_drag_kg, durability,
		       bearing_item_id, bearing_quality, bearing_factor,
		       oil_item_id, oil_quality, oil_wear_factor,
		       cast_friction, cast_efficiency, wear_progress,
		       spool_durability, drag_durability, bearing_durability, oil_durability, gear_durability,
		       line_out_m, retrieve_m, drag_work, updated_ts
		FROM reel_performance WHERE reel_guid=?`, reelGUID).Scan(
		&v.ReelGUID, &v.UID, &v.BaseDragKg, &v.LockedDragKg, &v.Durability,
		&v.BearingItemID, &v.BearingQuality, &v.BearingFactor,
		&v.OilItemID, &v.OilQuality, &v.OilWearFactor,
		&v.CastFriction, &v.CastEfficiency, &v.WearProgress,
		&v.SpoolDurability, &v.DragDurability, &v.BearingDurability, &v.OilDurability, &v.GearDurability,
		&v.LineOutM, &v.RetrieveM, &v.DragWork, &v.UpdatedTS)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// ResetReelComponentWear 在工坊整体修理轮子后恢复五个部件状态。
func (d *DB) ResetReelComponentWear(ctx context.Context, reelGUID string) error {
	_, err := d.SQL.ExecContext(ctx, `UPDATE reel_performance SET
		spool_durability=1, drag_durability=1, bearing_durability=1,
		oil_durability=1, gear_durability=1, wear_progress=0, durability=1,
		updated_ts=? WHERE reel_guid=?`, float64(time.Now().Unix()), reelGUID)
	return err
}
