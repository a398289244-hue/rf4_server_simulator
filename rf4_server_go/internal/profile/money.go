package profile

import "strconv"

// Money 把「最小单位」的钱写成日志里给人看的数（1 银币 = 100 最小单位）。
// 帧里一律发整数最小单位，只有打日志才换算，别在别处重复这个除法。
func Money(minor int64) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	s := strconv.FormatInt(minor/100, 10) + "." + pad2(minor%100)
	if neg {
		return "-" + s
	}
	return s
}

// pad2 角分补齐两位（7 分要显示 .07，不是 .7）
func pad2(v int64) string {
	if v < 10 {
		return "0" + strconv.FormatInt(v, 10)
	}
	return strconv.FormatInt(v, 10)
}
