package gamestore

// 装配这一层的库操作：改物件记录字节（objects.elem 是权威）、把「这根竿上装了什么」
// 镜像进 rig / rig_component 两张表给少爷直接看。
//
// 主次关系照 glance522 的口径：权威是物件记录本身（客户端读的就是它），
// 那两张表只是投影 —— 写失败不拦游戏，只记一行日志。

import (
	"context"
	"errors"
	"time"
)

// UpdateObjectElem 改一件货的物件记录字节（装配、卖鱼改完记录都要写回来）。
// 返回是否真改了这一行（false = 这个 GUID 不在这个号名下）。
func (m *MySQL) UpdateObjectElem(ctx context.Context, username, guid, elemHex string) (bool, error) {
	if m == nil || m.db == nil {
		return false, errors.New("gamestore: 没接上 MySQL")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := m.db.ExecContext(ctx,
		`UPDATE objects SET elem = ? WHERE username = ? AND guid = ?`,
		elemHex, username, guid)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

// RigPart 钓组上的一个零件（152 组件那条）。Slot 用 glance522 的口径 = 物品品类
// （11 鱼线 6 渔轮 28 浮漂 12 鱼钩 27 鱼饵 17 拟饵 13 前导），Extra 放 153 里那个槽索引。
type RigPart struct {
	Slot    int    // 槽位（=品类号）
	Ordinal int    // 在零件表里的第几条
	GUID    string // 零件自己的 GUID
	ItemKey string // 零件的物品编号
	Extra   int    // 153 里的槽索引
}

// RigState 一根竿现在的装配状态（rig + rig_component 那两张表的形状）
type RigState struct {
	Username string    // 谁的竿
	RodGUID  string    // 竿的 GUID
	LineGUID string    // 第 3 格：鱼线（空 = 没装）
	ReelGUID string    // 第 4 格：渔轮
	RigName  string    // 钓组名（真服那些竿这里多半是空）
	RigType  int       // 钓组类型（决定哪些零件槽能用）
	Parts    []RigPart // 钓组上的零件
}

// SaveRig 把一根竿的装配状态写进两张表（先删后插，保持和记录一致）。
// 表是「给少爷看的投影」，写坏了不拦游戏，所以调用方只管记日志。
func (m *MySQL) SaveRig(ctx context.Context, st RigState) error {
	if m == nil || m.db == nil {
		return errors.New("gamestore: 没接上 MySQL")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	at := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `DELETE FROM rig WHERE username = ? AND rod_guid = ?`, st.Username, st.RodGUID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO rig(username, rod_guid, line_guid, reel_guid, rig_name, rig_type, updated_at)
         VALUES(?,?,?,?,?,?,?)`,
		st.Username, st.RodGUID, nullableStr(st.LineGUID), nullableStr(st.ReelGUID),
		nullableStr(st.RigName), st.RigType, at); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rig_component WHERE rod_guid = ?`, st.RodGUID); err != nil {
		return err
	}
	for _, p := range st.Parts {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO rig_component(rod_guid, slot, ordinal, guid, item_key, extra, updated_at)
             VALUES(?,?,?,?,?,?,?)`,
			st.RodGUID, p.Slot, p.Ordinal, p.GUID, p.ItemKey, p.Extra, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}
