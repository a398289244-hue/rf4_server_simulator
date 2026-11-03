package rig

import (
	"strconv"
	"strings"
)

// Describe 把拆下来的那几件拼成一行，只为日志好看：
// "Korda 竿线(9 件) 第3格 / 浮漂 第28类槽索引125" 这种。
func Describe(items []RemovedItem) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		where := "第" + strconv.Itoa(it.Slot) + "格"
		if it.Part {
			where = catName(it.Slot) + " 槽索引 " + strconv.Itoa(it.Index)
		}
		key := it.Key
		if key == "" {
			key = shortGUID(it.GUID)
		}
		parts = append(parts, key+"("+where+")")
	}
	return strings.Join(parts, " ")
}

// shortGUID 前 8 位，日志里够认就行
func shortGUID(g string) string {
	if len(g) <= 8 {
		return g
	}
	return g[:8]
}
