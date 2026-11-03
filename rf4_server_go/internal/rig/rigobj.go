package rig

// 竿上那个 153 钓组对象的读写：零件表就是它字段 #2 里的参数表"81"，
// 每条是一个 152{ 品类, 零件GUID, 槽索引 }。这些函数只管字节结构，不碰库。

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"rf4server/internal/gamestore"
	"rf4server/internal/wire"
)

// Logger 装配这一域的日志出口（logging.Logger 正好符合）。没给就闭嘴。
type Logger interface {
	Line(topic, msg string)
}

// WithLogger 挂个日志器进去，镜像写坏了能说一声
func (s *Service) WithLogger(l Logger) *Service {
	if s != nil {
		s.log = l
	}
	return s
}

// typeIDOf 按混淆类名查类型号（类型号一律从字段表拿，别写死）
func (s *Service) typeIDOf(class string) (int, error) {
	c, ok := s.tbl.Class(class)
	if !ok {
		return 0, fmt.Errorf("字段表里没有这个类 %s", class)
	}
	return c.TypeID, nil
}

// rigObjOf 取竿上的钓组对象（第 5 格）。create=true 时没有就现造一个空的：
// 名字 null、类型 1、零件表 args"81" 空 —— 真服那些没装过的竿就是这个形状。
func rigObjOf(rod *wire.Obj, create func() (*wire.Obj, error)) (*wire.Obj, error) {
	f := fieldAt(rod, 5)
	if f == nil {
		return nil, fmt.Errorf("竿记录没有第 5 格（钓组那一格）")
	}
	if o, ok := f.Value.(*wire.Obj); ok && o != nil {
		return o, nil
	}
	if !createNeeded(create) {
		return nil, nil
	}
	o, err := create()
	if err != nil {
		return nil, err
	}
	f.Value = o
	return o, nil
}

// createNeeded 把「传了构造函数 = 要造」这层意思说清楚一点
func createNeeded(create func() (*wire.Obj, error)) bool { return create != nil }

// rigTypeOf 这根竿现在的钓组类型（决定哪些零件槽能用）
func rigTypeOf(rod *wire.Obj) int {
	rig, err := rigObjOf(rod, nil)
	if err != nil || rig == nil {
		return 0
	}
	f := fieldAt(rig, 1)
	if f == nil {
		return 0
	}
	switch v := f.Value.(type) {
	case wire.Num32:
		return int(v.V)
	case wire.OptByte:
		return int(v.V)
	}
	return 0
}

// partsOf 现在的零件列表（没有钓组对象就是空表）
func partsOf(rod *wire.Obj) []*wire.Obj {
	rig, err := rigObjOf(rod, nil)
	if err != nil || rig == nil {
		return nil
	}
	f := fieldAt(rig, 2)
	if f == nil {
		return nil
	}
	args, ok := f.Value.(wire.Args)
	if !ok {
		return nil
	}
	var out []*wire.Obj
	for _, it := range args.Items {
		if o, ok := it.(*wire.Obj); ok {
			out = append(out, o)
		}
	}
	return out
}

// setParts 重写零件表。真服即使一个零件没有也保留那个 args"81" 容器，所以这边照做。
func setParts(rod *wire.Obj, t *wire.Table, keep []*wire.Obj) error {
	rigID, ok := t.Class(rigClass)
	if !ok {
		return fmt.Errorf("字段表里没有钓组类 %s", rigClass)
	}
	rig, err := rigObjOf(rod, func() (*wire.Obj, error) { return t.NewObj(rigID.TypeID) })
	if err != nil {
		return err
	}
	if rig == nil {
		return fmt.Errorf("竿上没有钓组那一格")
	}
	items := make([]wire.Value, 0, len(keep))
	for _, o := range keep {
		items = append(items, o)
	}
	f := fieldAt(rig, 2)
	if f == nil || f.Op != "VAL" {
		return fmt.Errorf("钓组对象没有零件那一格")
	}
	f.Value = wire.Args{Code: "81", Items: items}
	if name := fieldAt(rig, 0); name != nil && name.Op == "STR" {
		// 钓组名保持「没名字」：真服那些竿这里都是 null，别自己编一个上去
		if s, ok := name.Value.(wire.StrField); ok && s.IsNull {
			name.Value = wire.StrField{IsNull: true}
		}
	}
	return nil
}

