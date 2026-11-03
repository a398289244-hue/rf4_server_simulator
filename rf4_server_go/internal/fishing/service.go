package fishing

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"rf4server/internal/gamestore"
	"rf4server/internal/profile"
	"rf4server/internal/wire"
)

// Logger 日志出口。写成接口是为了单测不用真开日志。
type Logger interface {
	Line(topic, msg string)
}

// Service 钓鱼域的门面：出鱼、拼帧、把渔获写进库。
// 它不认识 socket，也不认识「会话」这个概念 —— 那些是 internal/gamesess 的事，
// 这样以后要加「离线钓鱼」「比赛钓鱼」，这一层可以整块复用。
type Service struct {
	tbl *wire.Table // 协议字段表：物件怎么拼全从它来
	cat *Catalog    // 鱼种 + 水域名册
	st  Store       // 数据入口（读定义、写渔获）
	log Logger      // 日志出口，可为空
}

// New 造服务。依赖全从外面注入，包里自己不 open 文件、不连库。
func New(t *wire.Table, cat *Catalog, st Store) *Service {
	return &Service{tbl: t, cat: cat, st: st}
}

// WithLogger 挂个日志器（装配类操作出岔子要说一声，但不拦流程）
func (s *Service) WithLogger(l Logger) *Service {
	if s != nil {
		s.log = l
	}
	return s
}

// Table 协议字段表（gamesess 那边偶尔要借它拼个别的帧）
func (s *Service) Table() *wire.Table { return s.tbl }

// Catalog 鱼种表（开机日志要知道装了几个鱼种、几个水域）
func (s *Service) Catalog() *Catalog { return s.cat }

// NewPicker 给一个会话造出鱼器。loc 是这片钓鱼场对应的水域 id（--fish-loc），
// forced 是「就必须出这一种」（--fish-key，测某条鱼的时候用）。
func (s *Service) NewPicker(loc, forced string) *Picker {
	return NewPicker(s.cat, loc, forced, nil)
}

// CastPrepareFrame 回 14/1（抛竿准备）。
func (s *Service) CastPrepareFrame(callID uint32, gearGUID string) ([]byte, error) {
	return CastReply(s.tbl, callID, gearGUID)
}

// BiteFrame 拼 14/14「有鱼咬钩」推送。gear 是这次抛竿那套钓具的 GUID。
func (s *Service) BiteFrame(gear string, f Fish) ([]byte, error) {
	return SetupPush(s.tbl, gear, f)
}

// EndFrame 回 14/4（起鱼结算）。档案是现取的，所以银币/技能点跟着最新状态走。
func (s *Service) EndFrame(callID uint32, prof profile.Data, gearRecords [][]byte, fishGUIDs []string) ([]byte, error) {
	return EndReply(s.tbl, callID, EndInput{Profile: prof, GearRecords: gearRecords, FishGUIDs: fishGUIDs})
}

// KeepInput 留鱼（14/5）要的全部输入。
type KeepInput struct {
	CallID   uint32 // 要应答的那条请求的 call_id（应答头里得填对，客户端才认）
	Username string // 谁钓的
	GearGUID string // 哪根竿（4/22 推送要按它找装备）
	FishGUID string // 客户端 keep 回来的那条鱼（= 我们 14/14 里发的 #00）
	Fish     Fish   // 这条鱼的全部事实（种名/体重/体长/档位）
	Place    string // 钓点（鱼卡片 #10）
	Bait     string // 用的饵（鱼卡片 #08）
}

// KeepResult 留鱼这一单的结果。字段全是给日志用的。
type KeepResult struct {
	Reply   []byte // 14/5 的应答帧（含 9 字节头）
	Push422 []byte // 「装备更新了」那条推送；没有可推的装备就是 nil
	CardHex string // 发出去那条鱼记录的字节（hex），也是该写进流水的东西
	Cm      float64
	Saved   bool   // 库里到底写进去没有
	SaveErr string // 没写进去的话为什么（不拦游戏，只说一声）
}

