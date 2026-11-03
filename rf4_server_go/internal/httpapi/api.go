// Package httpapi 是本机自己加的只读小接口，不走登录器那套 /lv33/https://... 前缀。
// 目的就一个：少爷开浏览器就能看到「库里到底存了什么」，不用装 Postman。
// 这里只读不写 —— 写数据统一走游戏通道那条路，两个入口都写就会互相盖。
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rf4server/internal/gamestore"
	"rf4server/internal/logging"
	"rf4server/internal/skilldef"
	"rf4server/internal/store"
)

// Handler 把依赖从外面注进来，包自己不 open 文件、不连库，这样单测能塞假数据。
type Handler struct {
	DB   *store.DB         // 账号定义 db.json（只读）
	Defs *skilldef.Defs    // 技能定义表，用来报「这张表一共多少个节点」
	Run  gamestore.Runtime // MySQL 运行时数据；nil = 这台机器 MySQL 没开，退回 db.json
	Log  *logging.Logger
}

// New 建接口集合。Run 允许传 nil。
func New(db *store.DB, defs *skilldef.Defs, run gamestore.Runtime, log *logging.Logger) *Handler {
	return &Handler{DB: db, Defs: defs, Run: run, Log: log}
}

// ServeHTTP 按路径分发。写死几个 case 就够，这层不需要花哨路由。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch strings.ToLower(r.URL.Path) {
	case "/api/skills":
		h.skills(w, r)
	case "/api/account":
		h.account(w, r)
	default:
		h.write(w, http.StatusNotFound, map[string]any{
			"error": "没有这个接口",
			"能用的":   []string{"/api/skills?user=return2", "/api/account?user=return2"},
		})
	}
}

// SkillView 一个账号的技能状态快照。JSON 字段名用英文是为了别乱码，含义全写在注释里。
type SkillView struct {
	User      string             `json:"user"`       // 查的登录名
	Via       string             `json:"via"`        // 这份数据从哪读的：MySQL 还是 db.json
	Found     bool               `json:"found"`      // 账号表里有没有这个人
	Level     int                `json:"level"`      // 角色等级
	Silver    float64            `json:"silver"`     // 银币（已换算成「银币」，方便肉眼看）
	Granted   int                `json:"granted"`    // 发放了多少技能点
	Spent     int                `json:"spent"`      // 已经投出去多少（每级 1 点累加）
	Available int                `json:"available"`  // 还能投多少 = 发放 - 已投
	Mastery   map[string]float64 `json:"mastery"`    // 各大类熟练度百分比，键是大类号
	Nodes     map[string]int     `json:"nodes"`      // 已学节点：键是节点号，值是等级
	NodeTotal int                `json:"node_total"` // 定义表里一共多少个节点（拿它对总数）
	Src       string             `json:"src"`        // 具体文件或库名，方便确认连的是哪一套
}

// skills 处理 GET /api/skills?user=return2。
// 有 MySQL 就认 MySQL（少爷定的唯一真值），没连上才退回 db.json，并把来源写在 via 字段里。
func (h *Handler) skills(w http.ResponseWriter, r *http.Request) {
	name := h.pickUser(r)
	view := SkillView{User: name, Mastery: map[string]float64{}, Nodes: map[string]int{}, Via: "db.json", Src: h.DB.Path}
	if h.Defs != nil {
		view.NodeTotal = h.Defs.Meta.NodeCount
	}

	if h.Run != nil {
		// 走 MySQL：账号那边取等级和银币，技能那边取发放点数 + 熟练度 + 已学节点
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		view.Via, view.Src = "MySQL", "mysql"
		if acct, err := h.Run.Account(ctx, name); err == nil {
			view.Found = true
			view.Level, view.Silver = acct.Level, float64(acct.SilverMinor)/100
		} else {
			h.Log.Line("http", "MySQL 读账号失败 "+name+": "+err.Error())
		}
		if st, err := h.Run.SkillState(ctx, name); err == nil {
			view.Granted = st.Granted
			for cat, pts := range st.Cats {
				view.Mastery[strconv.Itoa(cat)] = pts
			}
			for node, lv := range st.Nodes {
				view.Nodes[strconv.Itoa(node)] = lv
			}
		} else {
			h.Log.Line("http", "MySQL 读技能失败 "+name+": "+err.Error())
		}
	} else {
		if u, ok := h.DB.FindUser(name); ok {
			view.Found, view.Level = true, u.Level
			view.Silver = float64(u.SilverMinor) / 100
		}
		row := h.DB.SkillsFor(name)
		view.Granted = row.PointsGranted
		for k, v := range row.Cats {
			view.Mastery[k] = v
		}
		for k, v := range row.Nodes {
			view.Nodes[k] = v
		}
	}

	view.Spent = sumLevels(view.Nodes)
	view.Available = view.Granted - view.Spent
	if view.Available < 0 {
		view.Available = 0
	}
	h.Log.Line("http", "查技能 user="+name+" 来源 "+view.Via+" 等级 "+strconv.Itoa(view.Level)+
		" 发放 "+strconv.Itoa(view.Granted)+" 可分配 "+strconv.Itoa(view.Available)+
		" 已学节点 "+strconv.Itoa(len(view.Nodes)))
	h.write(w, http.StatusOK, view)
}

