package itemdef

import "testing"

func TestBundleItemsReadsRealShape(t *testing.T) {
	attrs, err := ParseAttrs(`{"nbkhfhenbag":[{"cemjmlpafmc":"loaf_01","ljnnpokapig":10,"mlapkoadfnh":null}]}`)
	if err != nil {
		t.Fatal(err)
	}
	d := &Def{Key: "box_loaf_01_10", Cat: 55, Attrs: attrs}
	items, ok := d.BundleItems()
	if !ok || len(items) != 1 || items[0].Key != "loaf_01" || items[0].Quantity != 10 {
		t.Fatalf("礼包内容没按真实字段读出来: ok=%v items=%+v", ok, items)
	}
}

func TestBundleItemsRejectsBrokenDefinition(t *testing.T) {
	d := &Def{Key: "broken_box", Cat: 55, Attrs: map[string]Value{}}
	if items, ok := d.BundleItems(); ok || len(items) != 0 {
		t.Fatalf("损坏礼包不能被当成可售商品: ok=%v items=%+v", ok, items)
	}
}
