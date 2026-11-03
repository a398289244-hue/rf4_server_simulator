// 这个文件管「物品编号表」那一帧（客户端 4/4）：告诉客户端「你名下一共有哪些物件」。
//
// 帧形（凭据 = 真服 4/4 应答 892 字节，55 件）：
//
//	01 | marker(-2) | call_id | 06 | 数量(u16) | GUID16 GUID16 ...
//
// 它是背包能不能「下载完成」的前半段：客户端先拿这份编号表，再去对 4/5 的物件记录；
// 编号表里没有的物件，界面上根本不会出现；编号表里有、记录里没有，就会一直转圈。
// 所以这两帧必须出自同一份数据 —— 都从 MySQL objects 现算，谁也别去翻录制。
package inventory

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"rf4server/internal/wire"
)

// GUIDs 列出账号名下所有物件的 GUID（16 字节一份），顺序就是 MySQL objects 的顺序。
// 顺序稳定很重要：客户端按这份清单逐个去认物件，来回换顺序会看着像「物品乱跳」。
func (s *Service) GUIDs(ctx context.Context, username string) ([][]byte, error) {
	if s.Run == nil {
		return nil, fmt.Errorf("没接上 MySQL，物品编号表暂时拼不出来")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	objs, err := s.Run.Objects(ctx, username)
	if err != nil {
		return nil, fmt.Errorf("读取名下物品失败: %w", err)
	}
	cats := map[string]int{}
	if s.Cat != nil {
		keys := make([]string, 0, len(objs))
		for _, o := range objs {
			keys = append(keys, o.ItemKey)
		}
		cats, err = s.Cat.Categories(ctx, keys)
		if err != nil {
			return nil, fmt.Errorf("读取物品类别失败: %w", err)
		}
	}
	out := make([][]byte, 0, len(objs))
	for _, o := range objs {
		// 4/4 报出的每个 GUID 都必须能在 4/5 收到详情。类别55包装和缺记录物品
		// 会被 4/5 跳过，这里也必须同步跳过，否则客户端会永远卡在“正在下载”。
		if cats[o.ItemKey] == CategoryContainer || o.Elem == "" {
			continue
		}
		if _, err := hex.DecodeString(o.Elem); err != nil {
			continue
		}
		raw, err := hex.DecodeString(o.GUID)
		if err != nil {
			return nil, fmt.Errorf("物品 %s(%s) 的 GUID 不是十六进制: %w", o.ItemKey, o.GUID, err)
		}
		if len(raw) != 16 {
			return nil, fmt.Errorf("物品 %s(%s) 的 GUID 是 %d 字节，要 16 字节", o.ItemKey, o.GUID, len(raw))
		}
		out = append(out, raw)
	}
	return out, nil
}

// BuildGUIDFrame 把 GUID 列表拼成一整帧应答（含 9 字节帧头）。
// 空列表也照样回（回一帧「你手上一件都没有」），不能不回 —— 客户端正等着这条收尾。
func BuildGUIDFrame(callID uint32, guids [][]byte) []byte {
	body, err := wire.GUIDArray{Items: guids}.Encode()
	if err != nil {
		// 只有 GUID 不是 16 字节才会走到这儿，GUIDs() 已经挡在前面了。
		// 真走到了就退化成空表：宁可少给几件，也别发一帧客户端解不开的。
		body = []byte{0x06, 0x00, 0x00}
	}
	return append(wire.ResponseHead(callID), body...)
}
