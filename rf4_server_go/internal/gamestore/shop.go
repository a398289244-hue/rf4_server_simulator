package gamestore

// 商城和仓库这一层：货架行、购买流水、容器（21/1 那个工坊/仓库）里的货。
// 一律是「读库 + 写库」，不带业务判断（该不该上架、能不能买是 internal/shop、internal/buy 的事）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ShopRow 货架上的一件货（static_shop_item 的一行）。
//
// 两个字段的口径是拿真服货架帧对过答案的（脚本结果见 HANDOFF）：
//
//	silver = 银币价        和真服 22/1 里那格价格 96.5% 一致（其余是官方表更合理的价）
//	gold   = 克重 f2       和真服那条的 f2 98.2% 一致（个别金币商品会显示成克重，纯展示，不影响买）
//
// 少爷要调价就直接 UPDATE 这张表，后端每开一次商店都现读一遍。
type ShopRow struct {
	ItemID string // 物品编号，static_shop_item.item_id
	Price  int64  // 银币价（最小单位），static_shop_item.silver
	Gold   int64  // 金币价（最小单位），static_shop_item.gold，对应 22/1 的 #02
	Stock  int64  // 库存数量，static_shop_item.stock，对应 22/1 的 #03
}

// ShelfRows 一家店的货架行。按 item_id 排序，保证每次开商店的顺序一样
// （客户端按列表判断「有没有货」，顺序乱掉只是界面重排，但日志对照会很难看）。
func (m *MySQL) ShelfRows(ctx context.Context, store string) ([]ShopRow, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("gamestore: 没接上 MySQL")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := m.db.QueryContext(ctx,
		`SELECT COALESCE(item_id, ''), COALESCE(silver, 0), COALESCE(gold, 0), COALESCE(stock, 0)
           FROM static_shop_item WHERE store_id = ? ORDER BY item_id`, store)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShopRow
	for rows.Next() {
		var r ShopRow
		if err := rows.Scan(&r.ItemID, &r.Price, &r.Gold, &r.Stock); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Purchase 一条购买流水（purchases 表）。买成功就写一行，出事时能顺着 GUID 找回是哪一单。
type Purchase struct {
	Username string // 谁买的
	UserID   int64  // 账号数字 id
	Store    string // 在哪家店
	ItemKey  string // 买了哪件货
	ItemID   int    // 服务器数字编号（没编号填 0）
	Price    int64  // 成交价（最小单位）
	Grams    int64  // 货架上那份克重 f2，留着对账
	GUID     string // 发出去的那个物件 GUID
	At       int64  // 时间（毫秒）
	Payload  string // 应答帧 hex，排查时能直接拆一遍看
}

// InsertPurchase 写一条购买流水。写不上不拦购买本身（调用方只记一行日志）。
func (m *MySQL) InsertPurchase(ctx context.Context, p Purchase) error {
	if m == nil || m.db == nil {
		return errors.New("gamestore: 没接上 MySQL")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO purchases(username, user_id, store, item_key, item_id, price, grams, guid, at, payload)
         VALUES(?,?,?,?,?,?,?,?,?,?)`,
		p.Username, p.UserID, p.Store, p.ItemKey, nullableInt(p.ItemID), p.Price, p.Grams, p.GUID, p.At, nullableStr(p.Payload))
	return err
}

// ContainerMeta 一个容器（21/1 问的那个工坊/仓库格子）的外壳参数（containers 表）。
// 三个数其实就是「这坨东西怎么编码」：标记=参数表(3)、参数表名、里面元素的类型号低字节。
type ContainerMeta struct {
	Username  string // 谁的容器
	Container string // 容器名，如 workshop_02
	Tag       int    // 值标记，3=参数表（frames.go 里 Args 的 marker）
	Field     string // 参数表名，如 "144"
	ElemTag   int    // 元素物件类型号低字节（249 -> typeID 505）
	Seeded    string // 这批语法哪来的（人话说明，只为日志和排查）
}

// ContainerRows 一个容器里的货（container_items 表）。
// 每行存的是「元素各段字节」：head(元素头 5 字节) + 成色/计数的原始位模式 + 可选的 u32 数组。
// 这些字节是当年从真服那帧拆出来存进库的，现在就是「这个号自己的数据」，能直接改。
type ContainerRows struct {
	Container string
	Meta      ContainerMeta
	Items     []ContainerItem
}

// ContainerItem 容器里的一件货
type ContainerItem struct {
	Pos      int      // 第几格
	ItemKey  string   // 物品编号（可空，真服那帧里就有空的）
	GUID     string   // 物件 GUID
	Price    int64    // 当时那一格带的价格
	Head     string   // 元素头 5 字节 hex（01 + 类型号 + 010000）
	CondBits string   // 成色的 f32 位模式 hex
	N1Bits   string   // 另一个 u32 的 hex
	Cond     float64  // 成色（人看的）
	N1       int      // 同上
	Tag      string   // u32 数组的参数表名（如 "293"），空 = 这一格没带数组
	Args     []uint32 // 那串 u32（槽位/数量之类）
}

// Container 读一个容器（含外壳参数）。库里没这一格返回 ErrNotFound。
func (m *MySQL) Container(ctx context.Context, username, container string) (ContainerRows, error) {
	if m == nil || m.db == nil {
		return ContainerRows{}, errors.New("gamestore: 没接上 MySQL")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var out ContainerRows
	meta := m.db.QueryRowContext(ctx,
		`SELECT COALESCE(tag, 3), COALESCE(field, ''), COALESCE(elem_tag, 0), COALESCE(seeded_from, '')
           FROM containers WHERE username = ? AND container = ? LIMIT 1`, username, container)
	if err := meta.Scan(&out.Meta.Tag, &out.Meta.Field, &out.Meta.ElemTag, &out.Meta.Seeded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ContainerRows{}, ErrNotFound
		}
		return ContainerRows{}, err
	}
	out.Meta.Container = container
	out.Container = container

	rows, err := m.db.QueryContext(ctx,
		`SELECT pos, item_key, guid, price, head, cond_bits, n1_bits, cond, n1, tag, args
           FROM container_items WHERE username = ? AND container = ? ORDER BY pos`, username, container)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			it       ContainerItem
			guid     sql.NullString
			itemKey  string
			argsJSON sql.NullString
			tag      string
		)
		if err := rows.Scan(&it.Pos, &itemKey, &guid, &it.Price, &it.Head, &it.CondBits,
			&it.N1Bits, &it.Cond, &it.N1, &tag, &argsJSON); err != nil {
			return out, err
		}
		it.ItemKey = itemKey
		it.GUID = guid.String
		it.Tag = tag
		if argsJSON.String != "" {
			it.Args = parseU32Array(argsJSON.String)
		}
		out.Items = append(out.Items, it)
	}
	return out, rows.Err()
}

// nullableInt / nullableStr 把 Go 的零值写成 NULL（purchases 那两列是可空的）
func nullableInt(v int) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

func nullableStr(v string) interface{} {
	if v == "" {
		return nil
	}
	return v
}

// parseU32Array 把库里存的那串 u32（JSON 数组文本，像 "[36,4]"）读回来。
// 读不动就当没有：少一串 u32 只是那一格的附属信息没了，不该把整个容器判失败。
func parseU32Array(text string) []uint32 {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var raw []json.Number
	if err := dec.Decode(&raw); err != nil {
		var plain []uint32
		if err := json.Unmarshal([]byte(text), &plain); err != nil {
			return nil
		}
		return plain
	}
	out := make([]uint32, 0, len(raw))
	for _, n := range raw {
		i, err := n.Int64()
		if err != nil {
			continue
		}
		out = append(out, uint32(i))
	}
	return out
}

// ContainerSyntax 按容器名借一份语法：这个号还没这个容器时，
// 参数表名/元素类型号还是得有个出处，就借同一容器名在别人那一行里存的那份
// （库里 return 那个号有 workshop_02 的行，return2 没有，就借它）。
func (m *MySQL) ContainerSyntax(ctx context.Context, container string) (ContainerMeta, error) {
	if m == nil || m.db == nil {
		return ContainerMeta{}, errors.New("gamestore: 没接上 MySQL")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var meta ContainerMeta
	row := m.db.QueryRowContext(ctx,
		`SELECT username, COALESCE(tag, 3), COALESCE(field, ''), COALESCE(elem_tag, 0), COALESCE(seeded_from, '')
           FROM containers WHERE container = ? ORDER BY username LIMIT 1`, container)
	if err := row.Scan(&meta.Username, &meta.Tag, &meta.Field, &meta.ElemTag, &meta.Seeded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ContainerMeta{}, ErrNotFound
		}
		return ContainerMeta{}, err
	}
	meta.Container = container
	return meta, nil
}
