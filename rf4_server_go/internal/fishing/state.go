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
	st.casts++
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
