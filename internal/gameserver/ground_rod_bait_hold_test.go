package gameserver

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"rf4server/internal/proto"
)

// 少爷要求：水底那根竿脱钩不该白吃一份饵，只有真上鱼（14/4 结算）或者线断了，
// 才把当前占用的饵删掉。实测那局就是咬口->秒脱钩->饵没了->整根竿彻底停口。

func mountedBaitQty(t *testing.T, s *Server, guid string) int64 {
	t.Helper()
	row, err := s.DB.GetItemByGUID(context.Background(), guid)
	if err == sql.ErrNoRows {
		return 0
	}
	if err != nil {
		t.Fatalf("读挂载饵 %s 失败: %v", shortGUID(guid), err)
	}
	if row == nil {
		return 0
	}
	return row.Qty
}

// 地上的竿：咬钩到脱钩整条链路走完，钩上那份饵还是一份不少。
func TestGroundRodBiteAndEscapeDoNotConsumeBait(t *testing.T) {
	s, _ := newRigTestServer(t)
	t.Cleanup(func() { stopFishingRecoveryTimers(s) })
	sess := registerBiteTestSession(t, s, 713, nil)
	setWeatherTestTunable(t, s, "bite_delay_fixed", "0.05")
	setWeatherTestTunable(t, s, "bite_inwater_fallback_seconds", "1")

	groundRod := baitTestRod(t, s)
	mountHookForBaitTest(t, s, groundRod, 0)
	_, bait := baitTestMount(t, s, groundRod, "worm_01", 3, 350)
	ground := castOneRod(t, s, sess, groundRod)
	putRodOnGround(t, s, sess, groundRod)
	heldRod := baitTestRod(t, s)
	mountHookForBaitTest(t, s, heldRod, 0)
	baitTestMount(t, s, heldRod, "worm_01", 3, 350)
	castOneRod(t, s, sess, heldRod)

	before := mountedBaitQty(t, s, bait)
	if before <= 0 {
		t.Fatal("挂载饵数量为 0，本用例前提失效")
	}
	if !rodWaitUntil(func() bool {
		f, _, _ := rodHookState(ground)
		return f != nil && f.GearGUID == groundRod
	}, 5*time.Second) {
		t.Fatal("地上的竿没派到自己的鱼，本用例前提失效")
	}
	fish := rodPendingFish(ground)
	rodHookedVia14_11(t, s, sess, groundRod)
	if got := mountedBaitQty(t, s, bait); got != before {
		t.Fatalf("地上竿中钩就吃掉一份饵：%d -> %d", before, got)
	}
	// 玩家赶过来之前鱼脱钩了：这颗饵必须还在钩上。
	if _, ok := hFishingLeft(leftFrameCtx(t, s, sess, groundRod, fish.FishGUID, 2)); !ok {
		t.Fatal("14/12 f0=2 未受理")
	}
	if got := mountedBaitQty(t, s, bait); got != before {
		t.Fatalf("地上竿脱钩把饵吃掉了：%d -> %d", before, got)
	}
	if !s.canFishWithBait(s.dbCtx(), sess.Userid, groundRod) {
		t.Fatal("脱钩之后这根竿已经没有饵可用了，正是要修的毛病")
	}
}

// 同一根竿真把鱼钓上来了（14/4 结算有鱼），这份饵才算消耗掉。
func TestGroundRodLandedFishConsumesBait(t *testing.T) {
	s, _ := newRigTestServer(t)
	t.Cleanup(func() { stopFishingRecoveryTimers(s) })
	sess := registerBiteTestSession(t, s, 714, nil)
	setWeatherTestTunable(t, s, "bite_delay_fixed", "0.05")
	setWeatherTestTunable(t, s, "bite_inwater_fallback_seconds", "1")

	groundRod := baitTestRod(t, s)
	mountHookForBaitTest(t, s, groundRod, 0)
	_, bait := baitTestMount(t, s, groundRod, "worm_01", 3, 350)
	ground := castOneRod(t, s, sess, groundRod)
	putRodOnGround(t, s, sess, groundRod)
	heldRod := baitTestRod(t, s)
	mountHookForBaitTest(t, s, heldRod, 0)
	baitTestMount(t, s, heldRod, "worm_01", 3, 350)
	castOneRod(t, s, sess, heldRod)

	before := mountedBaitQty(t, s, bait)
	if !rodWaitUntil(func() bool {
		f, _, _ := rodHookState(ground)
		return f != nil && f.GearGUID == groundRod
	}, 5*time.Second) {
		t.Fatal("地上的竿没派到自己的鱼，本用例前提失效")
	}
	rodHookedVia14_11(t, s, sess, groundRod)
	if got := mountedBaitQty(t, s, bait); got != before {
		t.Fatalf("中钩确认就把饵吃掉了，本该等上鱼结算：%d -> %d", before, got)
	}
	endCtx := dynamicTestCtx(s, rigRequestTree(t, proto.Guid(groundRod)))
	endCtx.Main, endCtx.Sub, endCtx.Sess = 14, 4, sess
	if body, ok := hFishingEnd(endCtx); !ok || len(body) == 0 {
		t.Fatalf("14/4 收竿结算未受理：%s", shortGUID(groundRod))
	}
	for _, cb := range endCtx.afterResponse {
		cb()
	}
	if got := mountedBaitQty(t, s, bait); got != before-1 {
		t.Fatalf("上鱼结算没消耗这份饵：%d -> %d", before, got)
	}
}
