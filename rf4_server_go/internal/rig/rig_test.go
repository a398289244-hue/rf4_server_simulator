package rig

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"rf4server/internal/assetpaths"
	"rf4server/internal/gamestore"
	"rf4server/internal/itemdef"
	"rf4server/internal/itemwire"
	"rf4server/internal/wire"
)

// fakeStore 内存版数据入口：只装这一域用到的三件事
type fakeStore struct {
	objs     []gamestore.Object
	updates  map[string]string // guid -> 新写的 elem hex
	rigs     []gamestore.RigState
	failSave bool
}

func (f *fakeStore) Objects(ctx context.Context, user string) ([]gamestore.Object, error) {
	return f.objs, nil
}

func (f *fakeStore) UpdateObjectElem(ctx context.Context, user, guid, elemHex string) (bool, error) {
	if f.updates == nil {
		f.updates = map[string]string{}
	}
	f.updates[strings.ToLower(guid)] = elemHex
	for i := range f.objs {
		if strings.ToLower(f.objs[i].GUID) == strings.ToLower(guid) {
			f.objs[i].Elem = elemHex
		}
	}
	return true, nil
}

func (f *fakeStore) SaveRig(ctx context.Context, st gamestore.RigState) error {
	f.rigs = append(f.rigs, st)
	return nil
}

// 三件测试用的物品编号 + 它们自己的 GUID（GUID 是随机生成的，测试里固定下来更好比）
const (
	rodKey  = "tele_10048_SS13" // 竿（类别 3）
	hookKey = "b_tech_round_B_12"
	lineKey = "mono_15063"
)

type harness struct {
	tbl   *wire.Table
	svc   *Service
	store *fakeStore
	rod   gamestore.Object
	item  gamestore.Object
	guids []string
}

// newHarness 用真定义 + 真字段表造一根竿和一件要装的零件，塞进内存背包。
// 定义表在 MySQL static_item_def 里，库没起就跳过（和线上一样的数据来源，不造假定义）。
func newHarness(t *testing.T, itemKey string) *harness {
	t.Helper()
	tp, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatalf("找不到凭据目录: %v", err)
	}
	tbl, err := wire.LoadTable(tp)
	if err != nil {
		t.Fatalf("读字段表失败: %v", err)
	}
	my, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没连着，读不到 static_item_def: %v", err)
	}
	defer my.Close()
	rows, err := my.ItemDefRows(context.Background())
	if err != nil {
		t.Fatalf("读定义表失败: %v", err)
	}
	var keep []itemdef.Row
	want := map[string]bool{rodKey: true, itemKey: true}
	for _, r := range rows {
		if want[r.Key] {
			keep = append(keep, itemdef.Row{Key: r.Key, NumericID: r.NumericID, Category: r.Category,
				Kind: r.Kind, Brand: r.Brand, AttrsJSON: r.AttrsJSON})
		}
	}
	if len(keep) != 2 {
		t.Skipf("static_item_def 里缺测试用的编号（找到 %d 条）", len(keep))
	}
	defs, err := itemdef.Load(context.Background(), func(ctx context.Context) ([]itemdef.Row, error) {
		return keep, nil
	})
	if err != nil {
		t.Fatalf("造定义表失败: %v", err)
	}
	iw := itemwire.New(tbl, defs, itemwire.Options{})
	rodGUID, itemGUID := itemwire.NewGUID(), itemwire.NewGUID()
	rodB, err := iw.Build(itemwire.Input{Key: rodKey, GUID: rodGUID})
	if err != nil {
		t.Fatalf("拼竿失败: %v", err)
	}
	itemB, err := iw.Build(itemwire.Input{Key: itemKey, GUID: itemGUID})
	if err != nil {
		t.Fatalf("拼零件失败: %v（这件货可能没 wire 类）", err)
	}
	h := &harness{
		tbl: tbl,
		store: &fakeStore{objs: []gamestore.Object{
			{GUID: rodGUID, Username: "return2", ItemKey: rodKey, Elem: hex.EncodeToString(rodB.Frame)},
			{GUID: itemGUID, Username: "return2", ItemKey: itemKey, Elem: hex.EncodeToString(itemB.Frame)},
		}},
		rod:   gamestore.Object{GUID: rodGUID, ItemKey: rodKey},
		item:  gamestore.Object{GUID: itemGUID, ItemKey: itemKey},
		guids: []string{rodGUID, itemGUID},
	}
	h.svc = New(tbl, defs, h.store)
	return h
}

