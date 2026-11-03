package entry

import (
	"errors"
	"fmt"
	"strconv"

	"rf4server/internal/wire"
)

// 三个「拼不出来」的错误。宁可报错也别硬发一帧半生不熟的：
// 客户端拿到怪帧会一直卡在「下载中」，比后端当场说一句人话难查得多。
var (
	errNoTable  = errors.New("没加载协议字段表，拼不出对象")
	errNoClass  = errors.New("字段表里没有这个对象类型")
	errNoFields = errors.New("对象没有这个字段")
)

// badField 说清楚「第几个字段不是要的写入法」，字段表一改就能立刻定位
func badField(o *wire.Obj, idx int, want string) error {
	if idx < 0 || idx >= len(o.Fields) {
		return fmt.Errorf("%w：对象 %s(typeID %d) 只有 %d 个字段，要第 %d 个",
			errNoFields, o.Class, o.TypeID, len(o.Fields), idx)
	}
	f := o.Fields[idx]
	return fmt.Errorf("对象 %s(typeID %d) 第 %d 个字段 %s 的写入法是 %q，要的是 %s",
		o.Class, o.TypeID, idx, f.Name, f.Op, want)
}

// itoa 整数转字符串。拼错误说明文案用，不想为此在每个文件都 import strconv
func itoa(v int) string { return strconv.Itoa(v) }
