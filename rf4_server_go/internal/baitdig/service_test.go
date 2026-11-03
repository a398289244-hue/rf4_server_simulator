package baitdig

import (
	"context"
	"encoding/hex"
	"testing"

	"rf4server/internal/assetpaths"
	"rf4server/internal/gamestore"
	"rf4server/internal/itemdef"
	"rf4server/internal/itemwire"
	"rf4server/internal/wire"
)

type fakeStore struct {
	state        gamestore.SkillState
	objs         []gamestore.Object
	inserted     []gamestore.Object
	updatedGUID  string
	updatedElem  string
	mastery      int
	masteryValue float64
}

func (f *fakeStore) SkillState(context.Context, string) (gamestore.SkillState, error) {
	return f.state, nil
}
func (f *fakeStore) SetMastery(_ context.Context, _ string, cat int, p float64) error {
	f.mastery = cat
	f.masteryValue = p
	return nil
}
func (f *fakeStore) Objects(context.Context, string) ([]gamestore.Object, error) {
	return f.objs, nil
}
func (f *fakeStore) InsertObject(_ context.Context, o gamestore.Object) error {
	f.inserted = append(f.inserted, o)
	return nil
}
func (f *fakeStore) UpdateObjectElem(_ context.Context, _, guid, elem string) (bool, error) {
	f.updatedGUID, f.updatedElem = guid, elem
	return true, nil
}

func testService(t *testing.T) (*Service, *fakeStore) {
	t.Helper()
	tp, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := wire.LoadTable(tp)
	if err != nil {
		t.Fatal(err)
	}
	my, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没起来: %v", err)
	}
	t.Cleanup(func() { _ = my.Close() })
	defs, err := itemdef.Load(context.Background(), itemdef.FromMySQL(my))
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeStore{state: gamestore.SkillState{Cats: map[int]float64{SkillCaptureBait: 50}}}
	svc := New(defs, itemwire.New(tbl, defs, itemwire.Options{}), fs).WithRand(func(int) int { return 0 })
	return svc, fs
}

func TestDigAddsBaitAndMastery(t *testing.T) {
	svc, fs := testService(t)
	res, handled, err := svc.Dig(context.Background(), "return2", ToolShovel)
	if err != nil || !handled {
		t.Fatalf("挖饵失败 handled=%v err=%v", handled, err)
	}
	if res.BaitKey != "worm_01" || res.Quantity != 1 {
		t.Fatalf("结果不对: %+v", res)
	}
	if fs.mastery != SkillCaptureBait || fs.masteryValue != 50.1 {
		t.Fatalf("熟练度写错: cat=%d value=%v", fs.mastery, fs.masteryValue)
	}
	if len(fs.inserted) != 1 || len(res.Records) != 1 || len(res.GUIDs) != 1 {
		t.Fatalf("背包写入不对: inserted=%d rec=%d guids=%d", len(fs.inserted), len(res.Records), len(res.GUIDs))
	}
	if fs.inserted[0].ItemKey != "worm_01" || res.StackQuantity != 1 {
		t.Fatalf("写入物品=%s 堆数量=%d", fs.inserted[0].ItemKey, res.StackQuantity)
	}
	raw, err := hex.DecodeString(fs.inserted[0].Elem)
	if err != nil {
		t.Fatal(err)
	}
	qty, err := svc.iw.Quantity(raw)
	if err != nil || qty != 1 {
		t.Fatalf("新堆数量=%d err=%v，应为 1", qty, err)
	}
}

func TestDigScoopAtHighMastery(t *testing.T) {
	svc, fs := testService(t)
	fs.state.Cats[SkillCaptureBait] = 80
	res, handled, err := svc.Dig(context.Background(), "return2", ToolScoop)
	if err != nil || !handled {
		t.Fatalf("红虫长柄勺失败 handled=%v err=%v", handled, err)
	}
	if res.BaitKey != "bloodworm" {
		t.Fatalf("高熟练红虫勺首选应仍是红虫，实际 %s", res.BaitKey)
	}
	if fs.masteryValue != 80.1 {
		t.Fatalf("熟练度应到80.1，实际 %v", fs.masteryValue)
	}
}

func TestDigMasteryCapsAt100(t *testing.T) {
	svc, fs := testService(t)
	fs.state.Cats[SkillCaptureBait] = 99.95
	res, handled, err := svc.Dig(context.Background(), "return2", ToolShovel)
	if err != nil || !handled {
		t.Fatal(err)
	}
	if res.MasteryAfter != 100 || fs.masteryValue != 100 {
		t.Fatalf("应封顶100: %+v", res)
	}
}

func TestNonDiggingToolNotHandled(t *testing.T) {
	svc, _ := testService(t)
	_, handled, err := svc.Dig(context.Background(), "return2", "tele_10112_4")
	if err != nil || handled {
		t.Fatalf("普通物品不应走挖饵: handled=%v err=%v", handled, err)
	}
}
func TestAllLootDefinedAndBuildable(t *testing.T) {
	svc, _ := testService(t)
	seen := map[string]bool{}
	for _, bands := range [][]band{shovelBands, scoopBands} {
		for _, b := range bands {
			for _, it := range b.table {
				seen[it.key] = true
			}
		}
	}
	for key := range seen {
		def, ok := svc.defs.Get(key)
		if !ok || def.ID == 0 {
			t.Errorf("饵 %s 缺少物品定义或数字编号", key)
			continue
		}
		if _, err := svc.iw.Build(itemwire.Input{Key: key, Units: 1}); err != nil {
			t.Errorf("饵 %s 拼不出标准物件记录: %v", key, err)
		}
	}
}

func TestDigAddsToExistingStack(t *testing.T) {
	svc, fs := testService(t)
	old, err := svc.iw.Build(itemwire.Input{Key: "worm_01", GUID: "00112233445566778899aabbccddeeff", Units: 1})
	if err != nil {
		t.Fatal(err)
	}
	fs.objs = []gamestore.Object{{GUID: old.GUID, Username: "return2", ItemKey: "worm_01", ItemID: 16041, Elem: hex.EncodeToString(old.Frame)}}
	res, handled, err := svc.Dig(context.Background(), "return2", ToolShovel)
	if err != nil || !handled {
		t.Fatalf("挖饵失败 handled=%v err=%v", handled, err)
	}
	if len(fs.inserted) != 0 || fs.updatedGUID != old.GUID {
		t.Fatalf("应更新旧堆，不应插新行: inserted=%d updated=%s", len(fs.inserted), fs.updatedGUID)
	}
	if res.StackQuantity != 31 || res.GUIDs[0] != old.GUID {
		t.Fatalf("旧堆应 30→31，结果=%+v", res)
	}
}
