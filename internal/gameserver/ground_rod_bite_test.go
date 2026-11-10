package gameserver

import (
	"testing"
	"time"

	"rf4server/internal/proto"
	"rf4server/internal/wiregen"
)

// castOneRod 走玩家真实顺序：17/7 取竿 → 14/1 准备 → 14/2 抛投。
func castOneRod(t *testing.T, s *Server, sess *Session, rod string) *FishState {
	t.Helper()
	take := dynamicTestCtx(s, rigRequestTree(t, proto.Guid(rod)))
	take.Main, take.Sub, take.Sess = 17, 7, sess
	if body, ok := hRodTake(take); !ok || len(body) == 0 {
		t.Fatalf("取竿 %s 失败", rod)
	}
	prep := dynamicTestCtx(s, rigRequestTree(t, proto.Guid(rod)))
	prep.Main, prep.Sub, prep.Sess = 14, 1, sess
	if body, ok := hCastPrepare(prep); !ok || len(body) == 0 {
		t.Fatalf("竿 %s 的 14/1 未受理", rod)
	}
	cast := dynamicTestCtx(s, rigRequestTree(t, proto.Guid(rod)))
	cast.Main, cast.Sub, cast.Sess = 14, 2, sess
	if body, ok := hCast(cast); !ok || len(body) == 0 {
		t.Fatalf("竿 %s 的 14/2 未受理抛竿", rod)
	}
	return s.fishingStateForRod(sess.Userid, rod)
}

// putRodOnGround 是 17/8 放竿：无响应、不重建装备、保留水中状态。
func putRodOnGround(t *testing.T, s *Server, sess *Session, rod string) {
	t.Helper()
	put := dynamicTestCtx(s, rigRequestTree(t, proto.Guid(rod)))
	put.Main, put.Sub, put.Sess, put.ExpectResponse = 17, 8, sess, false
	if body, ok := hRodPut(put); !ok || body != nil {
		t.Fatalf("放竿 %s 必须无响应保留水中状态", rod)
	}
}

// confirmHeldRodInWater 模拟当前手持竿发出的 14/7 入水动画位。该报文不带竿 GUID，
// 因而只能确认当前手上的一根；落地竿必须靠自己的兜底。
func confirmHeldRodInWater(t *testing.T, s *Server, sess *Session) {
	t.Helper()
	step := dynamicTestCtx(s, rigRequestTree(t, wiregen.MustMakeObj("lobemkloehp",
		map[int]*proto.Value{5: proto.U32(0x600)})))
	step.Main, step.Sub, step.Sess = 14, 7, sess
	if _, ok := hFishingStep(step); !ok {
		t.Fatal("手持竿 14/7 入水位未受理")
	}
}

