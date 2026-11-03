package craft

import (
	"testing"

	"rf4server/internal/assetpaths"
	"rf4server/internal/wire"
)

func TestResultReplyShape(t *testing.T) {
	path, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := wire.LoadTable(path)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := ResultReply(tbl, 93, true)
	if err != nil {
		t.Fatal(err)
	}
	v, used, err := tbl.DecodeValue(frame, 9)
	if err != nil || used != len(frame) {
		t.Fatalf("803 回包解不开: used=%d len=%d err=%v", used, len(frame), err)
	}
	o, ok := v.(*wire.Obj)
	if !ok || o.TypeID != ResultType {
		t.Fatalf("应为物件 803，实际 %T %+v", v, v)
	}
	if b, ok := o.Fields[0].Value.(wire.OptByte); !ok || b.V != 1 {
		t.Fatalf("f0 应为成功=1，实际 %#v", o.Fields[0].Value)
	}
	a, ok := o.Fields[1].Value.(wire.Args)
	if !ok || a.Code != "61" || len(a.Items) != 0 {
		t.Fatalf("f1 应为空 args(61)，实际 %#v", o.Fields[1].Value)
	}
	if _, ok := o.Fields[2].Value.(wire.Null); !ok {
		t.Fatalf("f2 应为 null，实际 %#v", o.Fields[2].Value)
	}
}

func TestDigResultReplyCarriesProducedAndUpsert(t *testing.T) {
	path, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := wire.LoadTable(path)
	if err != nil {
		t.Fatal(err)
	}
	dummy, err := tbl.NewObj(ResultType)
	if err != nil {
		t.Fatal(err)
	}
	record, err := dummy.Encode()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := DigResultReply(tbl, 94, [][]byte{record}, 4, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	v, used, err := tbl.DecodeValue(frame, 9)
	if err != nil || used != len(frame) {
		t.Fatalf("9/3 回包解不开: %v", err)
	}
	o := v.(*wire.Obj)
	if b := o.Fields[0].Value.(wire.OptByte); b.V != 1 {
		t.Fatalf("f0=%d", b.V)
	}
	f1 := o.Fields[1].Value.(wire.Args)
	if f1.Code != "61" || len(f1.Items) != 1 {
		t.Fatalf("f1=%+v", f1)
	}
	f6 := o.Fields[6].Value.(wire.Args)
	if f6.Code != "61" || len(f6.Items) != 1 {
		t.Fatalf("f6=%+v", f6)
	}
	gain, ok := o.Fields[2].Value.(*wire.Obj)
	if !ok || gain.TypeID != 310 {
		t.Fatalf("f2 应为 310 熟练度块，实际 %T %#v", o.Fields[2].Value, o.Fields[2].Value)
	}
	if got := gain.Fields[2].Value.(wire.OptByte).V; got != 4 {
		t.Fatalf("310 f2 SkillType=%d，期望捕获鱼饵=4", got)
	}
	if got := wire.BitsF32(gain.Fields[3].Value.(wire.Num32).V); got < 0.0999 || got > 0.1001 {
		t.Fatalf("310 f3 熟练度增量=%f，期望 0.1", got)
	}
	if _, ok := o.Fields[3].Value.(wire.Null); !ok {
		t.Fatalf("803 f3 必须保持 null，不能写成整数，实际 %#v", o.Fields[3].Value)
	}
}

func TestProducedReplyCarriesOutputAndBagUpsert(t *testing.T) {
	path, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := wire.LoadTable(path)
	if err != nil {
		t.Fatal(err)
	}
	dummy, err := tbl.NewObj(ResultType)
	if err != nil {
		t.Fatal(err)
	}
	record, err := dummy.Encode()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := ProducedReply(tbl, 95, [][]byte{record}, SkillCaptureBait, MasteryPerCraft)
	if err != nil {
		t.Fatal(err)
	}
	v, used, err := tbl.DecodeValue(frame, 9)
	if err != nil || used != len(frame) {
		t.Fatalf("9/1 回包解不开: used=%d len=%d err=%v", used, len(frame), err)
	}
	o := v.(*wire.Obj)
	if got := o.Fields[1].Value.(wire.Args); got.Code != "61" || len(got.Items) != 1 {
		t.Fatalf("f1 产物列表不对: %+v", got)
	}
	if got := o.Fields[6].Value.(wire.Args); got.Code != "61" || len(got.Items) != 1 {
		t.Fatalf("f6 背包增量不对: %+v", got)
	}
	gain, ok := o.Fields[2].Value.(*wire.Obj)
	if !ok || gain.TypeID != 310 {
		t.Fatalf("f2 应为 310 熟练度块，实际 %T %#v", o.Fields[2].Value, o.Fields[2].Value)
	}
	if got := gain.Fields[2].Value.(wire.OptByte).V; got != SkillCaptureBait {
		t.Fatalf("310 f2 SkillType=%d，期望 %d", got, SkillCaptureBait)
	}
	if got := wire.BitsF32(gain.Fields[3].Value.(wire.Num32).V); got < 0.0999 || got > 0.1001 {
		t.Fatalf("310 f3 熟练度增量=%f，期望0.1", got)
	}
	if _, ok := o.Fields[3].Value.(wire.Null); !ok {
		t.Fatalf("f3 必须为 null，实际 %#v", o.Fields[3].Value)
	}
}
