package container

import (
	"context"
	"encoding/hex"
	"testing"

	"rf4server/internal/assetpaths"
	"rf4server/internal/gamestore"
	"rf4server/internal/wire"
)

func table(t *testing.T) *wire.Table {
	t.Helper()
	p, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatalf("找不到凭据目录: %v", err)
	}
	tt, err := wire.LoadTable(p)
	if err != nil {
		t.Fatalf("读协议字段表失败: %v", err)
	}
	return tt
}

// fakeSrc 内存版容器数据，用来测「借语法」和「空容器」这两条路
type fakeSrc struct {
	own    gamestore.ContainerRows
	ownErr error
	borrow gamestore.ContainerMeta
	borErr error
}

func (f fakeSrc) Container(ctx context.Context, user, name string) (gamestore.ContainerRows, error) {
	return f.own, f.ownErr
}

func (f fakeSrc) ContainerSyntax(ctx context.Context, name string) (gamestore.ContainerMeta, error) {
	return f.borrow, f.borErr
}

// TestContainerFrameMatchesRealServer 拿真服那条 21/1 应答当标准答案：
// 输入就是库里 return 那个号 workshop_02 那几行（当年从这条帧拆出来的），
// 输出必须逐字节一样 —— 不一样就说明这边拼回去拼歪了。
func TestContainerFrameMatchesRealServer(t *testing.T) {
	tbl := table(t)
	real, err := assetpaths.RealAnswer("21/1")
	if err != nil {
		t.Skipf("考卷里没有 21/1，这条不跑: %v", err)
	}
	my, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没连着，读不到容器行，这条不跑: %v", err)
	}
	defer my.Close()

	rows, err := my.Container(context.Background(), "return", "workshop_02")
	if err != nil {
		t.Skipf("库里没有 return/workshop_02 那几行: %v", err)
	}
	if len(rows.Items) == 0 {
		t.Skip("return 的容器是空的，比不出东西")
	}
	cid := uint32(real[5]) | uint32(real[6])<<8 | uint32(real[7])<<16 | uint32(real[8])<<24

	svc := New(tbl, fakeSrc{own: rows})
	frame, st, err := svc.Frame(context.Background(), "return", "workshop_02", cid)
	if err != nil {
		t.Fatalf("拼容器失败: %v", err)
	}
	if st.Items != len(rows.Items) {
		t.Fatalf("件数不对：拼了 %d 件，库里 %d 件（%s）", st.Items, len(rows.Items), st.Reason)
	}
	// 不比「整帧等长」：库里这个容器会被后来买入的东西长大一行，
	// 比的是「真服那 11 件，我们一件不差、字节一模一样」，多出来的算后来买进去的。
	mineArgs := mustArgs(t, tbl, frame)
	realArgs := mustArgs(t, tbl, real)
	if len(mineArgs.Items) < len(realArgs.Items) {
		t.Fatalf("我们只拼了 %d 件，真服那帧有 %d 件，少一件客户端就少一格",
			len(mineArgs.Items), len(realArgs.Items))
	}
	for i := range realArgs.Items {
		if enc(t, mustObj(t, realArgs.Items[i])) == "" {
			t.Fatalf("考卷第 %d 件解不开", i)
		}
		a := enc(t, mustObj(t, realArgs.Items[i]))
		b := enc(t, mustObj(t, mineArgs.Items[i]))
		if a != b {
			t.Fatalf("第 %d 件和真服不一样：\n 真服 %s\n 我们 %s", i, a, b)
		}
	}
	t.Logf("真服那 %d 件逐字节一样；库里现在共 %d 件（后来买入的也带上了）",
		len(realArgs.Items), len(mineArgs.Items))
}

