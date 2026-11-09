package gameserver

import (
	"context"
	"testing"
	"time"

	"rf4server/internal/proto"
)

// 少爷实机实测：水底那根竿刚中钩，人还没走到竿边就被一条张力帧判成跑鱼。
// 放在地上的竿没有人在管泄力和收线张力，松线（放线）也好一直拉着也好，
// 都不该让已经咬钩上钩的鱼脱钩。取到手上之后才按正常搏鱼算。

func readFishingCounter(t *testing.T, s *Server, uid int64, key string) int64 {
	t.Helper()
	var v int64
	if err := s.DB.SQL.QueryRowContext(context.Background(),
		`SELECT COALESCE(SUM(value),0) FROM player_counters WHERE uid=? AND key=?`, uid, key).Scan(&v); err != nil {
		t.Fatalf("读计数器 %s 失败: %v", key, err)
	}
	return v
}

// 14/12 的 f0 帧：args = [竿 GUID, 鱼 GUID, 状态对象]，与实机报文一致。
func leftFrameCtx(t *testing.T, s *Server, sess *Session, rod, fish string, f0 uint64) *Ctx {
	t.Helper()
	ctx := dynamicTestCtx(s, proto.Args524(proto.Guid(rod), proto.Guid(fish),
		proto.Obj("lhicjpmglbo", 0, proto.Field{Op: "U32", Num: ptrU64(f0)})))
	ctx.Main, ctx.Sub, ctx.Sess = 14, 12, sess
	return ctx
}

func rodHookedVia14_11(t *testing.T, s *Server, sess *Session, rod string) {
	t.Helper()
	ctx := dynamicTestCtx(s, rigRequestTree(t, proto.Guid(rod)))
	ctx.Main, ctx.Sub, ctx.Sess = 14, 11, sess
	if _, ok := hFishingHooked(ctx); !ok {
		t.Fatalf("14/11 中钩未受理：%s", shortGUID(rod))
	}
}

func rodHookState(st *FishState) (pending *RolledFish, hooked, castActive bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.Pending, st.Hooked, st.CastActive
}

// 地上的竿：已中钩的鱼遇到松线/一直拉着的张力帧，不能被判成跑鱼。
func TestGroundRodHookedFishSurvivesTensionFrame(t *testing.T) {
	s, _ := newRigTestServer(t)
	t.Cleanup(func() { stopFishingRecoveryTimers(s) })
	sess := registerBiteTestSession(t, s, 711, nil)
	setWeatherTestTunable(t, s, "bite_delay_fixed", "0.05")
	setWeatherTestTunable(t, s, "bite_inwater_fallback_seconds", "1")

	groundRod := baitTestRod(t, s)
	mountHookForBaitTest(t, s, groundRod, 0)
	baitTestMount(t, s, groundRod, "worm_01", 3, 350)
	ground := castOneRod(t, s, sess, groundRod)
	putRodOnGround(t, s, sess, groundRod)
	// 手上另有一根，地上这根从这一刻起没人管张力。
	heldRod := baitTestRod(t, s)
	mountHookForBaitTest(t, s, heldRod, 0)
	baitTestMount(t, s, heldRod, "worm_01", 3, 350)
	castOneRod(t, s, sess, heldRod)

	if !rodWaitUntil(func() bool {
		f, _, _ := rodHookState(ground)
		return f != nil && f.GearGUID == groundRod
	}, 5*time.Second) {
		t.Fatal("地上的竿没派到自己的鱼，本用例前提失效")
	}
	before, hooked, castActive := rodHookState(ground)
	fishGUID := before.FishGUID
	rodHookedVia14_11(t, s, sess, groundRod)
	escapesBefore := readFishingCounter(t, s, sess.Userid, "escapes")

	// 松线/一直拉着这类张力帧（1/2/8）与鱼实体被退回（3/5）都不算跑鱼。
	for _, f0 := range []uint64{1, 2, 8, 3, 5} {
		if _, ok := hFishingLeft(leftFrameCtx(t, s, sess, groundRod, fishGUID, f0)); !ok {
			t.Fatalf("14/12 f0=%d 未受理", f0)
		}
	}
	after, hookedAfter, castActiveAfter := rodHookState(ground)
	if after != before || after.FishGUID != fishGUID {
		t.Fatalf("地上竿的鱼被张力帧顶掉了：want %s got %s", shortGUID(fishGUID), shortGUID(after.FishGUID))
	}
	if !hookedAfter {
		t.Fatal("地上竿的鱼被张力帧判成脱钩，Hooked 被清掉了")
	}
	if !castActive || !castActiveAfter {
		t.Fatal("地上竿的在水中状态被张力帧改掉")
	}
	if got := readFishingCounter(t, s, sess.Userid, "escapes"); got != escapesBefore {
		t.Fatalf("地上竿没脱钩却记了跑鱼：%d -> %d（用例开始前 hooked=%v cast_active=%v）", escapesBefore, got, hooked, castActive)
	}
}

// 同一根竿取到手上之后，张力帧就该照旧判真脱钩，别把保护过头变成鱼永远不掉。
func TestTakenUpRodHookedFishStillEscapesOnTensionFrame(t *testing.T) {
	s, _ := newRigTestServer(t)
	t.Cleanup(func() { stopFishingRecoveryTimers(s) })
	sess := registerBiteTestSession(t, s, 712, nil)
	setWeatherTestTunable(t, s, "bite_delay_fixed", "0.05")
	setWeatherTestTunable(t, s, "bite_inwater_fallback_seconds", "1")

	rodA := baitTestRod(t, s)
	mountHookForBaitTest(t, s, rodA, 0)
	baitTestMount(t, s, rodA, "worm_01", 3, 350)
	stA := castOneRod(t, s, sess, rodA)
	putRodOnGround(t, s, sess, rodA)
	other := baitTestRod(t, s)
	mountHookForBaitTest(t, s, other, 0)
	baitTestMount(t, s, other, "worm_01", 3, 350)
	castOneRod(t, s, sess, other)

	if !rodWaitUntil(func() bool {
		f, _, _ := rodHookState(stA)
		return f != nil && f.GearGUID == rodA
	}, 5*time.Second) {
		t.Fatal("地上的竿没派到自己的鱼，本用例前提失效")
	}
	_, _, _ = rodHookState(stA)
	fishGUID := rodPendingFish(stA).FishGUID
	rodHookedVia14_11(t, s, sess, rodA)
	escapesBefore := readFishingCounter(t, s, sess.Userid, "escapes")

	// 玩家走过去把那根竿取到手上（17/7）：这根竿变成"有人管"。
	take := dynamicTestCtx(s, rigRequestTree(t, proto.Guid(rodA)))
	take.Main, take.Sub, take.Sess = 17, 7, sess
	if body, ok := hRodTake(take); !ok || len(body) == 0 {
		t.Fatalf("取竿 %s 失败", shortGUID(rodA))
	}
	if _, ok := hFishingLeft(leftFrameCtx(t, s, sess, rodA, fishGUID, 2)); !ok {
		t.Fatal("14/12 f0=2 未受理")
	}
	got, hooked, _ := rodHookState(stA)
	if got != nil || hooked {
		t.Fatalf("取到手上的竿仍被当成没人管：pending=%+v hooked=%v", got, hooked)
	}
	if after := readFishingCounter(t, s, sess.Userid, "escapes"); after != escapesBefore+1 {
		t.Fatalf("手上的竿真脱钩应记一次 escapes：%d -> %d", escapesBefore, after)
	}
}
