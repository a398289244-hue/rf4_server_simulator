package wire

import "time"

// 协议里几个「时刻」字段（1/2 服务器时间、物件 307 里那两个 FILETIME）用的是
// .NET FILETIME：从 1601-01-01 起算的 100 纳秒间隔数，写成 u64 小端。
// Go 的 Unix 时间从 1970-01-01 起算，中间正好差 Ticks1601To1970。
// 凭据：2026-10-31 真服 1/2 那帧回的是 134379133292066771，按这个常数倒推回去
// 正好是 2026-10-31 09:42:09 UTC（和抓包那一刻对得上）。

// Ticks1601To1970 从 1601-01-01 到 1970-01-01 的 100 纳秒间隔数
const Ticks1601To1970 = 116444736000000000

// Filetime 把一个 Go 时间换算成协议里的 FILETIME
func Filetime(now time.Time) uint64 {
	return uint64(now.UTC().UnixNano()/100 + Ticks1601To1970)
}

// FromFiletime 反方向：协议里的 FILETIME 换回 Go 时间（测试和日志核对用）
func FromFiletime(v uint64) time.Time {
	return time.Unix(0, int64(v-Ticks1601To1970)*100).UTC()
}

// Ticks0001To1970 从 0001-01-01 到 1970-01-01 的 100 纳秒间隔数。
// .NET DateTime.Ticks 是从 0001-01-01 起算的，比 FILETIME 又早 1600 年。
const Ticks0001To1970 = 621355968000000000

// NetTicks 把一个 Go 时间换算成物件记录「创建时间」那一格用的 .NET ticks。
// 凭据：真服那条购买应答里新建物件的创建时间是 1（不是时刻），
// 但装配、仓库那几帧里能看到真 ticks，所以两种都得能发。
func NetTicks(t time.Time) uint64 { return Ticks0001To1970 + uint64(t.UnixMilli())*10000 }

// FromNetTicks 反方向换算（测试和日志核对用）
func FromNetTicks(v uint64) time.Time {
	if v < Ticks0001To1970 {
		return time.UnixMilli(0).UTC()
	}
	return time.UnixMilli(int64((v - Ticks0001To1970) / 10000)).UTC()
}
