package gamesess

// 这条测试模拟后端重启后的真实情况：客户端本地还拿着旧 token，内存令牌表是空的，
// 但 MySQL login_tokens 里那张票仍然有效。握手必须从数据库认回账号并发出 2/1 玩家状态，
// 否则实机就会一直停在「载入个人资料」。

import (
	"context"
	"testing"
	"time"

	"rf4server/internal/gamestore"
)

func TestHandshakeFindsTokenInMySQLAfterRestart(t *testing.T) {
	deps, db := testDeps(t)
	my, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没起来，跳过登录票实库测试: %v", err)
	}
	t.Cleanup(func() { _ = my.Close() })
	deps.Run = my

	const token = "cafebabecafebabecafebabecafebabe"
	const loginToken = "1234567890abcdef1234567890abcdef1234567890abcdef"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := my.SaveToken(ctx, gamestore.LoginTicket{
		Token: token, LoginToken: loginToken, Username: "return2", UserID: 1000002,
		Expires: time.Now().Unix() + 3600, CreatedAt: time.Now().Unix(), IP: "127.0.0.1",
	}); err != nil {
		t.Fatalf("写 MySQL 登录票失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = my.DB().ExecContext(context.Background(), `DELETE FROM login_tokens WHERE token = ?`, token)
	})
	if _, found := db.Tokens().Lookup(token); found {
		t.Fatal("测试前置条件不对：进程内令牌表不应有这张票")
	}

	srv := New(deps, Config{Addr: "127.0.0.1:0", StatePush: -1})
	if err := srv.Listen(); err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() { _ = srv.Close() })

	p := dialAndHandshake(t, srv, token)
	if _, main, sub := p.nextFrame("进场推送"); main != 0 || sub != 1 {
		t.Fatalf("第一条进场推送应是 0/1，实际 %d/%d", main, sub)
	}
	if _, main, sub := p.nextFrame("玩家状态"); main != 2 || sub != 1 {
		t.Fatalf("MySQL 已认到账号却没发 2/1 玩家状态，实际 %d/%d", main, sub)
	}
	t.Log("后端重启场景通过：进程内无票，握手从 MySQL login_tokens 认回 return2，并发出 2/1")
}
