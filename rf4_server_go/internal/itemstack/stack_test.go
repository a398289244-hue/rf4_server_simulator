package itemstack

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
	objs       []gamestore.Object
	inserted   []gamestore.Object
	updatedGID string
	updated    string
}

func (f *fakeStore) Objects(context.Context, string) ([]gamestore.Object, error) { return f.objs, nil }
func (f *fakeStore) InsertObject(_ context.Context, o gamestore.Object) error {
	f.inserted = append(f.inserted, o)
	return nil
}
func (f *fakeStore) UpdateObjectElem(_ context.Context, _, guid, elem string) (bool, error) {
	f.updatedGID, f.updated = guid, elem
	return true, nil
}

func testServices(t *testing.T) (*itemwire.Service, *wire.Table) {
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
	return itemwire.New(tbl, defs, itemwire.Options{}), tbl
}

func TestAddUpdatesExistingStack(t *testing.T) {
	iw, _ := testServices(t)
	old, err := iw.Build(itemwire.Input{Key: "worm_01", GUID: "00112233445566778899aabbccddeeff", Units: 1})
	if err != nil {
		t.Fatal(err)
	}
	newFrame, err := iw.SetQuantity(old.Frame, 5)
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeStore{objs: []gamestore.Object{{GUID: old.GUID, Username: "return2", ItemKey: "worm_01", ItemID: 16041, Elem: hex.EncodeToString(old.Frame)}}}
	svc := New(iw, fs)
	res, err := svc.Add(context.Background(), "return2", gamestore.Object{GUID: "ffeeddccbbaa99887766554433221100", Username: "return2", ItemKey: "worm_01", ItemID: 16041, Elem: hex.EncodeToString(newFrame)})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Existing || res.GUID != old.GUID || res.Quantity != 35 {
		t.Fatalf("堆叠结果不对: %+v", res)
	}
	if fs.updatedGID != old.GUID || len(fs.inserted) != 0 {
		t.Fatalf("应更新旧堆不插新行: updated=%s inserted=%d", fs.updatedGID, len(fs.inserted))
	}
}

func TestAddCreatesStackWhenMissing(t *testing.T) {
	iw, _ := testServices(t)
	built, err := iw.Build(itemwire.Input{Key: "worm_01", GUID: "00112233445566778899aabbccddeeff", Units: 1})
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeStore{}
	svc := New(iw, fs)
	res, err := svc.Add(context.Background(), "return2", gamestore.Object{GUID: built.GUID, Username: "return2", ItemKey: "worm_01", ItemID: 16041, Elem: hex.EncodeToString(built.Frame)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Existing || res.Quantity != 30 || len(fs.inserted) != 1 {
		t.Fatalf("新堆结果不对: res=%+v inserted=%d", res, len(fs.inserted))
	}
}
