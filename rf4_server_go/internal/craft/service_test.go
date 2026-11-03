package craft

import (
	"context"
	"errors"
	"testing"

	"rf4server/internal/assetpaths"
	"rf4server/internal/gamestore"
	"rf4server/internal/itemdef"
	"rf4server/internal/itemwire"
	"rf4server/internal/wire"
)

type fakeRecipeSource struct{ row gamestore.Recipe }

func (f fakeRecipeSource) Recipe(context.Context, string) (gamestore.Recipe, error) {
	if f.row.RecipeID == "" {
		return gamestore.Recipe{}, gamestore.ErrNotFound
	}
	return f.row, nil
}

type fakeCraftStore struct {
	objects []gamestore.Object
	state   gamestore.SkillState
}

func (f *fakeCraftStore) SkillState(context.Context, string) (gamestore.SkillState, error) {
	if f.state.Cats == nil {
		f.state.Cats = map[int]float64{}
	}
	return f.state, nil
}

func (f *fakeCraftStore) SetMastery(_ context.Context, _ string, cat int, points float64) error {
	if f.state.Cats == nil {
		f.state.Cats = map[int]float64{}
	}
	f.state.Cats[cat] = points
	return nil
}

func (f *fakeCraftStore) Objects(context.Context, string) ([]gamestore.Object, error) {
	return append([]gamestore.Object(nil), f.objects...), nil
}
func (f *fakeCraftStore) InsertObject(_ context.Context, o gamestore.Object) error {
	f.objects = append(f.objects, o)
	return nil
}
func (f *fakeCraftStore) UpdateObjectElem(_ context.Context, username, guid, elem string) (bool, error) {
	for i := range f.objects {
		if f.objects[i].Username == username && f.objects[i].GUID == guid {
			f.objects[i].Elem = elem
			return true, nil
		}
	}
	return false, nil
}

func craftTestTable(t *testing.T) *wire.Table {
	t.Helper()
	p, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := wire.LoadTable(p)
	if err != nil {
		t.Fatal(err)
	}
	return tbl
}

