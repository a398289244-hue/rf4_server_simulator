package gamestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // 只注册驱动，不在代码里直接用它的方法
)

// Config 连库参数。默认值指向少爷这台机器的实测环境：服务 MySQLRF4，库和人都叫 rf4。
// 写死默认值是为了少配一处，命令行仍然可以覆盖。
type Config struct {
	Host     string // 数据库地址，默认 127.0.0.1
	Port     int    // 端口，默认 3306
	User     string // 登录名，默认 rf4（别用 root，权限太大）
	Password string // 密码，默认 rf4pass
	Database string // 库名，默认 rf4
	MaxOpen  int    // 连接池上限，默认 8：游戏连接本来就少，大了反而和 Node 版抢
}

// DSN 拼驱动要的连接串。charset 必须写 utf8mb4，不然中文说明进去会变问号。
func (c Config) DSN() string {
	if c.Host == "" {
		c.Host = "127.0.0.1"
	}
	if c.Port == 0 {
		c.Port = 3306
	}
	if c.User == "" {
		c.User = "rf4"
	}
	if c.Password == "" {
		c.Password = "rf4pass"
	}
	if c.Database == "" {
		c.Database = "rf4"
	}
	if c.MaxOpen == 0 {
		c.MaxOpen = 8
	}
	return c.User + ":" + c.Password + "@tcp(" + c.Host + ":" + strconv.Itoa(c.Port) + ")/" + c.Database +
		"?charset=utf8mb4&parseTime=true&loc=Local"
}

// MySQL 就是 Runtime 的 MySQL 实现。里面只有一池连接，没有任何业务规则，
// 所以想单测业务域时，写个假的 Runtime 就够了，不用起库。
type MySQL struct {
	db *sql.DB // 连接池
}

// 编译期就把规矩钉死：MySQL 必须把 Runtime 每个方法都实现全，少一个都编译不过
var (
	_ Runtime = (*MySQL)(nil) // 编译期钉死：Runtime 每个方法都得实现
	_ Catalog = (*MySQL)(nil) // 静态定义查询也由同一个连接池顺手实现
)

// Open 建连接池并立刻探一次活。探不上就直接报错，别让后端「假装起来了」——
// 之前吃过快照和端口那种哑巴亏，起不来就要当场说清楚。
func Open(cfg Config) (*MySQL, error) {
	pool, err := sql.Open("mysql", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("打不开数据库连接: %w", err)
	}
	if cfg.MaxOpen > 0 {
		pool.SetMaxOpenConns(cfg.MaxOpen)
	}
	pool.SetMaxIdleConns(2)
	pool.SetConnMaxLifetime(time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("数据库连不上（服务 MySQLRF4 起来了吗）: %w", err)
	}
	return &MySQL{db: pool}, nil
}

// DB 把底层连接池露出去，给以后要写复杂 SQL（比如商城联表查货架）的域用。
// 但业务域优先用这个包里的方法，别一上来就自己拼 SQL。
func (m *MySQL) DB() *sql.DB { return m.db }

// Close 关连接池
func (m *MySQL) Close() error { return m.db.Close() }

// TableCount 数一下当前库里有多少张表，只为开机体检日志用：
// 万一连错库（比如连到一个空库）当场就能看出来，不会后面查什么都「没有这条记录」。
func (m *MySQL) TableCount(ctx context.Context) (int, error) {
	var n int
	if err := m.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()").
		Scan(&n); err != nil {
		return 0, fmt.Errorf("数表失败: %w", err)
	}
	return n, nil
}

// Categories 批量查物品类别（static_item_def.category）。
// 一次 IN 查询拿全，省得一件跑一趟。查不到的键就不出现在结果里，让调用方自己决定怎么处理。
func (m *MySQL) Categories(ctx context.Context, keys []string) (map[string]int, error) {
	out := map[string]int{}
	if len(keys) == 0 {
		return out, nil
	}
	marks := strings.TrimRight(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, 0, len(keys))
	for _, k := range keys {
		args = append(args, k)
	}
	rows, err := m.db.QueryContext(ctx,
		`SELECT item_id, category FROM static_item_def WHERE item_id IN (`+marks+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("读物品类别失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			key string
			cat int
		)
		if err := rows.Scan(&key, &cat); err != nil {
			return nil, fmt.Errorf("读物品类别某一行失败: %w", err)
		}
		out[key] = cat
	}
	return out, rows.Err()
}

