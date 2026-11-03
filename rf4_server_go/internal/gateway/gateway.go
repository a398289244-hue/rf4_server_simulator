// Package gateway 复刻真网关那条通道：登录器发 GET /lv33/https://api.rf4game.ru/login.php?... ，
// 我们回一份和真网关逐字段一样的 XML，里面写着「游戏通道连 127.0.0.1:19213 + 一次性的 32 位钥匙」。
// 抓包实测（2026-10-30 22:05:55，Content-Length: 328）就是 loginSuccessBody 那个模板的样子。
package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"rf4server/internal/gamestore"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

// Server 网关。无状态，可以多个连接同时进来。
type Server struct {
	DB    *store.DB       // 账号表和网关配置（只读）
	Log   *logging.Logger // 日志，类别用 gateway
	Host  string          // 回给客户端的游戏服地址；空则用 db.GW.LocalHost，再空用 127.0.0.1
	Port  int             // 回给客户端的游戏服端口；0 则用 db.GW.GamePort
	Store TokenStore      // 登录票持久化（MySQL login_tokens）；nil = 只记内存，后端一重启旧票就作废
}

// New 建网关
// TokenStore 是登录票的持久化口（MySQL 的 login_tokens 表）。
// gateway 只说「我要存票、我要查票」，具体谁来实现它不管：没接上库就是 nil，票只记内存。
type TokenStore interface {
	// SaveToken 落一张新票（同一个 token 重复写就当续期）
	SaveToken(ctx context.Context, t gamestore.LoginTicket) error
	// FindToken 按 token 或 logintoken 查票；没查到不算错
	FindToken(ctx context.Context, key string) (gamestore.LoginTicket, bool, error)
}

func New(db *store.DB, log *logging.Logger, host string, port int) *Server {
	return &Server{DB: db, Log: log, Host: host, Port: port}
}

// lvPrefix 匹配路由前缀 /lv33/ 这种（数字会变，v33 是现在这套客户端的版本）
var lvPrefix = regexp.MustCompile(`^/(lv\d+)(/.*)$`)

// upstreamPrefix 匹配 /https://api.rf4game.ru/login.php 这种「上游绝对地址」写法
var upstreamPrefix = regexp.MustCompile(`^https?://[^/]+(/.*)?$`)

// splitRoute 把 /lv33/https://api.rf4game.ru/login.php 拆成 (版本, 真正的路径)
func splitRoute(full string) (version, path string) {
	path = full
	if m := lvPrefix.FindStringSubmatch(path); m != nil {
		version, path = m[1], m[2]
	}
	// 剥掉版本段之后剩下的是 "/https://api.rf4game.ru/login.php"，最前面那个斜杠得先去掉，
	// 否则下面那条「绝对地址」正则匹配不上 —— 登录器实测就是 /lv33/https://... 这么发的，
	// 少这一步就会把所有登录请求判成「认不出的路由」回 400（2026-11-02 首次跑 Go 版就栽在这儿）。
	if strings.HasPrefix(path, "/http://") || strings.HasPrefix(path, "/https://") {
		path = path[1:]
	}
	if m := upstreamPrefix.FindStringSubmatch(path); m != nil {
		path = m[1]
		if path == "" {
			path = "/"
		}
	}
	return version, path
}

// ServeHTTP 路由分发：登录、令牌续期、其它一律按「认不出的路由」回
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ver, path := splitRoute(r.URL.Path)
	q := r.URL.Query()
	s.Log.Line("gateway", "[请求] "+r.Method+" "+r.URL.Path+" lv="+ver+" login="+q.Get("login")+" ip="+clientIP(r))
	switch strings.ToLower(path) {
	case "/login.php":
		s.login(w, r)
	case "/login_token.php":
		s.loginToken(w, r)
	default:
		s.unknown(w, r)
	}
}

// login 处理 /login.php：验账号 + 验密码（sha1）+ 发钥匙 + 回成功 XML
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("login")
	sha1Hex := strings.ToLower(strings.TrimSpace(q.Get("password"))) // 客户端传来的就是密码的 sha1，不是明文
	user, ok := s.DB.FindUser(name)
	if !ok {
		s.errXML(w, r, "noAccount", "")
		s.Log.Line("gateway", "登录失败：账号不存在 login="+name)
		return
	}
	if !strings.EqualFold(user.PasswordSha1, sha1Hex) {
		// 算一遍给日志看：客户端传来的 sha1 对不对得上库里那份（不对就打印两边，方便对密码）
		sum := sha1.Sum([]byte(q.Get("password")))
		s.errXML(w, r, "badPassword", "")
		s.Log.Line("gateway", "登录失败：密码不对 login="+name+" 客户端 sha1="+sha1Hex+" 库里="+user.PasswordSha1+"（明文再算一次="+hex.EncodeToString(sum[:])+"）")
		return
	}
	rec := s.issueToken(r, user, q.Get("version"), q.Get("id"))
	s.okXML(w, r, s.DB.GW.LoginSuccessBody, rec)
	s.Log.Line("gateway", "登录成功 login="+user.Username+" userid="+strconv.Itoa(user.UserID)+" token="+rec.Token+" lv="+q.Get("version"))
}

