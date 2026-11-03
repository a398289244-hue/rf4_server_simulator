package itemwire

import (
	"fmt"

	"rf4server/internal/wire"
)

// Quantity 读物件记录里的可堆叠数量（字段 ljnnpokapig）。
func (s *Service) Quantity(frame []byte) (uint64, error) {
	o, err := s.decodeObj(frame)
	if err != nil {
		return 0, err
	}
	q, ok := numByName(o, fQty)
	if !ok {
		return 0, fmt.Errorf("itemwire: %s(typeID %d) 没有数量字段 %s", o.Class, o.TypeID, fQty)
	}
	return q, nil
}

// SetQuantity 改物件记录里的数量，其余字段原样保留并重新编码。
func (s *Service) SetQuantity(frame []byte, qty uint64) ([]byte, error) {
	o, err := s.decodeObj(frame)
	if err != nil {
		return nil, err
	}
	idx := fieldIdx(o, fQty)
	if idx < 0 {
		return nil, fmt.Errorf("itemwire: %s(typeID %d) 没有数量字段 %s", o.Class, o.TypeID, fQty)
	}
	setNumByOp(o, idx, o.Fields[idx].Op, qty)
	out, err := o.Encode()
	if err != nil {
		return nil, fmt.Errorf("itemwire: 改完数量重新编码失败: %w", err)
	}
	return out, nil
}

func (s *Service) decodeObj(frame []byte) (*wire.Obj, error) {
	if s == nil || s.table == nil {
		return nil, fmt.Errorf("itemwire: 协议字段表没载入")
	}
	v, used, err := s.table.DecodeValue(frame, 0)
	if err != nil {
		return nil, fmt.Errorf("itemwire: 物件记录解不开: %w", err)
	}
	if used != len(frame) {
		return nil, fmt.Errorf("itemwire: 物件记录后面还有 %d 字节没吃掉", len(frame)-used)
	}
	o, ok := v.(*wire.Obj)
	if !ok {
		return nil, fmt.Errorf("itemwire: 记录不是物件，实际 %T", v)
	}
	return o, nil
}
