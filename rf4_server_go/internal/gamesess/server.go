// Package gamesess 是 TCP 游戏通道：客户端登录成功之后连的就是这条口。
// 顺序是：服务器先发 36 字节明文会话 GUID -> 客户端发 token -> 再发一帧明文 hermes XML
// -> 之后两边 payload 都走 RC4（钥匙就是 token 那串 ASCII 本身）。
// 这一层只管「监听、握手、切帧、回 ack、发心跳」，业务命令一律走 handlers.go 那张路由表。
// 想加商城/背包，只要往路由表挂一个函数就行，不用动这里 —— 解耦就靠这道口子。
package gamesess

import (
	"net"
	"strconv"
	"time"

	"rf4server/internal/baitdig"
	"rf4server/internal/buy"
	"rf4server/internal/container"
	"rf4server/internal/craft"
	"rf4server/internal/fishing"
	"rf4server/internal/gamecfg"
	"rf4server/internal/gamestore"
	"rf4server/internal/logging"
	"rf4server/internal/mapworld"
	"rf4server/internal/rig"
	"rf4server/internal/shop"
	"rf4server/internal/skilldef"
	"rf4server/internal/staticdef"
	"rf4server/internal/store"
	"rf4server/internal/wire"
)

// Config 这条 TCP 通道的参数。不填的项由 New 补默认值，main 只覆盖命令行传进来的那几项。
type Config struct {
	Host string        // 监听地址，默认 0.0.0.0（本机登录器连 127.0.0.1 就够）
	Port int           // 监听端口，默认 19213；注意和 Node 版抢同一个端口，两边只能开一个
	Addr string        // 整串监听地址，填了就优先用它；测试里写 127.0.0.1:0 让系统挑空闲端口
	HB   time.Duration // 心跳间隔：真服实测约 3.4 秒一条 4 字节全零空帧，客户端靠它判断连接还活着
	// StatePush 玩家状态（2/1）多久补推一次。Node 版挂机实测就是 12500ms 一条；
	// 填 0 = New 补默认值 12500ms，命令行填负数 = 只发进场那一条，不再补推。
	StatePush time.Duration
	// SessionGUID 开服首包那 36 字节的服务器身份 ID。留空 = 用 identity.go 里那个固定值；
	// 填 random = 每次连接随机一个（只为对照实验，真客户端会拒，别日常用）。
	SessionGUID string

	// ---- 钓鱼（14/x）这一片的参数，命令行全能调 ----
	// FishLoc 这次钓鱼对应哪个水域（static_map_fish_roster.loc）。
	// 出鱼的候选名单按它挑；留鱼时鱼卡片上那个钓点名也填它。
	FishLoc string
	// FishKey 强制只出这一种鱼（static_species_weight.fish_key），测某条鱼时用；留空 = 按名册随机。
	FishKey string
	// FishBait 鱼卡片上写「用的什么饵」（obj25#08）。还没和装配联动，先给个固定值。
	FishBait string
	// AutoBite 抛竿后要不要自动派咬钩。false = 只回抛竿包、水里不出鱼（只想对着日志看流程时用）。
	// 注意：默认值由 cmd/rf4server 那个 --auto-bite 给（默认 true），New 不替它做主。
	AutoBite bool
	// BiteMin / BiteMax 抛出去多久之后咬钩，中间随机。真服实测就是这个量级，不是定值。
	BiteMin time.Duration
	BiteMax time.Duration
}

