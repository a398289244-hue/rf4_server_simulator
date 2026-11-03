// Package itemstack 把可堆叠物品累加到同一件真实背包物件上。
//
// MySQL objects 表一件物品一行，数量存在 elem 记录的数量字段里；这不是快照。
// 挖饵、商城购买都应先找账号下同编号的顶层物件，加数量并更新同一个 GUID。
package itemstack

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"rf4server/internal/gamestore"
	"rf4server/internal/itemwire"
)

// Store 是堆叠所需的最小数据库操作集合，*gamestore.MySQL 直接满足。
type Store interface {
	Objects(ctx context.Context, username string) ([]gamestore.Object, error)
	InsertObject(ctx context.Context, o gamestore.Object) error
	UpdateObjectElem(ctx context.Context, username, guid, elemHex string) (bool, error)
}

// Result 一次加数量后的真实物件状态。
type Result struct {
	GUID           string
	Record         []byte
	Quantity       uint64
	Existing       bool
	DuplicateCount int
}

// Service 可堆叠物品入包服务。
type Service struct {
	iw  *itemwire.Service
	run Store
}

// New 造服务。
func New(iw *itemwire.Service, run Store) *Service { return &Service{iw: iw, run: run} }

// Add 把 newObj 携带的数量加到同编号的顶层物件；没有旧堆时插入 newObj。
// newObj.Elem 必须是已经拼好的标准物件记录，数量也写在里面。
func (s *Service) Add(ctx context.Context, username string, newObj gamestore.Object) (Result, error) {
	var res Result
	if s == nil || s.iw == nil || s.run == nil {
		return res, fmt.Errorf("itemstack: 服务没接完整")
	}
	raw, err := hex.DecodeString(newObj.Elem)
	if err != nil {
		return res, fmt.Errorf("itemstack: 新物件 elem 不是十六进制: %w", err)
	}
	add, err := s.iw.Quantity(raw)
	if err != nil {
		return res, fmt.Errorf("itemstack: 读新物件数量失败: %w", err)
	}

	objs, err := s.run.Objects(ctx, username)
	if err != nil {
		return res, fmt.Errorf("itemstack: 读背包失败: %w", err)
	}
	best := -1
	matches := 0
	for i, o := range objs {
		if o.ParentGUID != "" || !strings.EqualFold(o.ItemKey, newObj.ItemKey) {
			continue
		}
		matches++
		if best < 0 || (o.ItemID == newObj.ItemID && objs[best].ItemID != newObj.ItemID) {
			best = i
		}
	}
	if best < 0 {
		if err := s.run.InsertObject(ctx, newObj); err != nil {
			return res, fmt.Errorf("itemstack: 新堆入包失败: %w", err)
		}
		res.GUID, res.Record, res.Quantity = newObj.GUID, raw, add
		return res, nil
	}

	old := objs[best]
	oldRaw, err := hex.DecodeString(old.Elem)
	if err != nil {
		return res, fmt.Errorf("itemstack: 旧物件 %s elem 不是十六进制: %w", old.GUID, err)
	}
	before, err := s.iw.Quantity(oldRaw)
	if err != nil {
		return res, fmt.Errorf("itemstack: 读旧物件 %s 数量失败: %w", old.GUID, err)
	}
	total := before + add
	updated, err := s.iw.SetQuantity(oldRaw, total)
	if err != nil {
		return res, fmt.Errorf("itemstack: 更新数量失败: %w", err)
	}
	ok, err := s.run.UpdateObjectElem(ctx, username, old.GUID, hex.EncodeToString(updated))
	if err != nil {
		return res, fmt.Errorf("itemstack: 写回物件 %s 失败: %w", old.GUID, err)
	}
	if !ok {
		return res, fmt.Errorf("itemstack: 物件 %s 不在账号 %s 名下", old.GUID, username)
	}
	res.GUID, res.Record, res.Quantity = old.GUID, updated, total
	res.Existing = true
	if matches > 1 {
		res.DuplicateCount = matches - 1
	}
	return res, nil
}
