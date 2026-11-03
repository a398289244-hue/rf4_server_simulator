// Command rf4server 是 Go 版后端的入口：读命令行参数 -> 把依赖装好 -> 起 HTTP 网关和 TCP 游戏通道。
// 这里只做「接线」，一行业务逻辑都不写：业务都在 internal 下面的各个包里。
// 想加一块功能，先去 internal 加包，再回来多接一行，别往这个文件里塞逻辑。
package main

import (
	"context"
	"flag"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rf4server/internal/assetpaths"
	"rf4server/internal/baitdig"
	"rf4server/internal/buy"
	"rf4server/internal/container"
	"rf4server/internal/craft"
	"rf4server/internal/fishing"
	"rf4server/internal/gamecfg"
	"rf4server/internal/gamesess"
	"rf4server/internal/gamestore"
	"rf4server/internal/gateway"
	"rf4server/internal/httpapi"
	"rf4server/internal/itemdef"
	"rf4server/internal/itemwire"
	"rf4server/internal/logging"
	"rf4server/internal/mapworld"
	"rf4server/internal/rig"
	"rf4server/internal/shop"
	"rf4server/internal/skilldef"
	"rf4server/internal/staticdef"
	"rf4server/internal/store"
	"rf4server/internal/wire"
)

// 默认端口和 Node 版一模一样：登录器的 GameAssembly.dll 已经写死 127.0.0.1:19214，
// 所以换 Go 版后端不用再动客户端。想双跑就命令行改端口，登录器只认 19214 那条。
const (
	defaultHTTPPort       = 19214 // HTTP 门户 + 网关（登录器登录时连这个）
	defaultHTTPCompatPort = 7515  // 当前 CN 客户端内置的登录入口；与 19214 共用同一套处理器
	defaultTCPPort        = 19213 // TCP 游戏通道（登录成功后连这个）
	defaultHBMs           = 3400  // 心跳间隔，真服抓包实测约 3.4 秒一条
	defaultPushMs         = 12500 // 玩家状态 2/1 补推间隔，Node 版挂机实测就是这个节奏
)

// Version 后端版本号。改版本就动这一行，开机第一行日志会打出来 ——
// 实机出问题时常温「当时跑的到底是哪一版」，有这行就不用猜。
const Version = "0.1.0"

// config 一次启动用到的全部参数。集中成一个结构体，是为了 run() 能脱离命令行直接测。
type config struct {
	HTTPPort       int    // HTTP 端口
	HTTPCompatPort int    // 客户端兼容 HTTP 端口；0 表示不额外监听
	TCPPort        int    // TCP 游戏通道端口
	LogDir         string // 日志目录
	DBFile         string // 账号定义 db.json 路径，空 = 用凭据目录里那份
	HBMs           int    // 心跳毫秒
	PushMs         int    // 玩家状态 2/1 补推间隔毫秒；负数 = 只发进场那一条
	SessGUID       string // 开服首包那 36 字节的服务器身份 ID；空 = 用内置固定值，random = 每次随机（对照实验用）

	MySQLOn bool   // 要不要连 MySQL（关掉就一切都退回 db.json，只为对照测试用）
	MyHost  string // 数据库地址
	MyPort  int    // 数据库端口
	MyUser  string // 数据库登录名
	MyPass  string // 数据库密码
	MyName  string // 库名
	MyProbe bool   // 只做一次「能不能连上、库里有多少表」的体检然后退出

	// ---- 钓鱼（14/x）----
	// FishLoc 这次钓鱼对应哪个水域（static_map_fish_roster.loc）。留空 = 不挑水域，全鱼种随机。
	FishLoc string
	// FishKey 强制只出这一种鱼（static_species_weight.fish_key）。留空 = 按名册随机。
	FishKey string
	// FishBait 鱼卡片上「用的什么饵」那一格（obj25#08）。
	FishBait string
	// BiteMinMs / BiteMaxMs 抛竿后多久咬钩，中间随机。
	BiteMinMs int
	BiteMaxMs int
	// NoBite 只回抛竿包、不派咬钩（只想对着日志看流程时用）。
	NoBite bool
}

