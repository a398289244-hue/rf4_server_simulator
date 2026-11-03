// Package gamestore 是「运行时数据」这一层：账号、钱包、背包、快捷键、技能这些会变的数
// 全部从 MySQL 读写（少爷定的：数据要能在数据库里直接看直接改）。
// 业务域（技能、背包、商城……）只认下面那个 Runtime 接口，不认 SQL、不认驱动，
// 以后要换库、加缓存、或者做「Node 版和 Go 版双跑对答案」，都只动这一个包。
//
// 静态定义（技能节点表、物品属性、货架）不放这儿：那些是只读资源，走 internal/assetpaths
// 读真服凭据 JSON，或者是 static_* 那批 MySQL 表，改起来是「改版本」而不是「改玩家数据」。
package gamestore

import (
	"context"
	"errors"
)

// ErrNotFound 没找到这条记录。业务层拿它区分「没有」和「库出错了」，不要拿空结构体猜。
var ErrNotFound = errors.New("没有这条记录")

// Account 一个游戏账号的可变部分（对应 MySQL 的 users 表）。
// 金额一律用最小单位：1 银币 = 100 minor，发包里就是这个整数，别在这儿除来除去。
type Account struct {
	Username     string // 登录名，users.username
	Nickname     string // 游戏里显示的名字，users.nickname
	UserID       int64  // 数字账号 id，发包多处要用，users.user_id
	PasswordSha1 string // 登录密码的 sha1（40 位小写十六进制），users.password_sha1
	Level        int    // 角色等级，users.level
	Stars        int    // 星级，users.stars
	SilverMinor  int64  // 银币最小单位，users.silver_minor
	GoldMinor    int64  // 金币最小单位，users.gold_minor
	InPrison     bool   // 是否在服刑，users.in_prison
}

// SkillState 一个账号的技能状态（对应 skill_points + skill + skill_node 三张表）。
// Cats 的键是大类号（1/2/3/4/6/7/8/11），值是熟练度百分比；Nodes 的键是节点号，值是已学等级。
type SkillState struct {
	Granted int             // 发放了多少技能点（skill_points.granted）
	Cats    map[int]float64 // 大类号 -> 熟练度百分比
	Nodes   map[int]int     // 节点号 -> 已学等级
}

// Catalog 静态定义查询：物品编号 -> 类别号这类只读资源。
// 和 Runtime 分开是因为两者性质不同：Runtime 是「这个号会变的数据」，
// Catalog 是「整个游戏的定义表」（static_* 那批表），生命周期和缓存策略都不一样。
type Catalog interface {
	// Categories 按物品编号批量查类别号（static_item_def.category）。
	// 一次问一批，别一件一趟查；查不到的键不出现在返回的 map 里。
	Categories(ctx context.Context, keys []string) (map[string]int, error)
}

// Object 玩家名下一件货（对应 objects 表）。
// Elem 是这件货的物件记录字节（十六进制小写），发背包时直接用，不用再从定义反推；
// 它是「这个号自己的数据」，不是录制快照，所以不违反「不碰快照」那条规矩。
type Object struct {
	GUID       string // 16 字节 GUID 的 32 位十六进制，objects.guid
	Username   string // 归属账号，objects.username
	ItemKey    string // 物品编号，如 mono_15092，objects.item_key
	ItemID     int    // 服务器编号（商店货架那个 id），objects.item_id
	ParentGUID string // 装在谁里面（竿、仓库），空 = 背包顶层，objects.parent_guid
	PriceMinor int64  // 入手价（最小单位），objects.price
	How        string // 这件货的来历说明（日志用），objects.how
	Source     string // 哪个后端、什么时候建的，objects.source
	Elem       string // 物件记录字节（hex），objects.elem
	CreatedAt  int64  // 建立时间（毫秒），objects.created_at
}

// Shortcut 快捷键栏的一格（对应 shortcuts 表）：槽位号 -> 哪件货。
type Shortcut struct {
	Slot    int    // 槽位号，shortcuts.slot
	GUID    string // 绑的物品 GUID，shortcuts.guid
	ItemKey string // 物品编号（冗余存一份，下发时不用回查 objects），shortcuts.item_key
}

// Runtime 所有业务域共用的数据入口。
// 每个方法都收 context：以后加超时、加事务、加埋点都不用改调用方。
// 约定：查不到就返回 ErrNotFound（包装过的也算），别返回零值让上层猜。
type Runtime interface {
	// Account 按登录名取账号（含钱包）。没有这个人返回 ErrNotFound。
	Account(ctx context.Context, username string) (Account, error)
	// AddWallet 给钱包加减钱（单位：最小单位，可为负）。返回改完的最新值，省得调用方再查一遍。
	AddWallet(ctx context.Context, username string, silverDelta, goldDelta int64) (Account, error)

	// SkillState 取这个号的技能状态（发放点数 + 熟练度 + 已学节点）。
	SkillState(ctx context.Context, username string) (SkillState, error)
	// SetSkillPoints 设置发放的技能点（少爷要调成 100 就是改这一格）。
	SetSkillPoints(ctx context.Context, username string, granted int) error
	// SetMastery 写一个大类的熟练度。
	SetMastery(ctx context.Context, username string, cat int, points float64) error
	// SetSkillNode 写一个节点已学到几级；level 传 0 等于把这一格抹掉。
	SetSkillNode(ctx context.Context, username string, node, level int) error

	// Objects 列出这个号名下所有货（含装在竿/仓库里的）。
	Objects(ctx context.Context, username string) ([]Object, error)
	// Object 取单件货。
	Object(ctx context.Context, guid string) (Object, error)
	// InsertObject 新增一件货（购买、赠送、发初始装备都走这儿）。
	InsertObject(ctx context.Context, o Object) error
	// DeleteObject 删掉一件货，返回是否真删了。删竿时挂在它上面的零件要调用方自己一起清掉，
	// 免得「影子物品」那种事儿再来一遍。
	DeleteObject(ctx context.Context, username, guid string) (bool, error)

	// Shortcuts 列出这个号的快捷键栏，按槽位号排序。
	Shortcuts(ctx context.Context, username string) ([]Shortcut, error)
	// SetShortcut 绑一格；guid 传空串就是清空这一格。
	SetShortcut(ctx context.Context, username string, slot int, guid, itemKey string) error

	// Close 关掉连接池。
	Close() error
}
