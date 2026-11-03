// Package baitdig 处理铲子和红虫长柄勺挖饵。
//
// 数据规则：
//   - 工具：tool_shovel（铲子）、tool_scoop（红虫长柄勺）
//   - 技能：大类 4「捕获鱼饵」
//   - 每次使用如果熟练度未满 100%，增加 0.1%
//   - 技能阶段越高，可挖到的饵种类越多，数量也略微增加
//
// 所有产物都是标准物品记录，写入 MySQL objects 后再推 4/22。
package baitdig

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"rf4server/internal/gamestore"
	"rf4server/internal/itemdef"
	"rf4server/internal/itemstack"
	"rf4server/internal/itemwire"
)

const (
	SkillCaptureBait = 4
	ToolShovel       = "tool_shovel"
	ToolScoop        = "tool_scoop"
	MasteryCap       = 100.0
	MasteryPerDig    = 0.1
)

// Store 挖饵只依赖这三个数据库操作，便于测试时替换。
type Store interface {
	SkillState(ctx context.Context, username string) (gamestore.SkillState, error)
	SetMastery(ctx context.Context, username string, cat int, points float64) error
	Objects(ctx context.Context, username string) ([]gamestore.Object, error)
	InsertObject(ctx context.Context, o gamestore.Object) error
	UpdateObjectElem(ctx context.Context, username, guid, elemHex string) (bool, error)
}

// Result 一次挖饵结果。
type Result struct {
	ToolKey       string
	BaitKey       string
	Quantity      int
	StackQuantity uint64
	Records       [][]byte
	GUIDs         []string
	MasteryBefore float64
	MasteryAfter  float64
	Warning       string
}

// Service 挖饵服务。
type Service struct {
	defs  *itemdef.Defs
	iw    *itemwire.Service
	run   Store
	stack *itemstack.Service
	rng   func(int) int
}

// New 造服务。
func New(defs *itemdef.Defs, iw *itemwire.Service, run Store) *Service {
	return &Service{defs: defs, iw: iw, run: run, stack: itemstack.New(iw, run), rng: rand.Intn}
}

// WithRand 注入随机源，测试用。
func (s *Service) WithRand(rng func(int) int) *Service {
	if s != nil && rng != nil {
		s.rng = rng
	}
	return s
}

// Handles 判断这个工具是否走挖饵逻辑。
func (s *Service) Handles(toolKey string) bool {
	k := strings.ToLower(strings.TrimSpace(toolKey))
	return k == ToolShovel || k == ToolScoop
}

type loot struct {
	key    string
	weight int
}
type band struct {
	min   int
	table []loot
}

// 铲子：低熟练主要挖蚯蚓，随后逐步加入蛆虫、幼虫和稀有虫饵。
var shovelBands = []band{
	{0, []loot{{"worm_01", 90}, {"maggot", 10}}},
	{20, []loot{{"worm_01", 65}, {"maggot", 25}, {"naiad", 10}}},
	{40, []loot{{"worm_01", 45}, {"maggot", 25}, {"naiad", 20}, {"caddisfly", 10}}},
	{60, []loot{{"worm_01", 35}, {"maggot", 20}, {"naiad", 20}, {"caddisfly", 15}, {"unibeetle_larva", 10}}},
	{80, []loot{{"worm_01", 25}, {"maggot", 15}, {"naiad", 15}, {"caddisfly", 15}, {"unibeetle_larva", 15}, {"stonefly_nymph", 10}, {"scolytinae", 5}}},
}

// 红虫长柄勺：主产红虫，随熟练度增加其他软饵/幼虫。
var scoopBands = []band{
	{0, []loot{{"bloodworm", 70}, {"worm_01", 30}}},
	{20, []loot{{"bloodworm", 80}, {"naiad", 10}, {"worm_01", 10}}},
	{40, []loot{{"bloodworm", 70}, {"naiad", 15}, {"caddisfly", 10}, {"stonefly_nymph", 5}}},
	{60, []loot{{"bloodworm", 60}, {"naiad", 15}, {"caddisfly", 10}, {"stonefly_nymph", 10}, {"scolytinae", 5}}},
	{80, []loot{{"bloodworm", 50}, {"naiad", 15}, {"caddisfly", 10}, {"stonefly_nymph", 10}, {"scolytinae", 10}, {"unibeetle_larva", 5}}},
}

