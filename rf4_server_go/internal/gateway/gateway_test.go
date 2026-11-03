package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"rf4server/internal/assetpaths"
	"rf4server/internal/logging"
	"rf4server/internal/store"
)

// TestSplitRoute 登录器发的是 /lv33/https://api.rf4game.ru/login.php 这种三段式路径，
// 拆错一步就会把登录判成「认不出的路由」回 400。这里把几种写法一次钉住。
func TestSplitRoute(t *testing.T) {
	cases := []struct {
		full string // 客户端发来的整串路径
		ver  string // 期望认出的版本段
		path string // 期望剩下的真正路由
	}{
		{"/lv33/https://api.rf4game.ru/login.php", "lv33", "/login.php"},
		{"/lv54/https://api.rf4game.ru/login_token.php", "lv54", "/login_token.php"},
		{"/lv33/http://api.rf4game.ru/login.php", "lv33", "/login.php"},
		{"/lv33/https://api.rf4game.ru/", "lv33", "/"},
		{"/login.php", "", "/login.php"},
	}
	for _, c := range cases {
		ver, path := splitRoute(c.full)
		if ver != c.ver || path != c.path {
			t.Errorf("拆错 %q：得到 lv=%q path=%q，想要 lv=%q path=%q", c.full, ver, path, c.ver, c.path)
		}
	}
}

// TestLoginIssuesToken 拿凭据里的真账号走一遍登录：
// 要回 200 + 和真网关一样的 XML（success=true、32 位十六进制钥匙、游戏通道 127.0.0.1:19213），
// 再用这把钥匙走一遍续期。Node 版这条路早就验通了，Go 版必须一模一样。
func TestLoginIssuesToken(t *testing.T) {
	dbp, err := assetpaths.File("db.json")
	if err != nil {
		t.Fatalf("找不到凭据目录: %v", err)
	}
	db, err := store.Load(dbp)
	if err != nil {
		t.Fatalf("读账号库失败: %v", err)
	}
	user, ok := db.FindUser("return2")
	if !ok {
		t.Fatalf("凭据里没有 return2 这个账号")
	}
	log, err := logging.New(filepath.Join(t.TempDir(), "logs"), false)
	if err != nil {
		t.Fatalf("日志起不来: %v", err)
	}
	t.Cleanup(log.Close)

	srv := httptest.NewServer(New(db, log, "", 0)) // 端口传 0 = 用 db.json 里配的游戏端口
	t.Cleanup(srv.Close)

	url := srv.URL + "/lv33/https://api.rf4game.ru/login.php?login=return2&password=" +
		user.PasswordSha1 + "&version=rf4-4.0.25034-lan-v33-required-launcher&id=machine-test"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("登录请求发不出去: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	if resp.StatusCode != 200 {
		t.Fatalf("登录回了 HTTP %d，正文：%s", resp.StatusCode, text[:min(300, len(text))])
	}
	if !strings.Contains(text, `success="true"`) {
		t.Fatalf("登录没成功，正文：%s", text[:min(400, len(text))])
	}
	m := regexp.MustCompile(`[0-9a-f]{32}`).FindString(text)
	if m == "" {
		t.Fatalf("正文里没有 32 位钥匙：%s", text[:min(400, len(text))])
	}
	if !strings.Contains(text, "127.0.0.1") {
		t.Errorf("正文里没写游戏通道地址 127.0.0.1：%s", text[:min(400, len(text))])
	}
	if !strings.Contains(text, "19213") {
		t.Errorf("正文里没写游戏通道端口 19213：%s", text[:min(400, len(text))])
	}
	rec, ok := db.Tokens().Lookup(m)
	if !ok || rec.Username != "return2" {
		t.Fatalf("钥匙没进令牌表，TCP 那边握手就认不出人：%s", m)
	}

	// 续期那条路：拿刚才的 token 换一套 host/port/token，应当照样成功
	up := srv.URL + "/lv33/https://api.rf4game.ru/login_token.php?token=" + m
	resp2, err := http.Get(up)
	if err != nil {
		t.Fatalf("续期请求发不出去: %v", err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body2), `success="true"`) {
		t.Fatalf("续期失败，正文：%s", string(body2)[:min(400, len(string(body2)))])
	}
}

// TestWrongPassword 密码不对要按真网关那样回错误号，不能回成功
func TestWrongPassword(t *testing.T) {
	dbp, err := assetpaths.File("db.json")
	if err != nil {
		t.Fatalf("找不到凭据目录: %v", err)
	}
	db, err := store.Load(dbp)
	if err != nil {
		t.Fatalf("读账号库失败: %v", err)
	}
	log, err := logging.New(filepath.Join(t.TempDir(), "logs"), false)
	if err != nil {
		t.Fatalf("日志起不来: %v", err)
	}
	t.Cleanup(log.Close)
	srv := httptest.NewServer(New(db, log, "", 0))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/lv33/https://api.rf4game.ru/login.php?login=return2&password=deadbeef")
	if err != nil {
		t.Fatalf("请求发不出去: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), `success="true"`) {
		t.Fatalf("密码乱填也判成功了：%s", string(body)[:min(300, len(string(body)))])
	}
}

// min 小助手：截断正文用，省得为这个引一遍包
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
