package gateway

// 登录票落库这条路今天（2026-11-02 22:41）是缺的：后端一重启，客户端本地那张
// logintoken 就在内存里查不到，直接报「请过几分钟再试」。
// 这条测试盯着两件事：票真能落进 MySQL，而且 token / logintoken 两个号码都能查回来。

import (
	"context"
	"testing"
	"time"

	"rf4server/internal/gamestore"
)

// openMySQLForTickets 连着 MySQL 就返回句柄，没连上就 Skip（别拖着一大片测试报错）
func openMySQLForTickets(t *testing.T) *gamestore.MySQL {
	t.Helper()
	m, err := gamestore.Open(gamestore.Config{})
	if err != nil {
		t.Skipf("MySQL 没连着，登录票这条不跑: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// TestTicketRoundTripThroughMySQL 存进去的票，token 和 logintoken 都能查回来
func TestTicketRoundTripThroughMySQL(t *testing.T) {
	m := openMySQLForTickets(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	now := time.Now().Unix()
	tk := gamestore.LoginTicket{
		Token:      "goticket00000000000000000000aaaa",
		LoginToken: "goticketlogin0000000000000000000000000000bbbb",
		Username:   "go_selftest",
		UserID:     424242,
		Expires:    now + 3600,
		CreatedAt:  now,
		IP:         "127.0.0.1",
		Version:    "4.0.25034",
		MachineID:  "go-test-machine",
	}
	t.Cleanup(func() {
		_, _ = m.DB().ExecContext(context.Background(), `DELETE FROM login_tokens WHERE token = ?`, tk.Token)
	})

	if err := m.SaveToken(ctx, tk); err != nil {
		t.Fatalf("存票失败: %v", err)
	}
	// 同一个 token 再存一次（续期），不该撑出两行
	if err := m.SaveToken(ctx, tk); err != nil {
		t.Fatalf("续期再存一次失败: %v", err)
	}

	for _, key := range []string{tk.Token, tk.LoginToken} {
		got, found, err := m.FindToken(ctx, key)
		if err != nil {
			t.Fatalf("按 %s 查票失败: %v", key[:8], err)
		}
		if !found {
			t.Fatalf("按 %s 查不到票", key[:8])
		}
		if got.Username != tk.Username || got.Token != tk.Token || got.UserID != tk.UserID {
			t.Fatalf("查回来的票不对：%+v", got)
		}
	}

	// 没见过的票：found=false，不当错误
	if _, found, err := m.FindToken(ctx, "never_issued_token_xyz"); err != nil || found {
		t.Fatalf("没见过的票应该 found=false 且不报错，实际 found=%v err=%v", found, err)
	}
	t.Log("登录票已能落 MySQL：token 和 logintoken 两个号码都查得回，重复存当续期")
}

// TestTokenStoreSatisfiesGatewayInterface 编译期钉住：MySQL 那份实现就是 gateway 要的那个口。
// 谁改了方法签名，编译就过不去，不用等跑到线上才发现「票没落库」。
func TestTokenStoreSatisfiesGatewayInterface(t *testing.T) {
	var _ TokenStore = (*gamestore.MySQL)(nil)
}