// Deps 会话要用到的依赖：账号库、协议字段表、技能定义表、日志。
// 全部从外面注入、不在包里自己 open 文件，是为了单测能塞假数据、也能整域脱离网络跑。
type Deps struct {
	DB    *store.DB         // 账号定义 + 登录令牌表（db.json，只读）
	Run   gamestore.Runtime // MySQL 运行时数据：账号钱包、技能、背包、快捷键的真值；nil = 这台机器没开 MySQL
	Table *wire.Table       // 协议字段表 wire_tables.json：物件类别 -> 字段布局
	Defs  *skilldef.Defs    // 技能定义表 skill_defs.json：190 个节点的门槛/上限/互斥组
	// Static 静态定义帧（1/13 地图+天气、28/1 大区浏览器）。整帧都不是玩家自己的数据，
	// 所以走「定义 JSON + 现拼」这条路，不占用 MySQL；没装就等于这几条没搬。
	Static *staticdef.Library
	// Shop 商城货架（22/1）。商城这套全是「按静态定义 + MySQL 货架表现拼」，没接上库就是 nil，
	// 处理器会照样回一帧合法的空答案，不会让客户端干等。
	Shop *shop.Service
	// Buy 购买（22/2）：扣钱、现拼物件记录入库、回回执，全走 MySQL
	Buy *buy.Service
	// Container 容器（21/1 工坊/仓库）：读 MySQL containers + container_items
	Container *container.Service
	// Rig 装配（17/2 装上、17/3 拆、17/4 全拆）：改的是 objects.elem 那套物件记录，
	// 顺带把 rig / rig_component 两张表同步成给少爷看的投影
	Rig *rig.Service
	// BaitDig 挖饵（4/8 使用铲子/红虫长柄勺）。
	BaitDig *baitdig.Service
	// Craft 制作鱼饵（9/1）：配方来自 MySQL，产物写 MySQL objects 并实时推背包。
	Craft *craft.Service
	// Fishing 钓鱼域（14/1 抛竿、14/14 咬钩推送、14/4 起鱼、14/5 留鱼）。
	// 没接上 MySQL 就是 nil：出鱼要靠 static_species_weight + 水域名册那两张表，没库就没鱼可出。
	Fishing *fishing.Service
	// GameCfg 游戏开机那 9 块配置（internal/gamecfg）：1/4 按类型下发配置块本体，
	// 1/5 回这 9 块的 sha1 版本号。没装它 = 客户端开机那一步直接崩
	// （Game bootstrapping failed: Unimplemented RPC 1:4），所以实机必须装好。
	GameCfg *gamecfg.Library
	// MapWorld 水域定义（1/12）：按请求中的 level_* 选择 obj205，并把天气重锚到当前游戏时间。
	MapWorld *mapworld.Library

	Log *logging.Logger
}

// Server 监听一条 TCP 端口，每来一个连接起一个 Session。
type Server struct {
	Deps   Deps         // 依赖，见上
	Config Config       // 参数，见上
	ln     net.Listener // 真正的监听句柄（Addr 要用它拿真实端口）
	nextID int          // 会话编号，只为日志好认（Node 版也是从 1 开始递增）
}

// New 建服务器，顺便把没填的参数补成实测值。
func New(d Deps, c Config) *Server {
	if c.Host == "" {
		c.Host = "0.0.0.0"
	}
	if c.Port == 0 {
		c.Port = 19213
	}
	if c.HB <= 0 {
		c.HB = 3400 * time.Millisecond
	}
	// 状态补推间隔：默认照 Node 版挂机那个节奏 12.5 秒一条；
	// 命令行给个 -1 表示进场之后就不再补推（只想盯着第一现场的时候用）。
	// 钓鱼参数兜底：饵名给真服那个编号；咬钩区间照 Node 版实测的 4~16 秒。
	// FishLoc / FishKey 留空是有意义的（= 按库里名册随机），所以不补默认值。
	if c.FishBait == "" {
		c.FishBait = "worm_01"
	}
	if c.BiteMin <= 0 {
		c.BiteMin = 4 * time.Second
	}
	if c.BiteMax <= c.BiteMin {
		c.BiteMax = c.BiteMin + 12*time.Second
	}
	if c.StatePush == 0 {
		c.StatePush = 12500 * time.Millisecond
	} else if c.StatePush < 0 {
		c.StatePush = 0
	}
	return &Server{Deps: d, Config: c}
}

// Addr 该监听的地址；已经在监听就回报实际端口（传 0 让系统挑端口时也能拿到）。
func (s *Server) Addr() string {
	if s.ln != nil {
		return s.ln.Addr().String() // 已经在监听：回报真实端口（Addr 传 :0 时靠这个拿到系统分的端口）
	}
	if s.Config.Addr != "" {
		return s.Config.Addr
	}
	return s.Config.Host + ":" + strconv.Itoa(s.Config.Port)
}

// Listen 开始监听。测试里要先进程内起监听、再让假客户端连，所以这里和 Serve 分开。
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.Addr())
	if err != nil {
		return err
	}
	s.ln = ln
	return nil
}

// Close 停止监听（已在跑的会话各自跑完，不强杀）。
func (s *Server) Close() error {
	if s.ln == nil {
		return nil
	}
	return s.ln.Close()
}

// Serve 接连接，一个连接一个 goroutine；监听句柄被关掉就返回。
func (s *Server) Serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // 监听被关掉：正常退出，不是错误
		}
		s.nextID++
		id := s.nextID
		go func() {
			newSession(s, id, conn).Run()
		}()
	}
}

// ServeBoot 监听 + 打一行开机日志 + 进 Accept 循环。main 里一行就能起这条通道。
func (s *Server) ServeBoot() error {
	if err := s.Listen(); err != nil {
		return err
	}
	s.Deps.Log.Boot("game", "游戏服务器 "+s.Addr()+
		"（Go 版：已搬握手和技能树，背包/商城/进场还在 Node 版那边）")
	s.Serve()
	return nil
}