// Account 读一个账号。查不到返回 ErrNotFound。
func (m *MySQL) Account(ctx context.Context, username string) (Account, error) {
	var (
		a        Account
		inPrison int64
	)
	q := `SELECT username, nickname, user_id, password_sha1, level, stars, silver_minor, gold_minor, in_prison
	      FROM users WHERE username = ?`
	err := m.db.QueryRowContext(ctx, q, username).Scan(
		&a.Username, &a.Nickname, &a.UserID, &a.PasswordSha1,
		&a.Level, &a.Stars, &a.SilverMinor, &a.GoldMinor, &inPrison)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, fmt.Errorf("%w：账号 %s", ErrNotFound, username)
	}
	if err != nil {
		return Account{}, fmt.Errorf("读账号 %s 失败: %w", username, err)
	}
	a.InPrison = inPrison != 0
	return a, nil
}

// AddWallet 加减钱包（单位是最小单位，可以是负数）。放在事务里做，
// 买货扣钱这件事要么改成功要么整个不改，不会出现「钱扣了货没到」这种半截状态。
func (m *MySQL) AddWallet(ctx context.Context, username string, silverDelta, goldDelta int64) (Account, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, fmt.Errorf("开事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // 提交成功时 Rollback 是空操作，忘 commit 也不会漏

	res, err := tx.ExecContext(ctx,
		`UPDATE users SET silver_minor = silver_minor + ?, gold_minor = gold_minor + ? WHERE username = ?`,
		silverDelta, goldDelta, username)
	if err != nil {
		return Account{}, fmt.Errorf("改钱包失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Account{}, fmt.Errorf("%w：账号 %s", ErrNotFound, username)
	}
	if err := tx.Commit(); err != nil {
		return Account{}, fmt.Errorf("提交失败: %w", err)
	}
	// 提交后再读一遍：拿回来的是库里真正落地的值，不是我们自己加出来的数
	return m.Account(ctx, username)
}

// SkillState 一次把三张表拼成一个技能状态。空表就是全 0，不算错误。
func (m *MySQL) SkillState(ctx context.Context, username string) (SkillState, error) {
	st := SkillState{Cats: map[int]float64{}, Nodes: map[int]int{}}

	if err := m.db.QueryRowContext(ctx, `SELECT granted FROM skill_points WHERE username = ?`, username).
		Scan(&st.Granted); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return st, fmt.Errorf("读发放点数失败: %w", err)
	}

	rows, err := m.db.QueryContext(ctx, `SELECT skill_id, points FROM skill WHERE username = ?`, username)
	if err != nil {
		return st, fmt.Errorf("读熟练度失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cat int
			pts float64
		)
		if err := rows.Scan(&cat, &pts); err != nil {
			return st, fmt.Errorf("读熟练度某一行失败: %w", err)
		}
		st.Cats[cat] = pts
	}
	if err := rows.Err(); err != nil {
		return st, fmt.Errorf("遍历熟练度失败: %w", err)
	}

	nrows, err := m.db.QueryContext(ctx, `SELECT node_id, level FROM skill_node WHERE username = ?`, username)
	if err != nil {
		return st, fmt.Errorf("读已学节点失败: %w", err)
	}
	defer nrows.Close()
	for nrows.Next() {
		var (
			node int
			lv   int
		)
		if err := nrows.Scan(&node, &lv); err != nil {
			return st, fmt.Errorf("读已学节点某一行失败: %w", err)
		}
		if lv > 0 {
			st.Nodes[node] = lv
		}
	}
	if err := nrows.Err(); err != nil {
		return st, fmt.Errorf("遍历已学节点失败: %w", err)
	}
	return st, nil
}

// SetSkillPoints 设置发放的技能点。表主键就是 username，所以直接 UPSERT。
func (m *MySQL) SetSkillPoints(ctx context.Context, username string, granted int) error {
	q := `INSERT INTO skill_points (username, granted, updated_at) VALUES (?, ?, ?)
	      ON DUPLICATE KEY UPDATE granted = VALUES(granted), updated_at = VALUES(updated_at)`
	if _, err := m.db.ExecContext(ctx, q, username, granted, nowMs()); err != nil {
		return fmt.Errorf("写发放点数失败: %w", err)
	}
	return nil
}

// SetMastery 写一个大类的熟练度。钓鱼涨熟练度那一步就落在这张表上。
func (m *MySQL) SetMastery(ctx context.Context, username string, cat int, points float64) error {
	q := `INSERT INTO skill (username, skill_id, points, updated_at) VALUES (?, ?, ?, ?)
	      ON DUPLICATE KEY UPDATE points = VALUES(points), updated_at = VALUES(updated_at)`
	if _, err := m.db.ExecContext(ctx, q, username, cat, points, nowMs()); err != nil {
		return fmt.Errorf("写熟练度失败: %w", err)
	}
	return nil
}

// SetSkillNode 写某个节点学到几级。传 0 就直接删行 —— 和 Node 版一致，
// 因为「有一行 level=0」和「没这行」在技能树里等价，留着只会让以后统计变复杂。
func (m *MySQL) SetSkillNode(ctx context.Context, username string, node, level int) error {
	if level <= 0 {
		if _, err := m.db.ExecContext(ctx, `DELETE FROM skill_node WHERE username = ? AND node_id = ?`, username, node); err != nil {
			return fmt.Errorf("删技能节点失败: %w", err)
		}
		return nil
	}
	q := `INSERT INTO skill_node (username, node_id, level, updated_at) VALUES (?, ?, ?, ?)
	      ON DUPLICATE KEY UPDATE level = VALUES(level), updated_at = VALUES(updated_at)`
	if _, err := m.db.ExecContext(ctx, q, username, node, level, nowMs()); err != nil {
		return fmt.Errorf("写技能节点失败: %w", err)
	}
	return nil
}

// 物件表要查的列。写成一个常量，省得四处复制粘贴改漏。
const objectCols = `SELECT guid, username, item_key, item_id, parent_guid, price, how, source, elem, created_at
	                FROM objects`

// Objects 列出这个号名下所有货。按建立时间排，客户端那个「最新入手的排前面」靠这个顺序。
func (m *MySQL) Objects(ctx context.Context, username string) ([]Object, error) {
	rows, err := m.db.QueryContext(ctx, objectCols+` WHERE username = ? ORDER BY created_at`, username)
	if err != nil {
		return nil, fmt.Errorf("读名下物品失败: %w", err)
	}
	defer rows.Close()
	var out []Object
	for rows.Next() {
		o, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Object 按 GUID 取单件货（装配、删除、卖鱼都要先按 GUID 找到这件）。
func (m *MySQL) Object(ctx context.Context, guid string) (Object, error) {
	row := m.db.QueryRowContext(ctx, objectCols+` WHERE guid = ?`, guid)
	o, err := scanObject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Object{}, fmt.Errorf("%w：GUID %s", ErrNotFound, guid)
	}
	return o, err
}

// InsertObject 新增一件货。GUID 由调用方生成（谁发号谁负责唯一），
// created_at 传 0 就用当前时间。
func (m *MySQL) InsertObject(ctx context.Context, o Object) error {
	if o.CreatedAt == 0 {
		o.CreatedAt = nowMs()
	}
	var parent any
	if o.ParentGUID != "" {
		parent = o.ParentGUID // 空串要写 NULL，不能写空字符串：客户端看 NULL 才知道是顶层
	}
	q := `INSERT INTO objects (guid, username, item_key, item_id, parent_guid, price, how, source, elem, created_at)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := m.db.ExecContext(ctx, q, o.GUID, o.Username, o.ItemKey, o.ItemID, parent,
		o.PriceMinor, o.How, o.Source, o.Elem, o.CreatedAt); err != nil {
		return fmt.Errorf("新增物品失败 %s: %w", o.GUID, err)
	}
	return nil
}

// DeleteObject 删一件货，返回是否真删了（false = 本来就没这件）。
func (m *MySQL) DeleteObject(ctx context.Context, username, guid string) (bool, error) {
	res, err := m.db.ExecContext(ctx, `DELETE FROM objects WHERE username = ? AND guid = ?`, username, guid)
	if err != nil {
		return false, fmt.Errorf("删除物品失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("问删除条数失败: %w", err)
	}
	return n > 0, nil
}

// Shortcuts 列快捷键栏，按槽位号排序（下发顺序就得是这个）。
func (m *MySQL) Shortcuts(ctx context.Context, username string) ([]Shortcut, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT slot, guid, item_key FROM shortcuts WHERE username = ? ORDER BY slot`, username)
	if err != nil {
		return nil, fmt.Errorf("读快捷键失败: %w", err)
	}
	defer rows.Close()
	var out []Shortcut
	for rows.Next() {
		var s Shortcut
		if err := rows.Scan(&s.Slot, &s.GUID, &s.ItemKey); err != nil {
			return nil, fmt.Errorf("读快捷键某一行失败: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SetShortcut 绑一格；guid 传空串就是取消绑定（直接删行，不留空记录）。
func (m *MySQL) SetShortcut(ctx context.Context, username string, slot int, guid, itemKey string) error {
	if guid == "" {
		if _, err := m.db.ExecContext(ctx, `DELETE FROM shortcuts WHERE username = ? AND slot = ?`, username, slot); err != nil {
			return fmt.Errorf("取消快捷键失败: %w", err)
		}
		return nil
	}
	q := `INSERT INTO shortcuts (username, slot, guid, item_key, updated_at) VALUES (?, ?, ?, ?, ?)
	      ON DUPLICATE KEY UPDATE guid = VALUES(guid), item_key = VALUES(item_key), updated_at = VALUES(updated_at)`
	if _, err := m.db.ExecContext(ctx, q, username, slot, guid, itemKey, nowMs()); err != nil {
		return fmt.Errorf("写快捷键失败: %w", err)
	}
	return nil
}

// rowLike sql.Row 和 sql.Rows 的公共部分，这样 scanObject 一个函数能服务两处查询
type rowLike interface {
	Scan(dest ...any) error
}

// scanObject 把一行 objects 读成 Object。parent_guid 是可空列，必须走 NullString。
func scanObject(r rowLike) (Object, error) {
	var (
		o      Object
		parent sql.NullString
	)
	if err := r.Scan(&o.GUID, &o.Username, &o.ItemKey, &o.ItemID, &parent,
		&o.PriceMinor, &o.How, &o.Source, &o.Elem, &o.CreatedAt); err != nil {
		return Object{}, err
	}
	o.ParentGUID = parent.String
	return o, nil
}

// nowMs 当前时间（毫秒）。库里 created_at/updated_at 全是毫秒整数，别塞别的格式。
func nowMs() int64 { return time.Now().UnixMilli() }

// UsernamesWithObjects 列出「在 MySQL 里有背包」的账号。
// 对齐守卫用它划「活号」范围：哪边有货，哪边的钱包和物品就必须和另一边一件不差。
func (m *MySQL) UsernamesWithObjects(ctx context.Context) ([]string, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT DISTINCT username FROM objects ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("查背包账号失败: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("读背包账号失败: %w", err)
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// SaveToken 把一张登录票写进 login_tokens。同一个 token 重复写就当续期（覆盖旧值），
// 这样客户端重放同一张票也不会撑出两行脏数据。
func (m *MySQL) SaveToken(ctx context.Context, t LoginTicket) error {
	const q = `INSERT INTO login_tokens
		(token, logintoken, username, user_id, expires_at, created_at, used_at, ip, machine_id, version)
		VALUES (?, ?, ?, ?, ?, ?, NULL, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
		logintoken = VALUES(logintoken), username = VALUES(username), user_id = VALUES(user_id),
		expires_at = VALUES(expires_at), ip = VALUES(ip), machine_id = VALUES(machine_id), version = VALUES(version)`
	_, err := m.db.ExecContext(ctx, q, t.Token, t.LoginToken, t.Username, t.UserID,
		t.Expires, t.CreatedAt, t.IP, t.MachineID, t.Version)
	if err != nil {
		return fmt.Errorf("登录票落库失败: %w", err)
	}
	return nil
}

// FindToken 按 token 或 logintoken 查一张登录票（客户端两条路都可能拿其中一个来问）。
// 没查到返回 found=false，不当错误处理：票过期和根本没见过都算正常业务。
func (m *MySQL) FindToken(ctx context.Context, key string) (LoginTicket, bool, error) {
	const q = `SELECT token, COALESCE(logintoken, ''), username, user_id, expires_at, created_at,
		COALESCE(ip, ''), COALESCE(version, ''), COALESCE(machine_id, '')
		FROM login_tokens WHERE token = ? OR logintoken = ?
		ORDER BY (token = ?) DESC LIMIT 1`
	// 三个占位符都是同一个号码：前两个用来匹配，第三个用来「优先认 token 那一行」。
	// 客户端可能拿 token 来问、也可能拿 logintoken 来问，两种都得接；
	// 但回给客户端的 <token> 必须是游戏通道那把 32 位钥匙，所以要优先挑 token 列命中的行。
	row := m.db.QueryRowContext(ctx, q, key, key, key)

	var t LoginTicket
	err := row.Scan(&t.Token, &t.LoginToken, &t.Username, &t.UserID, &t.Expires, &t.CreatedAt,
		&t.IP, &t.Version, &t.MachineID)
	if errors.Is(err, sql.ErrNoRows) {
		return LoginTicket{}, false, nil
	}
	if err != nil {
		return LoginTicket{}, false, fmt.Errorf("查登录票失败: %w", err)
	}
	return t, true, nil
}
