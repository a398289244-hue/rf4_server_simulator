package craft

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"time"

	"rf4server/internal/gamestore"
	"rf4server/internal/itemdef"
	"rf4server/internal/itemstack"
	"rf4server/internal/itemwire"
	"rf4server/internal/wire"
)

const (
	BaitCategory = 27
	BaitPerCraft = 30
	// SkillCaptureBait 是技能表里的“捕获鱼饵”；制作和挖取鱼饵共用这项熟练度。
	SkillCaptureBait = 4
	MasteryPerCraft  = 0.1
	MasteryCap       = 100.0
	craftSource      = "go-craft-bait"
)

// RecipeSource 提供 MySQL static_recipe 配方。
type RecipeSource interface {
	Recipe(ctx context.Context, key string) (gamestore.Recipe, error)
}

// Store 是制作产物写入真实背包所需的最小 MySQL 接口。
type Store interface {
	SkillState(ctx context.Context, username string) (gamestore.SkillState, error)
	SetMastery(ctx context.Context, username string, cat int, points float64) error
	Objects(ctx context.Context, username string) ([]gamestore.Object, error)
	InsertObject(ctx context.Context, o gamestore.Object) error
	UpdateObjectElem(ctx context.Context, username, guid, elemHex string) (bool, error)
}

// Result 是一次鱼饵制作完成后的真实入包结果。
type Result struct {
	Recipe        string
	OutputKey     string
	Times         int
	Quantity      int
	StackQuantity uint64
	Records       [][]byte
	GUIDs         []string
	MasteryBefore float64
	MasteryAfter  float64
	Warning       string
}

// Service 只处理鱼饵制作；配方、物品定义和背包全部来自 MySQL。
type Service struct {
	table   *wire.Table
	defs    *itemdef.Defs
	iw      *itemwire.Service
	recipes RecipeSource
	run     Store
	stack   *itemstack.Service
}

func New(t *wire.Table, defs *itemdef.Defs, iw *itemwire.Service, recipes RecipeSource, run Store) *Service {
	return &Service{table: t, defs: defs, iw: iw, recipes: recipes, run: run, stack: itemstack.New(iw, run)}
}

// Make 根据 9/1 请求制作鱼饵。每制作一次固定产出 30 个，多次制作按次数相乘。
func (s *Service) Make(ctx context.Context, username string, plain []byte) (Result, error) {
	var res Result
	if s == nil || s.table == nil || s.defs == nil || s.iw == nil || s.recipes == nil || s.run == nil || s.stack == nil {
		return res, fmt.Errorf("制作鱼饵服务没接完整")
	}
	req, err := ParseRequest(s.table, plain)
	if err != nil {
		return res, err
	}
	recipe, err := s.recipes.Recipe(ctx, req.Recipe)
	if err != nil {
		return res, err
	}
	def, ok := s.defs.Get(recipe.OutputID)
	if !ok || def.ID == 0 {
		return res, fmt.Errorf("配方 %s 的产物 %s 缺少物品定义或数字编号", recipe.RecipeID, recipe.OutputID)
	}
	if def.Cat != BaitCategory {
		return res, fmt.Errorf("配方 %s 产物 %s 是类别 %d，不是鱼饵类别 %d", recipe.RecipeID, def.Key, def.Cat, BaitCategory)
	}
	skill, err := s.stackStore().SkillState(ctx, username)
	if err != nil {
		return res, fmt.Errorf("读取捕获鱼饵熟练度失败: %w", err)
	}
	before := skill.Cats[SkillCaptureBait]
	if before < 0 {
		before = 0
	}
	if before > MasteryCap {
		before = MasteryCap
	}

	qty := BaitPerCraft * req.Times
	built, err := s.iw.Build(itemwire.Input{Key: def.Key, Units: 1})
	if err != nil {
		return res, fmt.Errorf("制作产物拼不出来: %w", err)
	}
	// 鱼饵定义本身常带基础数量 30，不能再用 Units=30 相乘；这里直接把最终数量写成 30×次数。
	frame, err := s.iw.SetQuantity(built.Frame, uint64(qty))
	if err != nil {
		return res, fmt.Errorf("制作产物数量写不进记录: %w", err)
	}
	obj := gamestore.Object{
		GUID: built.GUID, Username: username, ItemKey: def.Key, ItemID: def.ID,
		PriceMinor: 0, How: "制作鱼饵 " + recipe.RecipeID, Source: craftSource,
		Elem: hex.EncodeToString(frame), CreatedAt: time.Now().UnixMilli(),
	}
	stacked, err := s.stack.Add(ctx, username, obj)
	if err != nil {
		return res, fmt.Errorf("制作鱼饵写背包失败: %w", err)
	}
	res = Result{
		Recipe: recipe.RecipeID, OutputKey: def.Key, Times: req.Times, Quantity: qty,
		StackQuantity: stacked.Quantity, Records: [][]byte{stacked.Record}, GUIDs: []string{stacked.GUID},
		MasteryBefore: before, MasteryAfter: before,
	}
	if before < MasteryCap {
		res.MasteryAfter = math.Min(MasteryCap, math.Round((before+MasteryPerCraft)*10)/10)
		if err := s.stackStore().SetMastery(ctx, username, SkillCaptureBait, res.MasteryAfter); err != nil {
			res.MasteryAfter = before
			res.Warning = "熟练度写库失败: " + err.Error()
		}
	}
	if stacked.DuplicateCount > 0 {
		if res.Warning != "" {
			res.Warning += "；"
		}
		res.Warning += fmt.Sprintf("同编号旧堆有 %d 件，本次已加到指定堆", stacked.DuplicateCount)
	}
	return res, nil
}

// stackStore 取出和 itemstack 共用的运行库。单独留字段，避免运行时从堆叠服务反射内部状态。
func (s *Service) stackStore() Store { return s.run }
