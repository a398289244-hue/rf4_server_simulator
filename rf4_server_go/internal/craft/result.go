// Package craft 处理制作域命令。
package craft

import "rf4server/internal/wire"

// ResultType 是制作/分解统一结果物件 803（beckiiffkem）。
const ResultType = 803

// ResultReply 拼 9/3 的 803 回包。
// 分解暂时没有额外产出，因此 f1 固定为空 args("61")，f2 为 null。
func ResultReply(t *wire.Table, callID uint32, ok bool) ([]byte, error) {
	o, err := t.NewObj(ResultType)
	if err != nil {
		return nil, err
	}
	flag := uint8(0)
	if ok {
		flag = 1
	}
	if err := o.SetByteAt(0, flag); err != nil {
		return nil, err
	}
	if err := o.SetValAt(1, wire.Args{Code: "61"}); err != nil {
		return nil, err
	}
	if err := o.SetValAt(2, wire.Null{}); err != nil {
		return nil, err
	}
	body, err := o.Encode()
	if err != nil {
		return nil, err
	}
	return append(wire.ResponseHead(callID), body...), nil
}

// ProducedReply 拼 9/1 制作成功结果：f1 是本次产物，f2 是熟练度增长提示，
// f6 是背包新增/更新记录。客户端靠 f2 在结算画面显示“熟练度 +0.1%”。
func ProducedReply(t *wire.Table, callID uint32, records [][]byte, skillType uint8, masteryGain float64) ([]byte, error) {
	o, err := t.NewObj(ResultType)
	if err != nil {
		return nil, err
	}
	if err := o.SetByteAt(0, 1); err != nil {
		return nil, err
	}
	items := wire.RawItems(records)
	if err := o.SetValAt(1, wire.Args{Code: "61", Items: items}); err != nil {
		return nil, err
	}
	gain, err := masteryGainObj(t, skillType, masteryGain)
	if err != nil {
		return nil, err
	}
	if err := o.SetValAt(2, gain); err != nil {
		return nil, err
	}
	if err := o.SetValAt(3, wire.Null{}); err != nil {
		return nil, err
	}
	if err := o.SetValAt(6, wire.Args{Code: "61", Items: wire.RawItems(records)}); err != nil {
		return nil, err
	}
	body, err := o.Encode()
	if err != nil {
		return nil, err
	}
	return append(wire.ResponseHead(callID), body...), nil
}

// DigResultReply 拼 9/3 挖饵成功结果：
// f1/f7 带本次饵物件，f2 带 310 熟练度增长块。
// f3 在客户端里的强类型是 312（ficpllhkhbp），不是整数枚举；当前由后续 2/4
// 全量技能树负责刷新，这里保持 null。写整数会让客户端反序列化异常并断线。
// 少 f2 时数据库和技能面板仍会变，但挖饵结算界面不会显示「熟练度 +0.1%」。
func DigResultReply(t *wire.Table, callID uint32, records [][]byte, skillType uint8, masteryGain float64) ([]byte, error) {
	o, err := t.NewObj(ResultType)
	if err != nil {
		return nil, err
	}
	if err := o.SetByteAt(0, 1); err != nil {
		return nil, err
	}
	if err := o.SetValAt(1, wire.Args{Code: "61", Items: wire.RawItems(records)}); err != nil {
		return nil, err
	}
	gain, err := masteryGainObj(t, skillType, masteryGain)
	if err != nil {
		return nil, err
	}
	if err := o.SetValAt(2, gain); err != nil {
		return nil, err
	}
	if err := o.SetValAt(3, wire.Null{}); err != nil {
		return nil, err
	}
	if err := o.SetValAt(6, wire.Args{Code: "61", Items: wire.RawItems(records)}); err != nil {
		return nil, err
	}
	// f7 是物件实例数组（801 f9 同款语义）。部分客户端拿这一格生成“获得物品”卡片。
	if err := o.SetValAt(7, wire.Args{Code: "61", Items: wire.RawItems(records)}); err != nil {
		return nil, err
	}
	body, err := o.Encode()
	if err != nil {
		return nil, err
	}
	return append(wire.ResponseHead(callID), body...), nil
}

// masteryGainObj 按真服渔获结算里的 310 形状拼熟练度通知。
// f0 是经验值（挖饵不发人物经验），f2 是 SkillType，f3 是本次增长百分点。
func masteryGainObj(t *wire.Table, skillType uint8, masteryGain float64) (*wire.Obj, error) {
	o, err := t.NewObj(310)
	if err != nil {
		return nil, err
	}
	if masteryGain < 0 {
		masteryGain = 0
	}
	if err := o.SetU64At(0, 0); err != nil {
		return nil, err
	}
	if err := o.SetU32At(1, 0); err != nil {
		return nil, err
	}
	if err := o.SetByteAt(2, skillType); err != nil {
		return nil, err
	}
	if err := o.SetU32At(3, wire.F32Bits(masteryGain)); err != nil {
		return nil, err
	}
	if err := o.SetValAt(4, wire.Args{Code: "245"}); err != nil {
		return nil, err
	}
	if err := o.SetU32At(5, 0); err != nil {
		return nil, err
	}
	if err := o.SetValAt(6, wire.Null{}); err != nil {
		return nil, err
	}
	return o, nil
}
