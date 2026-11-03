package fishing

import (
	"context"
	"errors"

	"rf4server/internal/gamestore"
	"sort"
	"sync"
)

// 鱼种数据这一层只管「把 MySQL 里那两张静态表变成能查的表」，不做随机、不做拼帧。
//
// 数据来源（少爷定的：数据都要在库里能直接看到直接改）：
//
//	static_species_weight   每个鱼种的常见体重范围、奖杯线、体长换算系数（实测 252 个鱼种）
//	static_map_fish_roster  每个水域里有哪些鱼（实测 614 行、12 个水域）
//
// 两张表都只读。哪天要在游戏里换鱼：直接 UPDATE 这两张表，重启后端（或者调 Reload）就生效。

// ErrNoStore 没接上数据库（--mysql=false 那种对照跑法），鱼种表无从读起。
var ErrNoStore = errors.New("fishing: 没接上数据库，鱼种表读不出来")

// Species 一个鱼种的关键数字。字段名和库里那一列一一对应，别在这儿加工。
type Species struct {
	// Key 鱼种编号（static_species_weight.fish_key），14/14 推送里的 #01 就是它。
	Key string
	// MinG / MaxG 常见体重范围（克）。库里这两列可空（例如 ruffe_n 就是 NULL），
	// 空的话 HasRange=false，这种鱼只能被指定，不参与随机。
	MinG int64
	MaxG int64
	// TrophyLineG / SuperTrophyLineG 体重大于等于这两个数就升档（Big / Trophy）。
	TrophyLineG      int64
	SuperTrophyLineG int64
	// LengthK / LengthP 体长换算：体长(米) = K * 克^P。库里给的是 0.0355 和 1/3，
	// 跟真服那条 14/5 的鱼卡片（53 克 -> 0.13147 米）差 0.03%，够用。
	LengthK float64
	LengthP float64
	// HasRange 库里到底给没给体重范围（NULL = 没给）。
	HasRange bool
}

// Store 钓鱼这一域要的全部数据操作，gamestore.MySQL 都有实现。
// 单独写这个小接口是为了：单测能塞假鱼种假流水、不用起库；以后换库只动 gamestore 那一处。
type Store interface {
	// SpeciesWeights 读全部鱼种的体重/体长定义（static_species_weight）。
	SpeciesWeights(ctx context.Context) (map[string]gamestore.SpeciesWeight, error)
	// Roster 读「哪个水域有哪些鱼」（static_map_fish_roster），键是水域 id。
	Roster(ctx context.Context) (map[string][]string, error)
	// SaveKeepnet 往这个号的鱼护里加一条（keepnets 表）。
	SaveKeepnet(ctx context.Context, username string, row gamestore.KeepnetRow) error
	// Keepnet 读这个号的鱼护；4/4、4/5 登录同步靠它恢复鱼 GUID 和鱼卡片。
	Keepnet(ctx context.Context, username string) ([]gamestore.KeepnetRow, error)
	// InsertFishRecord 写一条渔获流水（fish_records 表）。
	InsertFishRecord(ctx context.Context, r gamestore.FishRecord) error
	// Objects 这个号名下所有货。4/22 那条「装备更新了」的推送要用它取装备记录。
	Objects(ctx context.Context, username string) ([]gamestore.Object, error)
	// Account 取账号（渔获流水里那个 user_id 要的是数字 id，不是登录名）。
	Account(ctx context.Context, username string) (gamestore.Account, error)
}

// Catalog 鱼种表 + 水域名册的内存缓存。启动时装一次，之后基本只读；
// 少爷改了库里的表想立刻生效就调 Reload，不用重启后端。
type Catalog struct {
	st Store // 数据入口（构造时注入，自己不连库）

	mu      sync.RWMutex        // 保护下面三份缓存：Reload 写、钓鱼那边读
	species map[string]Species  // fish_key -> 定义
	rosters map[string][]string // 水域 id -> 这个水域能钓到的鱼种
	locs    []string            // 有哪些水域（排过序，开机日志和命令行提示用）
}

// NewCatalog 造一份空缓存。真正的数据要再调 Reload 才装进来。
func NewCatalog(st Store) *Catalog {
	return &Catalog{
		st:      st,
		species: map[string]Species{},
		rosters: map[string][]string{},
	}
}

// Reload 把两张静态表重新读一遍，返回「几个鱼种、几个水域」给开机日志。
// 读失败就把老数据留着：宁可拿旧表继续钓，也别因为一次查库失败把钓鱼整域干掉。
func (c *Catalog) Reload(ctx context.Context) (nSpecies, nLocs int, err error) {
	if c == nil || c.st == nil {
		return 0, 0, ErrNoStore
	}
	raw, err := c.st.SpeciesWeights(ctx)
	if err != nil {
		return 0, 0, err
	}
	// 库里的行变成域内的 Species：换算这一步放这儿，业务代码就不认 gamestore 的结构了
	sp := make(map[string]Species, len(raw))
	for _, w := range raw {
		sp[w.FishKey] = Species{
			Key:              w.FishKey,
			MinG:             w.MinG,
			MaxG:             w.MaxG,
			TrophyLineG:      w.TrophyLineG,
			SuperTrophyLineG: w.SuperTrophyLineG,
			LengthK:          w.LengthK,
			LengthP:          w.LengthP,
			HasRange:         w.HasRange,
		}
	}
	rs, err := c.st.Roster(ctx)
	if err != nil {
		return len(sp), 0, err
	}
	locs := make([]string, 0, len(rs))
	for loc, keys := range rs {
		if len(keys) > 0 {
			locs = append(locs, loc)
		}
	}
	sort.Strings(locs)

	c.mu.Lock()
	c.species, c.rosters, c.locs = sp, rs, locs
	c.mu.Unlock()
	return len(sp), len(locs), nil
}

// GetSpecies 按编号取一个鱼种定义。
func (c *Catalog) GetSpecies(key string) (Species, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.species[key]
	return s, ok
}

// Locs 有哪些水域（开机日志给少爷看一眼，命令行 --fish-loc 才知道填什么）。
func (c *Catalog) Locs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.locs...)
}

// Roster 这个水域能钓到哪些鱼。水域没名册就返回空。
func (c *Catalog) Roster(loc string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.rosters[loc]...)
}

// allSpeciesKeys 全部鱼种编号（排过序，水域没名册时兜底用）。
func (c *Catalog) allSpeciesKeys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.species))
	for k := range c.species {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Count 缓存里现在有多少鱼种 / 多少水域（开机日志用）。
func (c *Catalog) Count() (int, int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.species), len(c.locs)
}
