package gameserver

import (
	"context"
	"math"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// reelWearDelta 使用 0..1 耐久比例。数值表示本次要扣除的磨损，
// 正常一条鱼只会产生万分之几到千分之几，不再整档20%扣除。
type reelWearDelta struct {
	Spool, Drag, Bearing, Oil, Gear float64
	LineOutM, RetrieveM, DragWork   float64
}

func (d *reelWearDelta) add(v reelWearDelta) {
	d.Spool += v.Spool
	d.Drag += v.Drag
	d.Bearing += v.Bearing
	d.Oil += v.Oil
	d.Gear += v.Gear
	d.LineOutM += v.LineOutM
	d.RetrieveM += v.RetrieveM
	d.DragWork += v.DragWork
}

func (d reelWearDelta) empty() bool {
	return d.Spool <= 0 && d.Drag <= 0 && d.Bearing <= 0 && d.Oil <= 0 && d.Gear <= 0 &&
		d.LineOutM <= 0 && d.RetrieveM <= 0 && d.DragWork <= 0
}

func clampCondition(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// applyReelWear 原子地累计五个部件的耐久，并把加权结果同步到
// item.durability，这个综合值才是客户端轮子外层显示的耐久。
func (s *Server) applyReelWear(ctx context.Context, uid int64, rodGUID string, delta reelWearDelta, event string) (*store.ReelPerformance, error) {
	if s == nil || s.DB == nil || delta.empty() {
		return nil, nil
	}
	ids := s.rigGearIDs(ctx, uid, rodGUID)
	if ids.ReelGUID == "" {
		return nil, nil
	}
	return s.applyReelWearToReel(ctx, uid, rodGUID, ids.ReelGUID, delta, event)
}

// applyReelWearToReel 支持已明确知道轮子 GUID 的路径（例如 17/9 超载报告）。
// 不能强制依赖母竿投影，否则客户端只上报轮子时会漏记损耗。
func (s *Server) applyReelWearToReel(ctx context.Context, uid int64, rodGUID, reelGUID string, delta reelWearDelta, event string) (*store.ReelPerformance, error) {
	if s == nil || s.DB == nil || reelGUID == "" || reelGUID == zeroGUID || delta.empty() {
		return nil, nil
	}
	s.reelWearMu.Lock()
	defer s.reelWearMu.Unlock()
	reel, err := s.DB.GetItemByGUID(ctx, reelGUID)
	if err != nil || reel == nil || reel.UID != uid {
		return nil, err
	}
	before, err := s.syncReelPerformance(ctx, reel)
	if err != nil {
		return nil, err
	}
	// 润滑油品质和剩余状态决定金属运动件的实际磨损倍率。
	// 摩擦片有独立工作面，不用轴承/齿轮润滑油直接降低其损耗。
	lube := math.Max(.65, math.Min(1.45, before.OilWearFactor))
	delta.Spool *= .85 + .15*lube
	delta.Bearing *= lube
	delta.Gear *= lube
	delta.Oil *= lube
	after := *before
	after.SpoolDurability = clampCondition(before.SpoolDurability - math.Max(0, delta.Spool))
	after.DragDurability = clampCondition(before.DragDurability - math.Max(0, delta.Drag))
	after.BearingDurability = clampCondition(before.BearingDurability - math.Max(0, delta.Bearing))
	after.OilDurability = clampCondition(before.OilDurability - math.Max(0, delta.Oil))
	after.GearDurability = clampCondition(before.GearDurability - math.Max(0, delta.Gear))
	after.LineOutM += math.Max(0, delta.LineOutM)
	after.RetrieveM += math.Max(0, delta.RetrieveM)
	after.DragWork += math.Max(0, delta.DragWork)
	after.Durability = reelOverallDurability(after.SpoolDurability, after.DragDurability,
		after.BearingDurability, after.OilDurability, after.GearDurability)
	// 摩擦片耐久直接改变可承受公斤数；轴承状态则影响传力效率。
	after.BearingFactor = reelBearingFactor(after.BearingQuality) * (.85 + .15*after.BearingDurability)
	after.LockedDragKg = after.BaseDragKg * reelLockMultiplier(after.DragDurability) * after.BearingFactor
	effectiveOilQuality := after.OilQuality * after.OilDurability
	after.OilWearFactor = reelOilWearFactor(effectiveOilQuality)
	after.CastFriction = reelCastFriction(after.BearingQuality*after.BearingDurability,
		effectiveOilQuality, after.SpoolDurability)
	after.CastEfficiency = 1 - after.CastFriction
	after.UpdatedTS = float64(time.Now().Unix())
	if err := s.DB.UpsertReelPerformance(ctx, after); err != nil {
		return nil, err
	}
	if math.Abs(reel.Durability-after.Durability) > 1e-8 {
		if err := s.persistItemDurability(ctx, reel, after.Durability); err != nil {
			return nil, err
		}
	}
	if event != "" {
		_ = s.DB.AddItemDamageLog(ctx, store.ItemDamageLog{
			UID: uid, ItemGUID: reel.GUID, ItemID: reel.ItemID, Event: event,
			BeforeDurability: ptrFloat64(before.Durability), AfterDurability: ptrFloat64(after.Durability),
			DetailJSON: damageDetailJSON(map[string]any{
				"rod_guid": rodGUID, "spool": after.SpoolDurability, "drag": after.DragDurability,
				"bearing": after.BearingDurability, "oil": after.OilDurability, "gear": after.GearDurability,
				"locked_drag_kg": after.LockedDragKg, "line_out_m": delta.LineOutM,
				"retrieve_m": delta.RetrieveM, "drag_work": delta.DragWork,
			}),
		})
	}
	return &after, nil
}

func castReelWear(distanceM, powerRatio float64) reelWearDelta {
	distanceM = math.Max(0, math.Min(250, distanceM))
	powerRatio = math.Max(.1, math.Min(1.5, powerRatio))
	load := powerRatio * powerRatio
	return reelWearDelta{
		Spool:   distanceM * .0000025 * load,
		Bearing: distanceM * .0000015 * load,
		Oil:     distanceM * .0000008 * load,
	}
}

// fightReelWear 按客户端实际线长变化积分。正值为鱼拉出线，
// 负值为玩家收线；不锁轮时大鱼会泄线，主要磨摩擦制动器。
func fightReelWear(distanceDelta, tensionKg, dragCapacityKg float64) reelWearDelta {
	if math.Abs(distanceDelta) < .002 || tensionKg <= 0 {
		return reelWearDelta{}
	}
	capacity := math.Max(.1, dragCapacityKg)
	load := math.Max(.05, math.Min(2.5, tensionKg/capacity))
	meters := math.Abs(distanceDelta)
	if distanceDelta > 0 {
		work := tensionKg * meters
		return reelWearDelta{
			Spool:    meters * .0000015 * load,
			Drag:     meters * .000012 * math.Pow(load, 1.55),
			Bearing:  meters * .0000025 * load,
			Oil:      meters * .0000035 * load,
			LineOutM: meters, DragWork: work,
		}
	}
	return reelWearDelta{
		Spool:     meters * .0000005 * load,
		Bearing:   meters * .0000025 * load,
		Oil:       meters * .000003 * load,
		Gear:      meters * .000006 * load * load,
		RetrieveM: meters,
	}
}

func overloadReelWear(overloadRatio float64) reelWearDelta {
	overloadRatio = math.Max(0, math.Min(3, overloadRatio))
	severity := 1 + overloadRatio*overloadRatio
	return reelWearDelta{
		Spool:   .00015 * severity,
		Drag:    .0012 * severity,
		Bearing: .00035 * severity,
		Oil:     .00055 * severity,
		Gear:    .00065 * severity,
	}
}

func pullShockReelWear(loadRatio float64) reelWearDelta {
	loadRatio = math.Max(.1, math.Min(2.5, loadRatio))
	return reelWearDelta{
		Drag:    .000006 * loadRatio,
		Bearing: .000008 * loadRatio,
		Oil:     .000004 * loadRatio,
		Gear:    .000012 * loadRatio * loadRatio,
	}
}

// recordFightStep 使用 14/7.f9 的实际钓组距离判断泄线与收线。
// 每两秒批量落库一次，避免逐帧写 SQLite。
func (s *Server) recordFightStep(sess *Session, o *proto.Value) {
	if s == nil || sess == nil || o == nil || o.FieldCount() <= 9 || o.Field(9).Num == nil {
		return
	}
	distance := float64(math.Float32frombits(uint32(u64Of(o.Field(9).Num))))
	if math.IsNaN(distance) || math.IsInf(distance, 0) || distance < 0 || distance >= 5000 {
		return
	}
	now := time.Now()
	st := s.stateFor(sess.Userid)
	st.mu.Lock()
	if !st.FightDistanceSeen {
		st.FightLastDistance, st.FightDistanceSeen, st.FightLastTick = distance, true, now
		st.mu.Unlock()
		return
	}
	if !st.Hooked || st.Pending == nil || st.Resolved {
		st.FightLastDistance, st.FightLastTick = distance, now
		st.mu.Unlock()
		return
	}
	dt := now.Sub(st.FightLastTick)
	if dt < 0 || dt > time.Second {
		dt = time.Second
	}
	if st.FightStamina <= 0 {
		st.FightStamina = 1
	}
	st.FightElapsed += dt
	distanceDelta := distance - st.FightLastDistance
	weightKg := math.Max(.05, float64(st.Pending.WeightG)/1000)
	burst := .5 + .5*math.Sin(st.FightElapsed.Seconds()*.73+weightKg*.19)
	tension := weightKg * (.18 + .42*st.FightStamina + .25*burst)
	if now.Sub(st.FightLastPull) < 900*time.Millisecond {
		tension *= 1.12
	}
	if distanceDelta > 0 && st.FightDragCapacity > 0 {
		// 未锁轮时超出制动力的部分通过泄线释放，不应直接爆轮。
		tension = math.Min(tension, st.FightDragCapacity*1.02)
	}
	st.FightLastTension = tension
	capacity := st.FightDragCapacity
	if capacity <= 0 {
		capacity = math.Max(.1, weightKg*.5)
	}
	st.FightWear.add(fightReelWear(distanceDelta, tension, capacity))
	fatigue := .00035
	if distanceDelta > .002 {
		fatigue = .00075
	} else if distanceDelta < -.002 {
		fatigue = .0013
	}
	st.FightStamina = math.Max(.05, st.FightStamina-dt.Seconds()*fatigue)
	st.FightLastDistance, st.FightLastTick = distance, now
	if st.FightWearSince.IsZero() {
		st.FightWearSince = now
	}
	rod := st.GearGuid
	flush := now.Sub(st.FightWearSince) >= 2*time.Second
	wear := reelWearDelta{}
	if flush {
		wear, st.FightWear = st.FightWear, reelWearDelta{}
		st.FightWearSince = now
	}
	st.mu.Unlock()
	if flush && !wear.empty() {
		if _, err := s.applyReelWear(sess.pushCtx(), sess.Userid, rod, wear, ""); err != nil {
			s.log.Warn("搏鱼渔轮磨损未保存", "err", err)
		}
	}
}

func (s *Server) startReelFight(sess *Session, st *FishState, rod string) {
	now := time.Now()
	ids := s.rigGearIDs(sess.pushCtx(), sess.Userid, rod)
	spec := s.loadGearSpecForRig(sess.pushCtx(), ids, rod)
	st.mu.Lock()
	st.FightElapsed = 0
	st.FightStamina = 1
	st.FightLastTick = now
	st.FightWearSince = now
	st.FightWear = reelWearDelta{}
	st.FightDragCapacity = spec.ReelDragKg
	st.FightLastTension = 0
	st.mu.Unlock()
}

func (s *Server) recordPullShock(sess *Session, st *FishState) {
	if s == nil || sess == nil || st == nil {
		return
	}
	st.mu.Lock()
	st.FightLastPull = time.Now()
	rod, tension, capacity, hooked := st.GearGuid, st.FightLastTension, st.FightDragCapacity, st.Hooked
	st.mu.Unlock()
	if !hooked || rod == "" {
		return
	}
	load := 1.0
	if capacity > 0 && tension > 0 {
		load = tension / capacity
	}
	if _, err := s.applyReelWear(sess.pushCtx(), sess.Userid, rod, pullShockReelWear(load), ""); err != nil {
		s.log.Warn("抬竿冲击磨损未保存", "err", err)
	}
}

func (s *Server) flushReelFightWear(sess *Session, st *FishState) {
	if s == nil || sess == nil || st == nil {
		return
	}
	st.mu.Lock()
	wear, rod := st.FightWear, st.GearGuid
	st.FightWear = reelWearDelta{}
	st.FightWearSince = time.Now()
	st.mu.Unlock()
	if !wear.empty() {
		if _, err := s.applyReelWear(sess.pushCtx(), sess.Userid, rod, wear, ""); err != nil {
			s.log.Warn("搏鱼渔轮磨损未保存", "err", err)
		}
	}
}
