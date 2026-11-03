package gamesess

// 技能写库这一层单独放一个文件，是因为「判定规则」在 skilltree 包里（不连库、能单测），
// 「数据落地」只能在这儿做：MySQL skill_node / skill 表是少爷要直接看直接改的那一份，
// 游戏里点一下技能，库里就得真的多一行 —— 光改内存缓存就是之前那种「退出重进又变回去」。

import (
	"context"
	"sort"
	"strconv"
	"time"

	"rf4server/internal/skilltree"
)

// freshSkillState 现查一份本号技能状态（少爷直接在库里改过也要认），顺手把会话缓存刷成这份。
// 查不动就退回会话里那份，并在日志里说清 —— 宁可旧一点，也不要因为一次读失败就不回包。
func (s *Session) freshSkillState() (skilltree.State, string) {
	st, src, err := s.pullSkillState(context.Background())
	if err != nil {
		s.log(">> 技能：现查没成，先用会话里缓存那份：" + err.Error())
		s.skillMu.Lock()
		cached, cachedSrc := s.skills, s.skillSrc
		s.skillMu.Unlock()
		cached.Level = s.level()
		return cached, cachedSrc
	}
	st.Level = s.level()
	s.storeSkills(st, src)
	return st, src
}

// skillSource 会话缓存那份的来源（日志用）
func (s *Session) skillSource() string {
	s.skillMu.Lock()
	defer s.skillMu.Unlock()
	return s.skillSrc
}

// setSkillNodeInDB 把一个节点学到几级写进 MySQL（level=0 就是删行）。
// 返回空串代表写成了；否则是一句「为什么没写成」。没接上库也照实说，别假装写好了。
func (s *Session) setSkillNodeInDB(node, level int) string {
	if s.srv.Deps.Run == nil {
		return "没接上 MySQL，写不了 skill_node 表"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.srv.Deps.Run.SetSkillNode(ctx, s.user, node, level); err != nil {
		return "写 MySQL skill_node 失败: " + err.Error()
	}
	return ""
}

// setSkillNodesInDB 批量写（5/4 全部重置要用）。按节点号排序写，日志里的顺序就稳定了。
// 中途失败返回「已写到哪个节点为止」，调用方把后面的改动也回滚掉，界面上就不会出现
// 「日志说清了、库里还剩一半」这种最难查的半截状态。
func (s *Session) setSkillNodesInDB(levels map[int]int) (done []int, why string) {
	if s.srv.Deps.Run == nil {
		return nil, "没接上 MySQL，写不了 skill_node 表"
	}
	nodes := make([]int, 0, len(levels))
	for n := range levels {
		nodes = append(nodes, n)
	}
	sort.Ints(nodes)
	for _, n := range nodes {
		if why = s.setSkillNodeInDB(n, levels[n]); why != "" {
			return done, "写到节点 " + strconv.Itoa(n) + " 时断了: " + why
		}
		done = append(done, n)
	}
	return done, ""
}

// revertSkillNodes 把没写进库的改动退回原样（level 传原来的值，一般是 0）
func (s *Session) revertSkillNodes(st skilltree.State, levels map[int]int) {
	for node, lv := range levels {
		st.Nodes[node] = lv
	}
}

// pushSkillTree 把本号最新技能树按真服 2/4 推送出去。
// 挖饵改的是 skill.points，不回这帧时客户端技能面板会一直拿旧缓存。
func (s *Session) pushSkillTree(why string) bool {
	if s.srv.Deps.Table == nil || s.srv.Deps.Defs == nil {
		s.log(">> 技能树(2/4) 没发（" + why + "）：字段表或技能定义没载入")
		return false
	}
	st := s.skillState()
	frame, err := skilltree.PushTree(s.srv.Deps.Table, s.srv.Deps.Defs, st)
	if err != nil {
		s.log(">> 技能树(2/4) 拼帧失败（" + why + "）：" + err.Error())
		return false
	}
	if err := s.sendPush(frame); err != nil {
		s.log(">> 技能树(2/4) 推送失败（" + why + "）：" + friendlyErr(err))
		return false
	}
	s.log(">> 技能树(2/4) 推送 " + strconv.Itoa(len(frame)) + "B（" + why +
		"）捕获鱼饵=" + strconv.FormatFloat(st.Cats[baitSkillCat], 'f', 1, 64) + "%")
	return true
}

const baitSkillCat = 4