// addPart 往零件表里加一条 152
func addPart(rod *wire.Obj, t *wire.Table, cat int, guid string, index int) error {
	partID, ok := t.Class(partClass)
	if !ok {
		return fmt.Errorf("字段表里没有零件类 %s", partClass)
	}
	part, err := t.NewObj(partID.TypeID)
	if err != nil {
		return err
	}
	if err := part.SetByteAt(0, uint8(cat)); err != nil {
		return err
	}
	if err := part.SetGUIDAt(1, strings.ToLower(guid)); err != nil {
		return err
	}
	if err := part.SetU32At(2, uint32(index)); err != nil {
		return err
	}
	return setParts(rod, t, append(partsOf(rod), part))
}

// partGUID / partCat / partIndex 读一条零件的三个字段
func partGUID(part *wire.Obj) string {
	if part == nil {
		return ""
	}
	f := fieldAt(part, 1)
	if f == nil {
		return ""
	}
	if g, ok := f.Value.(wire.GUID); ok {
		return strings.ToLower(g.Hex)
	}
	return ""
}

func partCat(part *wire.Obj) int {
	f := fieldAt(part, 0)
	if f == nil {
		return 0
	}
	switch v := f.Value.(type) {
	case wire.OptByte:
		return int(v.V)
	case wire.Num32:
		return int(v.V)
	}
	return 0
}

func partIndex(part *wire.Obj) int {
	f := fieldAt(part, 2)
	if f == nil {
		return 0
	}
	if v, ok := f.Value.(wire.Num32); ok {
		return int(v.V)
	}
	return 0
}

// partByGUID 这个零件是不是已经在这根竿上了
func partByGUID(parts []*wire.Obj, guid string) *wire.Obj {
	want := strings.ToLower(guid)
	for _, p := range parts {
		if partGUID(p) == want {
			return p
		}
	}
	return nil
}

// takenIndexes 这个品类已经占了哪些槽索引
func takenIndexes(parts []*wire.Obj, cat int) map[int]bool {
	out := map[int]bool{}
	for _, p := range parts {
		if partCat(p) == cat {
			out[partIndex(p)] = true
		}
	}
	return out
}

// mirror 把「这根竿现在装了什么」投影进 rig / rig_component 两张表，
// 给少爷在数据库里直接看。写坏了只记一行日志，不拦游戏（权威永远是 objects.elem）。
func (s *Service) mirror(ctx context.Context, user, rodGUID string, rod *wire.Obj, objs []gamestore.Object) {
	if s.run == nil {
		return
	}
	st := gamestore.RigState{
		Username: user,
		RodGUID:  strings.ToLower(rodGUID),
		LineGUID: guidAt(rod, slotLine),
		ReelGUID: guidAt(rod, slotReel),
		RigType:  rigTypeOf(rod),
	}
	if r, err := rigObjOf(rod, nil); err == nil && r != nil {
		if f := fieldAt(r, 0); f != nil {
			if str, ok := f.Value.(wire.StrField); ok && !str.IsNull {
				st.RigName = str.S
			}
		}
	}
	for i, p := range partsOf(rod) {
		st.Parts = append(st.Parts, gamestore.RigPart{
			Slot:    partCat(p),
			Ordinal: i,
			GUID:    partGUID(p),
			ItemKey: keyOf(objs, partGUID(p)),
			Extra:   partIndex(p),
		})
	}
	if err := s.run.SaveRig(ctx, st); err != nil && s.log != nil {
		s.log.Line("db", "装配镜像写 rig/rig_component 没成（不影响游戏，权威在 objects.elem）: "+
			err.Error()+" 竿 "+st.RodGUID+" 零件 "+strconv.Itoa(len(st.Parts))+" 条")
	}
}
