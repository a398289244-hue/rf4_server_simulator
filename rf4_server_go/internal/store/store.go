// Package store 是数据入口。现在只有 db.json 一个实现，MySQL 之后加一个同接口的实现就行。
// 业务域不许自己 open 文件或者连库，只能依赖这里 —— 这就是解耦留的那个口子。
package store

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
)

// User 一个游戏账号（db.json 的 users 数组）
type User struct {
	Username     string   `json:"username"`     // 登录名
	Nickname     string   `json:"nickname"`     // 游戏里显示的名字
	UserID       int      `json:"userId"`       // 数字账号 id，发包里多处要用
	PasswordSha1 string   `json:"passwordSha1"` // 登录密码的 sha1（40 位小写十六进制）
	Level        int      `json:"level"`        // 角色等级
	SilverMinor  int64    `json:"silverMinor"`  // 银币的最小单位（1 银币 = 100 minor），发包里就是这个整数
	GoldMinor    int64    `json:"goldMinor"`    // 金币最小单位
	Stars        int      `json:"stars"`        // 星级
	MachineIDs   []string `json:"machineIds"`   // 这台账号绑定过的机器号（登录校验用，现在没拦）
	InPrison     bool     `json:"inPrison"`     // 是不是在服刑（会影响进游戏的流程）
}

// Gateway 网关配置（db.json 的 gateway 段）
type Gateway struct {
	RoutePrefix      string            `json:"routePrefix"`        // 网关路由前缀，实测 "/lv33/"
	GameVersion      string            `json:"gameVersion"`        // 要求的游戏版本串，日志里对照用
	LoginSuccessBody string            `json:"loginSuccessBody"`   // 登录成功的 XML 模板，里面有 {{token}} 这类占位
	UnknownRouteBody string            `json:"unknownRouteBody"`   // 认不出的路由回这个正文
	UnknownRouteStat int               `json:"unknownRouteStatus"` // 认不出的路由回这个 HTTP 状态码
	Region           string            `json:"region"`             // 分区，实测是 CN
	LoginHost        string            `json:"loginHost"`          // 下发给客户端的游戏服地址（这份 db.json 里没有，退回 localHost）
	LocalHost        string            `json:"localHost"`          // 本机等效地址，实测 127.0.0.1
	GamePort         int               `json:"gamePort"`           // 游戏通道端口，实测 19213
	Mode             string            `json:"mode"`               // local = 只回本地造的报文（我们就用这个）
	Errors           map[string]ErrDef `json:"errors"`             // 错误码表：banned/noAccount/badPassword/badVersion
}

// ErrDef 一个登录错误码：回给客户端的错误号和说明文字
type ErrDef struct {
	Code        int    `json:"code"`        // 错误号，实测 noAccount=3 badPassword=4 badVersion=5 banned=7
	Description string `json:"description"` // 说明文字，客户端直接显示
}

// SkillRow 一个账号的技能状态（db.json 的 skills 段）。Go 版之后换成 MySQL 读。
type SkillRow struct {
	Cats          map[string]float64 `json:"cats"`          // 大类号(字符串) -> 熟练度%
	Nodes         map[string]int     `json:"nodes"`         // 节点号(字符串) -> 已学等级
	PointsGranted int                `json:"pointsGranted"` // 发放的技能点；0 = 没设过，按等级曲线算
	UpdatedAt     int64              `json:"updatedAt"`     // 最后改动时间（毫秒）
}

// DB 是一份 db.json 的内存镜像。Go 版**只读不写**这个文件：
// 它现在归 Node 版后端管，两边都写会互相盖掉；要写的时候走 MySQL，或以后让 Go 独家接管。
// BagObject 是 db.json objects.<账号>.list 里的一件物品。
// Go 版只读这一份（Node 版还在写它），对齐核对只认 guid 和 key 两项。
type BagObject struct {
	GUID   string  `json:"guid"`   // 16 字节 GUID 的 32 位小写十六进制，两边主键就是它
	Key    string  `json:"key"`    // 物品编号，例如 tele_10112_4
	Price  int64   `json:"price"`  // 成交价（最小单位，1 银币 = 100）
	Parent *string `json:"parent"` // 装在谁身上（竿/仓库）；null = 背包最外层
	Elem   string  `json:"elem"`   // 物件记录字节（hex），下发给客户端那一段
}