// loginToken 处理 /login_token.php：拿旧令牌换一套 host/port/token（相当于续期）
// loginToken 处理 /login_token.php：拿旧票换一张新 host/port/token（相当于续期）。
// 客户端会把 logintoken 存在本地，所以这条路必须扛得住「后端重启过」——
// 内存表是空的，但 MySQL login_tokens 里还在，捞回来接着玩。
func (s *Server) loginToken(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("token")
	rec, ok := s.DB.Tokens().Lookup(key)
	if !ok {
		if fromDB, found := s.tokenFromStore(r, key); found {
			s.DB.Tokens().Save(fromDB) // 顺手塞回内存，登录后 TCP 那一关才接得上
			s.okXML(w, r, s.DB.GW.LoginSuccessBody, fromDB)
			s.Log.Line("gateway", "令牌续期成功（内存没有，从 MySQL login_tokens 捞回） login="+fromDB.Username+
				" token="+fromDB.Token)
			return
		}
		// 两条路都没有：那就当新设备第一次来，让登录页走正常登录
		s.errXML(w, r, "noAccount", "")
		s.Log.Line("gateway", "令牌续期失败：内存和 MySQL 都没有这个 token（真没发过、或已过期）token="+key)
		return
	}
	s.okXML(w, r, s.DB.GW.LoginSuccessBody, rec)
	s.Log.Line("gateway", "令牌续期成功 login="+rec.Username+" token="+rec.Token)
}

