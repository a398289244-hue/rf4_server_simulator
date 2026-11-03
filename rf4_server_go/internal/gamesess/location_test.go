package gamesess

import (
	"context"
	"testing"

	"rf4server/internal/fishing"
)

// TestFishPickerUsesSessionLocation 会话进入哪片水域，出鱼器就必须绑哪片；
// 命令行 --fish-loc 只能在没有 1/12 信息时兜底。
func TestFishPickerUsesSessionLocation(t *testing.T) {
	deps, _ := testDeps(t)
	fs := &fishStore{}
	cat := fishing.NewCatalog(fs)
	if _, _, err := cat.Reload(context.Background()); err != nil {
		t.Fatalf("装测试鱼种表失败: %v", err)
	}
	deps.Fishing = fishing.New(deps.Table, cat, fs)
	srv := New(deps, Config{Addr: "127.0.0.1:0", FishLoc: "level_000_home"})
	s := &Session{srv: srv}

	s.setCurrentLocation("level_002_kirzah_river")
	if got := s.currentLocation(); got != "level_002_kirzah_river" {
		t.Fatalf("当前水域=%q，应为 level_002_kirzah_river", got)
	}
	if got := s.fishPicker().Loc(); got != "level_002_kirzah_river" {
		t.Fatalf("出鱼器仍绑在 %q，没有跟随当前会话水域", got)
	}
	if got := s.fishPlace(); got != "level_002_kirzah_river" {
		t.Fatalf("鱼卡片钓点=%q，应为当前水域", got)
	}

	// 新会话还没收到 1/12 时，才使用命令行兜底。
	s2 := &Session{srv: srv}
	if got := s2.fishPicker().Loc(); got != "level_000_home" {
		t.Fatalf("无当前水域时应对齐 --fish-loc，实际 %q", got)
	}
}
