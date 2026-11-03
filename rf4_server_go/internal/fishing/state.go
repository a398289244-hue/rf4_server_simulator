package fishing

import (
	"sync"
	"time"
)

// State 一条连接上的钓鱼状态：手上这套钓具是哪个 GUID、这次钩上了哪条鱼、
// 一共抛了几竿咬了几口留了几条。每个会话一份，挂在 Session 上。
//
// 为什么要加锁：咬钩是定时器 goroutine 写（推送之前要把这条鱼存进来），
// 而 14/4、14/5 是帧循环那个 goroutine 读。两个 goroutine 碰同一份数据，不锁就会打架。
type State struct {
	mu      sync.Mutex
	gear    string // 最近一次 14/1 里客户端报上来的钓具 GUID
	place   string // 最近一次 14/2 里读到的钓点名（鱼卡片 #10 那格要填）
	thrown  bool   // 已收到 14/2，钓组正在飞行或等待第一帧水中状态
	inWater bool   // 已收到 14/7：饵/拟饵现在确实在水中，只有此时才允许派鱼
	castSeq uint64 // 每次抛出、入水、收线都会递增；旧定时器拿旧序号就自动作废
	retries int    // 当前这条 14/14 被客户端拒收后的重推次数
	pending *Fish  // 已经推了 14/14、还没起鱼的这条鱼；nil = 现在没鱼咬钩
	last    *Fish  // 最近咬过一次钩的那条（客户端 keep 慢半拍时兜底用）
	casts   int    // 抛竿次数
	bites   int    // 咬钩次数
	kept    int    // 留鱼次数
}

// SetGear 记下这次用的钓具 GUID（14/1 一到就调）。
func (st *State) SetGear(guid string) {
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.gear = guid
	st.thrown = false
	st.inWater = false
	st.castSeq++
	st.retries = 0
	st.pending = nil
	st.casts++
}

// MarkThrown 记录 14/2。此时只是钓组已经飞出去，还不能断言饵已经进水；
// 真正的入水证据是客户端随后开始发送的 14/7 水中钓组状态。
func (st *State) MarkThrown(guid string) {
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if guid != "" {
		st.gear = guid
	}
	st.thrown = true
	st.inWater = false
	st.castSeq++
	st.retries = 0
	st.pending = nil
}

// MarkInWater 由这一竿的首条 14/7 调用。first=false 表示只是后续周期状态，
// 不应重复启动咬钩定时器。
func (st *State) MarkInWater() (seq uint64, first bool) {
	if st == nil {
		return 0, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.thrown && !st.inWater {
		return 0, false
	}
	if st.inWater {
		return st.castSeq, false
	}
	st.thrown = false
	st.inWater = true
	st.castSeq++
	st.retries = 0
	return st.castSeq, true
}

// InWater 判断某个定时器是否仍属于当前这竿、且饵仍在水中。
func (st *State) InWater(seq uint64) bool {
	if st == nil {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.inWater && st.castSeq == seq
}

// LeaveWater 由收线/起鱼调用。递增序号会让已经排队的旧定时器立即失效。
// pending 暂时保留，因为 14/4 应答和紧随其后的 14/5 留鱼还需要它。
func (st *State) LeaveWater() {
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.thrown = false
	st.inWater = false
	st.castSeq++
	st.retries = 0
}

// Gear 现在这套钓具的 GUID（没记过就是空串）
func (st *State) Gear() string {
	if st == nil {
		return ""
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.gear
}

// SetPlace 记下钓点名（能从 14/2 请求里读到就填，读不到就算了）
func (st *State) SetPlace(name string) {
	if st == nil || name == "" {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.place = name
}

// Place 最近那次抛竿的钓点名
func (st *State) Place() string {
	if st == nil {
		return ""
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.place
}

// Arm 记一条刚推出去的咬钩。f 是这条鱼的全部事实，14/4/14/5 都靠它认账。
func (st *State) Arm(f Fish) {
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	cp := f
	st.pending = &cp
	st.last = &cp
	st.bites++
}

// ArmIfInWater 原子地复核“还是这一竿、饵仍在水中”再挂鱼。
// 定时器到点前若玩家已经收线或重新抛竿，这里返回 false，绝不把鱼推到岸上。
func (st *State) ArmIfInWater(seq uint64, f Fish) bool {
	if st == nil {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.inWater || st.castSeq != seq || st.pending != nil {
		return false
	}
	cp := f
	st.pending = &cp
	st.last = &cp
	st.bites++
	return true
}

// PendingRetry 给 14/12 f0=3 使用：客户端没有接收刚才的判鱼推送时，
// 最多重推两次同一条鱼。返回的序号仍受 InWater 守门。
func (st *State) PendingRetry() (Fish, uint64, int, bool) {
	if st == nil {
		return Fish{}, 0, 0, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.inWater || st.pending == nil || st.retries >= 2 {
		return Fish{}, st.castSeq, st.retries, false
	}
	st.retries++
	return *st.pending, st.castSeq, st.retries, true
}

// Pending 现在钩着的那条鱼（没有就 ok=false）
func (st *State) Pending() (Fish, bool) {
	if st == nil {
		return Fish{}, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.pending == nil {
		return Fish{}, false
	}
	return *st.pending, true
}

// Resolve 按 GUID 找这条鱼：先看正在钩着的那条，再看最近咬钩的那条（客户端的 keep
// 有可能慢半拍到重连之后才来）。都对不上就返回 ok=false，调用方自己决定怎么兜底。
func (st *State) Resolve(guid string) (Fish, bool) {
	if st == nil {
		return Fish{}, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, f := range []*Fish{st.pending, st.last} {
		if f != nil && (f.SetupGUID == guid || guid == "") {
			return *f, true
		}
	}
	return Fish{}, false
}

// ClearPending 起鱼/留鱼办完了，把「钩着的那条」清掉（省得 14/4 再来一次又算一遍）
func (st *State) ClearPending() {
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pending = nil
}

// MarkKept 留了一条鱼，计数 +1（断开那行日志要报）
func (st *State) MarkKept() {
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.kept++
}

// Stats 抛了几竿 / 咬了几口 / 留了几条（日志用）
func (st *State) Stats() (casts, bites, kept int) {
	if st == nil {
		return 0, 0, 0
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.casts, st.bites, st.kept
}

// BiteDelay 抽签决定这次「抛出去多久之后咬钩」。区间从外面给（命令行可调），
// 实测真服这个数不是定值，随机才不像挂机脚本。
func BiteDelay(rnd interface{ Int63n(int64) int64 }, minDur, maxDur time.Duration) time.Duration {
	if maxDur <= minDur {
		return minDur
	}
	return minDur + time.Duration(rnd.Int63n(int64(maxDur-minDur)))
}