// AccountView 账号 + 钱包快照，顺手把名下物品条数带上，方便对「买了到底到手没有」。
type AccountView struct {
	User    string  `json:"user"`    // 登录名
	Nick    string  `json:"nick"`    // 游戏里显示的名字
	UserID  int64   `json:"user_id"` // 数字账号 id
	Via     string  `json:"via"`     // 数据来源
	Level   int     `json:"level"`   // 等级
	Silver  float64 `json:"silver"`  // 银币
	Gold    float64 `json:"gold"`    // 金币
	Stars   int     `json:"stars"`   // 星级
	Objects int     `json:"objects"` // 名下物品条数（MySQL 的 objects 表）
}

// account 处理 GET /api/account?user=return2
func (h *Handler) account(w http.ResponseWriter, r *http.Request) {
	name := h.pickUser(r)
	view := AccountView{User: name}
	if h.Run == nil {
		u, ok := h.DB.FindUser(name)
		if !ok {
			h.write(w, http.StatusNotFound, map[string]any{"error": "账号表里没有 " + name, "via": "db.json"})
			return
		}
		view.Via, view.Nick, view.Level = "db.json", u.Nickname, u.Level
		view.Silver, view.Gold = float64(u.SilverMinor)/100, float64(u.GoldMinor)/100
		view.Stars = u.Stars
		h.write(w, http.StatusOK, view)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	view.Via = "MySQL"
	acct, err := h.Run.Account(ctx, name)
	if err != nil {
		h.write(w, http.StatusNotFound, map[string]any{"error": err.Error(), "via": view.Via})
		return
	}
	view.Nick, view.UserID, view.Level = acct.Nickname, acct.UserID, acct.Level
	view.Silver, view.Gold, view.Stars = float64(acct.SilverMinor)/100, float64(acct.GoldMinor)/100, acct.Stars
	if objs, err := h.Run.Objects(ctx, name); err == nil {
		view.Objects = len(objs)
	}
	h.Log.Line("http", "查账号 user="+name+" 等级 "+strconv.Itoa(view.Level)+
		" 银币 "+strconv.FormatFloat(view.Silver, 'f', 2, 64)+" 名下物品 "+strconv.Itoa(view.Objects)+" 件")
	h.write(w, http.StatusOK, view)
}

// pickUser 没给 user 参数就查第一个账号，省得每次手打
func (h *Handler) pickUser(r *http.Request) string {
	name := r.URL.Query().Get("user")
	if name == "" && len(h.DB.Users) > 0 {
		return h.DB.Users[0].Username
	}
	return name
}

// write 统一吐 JSON：中文不转义，浏览器里看着舒服
func (h *Handler) write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		h.Log.Line("http", "接口写不出去: "+err.Error())
	}
}

// sumLevels 把「每个节点几级」加起来 = 已投入点数（规则来源：每级 1 点）
func sumLevels(nodes map[string]int) int {
	total := 0
	for _, lv := range nodes {
		if lv > 0 {
			total += lv
		}
	}
	return total
}