func craftRequest(t *testing.T, tbl *wire.Table, recipe string, times uint32) []byte {
	t.Helper()
	o, err := tbl.NewObj(requestTypeID)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.SetShortStrAt(0, recipe); err != nil {
		t.Fatal(err)
	}
	if err := o.SetU32At(5, times); err != nil {
		t.Fatal(err)
	}
	b, err := o.Encode()
	if err != nil {
		t.Fatal(err)
	}
	body, err := (wire.Args{Code: "524", Items: []wire.Value{wire.Raw{Data: b}}}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	frame := append(wire.PushHead(7, 9, 1), body...)
	return append(frame, wire.RequestTrailer)
}

func TestParseRequestFindsObj8(t *testing.T) {
	tbl := craftTestTable(t)
	req, err := ParseRequest(tbl, craftRequest(t, tbl, "craft_bait_bread", 2))
	if err != nil {
		t.Fatal(err)
	}
	if req.Recipe != "craft_bait_bread" || req.Times != 2 || req.Source != "obj8" {
		t.Fatalf("请求解析错了: %+v", req)
	}
}

func TestMakeAddsThirtyAndStacks(t *testing.T) {
	tbl := craftTestTable(t)
	defs, err := itemdef.Load(context.Background(), func(context.Context) ([]itemdef.Row, error) {
		return []itemdef.Row{{Key: "bread", NumericID: 3583, Category: BaitCategory, AttrsJSON: `{}`}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	iw := itemwire.New(tbl, defs, itemwire.Options{})
	store := &fakeCraftStore{state: gamestore.SkillState{Cats: map[int]float64{SkillCaptureBait: 50}}}
	svc := New(tbl, defs, iw, fakeRecipeSource{row: gamestore.Recipe{
		RecipeID: "craft_bait_bread", OutputID: "bread", OutputQty: 1,
	}}, store)
	plain := craftRequest(t, tbl, "craft_bait_bread", 1)

	first, err := svc.Make(context.Background(), "return2", plain)
	if err != nil {
		t.Fatal(err)
	}
	if first.Quantity != BaitPerCraft || first.StackQuantity != BaitPerCraft || len(store.objects) != 1 {
		t.Fatalf("第一次制作结果不对: %+v objects=%d", first, len(store.objects))
	}
	if first.MasteryBefore != 50 || first.MasteryAfter != 50.1 || store.state.Cats[SkillCaptureBait] != 50.1 {
		t.Fatalf("第一次制作应把熟练度50.0加到50.1，实际 %+v db=%v", first, store.state.Cats)
	}
	second, err := svc.Make(context.Background(), "return2", plain)
	if err != nil {
		t.Fatal(err)
	}
	if second.StackQuantity != 2*BaitPerCraft || len(store.objects) != 1 {
		t.Fatalf("第二次应堆到60而非新建一行: %+v objects=%d", second, len(store.objects))
	}
	if second.MasteryBefore != 50.1 || second.MasteryAfter != 50.2 || store.state.Cats[SkillCaptureBait] != 50.2 {
		t.Fatalf("第二次制作应把熟练度50.1加到50.2，实际 %+v db=%v", second, store.state.Cats)
	}
	if second.GUIDs[0] != first.GUIDs[0] {
		t.Fatalf("堆叠后 GUID 变了: %v -> %v", first.GUIDs, second.GUIDs)
	}
}

func TestMakeDoesNotExceedMasteryCap(t *testing.T) {
	tbl := craftTestTable(t)
	defs, err := itemdef.Load(context.Background(), func(context.Context) ([]itemdef.Row, error) {
		return []itemdef.Row{{Key: "bread", NumericID: 3583, Category: BaitCategory, AttrsJSON: `{}`}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	iw := itemwire.New(tbl, defs, itemwire.Options{})
	store := &fakeCraftStore{state: gamestore.SkillState{Cats: map[int]float64{SkillCaptureBait: MasteryCap}}}
	svc := New(tbl, defs, iw, fakeRecipeSource{row: gamestore.Recipe{RecipeID: "craft_bait_bread", OutputID: "bread"}}, store)
	res, err := svc.Make(context.Background(), "return2", craftRequest(t, tbl, "craft_bait_bread", 1))
	if err != nil {
		t.Fatal(err)
	}
	if res.MasteryBefore != MasteryCap || res.MasteryAfter != MasteryCap {
		t.Fatalf("满熟练度不应继续增加: %+v", res)
	}
}

func TestMakeRejectsNonBaitOutput(t *testing.T) {
	tbl := craftTestTable(t)
	defs, err := itemdef.Load(context.Background(), func(context.Context) ([]itemdef.Row, error) {
		return []itemdef.Row{{Key: "not_bait", NumericID: 1, Category: 7, AttrsJSON: `{}`}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	iw := itemwire.New(tbl, defs, itemwire.Options{})
	svc := New(tbl, defs, iw, fakeRecipeSource{row: gamestore.Recipe{RecipeID: "x", OutputID: "not_bait"}}, &fakeCraftStore{})
	_, err = svc.Make(context.Background(), "return2", craftRequest(t, tbl, "x", 1))
	if err == nil || errors.Is(err, gamestore.ErrNotFound) {
		t.Fatalf("非鱼饵配方应被明确拒绝，实际 %v", err)
	}
}

func TestLiveMySQLHasBaitRecipe(t *testing.T) {
	my, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没起来: %v", err)
	}
	t.Cleanup(func() { _ = my.Close() })
	r, err := my.Recipe(context.Background(), "craft_bait_bread")
	if err != nil {
		t.Fatalf("真实配方表查不到 craft_bait_bread: %v", err)
	}
	if r.OutputID == "" {
		t.Fatalf("真实配方没有产物: %+v", r)
	}
}
