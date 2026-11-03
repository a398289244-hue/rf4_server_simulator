package fishing

import (
	"math"
	"math/rand"

	"rf4server/internal/itemwire"
)

// 大小档（14/14 推送 obj27 的 #02、鱼卡片 obj25 的 #03，客户端按它显示 小/中/大/特大）。
// 凭据：真服那条 ruffe 那帧 #02 = 2；字段表注释写了 1=Small 2=Medium 3=Big 4=Trophy。
const (
	ClassSmall  byte = 1 // 小
	ClassMedium byte = 2 // 中
	ClassBig    byte = 3 // 大
	ClassTrophy byte = 4 // 特大
)

// TrophyFlagOne 鱼卡片 obj25 的 #07：字段表注释是「cat>=2」，也就是中档以上就置 1。
// 凭据：真服那条 #03=2、#07=1，两格互相印证。
func TrophyFlag(class byte) byte {
	if class >= ClassMedium {
		return 1
	}
	return 0
}

// Fish 一条已经决定好的鱼：什么鱼、多重、多长、算哪一档、实例 GUID 是哪个。
// 这一坨就是「这次咬钩的全部事实」，14/14 推送、鱼卡片、写库三处都从它出，保证不打架。
type Fish struct {
	// Key 鱼种编号（ruffe、bream、s.orfe…）。来源 static_map_fish_roster。
	Key string
	// Grams 体重（克，整数）。发包时 obj25#01、obj27#04 都是这个整数。
	Grams int64
	// LengthM 体长（米，float32 存进 u32）。按 static_species_weight 的 K/P 现算。
	LengthM float64
	// Class 大小档（见上面那四个常量）。
	Class byte
	// SetupGUID 这条鱼的实例 GUID（32 位十六进制，去横线小写）。
	// 我们生成的：14/14 推送 #00 发出去，客户端在 14/11、14/5 里原样回显，
	// 鱼卡片 obj25#16 也填它 —— 三处必须是同一个数，否则客户端认不出这是同一条鱼。
	SetupGUID string
	// Stamina 搏鱼体力池（obj27#05）。0 = 按体重现算（setupStaminaPerGram）。
	// 实测两条真服样本换算下来不是同一个比例（63 克那条 5.80/克、53 克那条 2.39/克），
	// 说明这格是按鱼种给的，不是纯按体重线性换算 —— 等解剖到更多鱼种再换成按鱼种查表。
	Stamina float64
	// Force 拉力参数（obj27#08）。0 = 按体重现算。这个是真按比例：
	// 两条真服样本（63 克 -> 0.61781895、53 克 -> 0.51975244）换算下来都是 0.0098067/克，
	// 所以这条按体重线性发，两个样本都能逐字节对上。
	Force float64
	// ModelGUID obj27#23 那个「判鱼模型附加 guid」。真服每帧都不一样，含义还没查实，
	// 这边按 SetupGUID 派生（同一条鱼重登也不会变），比纯随机更好对答案。
	ModelGUID string
	// HookGUID 是当前钓组线末端鱼钩的真实实例 GUID。客户端拿 obj27#23 去钓组里
	// 找鱼挂在哪个钩上；发成鱼自己的随机 GUID 时会立刻回 14/12 拒绝这条鱼。
	HookGUID string
}

// Picker 负责「这一竿子上钩了什么」。水域、必出鱼种、随机源都能从外面塞进来，
// 所以单测能用固定随机数复现结果，实机能按少爷指定的鱼种强制出鱼。
type Picker struct {
	cat    *Catalog   // 鱼种 + 水域名册
	loc    string     // 这次按哪个水域的名册出鱼（空 = 不做名册限制，全鱼种里挑）
	forced string     // 强制出哪一种（--fish-key，测某条鱼时用；名册不管它）
	rnd    *rand.Rand // 随机源，注入是为了测试可复现
}

// NewPicker 造一个出鱼器。rnd 传 nil 就用全局随机。
func NewPicker(cat *Catalog, loc, forced string, rnd *rand.Rand) *Picker {
	if rnd == nil {
		rnd = rand.New(rand.NewSource(rand.Int63()))
	}
	return &Picker{cat: cat, loc: loc, forced: forced, rnd: rnd}
}

// Loc 这个出鱼器绑的水域（日志用）。
func (p *Picker) Loc() string {
	if p == nil {
		return ""
	}
	return p.loc
}

