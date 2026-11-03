// Package itemsync 拼「背包/装备同步」那一帧：物件 800（混淆类名 dlbahppbdad）。
//
// 三个字段（凭据 = 考卷里 14/2、14/5、17/2 那三条真服应答，逐字节拆出来的）：
//
//	#00 物件记录列表（参数表 "61"）  = 这次变了/新增/删完还剩的那几件，整包清单走 4/5
//	#01 钓组上的物件（参数表 "61"）  = 装配类回包才填，别处留空
//	#02 GUID 数组                    = 这次动过的东西（比如 4/7 删掉的那几件）
//
// 用得上它的命令一大串：4/6 挪位置、4/7 丢东西、4/8 使用、17/x 装配、22/2 购买、22/4…
// 所以单独成一个包，谁要回 800 就调这儿，别在每条命令里各拼一份。
package itemsync

import (
	"encoding/hex"
	"strings"

	"rf4server/internal/wire"
)

// TypeSnap 同步快照的物件类型号
const TypeSnap = 800

// EmptyFrame 空快照：三个字段全空。真服 14/2 那条应答就是它（29 字节），测试逐字节盯着。
func EmptyFrame(t *wire.Table, callID uint32) ([]byte, error) {
	return Frame(t, callID, nil, nil, nil)
}

// Frame 拼一帧 800 同步包。
//
//	items  放进 #00 的物件记录（已经编好的字节，通常就是 objects.elem）
//	rig    放进 #01 的钓组物件记录（装配那几件），没有就传 nil
//	guids  放进 #02 的 GUID（32 位十六进制字符串），告诉客户端「这几件动过了/没了」
func Frame(t *wire.Table, callID uint32, items [][]byte, rig [][]byte, guids []string) ([]byte, error) {
	o, err := t.NewObj(TypeSnap)
	if err != nil {
		return nil, err
	}
	if err := o.SetValAt(0, wire.Args{Code: "61", Items: wire.RawItems(items)}); err != nil {
		return nil, err
	}
	if err := o.SetValAt(1, wire.Args{Code: "61", Items: wire.RawItems(rig)}); err != nil {
		return nil, err
	}
	arr, err := guidArray(guids)
	if err != nil {
		return nil, err
	}
	if err := o.SetValAt(2, arr); err != nil {
		return nil, err
	}
	body, err := o.Encode()
	if err != nil {
		return nil, err
	}
	return append(wire.ResponseHead(callID), body...), nil
}

// guidArray GUID 字符串列表 -> 帧里的 GUID 数组。不合法的写一串零占位，
// 免得整帧拼不出来（条数必须对得上，客户端按条数读）。
func guidArray(guids []string) (wire.GUIDArray, error) {
	out := wire.GUIDArray{}
	for _, g := range guids {
		h := strings.ToLower(strings.ReplaceAll(g, "-", ""))
		if len(h) != 32 || !isHex(h) {
			h = "00000000000000000000000000000000"
		}
		raw, err := hex.DecodeString(h)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, raw)
	}
	return out, nil
}

// isHex 整串都是十六进制字符
func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