// tokenFromStore 是续期的第二条路：去 MySQL login_tokens 查。过期一律当没有。
func (s *Server) tokenFromStore(r *http.Request, key string) (store.Token, bool) {
	if s.Store == nil || key == "" {
		return store.Token{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	t, found, err := s.Store.FindToken(ctx, key)
	if err != nil {
		s.Log.Line("gateway", "查 MySQL 登录票失败: "+err.Error())
		return store.Token{}, false
	}
	if !found {
		return store.Token{}, false
	}
	if t.Expires > 0 && t.Expires < time.Now().Unix() {
		s.Log.Line("gateway", "票在 MySQL 里但已经过期，不认 token="+key)
		return store.Token{}, false
	}
	return store.Token{
		Token:      t.Token,
		LoginToken: t.LoginToken,
		Expires:    t.Expires,
		Username:   t.Username,
		UserID:     int(t.UserID),
		ClientIP:   t.IP,
		CreatedAt:  t.CreatedAt * 1000, // 库里存秒，内存里存毫秒，别混
	}, true
}

// issueToken 生成一把新钥匙并记账（钥匙 = 16 字节随机的 32 位十六进制，和实测一致）
// issueToken 发一把新钥匙并记下来（钥匙 = 16 字节随机的 32 位十六进制，和真服一致）。
// version / machineID 是客户端自己报的，顺手存进库里，以后查「谁在哪台机器登的」有用。
func (s *Server) issueToken(r *http.Request, u store.User, version, machineID string) store.Token {
	rec := store.Token{
		Token:      randHex(16),                  // 游戏通道钥匙：32 位十六进制字符
		LoginToken: randHex(24),                  // 网页/客户端长票：48 位十六进制字符
		Expires:    time.Now().Unix() + 31536000, // 实测 = 当前时间 + 365 天
		Username:   u.Username,
		UserID:     u.UserID,
		ClientIP:   clientIP(r),
		CreatedAt:  time.Now().UnixMilli(),
	}
	s.DB.Tokens().Save(rec)
	s.persistToken(r, rec, version, machineID)
	return rec
}

// persistToken 把刚签的票也写进 MySQL（login_tokens）：重启不丢、少爷在库里能直接看直接删。
// 写不进去不算登录失败 —— 内存里有票，这场游戏照玩；只在日志里说清后果。
func (s *Server) persistToken(r *http.Request, rec store.Token, version, machineID string) {
	if s.Store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	err := s.Store.SaveToken(ctx, gamestore.LoginTicket{
		Token:      rec.Token,
		LoginToken: rec.LoginToken,
		Username:   rec.Username,
		UserID:     int64(rec.UserID),
		Expires:    rec.Expires,
		CreatedAt:  rec.CreatedAt / 1000, // 库里存秒
		IP:         rec.ClientIP,
		Version:    version,
		MachineID:  machineID,
	})
	if err != nil {
		s.Log.Line("gateway", "登录票没落进 MySQL（登录照样成，只是后端重启后这张票会作废）: "+err.Error())
	}
}

// gameHost 回给客户端的游戏服地址：本机测试必须是 127.0.0.1，客户端才会连到我们
func (s *Server) gameHost() string {
	if s.Host != "" {
		return s.Host
	}
	if s.DB.GW.LocalHost != "" {
		return s.DB.GW.LocalHost
	}
	return "127.0.0.1"
}

// gamePort 回给客户端的游戏服端口
func (s *Server) gamePort() int {
	if s.Port > 0 {
		return s.Port
	}
	if s.DB.GW.GamePort > 0 {
		return s.DB.GW.GamePort
	}
	return 19213
}

// okXML 按登录成功模板填字段。模板里有几个占位：token/host/port/region/userid/logintoken/logintokenexpires
func (s *Server) okXML(w http.ResponseWriter, r *http.Request, tpl string, rec store.Token) {
	if tpl == "" {
		// db.json 里没配模板时，用实测那份兜底（字节级：声明后带一个换行）
		tpl = `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
			`<response success="true"><token>{{token}}</token><host>{{host}}</host><port>{{port}}</port>` +
			`<region>{{region}}</region><userid>{{userid}}</userid><logintoken>{{logintoken}}</logintoken>` +
			`<logintokenexpires>{{logintokenexpires}}</logintokenexpires></response>`
	}
	fields := map[string]string{
		"token":             rec.Token,
		"host":              s.gameHost(),
		"port":              strconv.Itoa(s.gamePort()),
		"region":            regionOf(s.DB),
		"userid":            strconv.Itoa(rec.UserID),
		"logintoken":        rec.LoginToken,
		"logintokenexpires": strconv.FormatInt(rec.Expires, 10),
		"login":             rec.Username,
	}
	body := fillTemplate(tpl, fields)
	writeXML(w, 200, body)
	s.Log.Line("gateway", "[响应] "+r.URL.Path+" -> "+strconv.Itoa(len(body))+" 字节 成功")
}

// errXML 回一份失败 XML，错误号从 db.json 的 gateway.errors 表里取
func (s *Server) errXML(w http.ResponseWriter, r *http.Request, key string, extra string) {
	def, ok := s.DB.GW.Errors[key]
	if !ok {
		def = store.ErrDef{Code: 9, Description: "Unknown error"}
	}
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n") // 实测：声明后面有一个换行
	b.WriteString("<response success=\"false\"><error>" + strconv.Itoa(def.Code) + "</error>")
	b.WriteString("<description>" + escapeXML(def.Description) + "</description>")
	b.WriteString("<region>" + escapeXML(regionOf(s.DB)) + "</region>")
	if extra != "" {
		b.WriteString("<block_reason>" + escapeXML(extra) + "</block_reason>")
	}
	b.WriteString("</response>")
	writeXML(w, 200, b.String())
}

// unknown 认不出的路由：回 db.json 配的 unknownRouteBody（登录器前端会发一些别的接口，别让它报错）
func (s *Server) unknown(w http.ResponseWriter, r *http.Request) {
	body := s.DB.GW.UnknownRouteBody
	if body == "" {
		body = `{"error":"Invalid local request route"}`
	}
	status := s.DB.GW.UnknownRouteStat
	if status == 0 {
		status = 400
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
	s.Log.Line("gateway", "[响应] 认不出的路由 "+r.URL.Path+" -> HTTP "+strconv.Itoa(status))
}

// fillTemplate 把 {{名字}} 换成值；模板里出现但表里没有的占位原样留着，方便发现漏了哪个字段
func fillTemplate(tpl string, fields map[string]string) string {
	out := tpl
	for k, v := range fields {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}

// writeXML 按真网关的头回 XML（实测头里有 no-store / close / text/xml）
func writeXML(w http.ResponseWriter, status int, body string) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "close")
	h.Set("Content-Type", "text/xml; charset=utf-8")
	h.Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// regionOf 取分区，没配就用实测的 CN
func regionOf(db *store.DB) string {
	if db.GW.Region != "" {
		return db.GW.Region
	}
	return "CN"
}

// clientIP 客户端地址；带 X-Forwarded-For 时优先用它
func clientIP(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		return strings.TrimSpace(strings.Split(xf, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// escapeXML XML 转义，防止说明文字里有 & < 之类
func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// randHex 生成 n 字节随机数的十六进制串（长度 2n）
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// 随机源出问题就退回时间戳，至少不会一样
		return hex.EncodeToString([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
	}
	return hex.EncodeToString(b)
}

// 编译期确保 url 包没白引（以后加转发会用）
