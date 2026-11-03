package rig

// 17/3 拆一件、17/4 全拆。
//
// 注意 17/3 请求里那个整数按 glance522 的口径是「槽位品类」（28 浮漂 / 12 鱼钩…），
// 只有 3 和 4 才是竿上的专用组件格。没给品类但给了物品 GUID，就按 GUID 精确拆。

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"rf4server/internal/wire"
)

// UnmountFrame 17/3。
func (s *Service) UnmountFrame(ctx context.Context, user string, cid uint32, plain []byte) ([]byte, Result, error) {
	return s.unmountFrame(ctx, user, cid, plain, false)
}

// UnmountAllFrame 17/4：这根竿上全部拆光（没装东西也回一帧空 800，客户端不转圈）。
func (s *Service) UnmountAllFrame(ctx context.Context, user string, cid uint32, plain []byte) ([]byte, Result, error) {
	return s.unmountFrame(ctx, user, cid, plain, true)
}

// unmountFrame 两条共用一条路：清掉槽里的 GUID / 零件条目，再把那些件退回背包。
func (s *Service) unmountFrame(ctx context.Context, user string, cid uint32, plain []byte, all bool) ([]byte, Result, error) {
	res := Result{Slot: -1, PartIndex: -1}
	if s == nil || s.tbl == nil {
		return nil, res, errNoTable
	}
	guids, ints := s.parseInto(plain)
	if len(guids) < 1 {
		res.Reason = "请求里没有竿的 GUID"
		return s.rejected(cid, res)
	}
	res.RodGUID = guids[0]
	itemGUID := ""
	if len(guids) >= 2 {
		itemGUID = guids[1]
	}
	// 第二个整数：3/4 是竿上的专用格，其它数字是「零件品类」
	wantSlot, wantCat := 0, 0
	if len(ints) >= 2 {
		n := int(ints[1])
		if n == slotLine || n == slotReel {
			wantSlot = n
		} else if n > 0 {
			wantCat = n
		}
	}
	res.Slot = wantSlot

	if s.run == nil {
		res.Reason = "没接上 MySQL，拆不了"
		return s.rejected(cid, res)
	}
	objs, err := s.run.Objects(ctx, user)
	if err != nil {
		return nil, res, fmt.Errorf("读 %s 名下物品失败: %w", user, err)
	}
	rod := findObj(objs, res.RodGUID)
	if rod == nil {
		res.Reason = "这根竿不在该账号名下"
		return s.rejected(cid, res)
	}
	if rod.Elem == "" {
		res.Reason = "库里这条还不是完整的物件记录"
		return s.rejected(cid, res)
	}
	rodObj, err := s.decode(rod.Elem)
	if err != nil {
		res.Reason = "竿的记录解不开: " + err.Error()
		return s.rejected(cid, res)
	}
	if rodObj.Class != rodClass {
		res.Reason = fmt.Sprintf("目标不是竿（记录类 %s，类型 %d）", rodObj.Class, rodObj.TypeID)
		return s.rejected(cid, res)
	}

	var removed []RemovedItem
	// 1) 竿上两个专用格
	for _, slot := range []int{slotLine, slotReel} {
		if wantSlot != 0 && wantSlot != slot && !all {
			continue
		}
		cur := guidAt(rodObj, slot)
		if cur == "" {
			continue
		}
		if itemGUID != "" && cur != itemGUID && !all {
			continue
		}
		setGUIDAt(rodObj, slot, "")
		removed = append(removed, RemovedItem{GUID: cur, Key: keyOf(objs, cur), Slot: slot})
	}
	// 2) 钓组零件（按 GUID、按品类，或者全拆）
	parts := partsOf(rodObj)
	var keep []*wire.Obj
	for _, part := range parts {
		pguid := partGUID(part)
		pcat := partCat(part)
		drop := all ||
			(itemGUID != "" && pguid == itemGUID) ||
			(wantCat != 0 && pcat == wantCat)
		if !drop {
			keep = append(keep, part)
			continue
		}
		removed = append(removed, RemovedItem{GUID: pguid, Key: keyOf(objs, pguid), Slot: pcat, Index: partIndex(part), Part: true})
	}
	if len(removed) > 0 {
		if err := setParts(rodObj, s.tbl, keep); err != nil {
			res.Reason = "重写钓组零件表失败: " + err.Error()
			return s.rejected(cid, res)
		}
	}
	if len(removed) == 0 {
		res.Reason = "这条竿上那几个槽本来就没装东西"
		return s.rejected(cid, res)
	}
	res.Removed, res.RigType = removed, rigTypeOf(rodObj)
	bumpCreated(rodObj)

	rodElem, err := s.encode(rodObj)
	if err != nil {
		res.Reason = "竿改完编不回去: " + err.Error()
		return s.rejected(cid, res)
	}
	// 真服顺序：先竿，再把拆下来的每件退回背包（父 GUID 清空 + 版本戳 +1）
	elems := [][]byte{rodElem}
	for _, r := range removed {
		it := findObj(objs, r.GUID)
		if it == nil || it.Elem == "" {
			continue
		}
		io2, err := s.decode(it.Elem)
		if err != nil {
			continue // 这件自己记录坏了不影响别的：竿已经更新过了
		}
		setParent(io2, "")
		bumpCreated(io2)
		b, err := s.encode(io2)
		if err != nil {
			continue
		}
		elems = append(elems, b)
		if _, err := s.run.UpdateObjectElem(ctx, user, strings.ToLower(it.GUID), hex.EncodeToString(b)); err != nil {
			res.Reason = "拆下来的 " + r.Key + " 写库失败: " + err.Error()
		}
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

// EmptyFrame 给 17/1（选钓组类型）、17/6（挂饵方式）用：规则还没搬，先认账别晾着客户端。
func (s *Service) EmptyFrame(cid uint32) ([]byte, error) {
	if s == nil || s.tbl == nil {
		return nil, errNoTable
	}
	return s.frame(cid, nil)
}