// Dig 执行一次挖饵。handled=false 表示不是挖饵工具。
func (s *Service) Dig(ctx context.Context, username, toolKey string) (Result, bool, error) {
	var res Result
	toolKey = strings.ToLower(strings.TrimSpace(toolKey))
	if !s.Handles(toolKey) {
		return res, false, nil
	}
	res.ToolKey = toolKey
	if s.run == nil || s.iw == nil || s.defs == nil {
		return res, true, fmt.Errorf("挖饵服务没接完整")
	}
	st, err := s.run.SkillState(ctx, username)
	if err != nil {
		return res, true, fmt.Errorf("读捕获鱼饵熟练度失败: %w", err)
	}
	before := st.Cats[SkillCaptureBait]
	if before < 0 {
		before = 0
	}
	if before > MasteryCap {
		before = MasteryCap
	}
	res.MasteryBefore = before
	after := before
	if before < MasteryCap {
		after = math.Min(MasteryCap, math.Round((before+MasteryPerDig)*10)/10)
		if err := s.run.SetMastery(ctx, username, SkillCaptureBait, after); err != nil {
			res.Warning = "熟练度写库失败: " + err.Error()
		}
	}
	res.MasteryAfter = after

	table := shovelBands
	if toolKey == ToolScoop {
		table = scoopBands
	}
	bait := chooseBand(table, int(before), s.rng)
	if bait == "" {
		return res, true, fmt.Errorf("熟练度阶段没有可用饵表")
	}
	def, ok := s.defs.Get(bait)
	if !ok || def.ID == 0 {
		return res, true, fmt.Errorf("饵 %s 缺少定义或数字编号", bait)
	}
	qty := 1
	if before >= 20 && s.rng(2) == 1 {
		qty++
	}
	if before >= 60 && s.rng(2) == 1 {
		qty++
	}
	built, err := s.iw.Build(itemwire.Input{Key: bait, Units: 1})
	if err != nil {
		return res, true, fmt.Errorf("饵记录拼不出来: %w", err)
	}
	// 静态定义里的 30 是商城一整包的基础数量；挖饵按本次实际挖到的只数入堆。
	frame, err := s.iw.SetQuantity(built.Frame, uint64(qty))
	if err != nil {
		return res, true, fmt.Errorf("饵数量写不进记录: %w", err)
	}
	obj := gamestore.Object{GUID: built.GUID, Username: username, ItemKey: def.Key, ItemID: def.ID,
		PriceMinor: 0, How: "挖饵 " + toolKey, Source: "go-baitdig", Elem: hex.EncodeToString(frame), CreatedAt: time.Now().UnixMilli()}
	stacked, err := s.stack.Add(ctx, username, obj)
	if err != nil {
		return res, true, fmt.Errorf("饵写背包失败: %w", err)
	}
	if stacked.DuplicateCount > 0 {
		res.Warning = fmt.Sprintf("同编号旧堆有 %d 件，本次已加到指定堆", stacked.DuplicateCount)
	}
	res.BaitKey, res.Quantity = bait, qty
	res.StackQuantity = stacked.Quantity
	res.Records = [][]byte{stacked.Record}
	res.GUIDs = []string{stacked.GUID}
	return res, true, nil
}

func chooseBand(bands []band, mastery int, rng func(int) int) string {
	chosen := bands[0]
	for _, b := range bands {
		if mastery >= b.min {
			chosen = b
		} else {
			break
		}
	}
	total := 0
	for _, it := range chosen.table {
		total += it.weight
	}
	if total <= 0 {
		return ""
	}
	n := rng(total)
	for _, it := range chosen.table {
		if n < it.weight {
			return it.key
		}
		n -= it.weight
	}
	return chosen.table[len(chosen.table)-1].key
}
