package rig

// 这一堆小活儿都只碰「已经解码成 wire.Obj 的物件记录」：连库、发帧都不在这儿。
// 装配真正改的就是那几格字节，所以把它们收在一处，改协议只改这里。

import (
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"rf4server/internal/gamestore"
	"rf4server/internal/itemsync"
	"rf4server/internal/wire"
)

// errNoTable 字段表没载入，这一域什么都干不了
var errNoTable = errors.New("rig: 协议字段表没载入")

// parseInto 从请求里按帧里的先后捞 GUID 和整数（装配请求里就是「竿GUID、物品GUID、一个整数」）
func (s *Service) parseInto(plain []byte) (guids []string, ints []uint64) {
	if s == nil || s.tbl == nil || len(plain) <= argOff || plain[argOff] != 0x03 {
		return nil, nil
	}
	v, _, err := s.tbl.DecodeValue(plain, argOff)
	if err != nil {
		return nil, nil
	}
	wire.Walk(v, func(it wire.WalkItem) {
		switch it.Kind {
		case "guid":
			if it.Hex != "" && it.Hex != zeroGUID {
				guids = append(guids, strings.ToLower(it.Hex))
			}
		case "u32", "u64", "u16", "byte":
			ints = append(ints, it.Num)
		}
	})
	return guids, ints
}

// frame 拼一帧 800 同步快照（#00 放改过的记录，其余两个字段留空）
func (s *Service) frame(cid uint32, elems [][]byte) ([]byte, error) {
	return itemsync.Frame(s.tbl, cid, elems, nil, nil)
}

// decode 物件记录 hex -> 可改的对象树。解不开就报错，绝不猜。
func (s *Service) decode(elemHex string) (*wire.Obj, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(elemHex))
	if err != nil {
		return nil, err
	}
	v, used, err := s.tbl.DecodeValue(raw, 0)
	if err != nil {
		return nil, err
	}
	if used != len(raw) {
		return nil, errors.New("记录只解了 " + strconv.Itoa(used) + " 字节，整条 " + strconv.Itoa(len(raw)) + " 字节")
	}
	o, ok := v.(*wire.Obj)
	if !ok {
		return nil, errors.New("记录顶层不是物件")
	}
	return o, nil
}

// encode 对象树 -> 记录字节（wire 保证「解码再编码」一字不差，所以没改的格子不会变）
func (s *Service) encode(o *wire.Obj) ([]byte, error) {
	if o == nil {
		return nil, errors.New("rig: 对象是空的")
	}
	return o.Encode()
}

// category 这件东西是什么品类（决定进哪个槽）
func (s *Service) category(key string) (int, bool) {
	if s.def == nil {
		return 0, false
	}
	return s.def.CategoryOf(key)
}

// findObj 按 GUID 找这个号名下的一件货（两边大小写不一样，先归一再比）
func findObj(objs []gamestore.Object, guid string) *gamestore.Object {
	want := strings.ToLower(strings.TrimSpace(guid))
	if want == "" {
		return nil
	}
	for i := range objs {
		if strings.ToLower(objs[i].GUID) == want {
			return &objs[i]
		}
	}
	return nil
}

// keyOf GUID -> 物品编号，只为日志好认
func keyOf(objs []gamestore.Object, guid string) string {
	if o := findObj(objs, guid); o != nil {
		return o.ItemKey
	}
	return ""
}

// fieldAt 按序号取字段（越界返回 nil，调用方会给出一句说得清的原因）
func fieldAt(o *wire.Obj, idx int) *wire.Field {
	if o == nil || idx < 0 || idx >= len(o.Fields) {
		return nil
	}
	return &o.Fields[idx]
}

// guidAt 读一格的 GUID（OPTGUID 没值就是空串）
func guidAt(o *wire.Obj, idx int) string {
	f := fieldAt(o, idx)
	if f == nil {
		return ""
	}
	switch v := f.Value.(type) {
	case wire.OptGUIDField:
		if !v.Present {
			return ""
		}
		return strings.ToLower(v.Hex)
	case wire.GUID:
		return strings.ToLower(v.Hex)
	}
	return ""
}

// setGUIDAt 写一格的 GUID；传空串就是「这一格空着」
func setGUIDAt(o *wire.Obj, idx int, guid string) bool {
	f := fieldAt(o, idx)
	if f == nil {
		return false
	}
	g := strings.ToLower(strings.TrimSpace(guid))
	switch f.Op {
	case "OPTGUID":
		f.Value = wire.OptGUIDField{Present: g != "", Hex: g}
	case "GUID16":
		f.Value = wire.GUID{Hex: g}
	default:
		return false
	}
	return true
}

// setParent 改一件东西的父 GUID（装到竿上=指向竿；拆下来=清空）
func setParent(o *wire.Obj, parent string) bool {
	if o == nil {
		return false
	}
	for i := range o.Fields {
		if o.Fields[i].Name != fParent {
			continue
		}
		p := strings.ToLower(strings.TrimSpace(parent))
		if o.Fields[i].Op == "OPTGUID" {
			o.Fields[i].Value = wire.OptGUIDField{Present: p != "", Hex: p}
			return true
		}
		o.Fields[i].Value = wire.GUID{Hex: p}
		return true
	}
	return false
}

// bumpCreated 版本戳 +1。客户端拿它判断「这条记录变了吗」，不加它就白装。
// 位置优先按字段名找（adhajflcpcd），找不到退回真服对出来的下标。
func bumpCreated(o *wire.Obj) bool {
	if o == nil {
		return false
	}
	idx := -1
	for i := range o.Fields {
		if o.Fields[i].Name == fCreated {
			idx = i
			break
		}
	}
	if idx < 0 {
		switch int(o.TypeID) {
		case 157:
			idx = 16
		case 140:
			idx = 9
		case 155, 156:
			idx = 6
		}
	}
	f := fieldAt(o, idx)
	if f == nil {
		return false
	}
	switch v := f.Value.(type) {
	case wire.Num64:
		f.Value = wire.U64(v.V + 1)
		return true
	case wire.Num32:
		f.Value = wire.U32(v.V + 1)
		return true
	}
	return false
}

// compSlotFor 这个品类进竿的哪个专用格：鱼线 3、渔轮 4，其余 -1（走钓组零件表）
func compSlotFor(cat int) int {
	switch cat {
	case catLine:
		return slotLine
	case catReel:
		return slotReel
	}
	return -1
}

// catName 品类的人话名字（日志用）
func catName(cat int) string {
	if n, ok := catNames[cat]; ok {
		return n + "(" + strconv.Itoa(cat) + ")"
	}
	return "类别 " + strconv.Itoa(cat)
}

// partIndexListFor 这个钓组类型下，这个品类能用哪些槽索引（模板里没有就用全局兜底）
func partIndexListFor(cat, rigType int) []int {
	var out []int
	for _, s := range rigSlotTemplates[rigType] {
		if s.cat == cat {
			out = append(out, s.index)
		}
	}
	if len(out) > 0 {
		return out
	}
	if ix, ok := canonicalRigIndex[cat]; ok {
		return []int{ix}
	}
	return nil
}