// Bag 是一个账号在 db.json objects 表里的那一格
type Bag struct {
	List       []BagObject `json:"list"`       // 背包里的每一件
	SeededFrom string      `json:"seededFrom"` // 这批货哪来的（人话说明，只对账时看）
}

type DB struct {
	Path    string              // 文件路径，报错时说清是哪份
	GW      Gateway             // 网关配置
	Users   []User              // 账号表
	Skill   map[string]SkillRow // 登录名 -> 技能状态
	Objects map[string]Bag      // 登录名 -> 背包（objects 表那一格，Go 只读）

	tokens *Tokens // 登录令牌表（内存），下面 Tokens() 返回它
}

// Token 一次登录发出去的令牌。游戏通道第一条 auth 帧带的就是 Token 这串（32 个十六进制字符）。
type Token struct {
	Token      string // 游戏通道钥匙
	LoginToken string // 网页/续期用的令牌，48 个十六进制字符
	Expires    int64  // 过期时间（epoch 秒），实测 = 当前时间 + 365 天
	Username   string // 谁的账号
	UserID     int    // 账号数字 id
	ClientIP   string // 从哪个 IP 来的
	CreatedAt  int64  // 发出时间（毫秒）
}

// Tokens 登录令牌表，只在内存里：游戏登录完马上就连 TCP，不需要跨进程留档，
// 也避免和 Node 版抢着写 db.json。
type Tokens struct {
	mu   sync.RWMutex
	m    map[string]Token // token 和 logintoken 都指向同一条记录（Node 版就这么存）
	Last string           // 最后一次发出去的 logintoken，方便日志对照
}

// NewTokens 建空表
func NewTokens() *Tokens { return &Tokens{m: map[string]Token{}} }

// Save 记一条令牌（token、logintoken 两个键都能查到）
func (t *Tokens) Save(rec Token) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.m[rec.Token] = rec
	if rec.LoginToken != "" {
		t.m[rec.LoginToken] = rec
	}
	t.Last = rec.LoginToken
}

// Lookup 按 token 或 logintoken 查；查不到返回 false
func (t *Tokens) Lookup(key string) (Token, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	rec, ok := t.m[strings.TrimSpace(key)]
	return rec, ok
}

// Load 读一份 db.json，只取要用的那几段
func Load(path string) (*DB, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Gateway Gateway             `json:"gateway"`
		Users   []User              `json:"users"`
		Skills  map[string]SkillRow `json:"skills"`
		Objects map[string]Bag      `json:"objects"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	db := &DB{Path: path, GW: raw.Gateway, Users: raw.Users, Skill: raw.Skills, Objects: raw.Objects, tokens: NewTokens()}
	if db.Skill == nil {
		db.Skill = map[string]SkillRow{}
	}

	if db.Objects == nil {
		db.Objects = map[string]Bag{}
	}
	if db.GW.Region == "" {
		db.GW.Region = "CN" // 实测默认
	}
	return db, nil
}

// FindUser 按登录名找账号
func (d *DB) FindUser(name string) (User, bool) {
	for _, u := range d.Users {
		if u.Username == name {
			return u, true
		}
	}
	return User{}, false
}

// Tokens 返回令牌表（网关写、游戏服读）
func (d *DB) Tokens() *Tokens { return d.tokens }

// SkillsFor 取某账号的技能状态；没有就返回全 0 的空状态
func (d *DB) SkillsFor(username string) SkillRow {
	row, ok := d.Skill[username]
	if !ok {
		return SkillRow{Cats: map[string]float64{}, Nodes: map[string]int{}}
	}
	if row.Cats == nil {
		row.Cats = map[string]float64{}
	}
	if row.Nodes == nil {
		row.Nodes = map[string]int{}
	}
	return row
}