// mountRequest 拼一条 17/2 请求：参数表"524"{ 竿GUID, 物品GUID, u32 0 }
func mountRequest(t *testing.T, tbl *wire.Table, rodGUID, itemGUID string) []byte {
	t.Helper()
	items := []wire.Value{wire.GUID{Hex: rodGUID}, wire.GUID{Hex: itemGUID}, wire.U32(0)}
	args, err := wire.Args{Code: "524", Items: items}.Encode()
	if err != nil {
		t.Fatalf("拼请求失败: %v", err)
	}
	return append(append(wire.PushHead(0, 17, 2), args...), wire.RequestTrailer)
}

// unmountRequest 拼一条 17/3 请求：参数表"524"{ 竿GUID, 物品GUID, u32 品类 }
func unmountRequest(t *testing.T, tbl *wire.Table, rodGUID, itemGUID string, slotCat uint32) []byte {
	t.Helper()
	items := []wire.Value{wire.GUID{Hex: rodGUID}, wire.GUID{Hex: itemGUID}, wire.U32(slotCat)}
	args, err := wire.Args{Code: "524", Items: items}.Encode()
	if err != nil {
		t.Fatalf("拼请求失败: %v", err)
	}
	return append(append(wire.PushHead(0, 17, 3), args...), wire.RequestTrailer)
}

// decodeSnap 把 800 快照解出来看
func decodeSnap(t *testing.T, tbl *wire.Table, frame []byte) *wire.Obj {
	t.Helper()
	v, used, err := tbl.DecodeValue(frame, 9)
	if err != nil || used != len(frame) {
		t.Fatalf("回包解不开: %v", err)
	}
	o, ok := v.(*wire.Obj)
	if !ok || int(o.TypeID) != 800 {
		t.Fatalf("回包不是 obj800，是 %T", v)
	}
	return o
}

// TestMountHookGoesIntoRigParts 鱼钩（类别 12）不进竿的专用格，而是进 153 钓组的零件表，
// 槽索引按「钓组类型 + 品类」那张表挑（钓组类型 1 -> 钩用 250）。
func TestMountHookGoesIntoRigParts(t *testing.T) {
	h := newHarness(t, hookKey)
	res0 := partsOf(mustDecode(t, h, h.rod.Elem))
	if len(res0) != 0 {
		t.Fatalf("新拼的竿本来就该没零件，实际有 %d 条", len(res0))
	}

	frame, res, err := h.svc.MountFrame(context.Background(), "return2", 12,
		mountRequest(t, h.tbl, h.rod.GUID, h.item.GUID))
	if err != nil {
		t.Fatalf("装配出错: %v", err)
	}
	if !res.OK {
		t.Fatalf("这单该装成，实际拒了：%s", res.Reason)
	}
	if res.Kind != "part" || res.PartIndex != 250 {
		t.Fatalf("该进钓组零件表槽索引 250，实际 kind=%s index=%d", res.Kind, res.PartIndex)
	}
	if res.Records != 2 {
		t.Fatalf("回包该有两条记录（先物品后竿），实际 %d", res.Records)
	}

	snap := decodeSnap(t, h.tbl, frame)
	items := snap.ArgsOf(0)
	if items == nil || len(items.Items) != 2 {
		t.Fatalf("800 的 #00 该是两条记录，实际 %v", items)
	}
	gotItem := items.Items[0].(*wire.Obj)
	if guidFieldOf(gotItem) != strings.ToLower(h.item.GUID) {
		t.Fatalf("第一条该是那件零件的记录，GUID 对不上")
	}
	gotRod := items.Items[1].(*wire.Obj)
	parts := partsOf(gotRod)
	if len(parts) != 1 {
		t.Fatalf("竿的零件表该有 1 条，实际 %d", len(parts))
	}
	if partCat(parts[0]) != catHook || partIndex(parts[0]) != 250 || partGUID(parts[0]) != strings.ToLower(h.item.GUID) {
		t.Fatalf("零件那条 152 不对：品类 %d 槽索引 %d GUID %s", partCat(parts[0]), partIndex(parts[0]), partGUID(parts[0]))
	}
	// 零件的父 GUID 必须指向竿，两边版本戳必须比原来大
	if parentOf(gotItem) != strings.ToLower(h.rod.GUID) {
		t.Fatalf("零件的父 GUID 没指向竿：%q", parentOf(gotItem))
	}
	if createdOf(gotRod) <= createdOf(mustDecode(t, h, h.rod.Elem)) {
		t.Fatal("竿的版本戳没变大，客户端会把这次更新丢掉")
	}
	// 库要真的写回去
	if h.store.updates[strings.ToLower(h.rod.GUID)] == "" || h.store.updates[strings.ToLower(h.item.GUID)] == "" {
		t.Fatalf("objects.elem 没写回去：%v", h.store.updates)
	}
	if len(h.store.rigs) != 1 || len(h.store.rigs[0].Parts) != 1 {
		t.Fatalf("rig/rig_component 投影没同步：%+v", h.store.rigs)
	}
	if h.store.rigs[0].Parts[0].Slot != catHook || h.store.rigs[0].Parts[0].Extra != 250 {
		t.Fatalf("投影里的零件不对：%+v", h.store.rigs[0].Parts[0])
	}
}

