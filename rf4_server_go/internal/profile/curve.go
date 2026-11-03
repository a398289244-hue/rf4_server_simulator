package profile

// 等级 ↔ 经验 ↔ 技能点 这三条曲线是从 Node 版 src/skills.js 搬过来的，
// 用来给档案帧里「经验/下一级所需」那几个数字找个说得圆的来源。
//
// 丑话写在前面：这条曲线和真服不是严格对得上 —— 真服 2026-10-31 那帧里
// 20 级那位老号 #03 是 522048，而本曲线 baseXp(20) 给的是 800000；
// 40 级这边真号实际是 105,969,536，本曲线给 106,000,000。
// 也就是说真服的 XP 是「玩家真实累计值」，曲线只是阈值表。
// 所以这些数字只当界面显示用，别当真理；哪天要精确，就把真号 XP 存进 MySQL 直接读。

// MaxLevel 等级上限（真服表就到 58 级）
const MaxLevel = 58

// MasteryCap 熟练度上限：客户端两处都按 110 截断（真服 190 个节点的解锁位就是这么算的）
const MasteryCap = 110

// levelXPReq[i] = 从 (i+1) 级升到 (i+2) 级需要的经验增量。
// 一共 57 项（1 级往上一路到 58 级封顶），逐字节抄自凭据 level_xp_req.json，
// 和 Node 版 src/skills.js 的 LEVEL_XP_REQ 同一份（测试 TestLevelXPTableMatchesCredential 盯着）。
var levelXPReq = []uint64{
	300, 400, 500, 800, 1000, 1500, 3000, 5000, 7500, 11000,
	18000, 31000, 45000, 55000, 80000, 90000, 100000, 150000, 200000, 400000,
	600000, 800000, 900000, 1000000, 1300000, 1500000, 1900000, 2200000, 2500000, 3000000,
	4100000, 5000000, 6000000, 7000000, 9000000, 11000000, 13000000, 15000000, 19000000, 22000000,
	45000000, 58500000, 67500000, 75000000, 90000000, 112000000, 135000000, 180000000, 240000000, 300000000,
	380000000, 480000000, 600000000, 740000000, 890000000, 1060000000, 1250000000,
}

// clampLevel 把等级压进 [1, MaxLevel]，免得越界或脏数据把帧写歪
func clampLevel(level int) int {
	if level < 1 {
		return 1
	}
	if level > MaxLevel {
		return MaxLevel
	}
	return level
}

// BaseXP 到本级为止累计需要的经验（= 前面所有等级增量之和）
func BaseXP(level int) uint64 {
	lv := clampLevel(level)
	var sum uint64
	for i := 0; i <= lv-2 && i < len(levelXPReq); i++ {
		sum += levelXPReq[i]
	}
	return sum
}

// NextLevelReq 升下一级要多少经验
func NextLevelReq(level int) uint64 {
	i := clampLevel(level) - 1
	if i < 0 {
		return levelXPReq[0]
	}
	if i >= len(levelXPReq) {
		return levelXPReq[len(levelXPReq)-1]
	}
	return levelXPReq[i]
}

// skillPointsForLevel 每级给几点：<20 级 1 点、<30 级 2 点、<40 级 3 点、<50 级 4 点、其余 5 点
func skillPointsForLevel(level int) int {
	switch {
	case level < 20:
		return 1
	case level < 30:
		return 2
	case level < 40:
		return 3
	case level < 50:
		return 4
	default:
		return 5
	}
}

// SkillPointsTotal 按等级累计「应该发过多少技能点」。
// 注意：少爷把 return2 的发放点手动改成 100 了（MySQL skill_points 表），
// 数据库里有的数以库为准，这个函数只在库里没记录时兜底。
func SkillPointsTotal(level int) int {
	lv := clampLevel(level)
	sum := 0
	for l := 2; l <= lv; l++ {
		sum += skillPointsForLevel(l)
	}
	return sum
}
