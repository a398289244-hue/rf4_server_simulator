package gamestore

// 钓鱼这一层的读写：鱼种/水域那两张静态定义表，加上「钓上来一条鱼」要写的两张流水表。
// 和别的域一样，这里只有 SQL，没有业务判断（该出什么鱼是 internal/fishing 的事）。
//
// 少爷要直接看/直接改的话：
//
//	static_species_weight    每个鱼种多重、多长、几条奖杯线（改一行就能调鱼）
//	static_map_fish_roster   哪个水域有哪几种鱼（往里 INSERT 一行，这水域就能钓到）
//	keepnets                 这个号鱼护里的货（payload 是个 JSON 数组，一行一个号）
//	fish_records             每一次留鱼的流水（一条鱼一行，按 user_id + at 有索引）

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// SpeciesWeight static_species_weight 的一行（只取钓鱼用得上的那几列）。
// 库里 min_g/max_g/length_k/length_p 都有 NULL 的（例如 ruffe_n），一律 COALESCE 成 0 再扫，
// HasRange 告诉上层「这行到底能不能拿来随机」。
type SpeciesWeight struct {
	FishKey          string  // fish_key，鱼种编号
	MinG             int64   // min_g 常见体重下限（克）
	MaxG             int64   // max_g 常见体重上限（克）
	TrophyLineG      int64   // trophy_line_g 大档门槛（克）
	SuperTrophyLineG int64   // super_trophy_line_g 特大档门槛（克）
	LengthK          float64 // length_k 体长换算系数：米 = K * 克^P
	LengthP          float64 // length_p 体长换算指数
	HasRange         bool    // min_g/max_g 都不是 NULL 才为 true
}