func main() {
	cfg := config{}
	flag.IntVar(&cfg.HTTPPort, "http-port", defaultHTTPPort, "HTTP 门户+网关端口（登录器连这个）")
	flag.IntVar(&cfg.HTTPCompatPort, "http-compat-port", defaultHTTPCompatPort, "额外兼容的 HTTP 登录端口；0 表示关闭")
	flag.IntVar(&cfg.TCPPort, "tcp-port", defaultTCPPort, "TCP 游戏通道端口（登录成功后连这个）")
	flag.StringVar(&cfg.LogDir, "logs", "logs", "日志目录（一个类别一个文件，同时打到控制台）")
	flag.StringVar(&cfg.DBFile, "db", "", "账号定义 db.json；不填就用凭据目录里那份（Go 版只读）")
	flag.IntVar(&cfg.HBMs, "hb-ms", defaultHBMs, "心跳间隔毫秒，真服实测约 3400")
	flag.IntVar(&cfg.PushMs, "state-push-ms", defaultPushMs, "玩家状态 2/1 补推间隔毫秒（进场后第一条一定会发）；填负数就只发进场那条")
	flag.StringVar(&cfg.SessGUID, "session-guid", "", "开服首包的服务器身份 ID；留空用内置固定值，填 random 则每次随机（只用于对照实验）")
	flag.BoolVar(&cfg.MySQLOn, "mysql", true, "连不连 MySQL（运行时数据的唯一真值）")
	flag.StringVar(&cfg.MyHost, "mysql-host", "127.0.0.1", "MySQL 地址")
	flag.IntVar(&cfg.MyPort, "mysql-port", 3306, "MySQL 端口")
	flag.StringVar(&cfg.MyUser, "mysql-user", "rf4", "MySQL 用户名（别用 root）")
	flag.StringVar(&cfg.MyPass, "mysql-pass", "rf4pass", "MySQL 密码")
	flag.StringVar(&cfg.MyName, "mysql-db", "rf4", "MySQL 库名")
	flag.StringVar(&cfg.FishLoc, "fish-loc", "level_000_home",
		"钓鱼按哪个水域出鱼（static_map_fish_roster.loc）；开机日志会列出库里有哪些水域")
	flag.StringVar(&cfg.FishKey, "fish-key", "", "强制只出这一种鱼（static_species_weight.fish_key）；留空=按名册随机")
	flag.StringVar(&cfg.FishBait, "fish-bait", "worm_01", "鱼卡片上写的饵编号（obj25#08）")
	flag.IntVar(&cfg.BiteMinMs, "bite-min-ms", 4000, "抛竿后最快多久咬钩（毫秒）")
	flag.IntVar(&cfg.BiteMaxMs, "bite-max-ms", 16000, "抛竿后最慢多久咬钩（毫秒）")
	flag.BoolVar(&cfg.NoBite, "no-bite", false, "抛竿后不派咬钩（水里不会出鱼，只用来看流程）")
	flag.BoolVar(&cfg.MyProbe, "mysql-probe", false, "只体检数据库能不能连、有多少表，然后退出")
	flag.Parse()

	if err := run(cfg); err != nil {
		// 起不来就把原因当场说清楚（多半是端口被 Node 版占了，或者 MySQL 服务没开）
		panic(err)
	}
}