// Keep 把一次留鱼办完：拼鱼卡片 -> 拼应答 -> 写鱼护和流水 -> 顺手把该推的装备推上。
//
// 注意「写库失败不拦游戏」这条：应答都发出去了，库没写进去顶多大厅里那条统计少一行，
// 不该为此把玩家踢下线；所以只把原因带出去，让上层写进日志。
func (s *Service) Keep(ctx context.Context, in KeepInput) (KeepResult, error) {
	card, err := CardRecord(s.tbl, CardInput{
		Key:     in.Fish.Key,
		Grams:   in.Fish.Grams,
		LengthM: in.Fish.LengthM,
		Class:   in.Fish.Class,
		BaitKey: in.Bait,
		Place:   in.Place,
		// 客户端 keep 回来的 GUID 必须原样回填，不然它认不出这是同一条鱼
		SetupGUID: firstNonEmpty(in.FishGUID, in.Fish.SetupGUID),
	})
	if err != nil {
		return KeepResult{}, err
	}
	reply, err := KeepReply(s.tbl, in.CallID, card)
	if err != nil {
		return KeepResult{}, err
	}
	res := KeepResult{
		Reply:   reply,
		CardHex: hex.EncodeToString(card),
		Cm:      in.Fish.LengthM * 100,
	}

	// 写库：鱼护 + 流水。任一失败都不回滚游戏状态，只是记日志。
	guid := dashGUID(firstNonEmpty(in.FishGUID, in.Fish.SetupGUID))
	at := time.Now().UnixMilli()
	if s.st != nil {
		if err := s.st.SaveKeepnet(ctx, in.Username, gamestore.KeepnetRow{
			Key: in.Fish.Key, Grams: in.Fish.Grams, Cm: res.Cm, GUID: guid, At: at,
			Class: in.Fish.Class, Place: in.Place, Bait: in.Bait, CardHex: res.CardHex,
		}); err != nil {
			res.SaveErr = "鱼护没写进去: " + err.Error()
		} else if uid, uidErr := s.userID(ctx, in.Username); uidErr != nil {
			res.SaveErr = "渔获流水没写进去（取不到账号 id）: " + uidErr.Error()
		} else if err := s.st.InsertFishRecord(ctx, gamestore.FishRecord{
			UserID: uid, Key: in.Fish.Key, Grams: in.Fish.Grams, Cm: res.Cm, At: at,
			GUID: guid, Place: in.Place, Bait: in.Bait, CardHex: res.CardHex,
		}); err != nil {
			res.SaveErr = "渔获流水没写进去: " + err.Error()
		} else {
			res.Saved = true
		}
	} else {
		res.SaveErr = "没接上数据库，这条鱼只发了游戏没入库"
	}

	// 4/22：真服留完鱼会跟着推一帧装备更新。我们没有耐久损耗模型，
	// 就把「这根竿和挂在它上面的零件」现在这份记录原样推回去，语义是对的。
	if recs, err := s.GearRecords(ctx, in.Username, in.GearGUID); err == nil {
		p, perr := KeepPush422(s.tbl, recs)
		if perr == nil {
			res.Push422 = p
		}
	}
	return res, nil
}

// KeepnetGUIDs 返回 4/4 要公布的鱼实例编号。鱼护和装备共用同一轮 4/4/4/5 同步，
// 少任何一边客户端都会一直停在“正在下载”。
func (s *Service) KeepnetGUIDs(ctx context.Context, username string) ([][]byte, error) {
	rows, err := s.keepnet(ctx, username)
	if err != nil {
		return nil, err
	}
	out := make([][]byte, 0, len(rows))
	for _, row := range rows {
		raw, err := hex.DecodeString(normGUID(row.GUID))
		if err != nil || len(raw) != 16 {
			return nil, fmt.Errorf("鱼 %s 的 GUID %q 不合法", row.Key, row.GUID)
		}
		out = append(out, raw)
	}
	return out, nil
}