// SpeciesWeights 读全部鱼种的体重/体长定义，键是 fish_key。
func (m *MySQL) SpeciesWeights(ctx context.Context) (map[string]SpeciesWeight, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("gamestore: 没接上 MySQL")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := m.db.QueryContext(ctx,
		`SELECT COALESCE(fish_key, ''), COALESCE(min_g, 0), COALESCE(max_g, 0),
		        COALESCE(trophy_line_g, 0), COALESCE(super_trophy_line_g, 0),
		        COALESCE(length_k, 0), COALESCE(length_p, 0),
		        CASE WHEN min_g IS NULL OR max_g IS NULL THEN 0 ELSE 1 END
		   FROM static_species_weight WHERE COALESCE(fish_key, '') <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SpeciesWeight{}
	for rows.Next() {
		var w SpeciesWeight
		var has int
		if err := rows.Scan(&w.FishKey, &w.MinG, &w.MaxG, &w.TrophyLineG, &w.SuperTrophyLineG,
			&w.LengthK, &w.LengthP, &has); err != nil {
			return nil, err
		}
		w.HasRange = has == 1
		out[w.FishKey] = w
	}
	return out, rows.Err()
}

// Roster 水域名册：水域 id（level_001_lake 这种）-> 这个水域里的鱼种列表。
func (m *MySQL) Roster(ctx context.Context) (map[string][]string, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("gamestore: 没接上 MySQL")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := m.db.QueryContext(ctx,
		`SELECT COALESCE(loc, ''), COALESCE(fish_key, '')
		   FROM static_map_fish_roster
		  WHERE COALESCE(loc, '') <> '' AND COALESCE(fish_key, '') <> ''
		  ORDER BY loc, fish_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	seen := map[string]map[string]bool{}
	for rows.Next() {
		var loc, key string
		if err := rows.Scan(&loc, &key); err != nil {
			return nil, err
		}
		if seen[loc] == nil {
			seen[loc] = map[string]bool{}
		}
		if seen[loc][key] {
			continue // 名册里有重复行，去个重，别让同一种鱼占两次抽签机会
		}
		seen[loc][key] = true
		out[loc] = append(out[loc], key)
	}
	return out, rows.Err()
}

// KeepnetRow 鱼护里的一条（keepnets.payload 那个 JSON 数组里的一个元素）。
// 字段名故意和 Node 版 db.json 里那份一致，两边对照着看不费劲。
type KeepnetRow struct {
	Key     string  `json:"key"`             // 鱼种编号
	Grams   int64   `json:"grams"`           // 体重（克）
	Cm      float64 `json:"cm"`              // 体长（厘米）
	GUID    string  `json:"guid"`            // 这条鱼的实例 GUID（带横线那种写法，和 Node 版一样）
	At      int64   `json:"at"`              // 时间（毫秒）
	Class   byte    `json:"class,omitempty"` // 大小档；旧数据没有时由鱼种表重算
	Place   string  `json:"place,omitempty"` // 钓点
	Bait    string  `json:"bait,omitempty"`  // 饵料
	CardHex string  `json:"card,omitempty"`  // 当时发给客户端的 obj25；重登时优先原样复用
}

// Keepnet 读取这个号的鱼护。旧版 payload 没有 Class/Place/Bait/CardHex 也能正常反序列化，
// 上层会用已有的 key/重量/长度/GUID 重建鱼卡片。
func (m *MySQL) Keepnet(ctx context.Context, username string) ([]KeepnetRow, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("gamestore: 没接上 MySQL")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var payload sql.NullString
	err := m.db.QueryRowContext(ctx, `SELECT payload FROM keepnets WHERE username = ?`, username).Scan(&payload)
	if err == sql.ErrNoRows || !payload.Valid || payload.String == "" {
		return []KeepnetRow{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []KeepnetRow
	if err := json.Unmarshal([]byte(payload.String), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SaveKeepnet 往这个号的鱼护里加一条。keepnets 一个号一行，payload 是整个清单的 JSON 数组，
// 所以是「读出来 -> 追加 -> 写回去」。最多留 200 条，超了就丢最老的（和 Node 版一个规矩）。
func (m *MySQL) SaveKeepnet(ctx context.Context, username string, row KeepnetRow) error {
	if m == nil || m.db == nil {
		return errors.New("gamestore: 没接上 MySQL")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var old sql.NullString
	err := m.db.QueryRowContext(ctx, `SELECT payload FROM keepnets WHERE username = ?`, username).Scan(&old)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	list := []KeepnetRow{}
	if err == nil && old.Valid && old.String != "" {
		if jerr := json.Unmarshal([]byte(old.String), &list); jerr != nil {
			list = nil // 库里那坨不是合法 JSON：别把整个域卡住，重新开始记
		}
	}
	list = append(list, row)
	if len(list) > 200 {
		list = list[len(list)-200:]
	}
	blob, err := json.Marshal(list)
	if err != nil {
		return err
	}
	_, err = m.db.ExecContext(ctx,
		`INSERT INTO keepnets (username, rows, payload, updated_at) VALUES (?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE rows = VALUES(rows), payload = VALUES(payload), updated_at = VALUES(updated_at)`,
		username, len(list), string(blob), time.Now().UnixMilli())
	return err
}

// FishRecord 一次留鱼的流水（fish_records 一行）。
type FishRecord struct {
	UserID  int64   // users.user_id
	Key     string  // 鱼种编号
	Grams   int64   // 体重（克）
	Cm      float64 // 体长（厘米）
	At      int64   // 时间（毫秒）
	GUID    string  // 这条鱼的实例 GUID
	Place   string  // 钓点/水域 id
	Bait    string  // 用的饵
	CardHex string  // 发给客户端那条 obj25 的字节（hex），出问题时能直接拆
}

// InsertFishRecord 写一条渔获流水。写不上不影响游戏（调用方只记一行日志）。
func (m *MySQL) InsertFishRecord(ctx context.Context, r FishRecord) error {
	if m == nil || m.db == nil {
		return errors.New("gamestore: 没接上 MySQL")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	blob, err := json.Marshal(map[string]any{
		"guid": r.GUID, "place": r.Place, "bait": r.Bait, "card": r.CardHex,
	})
	if err != nil {
		return err
	}
	_, err = m.db.ExecContext(ctx,
		`INSERT INTO fish_records (user_id, species_key, grams, length_cm, at, payload)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		r.UserID, r.Key, r.Grams, r.Cm, r.At, string(blob))
	return err
}