// run 按顺序接线：日志 -> 凭据 -> MySQL -> 账号定义 -> HTTP 网关 -> TCP 游戏通道。
// 任何一步失败都往上传，别让后端「假装起来了」。
func run(cfg config) error {
	log, err := logging.New(cfg.LogDir, true) // echo=true：IDEA 控制台实时能看到每一行
	if err != nil {
		return err
	}
	defer log.Close()

	// 凭据目录：那份真服录制和协议表仍用 Node 版 data/，两边共用一套，免得答案不一致
	dataDir, err := assetpaths.Dir()
	if err != nil {
		return err
	}
	log.Boot("boot", "RF4 私服后端 Go 版 版本 "+Version+"（Node 版已封存，运行期一帧录制都不发）")
	log.Boot("boot", "凭据目录: "+dataDir+"（直接读 Node 版那份 data/，没复制一份出来）")

	tablePath, err := assetpaths.File("wire_tables.json")
	if err != nil {
		return err
	}
	table, err := wire.LoadTable(tablePath)
	if err != nil {
		return err
	}
	log.Boot("boot", "协议字段表载入: "+strconv.Itoa(len(table.Classes))+" 个类（认不出类别时兜底用 "+table.FallbackClass+"）")

	defsPath, err := assetpaths.File("skill_defs.json")
	if err != nil {
		return err
	}
	defs, err := skilldef.Load(defsPath)
	if err != nil {
		return err
	}
	log.Boot("boot", "技能定义载入: "+strconv.Itoa(defs.Meta.NodeCount)+" 个节点 / "+
		strconv.Itoa(len(defs.Cats))+" 个大类（顶层 typeID "+strconv.Itoa(defs.Meta.TopTypeID)+"，形状照真服录制）")

	// ---- 运行时数据库：少爷定的规矩是「数据全在 MySQL，能直接看直接改」，所以它是默认路径 ----
	var run gamestore.Runtime
	// 商城三件套（货架/购买/容器）。没接上 MySQL 就一直是 nil，
	// 处理器会回一帧合法的空答案，不会让客户端干等。
	var (
		shopSvc      *shop.Service
		buySvc       *buy.Service
		containerSvc *container.Service
		rigSvc       *rig.Service
		fishSvc      *fishing.Service
		baitDigSvc   *baitdig.Service
		craftSvc     *craft.Service
	)
	if cfg.MySQLOn {
		my, err := gamestore.Open(gamestore.Config{Host: cfg.MyHost, Port: cfg.MyPort,
			User: cfg.MyUser, Password: cfg.MyPass, Database: cfg.MyName})
		if err != nil {
			if cfg.MyProbe {
				return err
			}
			log.Boot("boot", "MySQL 连不上，运行时数据先退回 db.json："+err.Error())
		} else {
			defer my.Close()
			if n, err := my.TableCount(context.Background()); err == nil {
				log.Boot("boot", "MySQL 已接上 "+cfg.MyHost+":"+strconv.Itoa(cfg.MyPort)+" 库 "+cfg.MyName+
					"（"+strconv.Itoa(n)+" 张表；账号/技能/背包/商城都从这儿读写）")
			}
			if cfg.MyProbe {
				acct, err := my.Account(context.Background(), "return2")
				if err != nil {
					return err
				}
				st, err := my.SkillState(context.Background(), "return2")
				if err != nil {
					return err
				}
				log.Boot("boot", "体检通过：return2 等级 "+strconv.Itoa(acct.Level)+" 银币 "+
					strconv.FormatFloat(float64(acct.SilverMinor)/100, 'f', 2, 64)+
					" 发放技能点 "+strconv.Itoa(st.Granted)+" 熟练度 "+strconv.Itoa(len(st.Cats))+
					" 项 已学节点 "+strconv.Itoa(len(st.Nodes)))
				return nil
			}
			run = my

			// 物品静态定义（MySQL static_item_def 整表）：买货、装配都要靠它现拼物件记录，
			// 所以必须在装商城之前读好。个别行属性坏了不拦启动，只报出来。
			itemDefs, err := itemdef.Load(context.Background(), itemdef.FromMySQL(my))
			if err != nil {
				log.Boot("boot", "物品定义表读得磕磕绊绊（能起，但个别货可能拼不出记录）："+err.Error())
			}
			if itemDefs != nil && itemDefs.Count() > 0 {
				iw := itemwire.New(table, itemDefs, itemwire.Options{})
				baitDigSvc = baitdig.New(itemDefs, iw, my)
				craftSvc = craft.New(table, itemDefs, iw, my, my)
				shopSvc = shop.New(table, itemDefs, my)
				buySvc = buy.New(table, itemDefs, iw, my, shopSvc, my)
				containerSvc = container.New(table, my)
				rigSvc = rig.New(table, itemDefs, my).WithLogger(log)

				// 钓鱼域：出鱼靠库里那两张静态表（鱼种体重 + 水域名册），
				// 渔获写 keepnets / fish_records。读不到表就是没鱼可出，不影响别的域。
				cat := fishing.NewCatalog(my)
				if nSp, nLoc, cerr := cat.Reload(context.Background()); cerr != nil {
					log.Boot("boot", "鱼种/水域表读失败，这次钓鱼开不起来（14/x 会打「钓鱼服务没装」）："+cerr.Error())
				} else {
					fishSvc = fishing.New(table, cat, my).WithLogger(log)
					locs := cat.Locs()
					if len(locs) > 12 {
						locs = locs[:12] // 开机日志别刷屏，列前 12 个够挑了
					}
					log.Boot("boot", "钓鱼已装: 14/1 准备 + 14/2 抛出 + 14/7 入水确认 + 14/14 咬钩推送 + 14/4 起鱼 + 14/5 留鱼（"+
						strconv.Itoa(nSp)+" 个鱼种 / "+strconv.Itoa(nLoc)+" 个水域，"+
						"本次水域 "+cfg.FishLoc+" 指定鱼种 "+chooseStr(cfg.FishKey != "", cfg.FishKey, "按名册随机")+"）")
					log.Boot("boot", "库里认识的水域: "+joinComma(locs)+" …（改 --fish-loc 换一片，改库里的名册换鱼）")
				}

				// 类别覆盖情况：没 wire 类的类别发不出去，上架闸门会挡掉，条数写出来一眼能对上
				noClass, noClassItems := 0, 0
				for cat, keys := range itemDefs.Categories() {
					if _, ok := table.WireClassFor(cat); !ok {
						noClass++
						noClassItems += len(keys)
					}
				}
				log.Boot("boot", "物品定义载入: "+strconv.Itoa(itemDefs.Count())+" 件（MySQL static_item_def）；"+
					"其中 "+strconv.Itoa(noClass)+" 个类别共 "+strconv.Itoa(noClassItems)+
					" 件没有 wire 类，商城闸门会把它们挡掉（买了背包装不下）")
				log.Boot("boot", "商城已装: 22/1 货架现拼 + 22/2 购买（物件记录按定义现拼，不用模板）+ 21/1 容器")
				log.Boot("boot", "制作鱼饵已装: 9/1 按 MySQL static_recipe 解析，制作一次固定 30 个并堆入 objects")
			}
		}
	} else {
		log.Boot("boot", "MySQL 已用 --mysql=false 关掉，运行时数据走 db.json（只适合对照，别拿它当真值）")
	}

	dbPath := cfg.DBFile
	if dbPath == "" {
		dbPath, err = assetpaths.File("db.json")
		if err != nil {
			return err
		}
	}
	db, err := store.Load(dbPath)
	if err != nil {
		return err
	}
	log.Boot("boot", "账号定义载入: "+strconv.Itoa(len(db.Users))+" 个账号 文件 "+dbPath+
		"（只读；钱包和技能这些会变的数一律认 MySQL）")

	// ---- HTTP：登录器那条通道（login.php / login_token.php）+ 我们自己加的只读接口 ----
	gw := gateway.New(db, log, "", cfg.TCPPort) // 传空串 = 用 db.json 里配的 127.0.0.1
	// 登录票落不落库，看接没接上 MySQL。落了库，后端重启也不会让客户端本地那张
	// logintoken 作废 —— 今天 22:41 就是栽在这儿：内存票没了，客户端报「请过几分钟再试」。
	if ts, ok := run.(gateway.TokenStore); ok {
		gw.Store = ts
		log.Boot("gateway", "登录票会落 MySQL login_tokens（重启不作废客户端本地票，库里还能直接看直接删）")
	} else {
		log.Boot("gateway", "没接上 MySQL：登录票只记内存，后端一重启客户端就得重新登录")
	}
	// 一个端口两条路：/api/* 走只读接口，其余交给网关按登录器那套前缀处理。
	// 不能用 http.ServeMux：它会把路径里的 // 折叠成一个，实测登录器那条 /lv33/https://api.rf4game.ru/login.php
	// 会被 307 跳到 /lv33/https:/...，登录当场废掉。
	routes := &router{
		api: httpapi.New(db, defs, run, log),
		gw:  gw,
	}
	httpAddr := "0.0.0.0:" + strconv.Itoa(cfg.HTTPPort)
	go func() {
		if err := http.ListenAndServe(httpAddr, routes); err != nil {
			log.Boot("gateway", "HTTP 起不来: "+err.Error()+"（多半是端口被 Node 版占了，两边只能开一个）")
		}
	}()
	log.Boot("gateway", "HTTP 门户+网关 "+httpAddr)
	if cfg.HTTPCompatPort > 0 && cfg.HTTPCompatPort != cfg.HTTPPort {
		compatAddr := "127.0.0.1:" + strconv.Itoa(cfg.HTTPCompatPort)
		go func() {
			if err := http.ListenAndServe(compatAddr, routes); err != nil {
				log.Boot("gateway", "HTTP 兼容入口起不来: "+err.Error())
			}
		}()
		log.Boot("gateway", "HTTP 客户端兼容入口 "+compatAddr+" → 同一网关")
	}
	log.Boot("gateway", "游戏网关路由前缀 "+db.GW.RoutePrefix+" 版本 "+db.GW.GameVersion)
	log.Boot("gateway", "只读接口 http://127.0.0.1:"+strconv.Itoa(cfg.HTTPPort)+"/api/skills?user=return2（浏览器直接开）")

	// ---- 静态定义帧：1/13（地图+天气）、28/1（大区浏览器）这类「整帧都是游戏定义」的应答。
	// 定义 JSON 由 cmd/rf4def 从真服原文解剖出来，放在凭据目录里，改完重启就生效。
	// 少一份定义就当启动失败：默默少一条命令，客户端就会在那条上干等，反而更难查。
	staticLib, err := staticdef.Load(table, map[string]string{
		"1/13": "map_weather_1_13.json",
		"28/1": "region_explorer_28_1.json",
	})
	if err != nil {
		return err
	}
	log.Boot("boot", "静态定义帧载入: "+strings.Join(staticLib.Names(), " ")+
		"（定义 JSON 在凭据目录里，能直接看直接改）")

	mapLib, err := mapworld.Load(table)
	if err != nil {
		return err
	}
	log.Boot("boot", "水域定义载入: "+strconv.Itoa(len(mapLib.Names()))+
		" 片（1/12 按 level_* 现拼 obj205，天气时间动态重锚）")

	// ---- 游戏开机那 9 块配置（1/4 下发本体、1/5 回 sha1 版本号）。
	// 少一块都不给发：客户端开机必须要 1/4，回不到就抛
	// `Game bootstrapping failed: Unimplemented RPC 1:4` 直接断开，
	// 所以这里读失败只是「警告 + 1/5 退回占位串」，不至于让整个后端起不来。
	gameCfg, err := gamecfg.Load()
	if err != nil {
		log.Boot("boot", "游戏配置块载入失败: "+err.Error()+
			" ｜ 客户端开机问 1/4 会拿不到配置（Game bootstrapping failed），1/5 暂时退回按账号现编的占位串")
		gameCfg = nil
	} else {
		log.Boot("boot", "游戏配置块载入: "+strconv.Itoa(gameCfg.Count())+" 块 共 "+
			strconv.Itoa(gameCfg.Bytes())+"B（LZMA 原样下发）｜ 1/4 按类型下发，1/5 回这 "+
			strconv.Itoa(gamecfg.TypeCount)+" 个 sha1 ｜ 来源 "+gameCfg.Source())
	}

	// ---- TCP：游戏通道。这条放在最后，它会一直占住主 goroutine ----
	gs := gamesess.New(gamesess.Deps{DB: db, Table: table, Defs: defs, Run: run, Static: staticLib, GameCfg: gameCfg, MapWorld: mapLib,
		Shop: shopSvc, Buy: buySvc, Container: containerSvc, Rig: rigSvc, Fishing: fishSvc,
		BaitDig: baitDigSvc, Craft: craftSvc, Log: log},
		gamesess.Config{Port: cfg.TCPPort, HB: time.Duration(cfg.HBMs) * time.Millisecond,
			StatePush: time.Duration(cfg.PushMs) * time.Millisecond,
			// 首包那 36 字节是服务器身份 ID；留空就走 identity.go 里的固定值，
			// 填 random 是留给「客户端到底认不认随机 ID」那次对照实验的开关。
			SessionGUID: cfg.SessGUID,
			// 钓鱼参数：水域/鱼种/饵都可在命令行调，咬钩区间随机（真服也不是定值）
			FishLoc: cfg.FishLoc, FishKey: cfg.FishKey, FishBait: cfg.FishBait,
			AutoBite: !cfg.NoBite,
			BiteMin:  time.Duration(cfg.BiteMinMs) * time.Millisecond,
			BiteMax:  time.Duration(cfg.BiteMaxMs) * time.Millisecond})
	log.Boot("boot", "还没搬的命令都会打一行「<< 还没实现 main=.. sub=..」，搬一块少一块")
	return gs.ServeBoot()
}

// chooseStr 条件成立取 a 否则取 b（开机日志里那种「配了就显示配置，没配就显示默认行为」）
func chooseStr(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}

// joinComma 拼一行水域名，日志里好看
func joinComma(items []string) string {
	out := ""
	for i, v := range items {
		if i > 0 {
			out += " "
		}
		out += v
	}
	return out
}

// router 手写的小分流器（原因见 run() 里那段注释：Go 的 ServeMux 会折叠 //，会把登录器那条路跳走）。
// 它只做一件事：看路径前缀决定给谁，一行业务都不碰。
type router struct {
	api http.Handler // /api/ 开头走这里：我们自己加的只读接口
	gw  http.Handler // 其余全交给网关：登录器那条 /lvNN/https://... 的路
}

// ServeHTTP 按前缀分流
func (r *router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if strings.HasPrefix(req.URL.Path, "/api/") {
		r.api.ServeHTTP(w, req)
		return
	}
	r.gw.ServeHTTP(w, req)
}