// TestBorrowedSyntaxForEmptyAccount 这个号还没这个容器：语法借同名容器别人那一行，回一帧空列表
func TestBorrowedSyntaxForEmptyAccount(t *testing.T) {
	tbl := table(t)
	svc := New(tbl, fakeSrc{
		ownErr: gamestore.ErrNotFound,
		borrow: gamestore.ContainerMeta{Username: "return", Container: "workshop_02", Tag: 3, Field: "144", ElemTag: 249},
		borErr: nil,
	})
	frame, st, err := svc.Frame(context.Background(), "return2", "workshop_02", 9)
	if err != nil {
		t.Fatalf("空容器也报错: %v", err)
	}
	if st.Items != 0 {
		t.Fatalf("该回空列表，实际 %d 件", st.Items)
	}
	if st.SrcUser != "return" {
		t.Fatalf("日志要说清语法是借谁的，实际 %q", st.SrcUser)
	}
	v, _, err := tbl.DecodeValue(frame, 9)
	if err != nil {
		t.Fatalf("空容器回包解不开: %v", err)
	}
	args, ok := v.(wire.Args)
	if !ok || args.Code != "144" || len(args.Items) != 0 {
		t.Fatalf("该是参数表 144 零项，实际 %v", v)
	}
}

// TestBadRowIsSkippedNotFatal 库里某一行字节残缺：跳过它、别的照发，并把原因写进 Stats
func TestBadRowIsSkippedNotFatal(t *testing.T) {
	tbl := table(t)
	rows := gamestore.ContainerRows{
		Container: "workshop_02",
		Meta:      gamestore.ContainerMeta{Tag: 3, Field: "144", ElemTag: 249},
		Items: []gamestore.ContainerItem{
			{Pos: 0, GUID: "f1289f009f09364395075c15c8655ed5", Head: "01f9010000", CondBits: "81ff7f3f", N1Bits: "04000000", Tag: "293", Args: []uint32{36, 4}},
			{Pos: 1, GUID: "这串不是十六进制", Head: "01f9010000", CondBits: "00000000", N1Bits: "00000000"},
		},
	}
	svc := New(tbl, fakeSrc{own: rows})
	frame, st, err := svc.Frame(context.Background(), "return", "workshop_02", 3)
	if err != nil {
		t.Fatalf("一行坏了不该把整个容器判失败: %v", err)
	}
	if st.Items != 1 {
		t.Fatalf("该只发能发的那 1 件，实际 %d 件", st.Items)
	}
	if st.Reason == "" {
		t.Fatal("跳过了东西却没写原因")
	}
	if _, _, err := tbl.DecodeValue(frame, 9); err != nil {
		t.Fatalf("回包解不开: %v", err)
	}
}

// firstDiff 说清第一个不一样的字节在哪，比对着两串 hex 眼睛找快多了
func firstDiff(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return "第 " + itoa(i/2) + " 个字符起不一样（我们 " + string(a[i]) + " 真服 " + string(b[i]) + "）"
		}
	}
	return "前 " + itoa(n/2) + " 字节一样，长度不同：我们 " + itoa(len(a)/2) + " 真服 " + itoa(len(b)/2)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [24]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// head 取 hex 串前 40 个字符（够看帧头 + 前两件的头），失败信息里好对照
func head(h string) string {
	if len(h) <= 40 {
		return h
	}
	return h[:40]
}

// mustArgs 把应答帧解成顶层参数表
func mustArgs(t *testing.T, tbl *wire.Table, frame []byte) wire.Args {
	t.Helper()
	v, used, err := tbl.DecodeValue(frame, 9)
	if err != nil || used != len(frame) {
		t.Fatalf("应答解不开: %v", err)
	}
	a, ok := v.(wire.Args)
	if !ok {
		t.Fatalf("顶层不是参数表，是 %T", v)
	}
	return a
}

// mustObj 参数表里的一项 -> 对象
func mustObj(t *testing.T, v wire.Value) *wire.Obj {
	t.Helper()
	o, ok := v.(*wire.Obj)
	if !ok {
		t.Fatalf("那一项不是对象，是 %T", v)
	}
	return o
}

// enc 对象回编成 hex
func enc(t *testing.T, o *wire.Obj) string {
	b, err := o.Encode()
	if err != nil {
		t.Fatalf("回编失败: %v", err)
	}
	return hex.EncodeToString(b)
}