// KeepnetRecords 返回 4/5 要下发的鱼卡片。请求里若点名了鱼 GUID 就只回点名项；
// 没命中任何鱼 GUID 时回全量，兼容空参数的首次同步。
func (s *Service) KeepnetRecords(ctx context.Context, username string, requestPlain []byte) (records [][]byte, requested []string, missing []string, err error) {
	rows, err := s.keepnet(ctx, username)
	if err != nil {
		return nil, nil, nil, err
	}
	wanted := map[string]bool{}
	for _, row := range rows {
		h := normGUID(row.GUID)
		raw, decErr := hex.DecodeString(h)
		if decErr == nil && len(raw) == 16 && bytes.Contains(requestPlain, raw) {
			wanted[h] = true
			requested = append(requested, row.Key)
		}
	}
	filter := len(wanted) > 0
	for _, row := range rows {
		h := normGUID(row.GUID)
		if filter && !wanted[h] {
			continue
		}
		if h == "" || len(h) != 32 {
			missing = append(missing, row.Key+"(GUID不合法)")
			continue
		}
		if row.CardHex != "" {
			if raw, decErr := hex.DecodeString(row.CardHex); decErr == nil && len(raw) > 0 {
				records = append(records, raw)
				continue
			}
		}
		class := row.Class
		if class == 0 && s.cat != nil {
			if sp, ok := s.cat.GetSpecies(row.Key); ok {
				class = SizeClass(sp, row.Grams)
			}
		}
		stamp := uint64(0)
		if row.At > 0 {
			stamp = wire.Filetime(time.UnixMilli(row.At).UTC())
		}
		card, cardErr := CardRecord(s.tbl, CardInput{
			Key: row.Key, Grams: row.Grams, LengthM: row.Cm / 100, Class: class,
			HookedFiletime: stamp, BaitKey: firstNonEmpty(row.Bait, "worm_01"),
			Place: row.Place, SetupGUID: h,
		})
		if cardErr != nil {
			missing = append(missing, row.Key+"("+cardErr.Error()+")")
			continue
		}
		records = append(records, card)
	}
	return records, requested, missing, nil
}

func (s *Service) keepnet(ctx context.Context, username string) ([]gamestore.KeepnetRow, error) {
	if s == nil || s.st == nil {
		return nil, errors.New("钓鱼服务没接上数据库")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return s.st.Keepnet(ctx, username)
}

// userID 取账号数字 id（fish_records.user_id 那一格要的是它，不是登录名）
func (s *Service) userID(ctx context.Context, username string) (int64, error) {
	a, err := s.st.Account(ctx, username)
	if err != nil {
		return 0, err
	}
	return a.UserID, nil
}

// GearRecords 取「这根竿 + 挂在它上面的零件」现在的物件记录字节，给 4/22 推送用。
// 权威数据就是 objects.elem（客户端读的就是它），所以不用重拼一遍。
func (s *Service) GearRecords(ctx context.Context, username, gearGUID string) ([][]byte, error) {
	if s.st == nil || gearGUID == "" {
		return nil, nil
	}
	objs, err := s.st.Objects(ctx, username)
	if err != nil {
		return nil, err
	}
	want := normGUID(gearGUID)
	var out [][]byte
	for _, o := range objs {
		if normGUID(o.GUID) != want && normGUID(o.ParentGUID) != want {
			continue // 跟这根竿无关的货不推
		}
		raw, err := hex.DecodeString(o.Elem)
		if err != nil || len(raw) == 0 {
			continue // 库里没记录就没这条可推（缺记录会在日志里说出来）
		}
		out = append(out, raw)
	}
	return out, nil
}

// firstNonEmpty 一串里挑第一个不是空的
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// normGUID 去掉横线统一小写，比较 GUID 只看内容长什么样（库里有横没横线两种写法）
func normGUID(s string) string {
	out := make([]byte, 0, 32)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '-' {
			continue
		}
		if c >= 'A' && c <= 'F' {
			c += 32
		}
		out = append(out, c)
	}
	return string(out)
}

// dashGUID 换成 8-4-4-4-12 那种带横线的写法。鱼护/流水是给人看的，
// 带横线跟 Node 版 db.json 里那份一致，两边对账不用换算。
func dashGUID(s string) string {
	h := normGUID(s)
	if len(h) != 32 {
		return s
	}
	b := []byte(h)
	out := make([]byte, 0, 36)
	out = append(out, b[0:8]...)
	out = append(out, '-')
	out = append(out, b[8:12]...)
	out = append(out, '-')
	out = append(out, b[12:16]...)
	out = append(out, '-')
	out = append(out, b[16:20]...)
	out = append(out, '-')
	out = append(out, b[20:32]...)
	return string(out)
}
