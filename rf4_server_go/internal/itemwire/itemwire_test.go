package itemwire

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"rf4server/internal/assetpaths"
	"rf4server/internal/gamestore"
	"rf4server/internal/itemdef"
	"rf4server/internal/wire"
)

const zeroGUIDHex = "00000000000000000000000000000000"

// loadEnv 字段表 + 定义表（定义表在 MySQL static_item_def 里，库没起就跳过这条）。
func loadEnv(t *testing.T) (*wire.Table, *itemdef.Defs, *Service) {
	t.Helper()
	tp, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatalf("找不到凭据目录: %v", err)
	}
	tbl, err := wire.LoadTable(tp)
	if err != nil {
		t.Fatalf("读协议字段表失败: %v", err)
	}
	my, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没连着，定义表读不到，这条不跑: %v", err)
	}
	t.Cleanup(func() { _ = my.Close() })
	defs, err := itemdef.Load(context.Background(), itemdef.FromMySQL(my))
	if err != nil {
		t.Fatalf("读 static_item_def 失败: %v", err)
	}
	if n := defs.Count(); n < 10000 {
		t.Fatalf("定义表只读到 %d 行，static_item_def 应该有一万四千多行", n)
	}
	return tbl, defs, New(tbl, defs, Options{})
}

// buyTemplates 读真服购买应答那三条原文（data/buy_templates.json）。
// 它们是「考卷」：只在测试里当标准答案，运行期一行都不发。
func buyTemplates(t *testing.T) map[string]string {
	t.Helper()
	p, err := assetpaths.File("buy_templates.json")
	if err != nil {
		t.Skipf("没有 buy_templates.json 这份考卷，这条不跑: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读考卷失败: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("考卷解析失败: %v", err)
	}
	return m
}

// decodeAnswer 把应答帧解成顶层对象（应答头 9 字节：01 feffffff call_id）
func decodeAnswer(t *testing.T, tbl *wire.Table, frame []byte) *wire.Obj {
	t.Helper()
	if len(frame) < 10 {
		t.Fatalf("应答帧太短: %d 字节", len(frame))
	}
	v, used, err := tbl.DecodeValue(frame, 9)
	if err != nil {
		t.Fatalf("应答解不开: %v", err)
	}
	if used != len(frame) {
		t.Fatalf("只解到 %d 字节，整帧 %d 字节", used, len(frame))
	}
	o, ok := v.(*wire.Obj)
	if !ok {
		t.Fatalf("顶层不是对象，是 %T", v)
	}
	return o
}

// recordFromAnswer 从购买应答里取「那条物件记录」：obj804 的 #01 参数表"61" 第一项
func recordFromAnswer(t *testing.T, tbl *wire.Table, hexFrame string) *wire.Obj {
	t.Helper()
	frame, err := hex.DecodeString(strings.TrimSpace(hexFrame))
	if err != nil {
		t.Fatalf("考卷不是十六进制: %v", err)
	}
	top := decodeAnswer(t, tbl, frame)
	if top.TypeID != 804 {
		t.Fatalf("购买应答顶层该是 obj804，实际 %d", top.TypeID)
	}
	args := top.ArgsOf(P804Objects)
	if args == nil {
		t.Fatalf("obj804 第 %d 格不是参数表，实际 %T", P804Objects, top.Fields[P804Objects].Value)
	}
	if len(args.Items) != 1 {
		t.Fatalf("这次买一件，参数表里该有一条记录，实际 %d 条", len(args.Items))
	}
	rec, ok := args.Items[0].(*wire.Obj)
	if !ok {
		t.Fatalf("记录不是对象，是 %T", args.Items[0])
	}
	return rec
}

// zeroGUIDValues 把树里所有 GUID 抹成零。
// 真服的 GUID 是随机的（子件也是），我们这边随机一个 / 子件按 md5 派生，
// 比字节时这些位置必须豁免，其余每一格都得一模一样。
func zeroGUIDValues(v wire.Value) {
	switch x := v.(type) {
	case *wire.Obj:
		for i := range x.Fields {
			switch fv := x.Fields[i].Value.(type) {
			case wire.GUID:
				x.Fields[i].Value = wire.GUID{Hex: zeroGUIDHex}
			case wire.OptGUIDField:
				if fv.Present {
					x.Fields[i].Value = wire.OptGUIDField{Present: true, Hex: zeroGUIDHex}
				}
			default:
				zeroGUIDValues(fv)
			}
		}
	case wire.Args:
		for i := range x.Items {
			switch iv := x.Items[i].(type) {
			case wire.GUID:
				x.Items[i] = wire.GUID{Hex: zeroGUIDHex}
			default:
				zeroGUIDValues(iv)
			}
		}
	}
}

// fieldU64 / fieldStr 按混淆字段名读一个字段（取标准答案里的 GUID、时间、凭证）
func fieldU64(o *wire.Obj, name string) uint64 {
	v, ok := numByName(o, name)
	if !ok {
		t0 := 0
		return uint64(t0)
	}
	return v
}

func fieldStr(o *wire.Obj, name string) string {
	if o == nil {
		return ""
	}
	if f, ok := o.FieldByName(name); ok {
		if s, ok2 := f.Value.(wire.StrField); ok2 {
			return s.S
		}
	}
	return ""
}

// receiptFrom 从真服记录里把购买凭证那一格抠出来（照抄当输入，才能逐字节比）
func receiptFrom(t *testing.T, rec *wire.Obj) *Receipt {
	t.Helper()
	idx := fieldIdx(rec, fOrigin)
	if idx < 0 {
		return nil
	}
	rc, ok := rec.Fields[idx].Value.(*wire.Obj)
	if !ok {
		t.Logf("真服这条记录里没有凭证那一格（%T），就不比凭证", rec.Fields[idx].Value)
		return nil
	}
	return &Receipt{
		Store:   fieldStr(rc, rcStore),
		UserID:  int64(fieldU64(rc, rcBuyerID)),
		Balance: int64(fieldU64(rc, rcBalance)),
		Price:   int64(fieldU64(rc, rcPrice)),
		User:    fieldStr(rc, rcBuyer),
	}
}

// TestBuiltLineRecordMatchesRealServer 是最硬的一条：鱼线 mono_15063。
// 输入用真服那条记录里的 GUID 和凭证，输出必须和真服那 120 字节逐字节一样（GUID 抹零后比）。
func TestBuiltLineRecordMatchesRealServer(t *testing.T) {
	tbl, _, svc := loadEnv(t)
	tpl := buyTemplates(t)["mono_15063"]
	if tpl == "" {
		t.Skip("考卷里没有 mono_15063")
	}
	realRec := recordFromAnswer(t, tbl, tpl)
	guid := ""
	if f, ok := realRec.FieldByName(fGUID); ok {
		if g, ok2 := f.Value.(wire.GUID); ok2 {
			guid = g.Hex
		}
	}
	if guid == "" {
		t.Fatal("考卷记录里没有 GUID，比不了")
	}
	in := Input{Key: "mono_15063", GUID: guid, Created: u64p(fieldU64(realRec, fCreated)), Updated: u64p(fieldU64(realRec, fUpdated))}
	in.Receipt = receiptFrom(t, realRec)

	built, err := svc.Build(in)
	if err != nil {
		t.Fatalf("现拼失败: %v", err)
	}
	if built.TypeID != int(realRec.TypeID) {
		t.Fatalf("用的类型号不对：我们 %d，真服 %d", built.TypeID, realRec.TypeID)
	}

	// 两边都把 GUID 抹零（真服那个是随机号，我们这个是照抄它那个），其余每一格都得一样
	zeroGUIDValues(realRec)
	mine := mustDecode(t, tbl, built.Frame)
	zeroGUIDValues(mine)
	enc := func(o *wire.Obj) string {
		b, err := o.Encode()
		if err != nil {
			t.Fatalf("回编失败: %v", err)
		}
		return hex.EncodeToString(b)
	}
	if got, want := enc(mine), enc(realRec); got != want {
		t.Fatalf("现拼的记录和真服不一样：\n 我们 %d 字节 %s\n 真服 %d 字节 %s", len(got)/2, got, len(want)/2, want)
	}
}

// TestBuiltRodRecordMatchesRealServer 竿 tele_10048_SS13：类不同（157）、还有出厂坯/手柄两个子件。
// 子件 GUID 真服是随机的，所以两边都把 GUID 抹零再比。
func TestBuiltRodRecordMatchesRealServer(t *testing.T) {
	tbl, _, svc := loadEnv(t)
	tpl := buyTemplates(t)["tele_10048_SS13"]
	if tpl == "" {
		t.Skip("考卷里没有 tele_10048_SS13")
	}
	realRec := recordFromAnswer(t, tbl, tpl)
	guid := ""
	if f, ok := realRec.FieldByName(fGUID); ok {
		if g, ok2 := f.Value.(wire.GUID); ok2 {
			guid = g.Hex
		}
	}
	in := Input{Key: "tele_10048_SS13", GUID: guid, Created: u64p(fieldU64(realRec, fCreated)), Updated: u64p(fieldU64(realRec, fUpdated))}
	in.Receipt = receiptFrom(t, realRec)

	built, err := svc.Build(in)
	if err != nil {
		t.Fatalf("现拼竿失败: %v", err)
	}
	if built.TypeID != int(realRec.TypeID) {
		t.Fatalf("竿该用类表里那个类型号：我们 %d，真服 %d", built.TypeID, realRec.TypeID)
	}
	if len(built.SubGUIDs) == 0 {
		t.Fatal("竿的出厂部件没拼出来，客户端背包会解歪")
	}
	zeroGUIDValues(realRec)
	mine := mustDecode(t, tbl, built.Frame)
	zeroGUIDValues(mine)
	enc := func(o *wire.Obj) string {
		b, err := o.Encode()
		if err != nil {
			t.Fatalf("回编失败: %v", err)
		}
		return hex.EncodeToString(b)
	}
	if got, want := enc(mine), enc(realRec); got != want {
		t.Fatalf("现拼的竿和真服不一样：\n 我们 %d 字节\n 真服 %d 字节", len(got)/2, len(want)/2)
	}
}

// TestBuildsStayDecodable 是覆盖面：随便挑几百件各类货来拼，每件都必须「拼得出、还能解回来」。
// 这条管的是别把客户端解动的活儿整坏 —— 少一格、多一格都会在背包里卡住。
func TestBuildsStayDecodable(t *testing.T) {
	tbl, defs, svc := loadEnv(t)
	byCat := defs.Categories()
	var cats []int
	for c := range byCat {
		cats = append(cats, c)
	}
	sort.Ints(cats)

	tried, built, skipped := 0, 0, map[int]int{}
	for _, cat := range cats {
		keys := byCat[cat]
		limit := 6
		if len(keys) < limit {
			limit = len(keys)
		}
		for _, key := range keys[:limit] {
			def, ok := defs.Get(key)
			if !ok {
				continue
			}
			tried++
			if _, ok := svc.ClassFor(def); !ok {
				skipped[cat]++ // 兼容旧路径统计；现在通用回退应该让它保持 0
				continue
			}
			b, err := svc.Build(Input{Key: key})
			if err != nil {
				t.Fatalf("拼 %s（类别 %d）失败: %v", key, cat, err)
			}
			if len(b.Frame) < 6 {
				t.Fatalf("%s 拼出来才 %d 字节，肯定不对", key, len(b.Frame))
			}
			mustDecode(t, tbl, b.Frame)
			built++
		}
	}
	t.Logf("试了 %d 件，现拼成功 %d 件，通用类也拼不出的 %d 类次", tried, built, sumVals(skipped))
	if built < 100 {
		t.Fatalf("只拼成 %d 件，覆盖面不够（定义表或类表多半缺东西）", built)
	}
}

// TestBuildFallsBackForCategoryWithoutDedicatedClass 工具/材料等类别没有专属类时，
// 必须按通用物品类 140 现拼，而不是让商店整批显示“没库存”。
func TestBuildFallsBackForCategoryWithoutDedicatedClass(t *testing.T) {
	tbl, defs, svc := loadEnv(t)
	keys := defs.Categories()[41]
	if len(keys) == 0 {
		t.Skip("定义表里没有类别 41 的工具，跳过")
	}
	def, ok := defs.Get(keys[0])
	if !ok || def.ID == 0 {
		t.Fatalf("类别 41 第一件定义不完整: %+v", def)
	}
	if _, mapped := tbl.WireClassFor(def.Cat); mapped {
		t.Skipf("%s 所属类别已有专属 wire 类，换一件未知类别再测", def.Key)
	}
	built, err := svc.Build(Input{Key: def.Key})
	if err != nil {
		t.Fatalf("通用类回退失败: %v", err)
	}
	if built.Class != "jceogpbfpjh" || built.TypeID != 163 {
		t.Fatalf("回退类=%s/%d，期望真实基础物品类 jceogpbfpjh/163", built.Class, built.TypeID)
	}
	mustDecode(t, tbl, built.Frame)
}

// TestBuildRejectsWithoutDef 没定义的货要当场说清楚，不能发一条半截记录出去
func TestBuildRejectsWithoutDef(t *testing.T) {
	_, _, svc := loadEnv(t)
	if _, err := svc.Build(Input{Key: "这件货压根不存在"}); err == nil {
		t.Fatal("没定义也拼成功了，日志上就看不出为什么买不到东西")
	}
}

func mustDecode(t *testing.T, tbl *wire.Table, frame []byte) *wire.Obj {
	t.Helper()
	v, used, err := tbl.DecodeValue(frame, 0)
	if err != nil {
		t.Fatalf("现拼的记录解不开（%d 字节）: %v", len(frame), err)
	}
	if used != len(frame) {
		t.Fatalf("解到 %d 字节就断了，整条 %d 字节", used, len(frame))
	}
	o, ok := v.(*wire.Obj)
	if !ok {
		t.Fatalf("记录顶层不是对象，是 %T", v)
	}
	return o
}

func u64p(v uint64) *uint64 { return &v }

func sumVals(m map[int]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func TestQuantityRoundTrip(t *testing.T) {
	_, _, svc := loadEnv(t)
	built, err := svc.Build(Input{Key: "worm_01", Units: 1})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Quantity(built.Frame)
	if err != nil {
		t.Fatal(err)
	}
	if got != 30 {
		t.Fatalf("初始数量=%d，应为静态基础包 30", got)
	}
	frame, err := svc.SetQuantity(built.Frame, 19)
	if err != nil {
		t.Fatal(err)
	}
	got, err = svc.Quantity(frame)
	if err != nil {
		t.Fatal(err)
	}
	if got != 19 {
		t.Fatalf("更新后数量=%d，应为 19", got)
	}
}