// TestMountTwiceIsRejected 同一件装第二次要拒，别在零件表里塞两条重复的
func TestMountTwiceIsRejected(t *testing.T) {
	h := newHarness(t, hookKey)
	if _, res, err := h.svc.MountFrame(context.Background(), "return2", 1,
		mountRequest(t, h.tbl, h.rod.GUID, h.item.GUID)); err != nil || !res.OK {
		t.Fatalf("第一次就该装成：%+v %v", res, err)
	}
	_, res2, err := h.svc.MountFrame(context.Background(), "return2", 2,
		mountRequest(t, h.tbl, h.rod.GUID, h.item.GUID))
	if err != nil {
		t.Fatalf("第二次出错: %v", err)
	}
	if res2.OK || !strings.Contains(res2.Reason, "已经装在") {
		t.Fatalf("第二次该拒成「已经装了」，实际 OK=%v 原因=%s", res2.OK, res2.Reason)
	}
}

// TestMountLineGoesIntoSlot3 鱼线（类别 11）不进零件表，而是写竿的第 3 格
func TestMountLineGoesIntoSlot3(t *testing.T) {
	h := newHarness(t, lineKey)
	_, res, err := h.svc.MountFrame(context.Background(), "return2", 3,
		mountRequest(t, h.tbl, h.rod.GUID, h.item.GUID))
	if err != nil {
		t.Fatalf("出错: %v", err)
	}
	if !res.OK || res.Kind != "field" || res.Slot != slotLine {
		t.Fatalf("鱼线该写第 3 格，实际 %+v", res)
	}
	newRod := mustDecode(t, h, h.store.updates[strings.ToLower(h.rod.GUID)])
	if guidAt(newRod, slotLine) != strings.ToLower(h.item.GUID) {
		t.Fatalf("竿第 3 格没写上鱼线 GUID")
	}
	if len(partsOf(newRod)) != 0 {
		t.Fatal("鱼线不该进零件表")
	}
	if len(h.store.rigs) != 1 || h.store.rigs[0].LineGUID != strings.ToLower(h.item.GUID) {
		t.Fatalf("投影里 line_guid 没同步：%+v", h.store.rigs)
	}
}