// Pick 挑一条鱼。返回 ok=false 说明鱼种表还没装上（多半是没连上 MySQL 或者库是空的），
// 调用方就该记一行「这次没法出鱼」，而不是硬发一条没头没尾的推送。
func (p *Picker) Pick() (Fish, bool) {
	if p == nil || p.cat == nil {
		return Fish{}, false
	}

	// 1) 候选名单：指定鱼种优先；绑定了水域就严格只查该水域名册。
	//   只有 loc 为空时（明确的全鱼种模式）才允许从全部鱼种里挑。
	var candidates []string
	if p.forced != "" {
		candidates = []string{p.forced}
	} else if p.loc != "" {
		candidates = p.cat.Roster(p.loc)
	} else {
		candidates = p.cat.allSpeciesKeys()
	}
	if len(candidates) == 0 {
		return Fish{}, false
	}

	// 2) 名单里先只留「库里给了体重范围」的那些（这些才能随机出鱼）；
	//    一个都没有（整片水域都是 NULL 范围，或者指定了这么一种鱼）就退回整份名单。
	usable := make([]string, 0, len(candidates))
	for _, k := range candidates {
		if sp, ok := p.cat.GetSpecies(k); ok && sp.HasRange {
			usable = append(usable, k)
		}
	}
	if len(usable) == 0 {
		for _, k := range candidates {
			if _, ok := p.cat.GetSpecies(k); ok {
				usable = append(usable, k)
			}
		}
	}
	if len(usable) == 0 {
		return Fish{}, false
	}
	sp, ok := p.cat.GetSpecies(usable[p.rnd.Intn(len(usable))])
	if !ok {
		return Fish{}, false
	}

	// 体重一旦定了，体长和大小档都由它派生，三处不能再各抽一次随机
	grams := p.pickGrams(sp)
	f := Fish{
		Key:       sp.Key,
		Grams:     grams,
		LengthM:   LengthMeters(sp, grams),
		Class:     SizeClass(sp, grams),
		SetupGUID: itemwire.NewGUID(),
	}
	f.ModelGUID = itemwire.ChildGUID(f.SetupGUID, "fishmodel")
	return f, true
}

// pickGrams 在 [MinG, MaxG] 里抽一个体重。真随机会在两端留得少：
// 这里抽两次取平均（三角分布），中间厚两端薄，钓起来才不像每次都出极端值。
// 库里没范围就退到奖杯线附近（有 trophy_line_g 就用它当上限）。
func (p *Picker) pickGrams(sp Species) int64 {
	lo, hi := sp.MinG, sp.MaxG
	if !sp.HasRange || hi <= lo {
		if sp.TrophyLineG > 0 {
			return 1 + p.rnd.Int63n(sp.TrophyLineG) // 没范围就在奖杯线以下随便来
		}
		return 50 + p.rnd.Int63n(450) // 最后兜底：50~499 克
	}
	a := lo + p.rnd.Int63n(hi-lo+1)
	b := lo + p.rnd.Int63n(hi-lo+1)
	if a >= b {
		return a
	}
	return b
}

// LengthMeters 体重换算体长：米 = K * 克^P。系数来自 static_species_weight，少爷能直接改库。
func LengthMeters(sp Species, grams int64) float64 {
	if grams <= 0 {
		return 0
	}
	k, pw := sp.LengthK, sp.LengthP
	if k <= 0 || pw <= 0 {
		k, pw = 0.0355, 1.0/3.0 // 库里那两列的实测值，缺了就用它
	}
	return k * math.Pow(float64(grams), pw)
}

// SizeClass 体重定大小档。
//
// 规则是这么定的（说实话这一条还待校准，先记清楚）：
//
//	>= super_trophy_line_g -> 特大(4)
//	>= trophy_line_g       -> 大(3)
//	>= 下限 + (奖杯线-下限)*0.3 -> 中(2)
//	其余                    -> 小(1)
//
// 凭据：真服那条 ruffe 63 克（min 20 / trophy_line 140）标的是 2 档，按这条算正好是 2。
// 14/5 那条 53 克也标 2 档，按这条算是 1 —— 阈值肯定还能再准一点，
// 但这两帧本来就不是同一条鱼（14/14 那条体重格和体长格互相对不上），先照这条走。
func SizeClass(sp Species, grams int64) byte {
	if sp.SuperTrophyLineG > 0 && grams >= sp.SuperTrophyLineG {
		return ClassTrophy
	}
	if sp.TrophyLineG > 0 && grams >= sp.TrophyLineG {
		return ClassBig
	}
	lo := sp.MinG
	if lo <= 0 {
		lo = 0
	}
	line := sp.TrophyLineG
	if line > lo && grams >= lo+(line-lo)*3/10 {
		return ClassMedium
	}
	return ClassSmall
}