// 玩家反馈："水底三把竿只有手上这根会刷鱼，放在地上的不会。"
// 14/7 动画位请求不带竿 GUID（treeStrsDeep 只有水域名），因此永远落在手持竿上；
// 地上的竿一帧都收不到，只能靠入水兜底继续判鱼。本用例全程不发 14/7。
func TestGroundRodsRollTheirOwnFish(t *testing.T) {
	s, _ := newRigTestServer(t)
	t.Cleanup(func() { stopFishingRecoveryTimers(s) })
	sess := registerBiteTestSession(t, s, 705, nil)
	setWeatherTestTunable(t, s, "bite_delay_fixed", "0.05")
	setWeatherTestTunable(t, s, "bite_inwater_fallback_seconds", "0.02")

	var rods []string
	var states []*FishState
	for i := range 3 {
		rod := baitTestRod(t, s)
		mountHookForBaitTest(t, s, rod, 0)
		baitTestMount(t, s, rod, "worm_01", 3, 350)
		st := castOneRod(t, s, sess, rod)
		st.mu.Lock()
		castActive, tickSeen := st.CastActive, st.TickSeen
		st.mu.Unlock()
		if !castActive || tickSeen {
			t.Fatalf("竿 %d 抛竿状态异常 cast_active=%v tick=%v", i+1, castActive, tickSeen)
		}
		if i < 2 {
			putRodOnGround(t, s, sess, rod)
		}
		rods = append(rods, rod)
		states = append(states, st)
	}
	confirmHeldRodInWater(t, s, sess)
	deadline := time.Now().Add(3 * time.Second)
	for {
		all := true
		for i, st := range states {
			st.mu.Lock()
			got := st.Pending != nil && st.Pending.GearGUID == rods[i]
			st.mu.Unlock()
			all = all && got
		}
		if all {
			break
		}
		if time.Now().After(deadline) {
			for i, st := range states {
				st.mu.Lock()
				t.Logf("竿 %d pending=%+v", i+1, st.Pending)
				st.mu.Unlock()
			}
			t.Fatal("放在地上的竿没有派到自己的鱼（只有手持竿刷鱼）")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.fishingState(sess.Userid) != states[2] {
		t.Fatal("三次抛投后手持竿应是最后一根")
	}
}

// 关掉兜底就等于回到旧行为：收不到动画位的地上竿永远挂着 NeedBite，一尾鱼都不派。
func TestGroundRodWithoutFallbackNeverRollsFish(t *testing.T) {
	s, _ := newRigTestServer(t)
	t.Cleanup(func() { stopFishingRecoveryTimers(s) })
	sess := registerBiteTestSession(t, s, 706, nil)
	setWeatherTestTunable(t, s, "bite_delay_fixed", "0.05")
	setWeatherTestTunable(t, s, "bite_inwater_fallback_seconds", "0")

	groundRod := baitTestRod(t, s)
	mountHookForBaitTest(t, s, groundRod, 0)
	baitTestMount(t, s, groundRod, "worm_01", 3, 350)
	ground := castOneRod(t, s, sess, groundRod)
	putRodOnGround(t, s, sess, groundRod)

	heldRod := baitTestRod(t, s)
	mountHookForBaitTest(t, s, heldRod, 0)
	baitTestMount(t, s, heldRod, "worm_01", 3, 350)
	held := castOneRod(t, s, sess, heldRod)
	// 手持竿有自己的一帧动画位，地上的竿没有。
	confirmHeldRodInWater(t, s, sess)

	// 两条计时器都在后台 goroutine 跑；CI 机器繁忙时固定睡 400ms 可能还没轮到，
	// 按状态轮询而不是赌调度速度。
	if !rodWaitUntil(func() bool {
		f := rodPendingFish(held)
		return f != nil && f.GearGUID == heldRod
	}, 5*time.Second) {
		t.Fatal("手持竿应正常派鱼")
	}
	if !rodWaitUntil(func() bool { return rodStillWaiting(ground) }, 5*time.Second) {
		t.Fatalf("兜底关闭时地上竿应挂着等入水：pending=%+v", rodPendingFish(ground))
	}
}

// 手持竿没有 14/7 不能套用落地竿的入水兜底：这通常就是岸抛，既不能派鱼，
// 也不能把挂在钓组上的 PVA/窝料扣掉。
func TestHeldRodWithoutWaterStepDoesNotUseFallback(t *testing.T) {
	s, db := newRigTestServer(t)
	t.Cleanup(func() { stopFishingRecoveryTimers(s) })
	sess := registerBiteTestSession(t, s, 707, nil)
	setWeatherTestTunable(t, s, "bite_delay_fixed", "0.01")
	setWeatherTestTunable(t, s, "bite_inwater_fallback_seconds", "0.02")

	rod := baitTestRod(t, s)
	mountHookForBaitTest(t, s, rod, 0)
	baitTestMount(t, s, rod, "worm_01", 3, 350)
	pva, err := s.CreateItem(t.Context(), sess.Userid, "pva_stick_s", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	res := s.rigSetComponent(t.Context(), sess.Userid, rod, 0, pva.GUID, 0,
		rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
	if res.Rejected || res.MountedGUID == "" {
		t.Fatalf("挂载 PVA 失败: %+v", res)
	}

	st := castOneRod(t, s, sess, rod)
	if !rodWaitUntil(func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.NeedBite && !st.InWater && st.Pending == nil
	}, 5*time.Second) {
		st.mu.Lock()
		defer st.mu.Unlock()
		t.Fatalf("手持竿未等 14/7：in_water=%v need_bite=%v pending=%+v", st.InWater, st.NeedBite, st.Pending)
	}
	if row, err := db.GetItemByGUID(t.Context(), res.MountedGUID); err != nil || row == nil {
		t.Fatalf("手持岸抛未收到 14/7 时不得消耗 PVA: row=%+v err=%v", row, err)
	}
}