// TestUnmountReturnsToBag 拆下来：零件从 153 里掉出去，那件的父 GUID 清空、版本戳变大
func TestUnmountReturnsToBag(t *testing.T) {
	h := newHarness(t, hookKey)
	if _, res, err := h.svc.MountFrame(context.Background(), "return2", 1,
		mountRequest(t, h.tbl, h.rod.GUID, h.item.GUID)); err != nil || !res.OK {
		t.Fatalf("先装一次：%+v %v", res, err)
	}
	frame, res, err := h.svc.UnmountFrame(context.Background(), "return2", 2,
		unmountRequest(t, h.tbl, h.rod.GUID, h.item.GUID, catHook))
	if err != nil {
		t.Fatalf("拆的时候出错: %v", err)
	}
	if !res.OK || len(res.Removed) != 1 {
		t.Fatalf("该拆掉 1 件，实际 %+v", res)
	}
	snap := decodeSnap(t, h.tbl, frame)
	items := snap.ArgsOf(0)
	if items == nil || len(items.Items) != 2 {
		t.Fatalf("回包该是「竿 + 拆下来那件」两条，实际 %v", items)
	}
	rodNew := items.Items[0].(*wire.Obj)
	if len(partsOf(rodNew)) != 0 {
		t.Fatalf("拆完零件表该空了，还剩 %d 条", len(partsOf(rodNew)))
	}
	itemNew := items.Items[1].(*wire.Obj)
	if parentOf(itemNew) != "" {
		t.Fatalf("拆下来的那件父 GUID 该清空（退回背包），实际 %q", parentOf(itemNew))
	}
}

// TestMountRejectsForeignGuids 不是这个号名下的 GUID 一律拒（影子物品就是这么混进来的）
func TestMountRejectsForeignGuids(t *testing.T) {
	h := newHarness(t, hookKey)
	_, res, err := h.svc.MountFrame(context.Background(), "return2", 1,
		mountRequest(t, h.tbl, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", h.item.GUID))
	if err != nil {
		t.Fatalf("出错: %v", err)
	}
	if res.OK || !strings.Contains(res.Reason, "不在这个"[:5]) && !strings.Contains(res.Reason, "不在该账号") &&
		!strings.Contains(res.Reason, "不在") {
		t.Fatalf("该拒成「竿不在名下」，实际 OK=%v 原因=%s", res.OK, res.Reason)
	}
}

// TestRequestParsingGuardsBadFrames 请求读不懂不能 panic：GUID 不够就回空快照并说清
func TestRequestParsingGuardsBadFrames(t *testing.T) {
	h := newHarness(t, hookKey)
	frame, res, err := h.svc.MountFrame(context.Background(), "return2", 5, []byte{0x01, 0xff, 0xff, 0xff, 0xff, 17, 2})
	if err != nil {
		t.Fatalf("读不懂也该回帧，不该报错: %v", err)
	}
	if res.OK || res.Reason == "" {
		t.Fatalf("该拒并写原因，实际 %+v", res)
	}
	decodeSnap(t, h.tbl, frame)
}

// ---- 几个读记录字段的小 helper（只为测试好读）----

func mustDecode(t *testing.T, h *harness, elemHex string) *wire.Obj {
	t.Helper()
	o, err := h.svc.decode(elemHex)
	if err != nil {
		t.Fatalf("记录解不开: %v", err)
	}
	return o
}

func guidFieldOf(o *wire.Obj) string {
	for i := range o.Fields {
		if o.Fields[i].Name == "cahhdnicmbn" {
			if g, ok := o.Fields[i].Value.(wire.GUID); ok {
				return strings.ToLower(g.Hex)
			}
		}
	}
	return ""
}

func parentOf(o *wire.Obj) string {
	for i := range o.Fields {
		if o.Fields[i].Name != "odflendafcg" {
			continue
		}
		switch v := o.Fields[i].Value.(type) {
		case wire.OptGUIDField:
			if !v.Present {
				return ""
			}
			return strings.ToLower(v.Hex)
		case wire.GUID:
			return strings.ToLower(v.Hex)
		}
	}
	return ""
}

func createdOf(o *wire.Obj) uint64 {
	for i := range o.Fields {
		if o.Fields[i].Name != "adhajflcpcd" {
			continue
		}
		switch v := o.Fields[i].Value.(type) {
		case wire.Num64:
			return v.V
		case wire.Num32:
			return uint64(v.V)
		}
	}
	return 0
}
