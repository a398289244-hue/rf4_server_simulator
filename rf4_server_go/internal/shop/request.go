package shop

// 请求侧的解析。22/1（开商店）、21/1（开容器）、22/2（买）三条请求都是同一个壳：
//
//	01 | marker | call_id | main | sub | 03 <短串参数表名> <条数 u16> | 各项 | 00
//	其中「店名/容器名」是一个 0x14 短串项，紧跟在参数表头后面
//
// 所以名字这一个函数就够三条命令共用；22/2 多一个物品名和一个报价，整棵值树走一遍捞出来最稳。

import (
	"rf4server/internal/wire"
)

// argOff 客户端请求里参数表的起点：9 字节应答式壳 + main + sub = 第 11 字节
const argOff = 11

// RequestName 从请求里取第一个字符串（22/1 是店名，21/1 是容器名）。
// 取不到返回空串，调用方按「没这个名字」处理，不会崩。
func RequestName(plain []byte) string {
	if len(plain) <= argOff || plain[argOff] != 0x03 {
		return ""
	}
	// 参数表壳：03 | 名字短串 | 条数 u16 —— 名字短串自己带长度，跳过去就是第一项
	after := plain[argOff+1:]
	if len(after) < 4 || after[0] == 0xff {
		return ""
	}
	nameLen := int(after[0])
	i := 1 + nameLen + 2 // 跳过 长度字节 + 参数表名 + 条数
	if i >= len(after) || after[i] != 0x14 {
		return ""
	}
	i++
	if i >= len(after) {
		return ""
	}
	strLen := int(after[i])
	i++
	if strLen <= 0 || i+strLen > len(after) {
		return ""
	}
	return string(after[i : i+strLen])
}

// Purchase 一条 22/2 购买请求里读出来的东西
type Purchase struct {
	Store       string // 在哪家店买的
	Item        string // 买了哪件货
	PriceClient int64  // 客户端报的价（仅供参考，真正扣多少以货架表为准）
}

// RequestPurchase 解一条购买请求。解不动就返回 ok=false，调用方回一帧空回执并把原因写日志。
func RequestPurchase(tbl *wire.Table, plain []byte) (Purchase, bool) {
	var out Purchase
	if tbl == nil || len(plain) <= argOff || plain[argOff] != 0x03 {
		return out, false
	}
	v, _, err := tbl.DecodeValue(plain, argOff)
	if err != nil {
		return out, false
	}
	var strs []string
	var nums []int64
	wire.Walk(v, func(it wire.WalkItem) {
		switch it.Kind {
		case "str":
			if it.S != "" {
				strs = append(strs, it.S)
			}
		case "u64", "u32":
			nums = append(nums, int64(it.Num))
		}
	})
	if len(strs) < 2 {
		return out, false // 至少要有「店名 + 物品名」
	}
	out.Store, out.Item = strs[0], strs[1]
	if len(nums) > 0 {
		out.PriceClient = nums[0]
	}
	return out, true
}
