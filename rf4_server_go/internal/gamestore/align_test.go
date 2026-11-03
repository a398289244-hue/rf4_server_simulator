package gamestore

// 这里是「两边对齐守卫」。少爷定的规矩：MySQL 是唯一真值，
// Node 版那边还在写 db.json，两边一旦各写各的，就会出现
// 「背包里有东西游戏看不见」「银币对不上」「删了又回来的影子物品」这类怪事。
// 这个测试默认跳过；设置 RF4_LIVE_SNAPSHOT_AUDIT=1 后执行历史迁移对账。
//
// 判红范围（只盯真正会被游戏看到的活数据）：
//
//	活号 = 任一边背包里有货的账号。活号必须同时满足
//	  1) 钱包（银币/金币/星级/等级）两边一模一样；
//	  2) 背包 GUID 集合两边一模一样（多一件、少一件都算红）。
//
// 只在历史流水里出现、两边都没背包的账号（return / return1）只播报不判红：
// 它们是 2026-11-02 迁移之前的旧账，迁移之后当存档看，不再回头补。

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"rf4server/internal/assetpaths"
	"rf4server/internal/store"
)

// TestLiveAccountsStayAligned 是本次对齐的封口测试。
// 2026-11-02 22:10 已把两边抹平：db.json 的 7 件物品搬进 MySQL objects，
// MySQL 的 9 件补进 db.json，钱包统一成 MySQL 的 9,533,880 minor（95,338.80 银币）。
// 从那以后两边必须一直保持一样，这个测试就是守门人。
func TestLiveAccountsStayAligned(t *testing.T) {
	requireLiveSnapshotAudit(t)
	m := openLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	path, err := assetpaths.File("db.json")
	if err != nil {
		t.Fatalf("找不到凭据目录: %v", err)
	}
	db, err := store.Load(path)
	if err != nil {
		t.Fatalf("读 db.json 失败: %v", err)
	}

	// ---- 划「活号」范围：任一边背包有货的账号 ----
	live := map[string]bool{}
	for name, bag := range db.Objects {
		if len(bag.List) > 0 {
			live[name] = true
		}
	}
	myNames, err := m.UsernamesWithObjects(ctx)
	if err != nil {
		t.Fatalf("查 MySQL 背包账号失败: %v", err)
	}
	for _, name := range myNames {
		live[name] = true
	}
	if len(live) == 0 {
		t.Fatalf("两边都没有背包，活号范围为空——这不正常，先确认数据没被误删")
	}

	// ---- 逐个活号对账 ----
	for _, name := range sortedKeys(live) {
		acct, err := m.Account(ctx, name)
		if err != nil && !errors.Is(err, ErrNotFound) {
			t.Fatalf("账号 %s 读 MySQL 失败: %v", name, err)
		}
		if errors.Is(err, ErrNotFound) {
			t.Errorf("账号 %s 在 db.json 有背包，MySQL 却没这号人：登录都会失败", name)
			continue
		}

		var rec *store.User
		for i := range db.Users {
			if db.Users[i].Username == name {
				rec = &db.Users[i]
			}
		}
		if rec == nil {
			t.Errorf("账号 %s 在 MySQL 有背包，db.json 却没这号人", name)
			continue
		}

		// 1) 钱包必须一分不差
		if acct.SilverMinor != rec.SilverMinor {
			t.Errorf("%s 银币对不上：MySQL %.2f（%d minor）vs db.json %.2f（%d minor），差 %.2f",
				name, float64(acct.SilverMinor)/100, acct.SilverMinor,
				float64(rec.SilverMinor)/100, rec.SilverMinor,
				float64(acct.SilverMinor-rec.SilverMinor)/100)
		}
		if acct.GoldMinor != rec.GoldMinor {
			t.Errorf("%s 金币对不上：MySQL %d vs db.json %d", name, acct.GoldMinor, rec.GoldMinor)
		}
		if acct.Level != rec.Level {
			t.Errorf("%s 等级对不上：MySQL %d vs db.json %d", name, acct.Level, rec.Level)
		}
		if acct.Stars != rec.Stars {
			t.Errorf("%s 星级对不上：MySQL %d vs db.json %d", name, acct.Stars, rec.Stars)
		}

		// 2) 背包 GUID 集合必须一件不差
		objs, err := m.Objects(ctx, name)
		if err != nil {
			t.Fatalf("%s 读 MySQL 背包失败: %v", name, err)
		}
		mine := map[string]string{} // GUID -> 物品编号（MySQL 侧）
		for _, o := range objs {
			mine[o.GUID] = o.ItemKey
		}
		theirs := map[string]string{} // GUID -> 物品编号（db.json 侧）
		for _, o := range db.Objects[name].List {
			theirs[o.GUID] = o.Key
		}
		onlyMySQL, onlyDB := diffKeys(mine, theirs)
		if len(onlyMySQL) > 0 {
			t.Errorf("%s 只有 MySQL 有的物品（游戏里看不见）：%v", name, onlyMySQL)
		}
		if len(onlyDB) > 0 {
			t.Errorf("%s 只有 db.json 有的物品（影子物品来源）：%v", name, onlyDB)
		}
		// 同一个 GUID 两边编号不同，说明有一边改过物品没同步
		for guid, key := range mine {
			if k2, ok := theirs[guid]; ok && k2 != key {
				t.Errorf("%s 同一个 GUID %s 两边编号不同：MySQL %s vs db.json %s", name, guid, key, k2)
			}
		}
		t.Logf("活号 %s 两边一致：等级 %d 银币 %.2f 背包 %d 件（MySQL）/ %d 件（db.json）",
			name, acct.Level, float64(acct.SilverMinor)/100, len(mine), len(theirs))
	}

	// ---- 非活号只播报，不判红 ----
	for _, u := range db.Users {
		if live[u.Username] {
			continue
		}
		acct, err := m.Account(ctx, u.Username)
		if errors.Is(err, ErrNotFound) {
			t.Logf("账号 %s 只在 db.json 里（MySQL 没这号），迁移前的旧账，不判红", u.Username)
			continue
		}
		if err != nil {
			t.Fatalf("账号 %s 读 MySQL 失败: %v", u.Username, err)
		}
		if acct.SilverMinor != u.SilverMinor {
			t.Logf("历史账号 %s：MySQL 银币 %.2f / db.json %.2f 不一致——没背包、游戏看不见，只播报",
				u.Username, float64(acct.SilverMinor)/100, float64(u.SilverMinor)/100)
		} else {
			t.Logf("历史账号 %s：两边银币 %.2f，背包都为空", u.Username, float64(acct.SilverMinor)/100)
		}
	}
}

// sortedKeys 把 map 的键排个序，测试输出才稳定、好比对
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// diffKeys 返回「只有 a 有」和「只有 b 有」的两组键，都排好序
func diffKeys(a, b map[string]string) ([]string, []string) {
	var onlyA, onlyB []string
	for k := range a {
		if _, ok := b[k]; !ok {
			onlyA = append(onlyA, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			onlyB = append(onlyB, k)
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	return onlyA, onlyB
}
