package gameserver

import (
	"math"
	"testing"
)

func TestReelOverallDurabilityDragHasLargestInfluence(t *testing.T) {
	base := reelOverallDurability(1, 1, 1, 1, 1)
	dragLoss := base - reelOverallDurability(1, .9, 1, 1, 1)
	gearLoss := base - reelOverallDurability(1, 1, 1, 1, .9)
	spoolLoss := base - reelOverallDurability(.9, 1, 1, 1, 1)
	if math.Abs(base-1) > 1e-9 || dragLoss <= gearLoss || dragLoss <= spoolLoss {
		t.Fatalf("总耐久权重错误 base=%f drag=%f gear=%f spool=%f", base, dragLoss, gearLoss, spoolLoss)
	}
}

func TestFightReelWearSeparatesLineOutAndRetrieve(t *testing.T) {
	lineOut := fightReelWear(10, 5, 5)
	retrieve := fightReelWear(-10, 5, 5)
	if lineOut.LineOutM != 10 || lineOut.RetrieveM != 0 || lineOut.Drag <= lineOut.Gear {
		t.Fatalf("泄线应主要磨摩擦片: %+v", lineOut)
	}
	if retrieve.RetrieveM != 10 || retrieve.LineOutM != 0 || retrieve.Gear <= retrieve.Drag {
		t.Fatalf("收线应主要磨主动齿轮: %+v", retrieve)
	}
	if lineOut.Drag >= .001 || retrieve.Gear >= .001 {
		t.Fatalf("一次正常十米工作量不应造成突发爆轮: lineOut=%+v retrieve=%+v", lineOut, retrieve)
	}
}

func TestReelOverloadWearIsProgressive(t *testing.T) {
	mild := overloadReelWear(.15)
	heavy := overloadReelWear(1.0)
	if mild.Drag <= 0 || mild.Drag >= .005 || heavy.Drag <= mild.Drag {
		t.Fatalf("超载磨损应小幅且随严重度递增 mild=%+v heavy=%+v", mild, heavy)
	}
	if mild.Drag <= mild.Spool || mild.Drag <= mild.Bearing || mild.Drag <= mild.Gear {
		t.Fatalf("超载时摩擦片应是主要损耗件: %+v", mild)
	}
}
