package gamesess

import (
	"context"
	"errors"
	"fmt"
	"time"

	"rf4server/internal/gamestore"
	"rf4server/internal/profile"
	"rf4server/internal/skilltree"
)

// 档案这一帧的数据来源，日志里必须写清是哪一份 —— 「界面显示的到底是哪份数据」这事儿坑过太多次。
const (
	profileSrcMySQL  = "MySQL"   // 真值：users / skill_points 这些表
	profileSrcDBJSON = "db.json" // 兜底：没接上 MySQL 时才用（只够对着看看，别当数）
)

// profileData 现取一份本号的档案输入，返回「数据 + 这份从哪来的 + 错误」。
//
// 每发一帧就现取一次，不缓存：银币卖条鱼就变，缓存下来界面就会停在登录那一刻的数。
// 先 MySQL（少爷定的唯一真值），读不到才退回 db.json；两边都拿不到就报错，
// 宁可不发这一帧，也不要发一帧「录制那位老号」的数出去。
func (s *Session) profileData() (profile.Data, string, error) {
	if s.user == "" {
		return profile.Data{}, "", errors.New("还没对上账号，不知道该发谁的档案")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if s.srv.Deps.Run != nil {
		acct, err := s.srv.Deps.Run.Account(ctx, s.user)
		if err == nil {
			d := profile.Data{
				UserID:      acct.UserID,
				Nickname:    choose(acct.Nickname != "", acct.Nickname, acct.Username),
				Level:       acct.Level,
				SilverMinor: acct.SilverMinor,
				GoldMinor:   acct.GoldMinor,
			}
			d.SkillPoints = s.availablePointsFor(ctx, acct.Level)
			return d, profileSrcMySQL, nil
		}
		s.log(">> 档案：账号从 MySQL 读失败，退回 db.json：" + err.Error())
	}

	u, ok := s.srv.Deps.DB.FindUser(s.user)
	if !ok {
		return profile.Data{}, "", fmt.Errorf("账号表里没有 %s（db.json 和 MySQL 都没有）", s.user)
	}
	d := profile.Data{
		UserID:      int64(u.UserID),
		Nickname:    choose(u.Nickname != "", u.Nickname, u.Username),
		Level:       u.Level,
		SilverMinor: u.SilverMinor,
		GoldMinor:   u.GoldMinor,
	}
	d.SkillPoints = int(s.cachedSkillPoints())
	return d, profileSrcDBJSON, nil
}

// availablePointsFor 这个号还能投几点技能：优先现查 MySQL，查不到就用登录时缓存那份。
// 技能点是面板上「剩余点数」那个数，之前就是这儿取错了来源，界面才会一会儿 148 一会儿 100。
func (s *Session) availablePointsFor(ctx context.Context, level int) int {
	if s.srv.Deps.Run != nil {
		if st, err := s.srv.Deps.Run.SkillState(ctx, s.user); err == nil {
			return int(skilltree.State{
				Cats: st.Cats, Nodes: st.Nodes, Granted: st.Granted, Level: level,
			}.Available())
		} else {
			s.log(">> 技能点现查 MySQL 没查到，先用登录时缓存那份：" + err.Error())
		}
	}
	return int(s.cachedSkillPoints())
}

// cachedSkillPoints 登录时缓存那份技能状态里「还能投几点」。
// 这份缓存只在登录那一步写（loadSkillsAtLogin），之后 Go 版还没搬「学技能写库」，
// 所以推送 goroutine 读它是安全的；哪天 5/2 开始写库了，记得把这里也一起改成现查。
func (s *Session) cachedSkillPoints() uint32 {
	st := s.skills
	st.Level = s.level()
	return st.Available()
}

// accountOf 只给测试和日志用：拿当前这份档案的账号快照
func (s *Session) accountOf(ctx context.Context) (gamestore.Account, bool) {
	if s.srv.Deps.Run == nil {
		return gamestore.Account{}, false
	}
	acct, err := s.srv.Deps.Run.Account(ctx, s.user)
	return acct, err == nil
}
