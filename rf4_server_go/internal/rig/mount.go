package rig

// 17/2 装上。整条路：认人 -> 找两件记录 -> 决定进哪个槽 -> 改字节 -> 写库 -> 回 800 快照。
// 任何一步不对都拒绝并说清原因（拒了也回一帧空 800，别让客户端转圈）。

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
)

// MountFrame 17/2：把 guids[1] 那件装到 guids[0] 这根竿上。
func (s *Service) MountFrame(ctx context.Context, user string, cid uint32, plain []byte) ([]byte, Result, error) {
	res := Result{Slot: -1, PartIndex: -1}
	if s == nil || s.tbl == nil {
		return nil, res, errNoTable
	}
	guids, _ := s.parseInto(plain)
	if len(guids) < 2 {
		res.Reason = "请求里 GUID 不够（该有竿和要装的东西）"
		return s.rejected(cid, res)
	}
	res.RodGUID, res.ItemGUID = guids[0], guids[1]
	if s.run == nil {
		res.Reason = "没接上 MySQL，装不了"
		return s.rejected(cid, res)
	}

	objs, err := s.run.Objects(ctx, user)
	if err != nil {
		return nil, res, fmt.Errorf("读 %s 名下物品失败: %w", user, err)
	}
	rod := findObj(objs, res.RodGUID)
	item := findObj(objs, res.ItemGUID)
	if rod == nil {
		res.Reason = "这根竿不在该账号名下"
		return s.rejected(cid, res)
	}
	if item == nil {
		res.Reason = "这件东西不在该账号名下（多半是界面上残留的影子物品）"
		return s.rejected(cid, res)
	}
	if rod.Elem == "" || item.Elem == "" {
		res.Reason = "库里这两条还不是完整的物件记录，先重新买一次才会有"
		return s.rejected(cid, res)
	}
	rodObj, err := s.decode(rod.Elem)
	if err != nil {
		res.Reason = "竿的记录解不开: " + err.Error()
		return s.rejected(cid, res)
	}
	itemObj, err := s.decode(item.Elem)
	if err != nil {
		res.Reason = "那件东西的记录解不开: " + err.Error()
		return s.rejected(cid, res)
	}
	if rodObj.Class != rodClass {
		res.Reason = fmt.Sprintf("目标不是竿（记录类 %s，类型 %d）", rodObj.Class, rodObj.TypeID)
		return s.rejected(cid, res)
	}
	cat, ok := s.category(item.ItemKey)
	if !ok {
		res.Reason = "static_item_def 里查不到 " + item.ItemKey + " 的品类，不知道往哪个槽装"
		return s.rejected(cid, res)
	}
	res.Cat = cat

	slot := compSlotFor(cat) // 鱼线 -> 第 3 格，渔轮 -> 第 4 格，其余进钓组零件表
	res.RigType = rigTypeOf(rodObj)
	parts := partsOf(rodObj)
	elems := make([][]byte, 0, 3)

	if slot >= 0 {
		f := fieldAt(rodObj, slot)
		if f == nil || f.Op != "OPTGUID" {
			res.Reason = fmt.Sprintf("竿记录第 %d 格不是组件 GUID 槽，形状和真服不一样", slot)
			return s.rejected(cid, res)
		}
		prev := guidAt(rodObj, slot)
		if prev != "" && prev != res.ItemGUID {
			res.Prev = prev
		}
		setGUIDAt(rodObj, slot, res.ItemGUID)
		res.Kind, res.Slot = "field", slot
	} else {
		if partByGUID(parts, res.ItemGUID) != nil {
			res.Reason = catName(cat) + " 已经装在这条竿上了，不用重复装"
			return s.rejected(cid, res)
		}
		want := partIndexListFor(cat, res.RigType)
		if len(want) == 0 {
			res.Reason = fmt.Sprintf("%s 在钓组类型 %d 上没有槽位（先在游戏里换个匹配的钓组类型）", catName(cat), res.RigType)
			return s.rejected(cid, res)
		}
		taken := takenIndexes(parts, cat)
		free := -1
		for _, ix := range want {
			if !taken[ix] {
				free = ix
				break
			}
		}
		if free < 0 {
			res.Reason = fmt.Sprintf("%s 在这个钓组上只有 %d 个槽，已经占满（先拆一个）", catName(cat), len(want))
			return s.rejected(cid, res)
		}
		if err := addPart(rodObj, s.tbl, cat, res.ItemGUID, free); err != nil {
			res.Reason = "写钓组零件表失败: " + err.Error()
			return s.rejected(cid, res)
		}
		res.Kind, res.PartIndex = "part", free
	}

	// 版本戳必须严格变大、零件的父 GUID 必须指向竿：少一件客户端就当「没变化」
	bumpCreated(rodObj)
	setParent(itemObj, rod.GUID)
	bumpCreated(itemObj)

	rodElem, err := s.encode(rodObj)
	if err != nil {
		res.Reason = "竿改完编不回去: " + err.Error()
		return s.rejected(cid, res)
	}
	itemElem, err := s.encode(itemObj)
	if err != nil {
		res.Reason = "那件改完编不回去: " + err.Error()
		return s.rejected(cid, res)
	}

	// 真服那条 800 就是这顺序：先物品、后竿；被顶掉那件跟后面
	elems = append(elems, itemElem, rodElem)
	if res.Prev != "" {
		if old := findObj(objs, res.Prev); old != nil && old.Elem != "" {
			if oo, e := s.decode(old.Elem); e == nil {
				setParent(oo, "") // 退回背包，不然它既在竿上又在背包里，界面会多一件删不掉的
				bumpCreated(oo)
				if b, e2 := s.encode(oo); e2 == nil {
					elems = append(elems, b)
					if _, e3 := s.run.UpdateObjectElem(ctx, user, strings.ToLower(old.GUID), hex.EncodeToString(b)); e3 != nil {
						res.Reason = "被顶掉那件写库失败: " + e3.Error()
					}
				}
			}
		}
	}

	if _, err := s.run.UpdateObjectElem(ctx, user, item.GUID, hex.EncodeToString(itemElem)); err != nil {
		res.Reason = "写 objects(物品) 失败: " + err.Error()
		return s.rejected(cid, res)
	}
	if _, err := s.run.UpdateObjectElem(ctx, user, rod.GUID, hex.EncodeToString(rodElem)); err != nil {
		res.Reason = "写 objects(竿) 失败: " + err.Error()
		return s.rejected(cid, res)
	}
	s.mirror(ctx, user, rod.GUID, rodObj, objs)

	res.OK, res.Records = true, len(elems)
	frame, err := s.frame(cid, elems)
	return frame, res, err
}
