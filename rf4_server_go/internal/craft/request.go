package craft

import (
	"fmt"
	"strings"

	"rf4server/internal/wire"
)

const (
	requestBodyOffset = 11
	requestTypeID     = 8
	maxCraftTimes     = 100
)

// Request 是 9/1 制作请求里当前需要的字段。
type Request struct {
	Recipe string
	Times  int
	Source string
}

// ParseRequest 从 args 树里优先找 obj8；旧客户端若没套 obj8，则退回取第一个配方字符串。
func ParseRequest(t *wire.Table, plain []byte) (Request, error) {
	var out Request
	if t == nil {
		return out, fmt.Errorf("制作请求缺少协议字段表")
	}
	if len(plain) <= requestBodyOffset {
		return out, fmt.Errorf("制作请求太短：%dB", len(plain))
	}
	v, _, err := t.DecodeValue(plain, requestBodyOffset)
	if err != nil {
		return out, fmt.Errorf("制作请求解码失败: %w", err)
	}
	if o := findObj(v, requestTypeID); o != nil {
		if len(o.Fields) > 0 {
			if s, ok := o.Fields[0].Value.(wire.StrField); ok && !s.IsNull {
				out.Recipe = strings.TrimSpace(s.S)
			}
		}
		if len(o.Fields) > 5 {
			if n, ok := o.Fields[5].Value.(wire.Num32); ok {
				out.Times = int(n.V)
			}
		}
		out.Source = "obj8"
	}
	if out.Recipe == "" {
		wire.Walk(v, func(it wire.WalkItem) {
			if out.Recipe != "" || it.Kind != "str" {
				return
			}
			s := strings.TrimSpace(it.S)
			if s != "" {
				out.Recipe = s
				out.Source = "strings"
			}
		})
	}
	if out.Recipe == "" {
		return out, fmt.Errorf("制作请求里没有配方编号")
	}
	if out.Times < 1 || out.Times > maxCraftTimes {
		out.Times = 1
	}
	return out, nil
}

func findObj(v wire.Value, typeID uint32) *wire.Obj {
	switch x := v.(type) {
	case *wire.Obj:
		if x.TypeID == typeID {
			return x
		}
		for _, f := range x.Fields {
			if o := findObj(f.Value, typeID); o != nil {
				return o
			}
		}
	case wire.Args:
		for _, it := range x.Items {
			if o := findObj(it, typeID); o != nil {
				return o
			}
		}
	case wire.ValList:
		for _, it := range x.Items {
			if o := findObj(it, typeID); o != nil {
				return o
			}
		}
	case wire.Dict:
		for _, p := range x.Pairs {
			if o := findObj(p.Val, typeID); o != nil {
				return o
			}
		}
	}
	return nil
}
