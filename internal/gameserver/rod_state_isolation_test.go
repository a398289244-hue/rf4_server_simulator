package gameserver

import (
	"testing"
	"time"

	"rf4server/internal/proto"
)

// 玩家反馈：水底三把竿里只有手上那把会刷鱼。根因是入水动画位（14/7）不带竿 GUID，
// 永远只落在手持竿那一行状态上。下面钉住修好的语义：每把竿只用自己的那一行状态，
// 算自己的入水时刻、自己的水层、自己的鱼；放在地上不管的竿与拿在手上一视同仁。

func rodDepthCM(st *FishState) int64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.DepthCM
}

func rodPendingFish(st *FishState) *RolledFish {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.Pending
}

func rodStillWaiting(st *FishState) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.NeedBite && st.Pending == nil
}

// setRodDepth 走真实 17/12：一把竿的调深只改自己那一行状态。
func setRodDepth(t *testing.T, s *Server, sess *Session, rod string, depthCM uint32) {
	t.Helper()
	ctx := dynamicTestCtx(s, rigRequestTree(t, proto.Guid(rod), proto.U32(depthCM)))
	ctx.Main, ctx.Sub, ctx.Sess = 17, 12, sess
	if _, ok := hRodSetDepth(ctx); !ok {
		t.Fatalf("竿 %s 的 17/12 未受理", shortGUID(rod))
	}
}

func rodWaitUntil(f func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if f() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGroundRodComputesItsOwnWaterLayerAndFish(t *testing.T) {
	s, _ := newRigTestServer(t)
	t.Cleanup(func() { stopFishingRecoveryTimers(s) })
	sess := registerBiteTestSession(t, s, 707, nil)
	setWeatherTestTunable(t, s, "bite_delay_fixed", "0.05")
	// 上限比两把竿各自的下沉时间都大：谁先到自己的水层，谁先开口。
	setWeatherTestTunable(t, s, "bite_inwater_fallback_seconds", "6")

	groundRod := baitTestRod(t, s)
	mountHookForBaitTest(t, s, groundRod, 0)
	baitTestMount(t, s, groundRod, "worm_01", 3, 350)
	ground := castOneRod(t, s, sess, groundRod) // 这把没调过深 -> 默认 300cm，要 12s 才到底
	putRodOnGround(t, s, sess, groundRod)

	heldRod := baitTestRod(t, s)
	mountHookForBaitTest(t, s, heldRod, 0)
	baitTestMount(t, s, heldRod, "worm_01", 3, 350)
	// 真实顺序是先取竿、再调自己那把的钓深、最后抛投：取竿之后这把竿已经有自己
	// 那一行状态，17/12 才落到它自己头上，而不是写回上一把。
	take := dynamicTestCtx(s, rigRequestTree(t, proto.Guid(heldRod)))
	take.Main, take.Sub, take.Sess = 17, 7, sess
	if body, ok := hRodTake(take); !ok || len(body) == 0 {
		t.Fatalf("取竿 %s 失败", shortGUID(heldRod))
	}
	setRodDepth(t, s, sess, heldRod, 40)
	held := castOneRod(t, s, sess, heldRod)

	if got := rodDepthCM(ground); got != 0 {
		t.Fatalf("地上竿的钓深被手上那把设定改掉了：depth_cm=%d", got)
	}
	if got := rodDepthCM(held); got != 40 {
		t.Fatalf("手上那把没记住自己的钓深：depth_cm=%d", got)
	}

	// 手上这把 40cm，1.6s 就到自己的水层；地上那把还差得远，只能挂起等自己。
	if !rodWaitUntil(func() bool { f := rodPendingFish(held); return f != nil && f.GearGUID == heldRod }, 3*time.Second) {
		t.Fatal("手上那把没按自己的钓深派鱼")
	}
	heldFish := rodPendingFish(held)
	if heldFish.DepthCM > 60 {
		t.Fatalf("手上那把用了别人的水层：depth_cm=%d", heldFish.DepthCM)
	}
	if !rodStillWaiting(ground) {
		t.Fatalf("地上那把提前借用了别人的入水时刻：pending=%+v", rodPendingFish(ground))
	}

	// 到本竿自己的下沉时间之后，地上的竿要拿到属于自己那一尾。
	if !rodWaitUntil(func() bool { f := rodPendingFish(ground); return f != nil && f.GearGUID == groundRod }, 15*time.Second) {
		t.Fatal("放在地上不管的竿没有派到自己的鱼")
	}
	groundFish := rodPendingFish(ground)
	if groundFish.DepthCM < 120 {
		t.Fatalf("地上竿的饵深用的是别人的数：%dcm，期望本竿下沉进度", groundFish.DepthCM)
	}
	if groundFish.FishGUID == heldFish.FishGUID {
		t.Fatal("两把竿共用了同一尾鱼")
	}
}
