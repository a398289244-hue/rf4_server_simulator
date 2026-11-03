package gamesess

// 身份 ID 那 36 字节是从哪儿来的、能不能随便改，这条测试说了算：
// 它去抓包凭据 replay_bootstrap.json 的 meta.s2c_uuid_b64 对一遍。
// 谁改了常量、或者哪天换服了要换 ID，这里会先提醒他「凭据里那份不一样了」。

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"rf4server/internal/assetpaths"
)

// TestServerSessionGUIDMatchesCredential 常量必须等于真服当时发的那 36 字节
func TestServerSessionGUIDMatchesCredential(t *testing.T) {
	p, err := assetpaths.File("replay_bootstrap.json")
	if err != nil {
		t.Skipf("没有历史录制，跳过服务身份逐字节对账: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("读不到历史录制，跳过服务身份逐字节对账: %v", err)
	}
	var raw struct {
		Meta struct {
			S2CUUIDB64 string `json:"s2c_uuid_b64"` // 真服开服首包（base64）
		} `json:"meta"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("解析凭据失败: %v", err)
	}
	want, err := base64.StdEncoding.DecodeString(raw.Meta.S2CUUIDB64)
	if err != nil {
		t.Fatalf("凭据里的 s2c_uuid_b64 不是合法 base64: %v", err)
	}
	if len(want) != 36 {
		t.Fatalf("真服首包是 %d 字节，要 36 字节", len(want))
	}
	if string(want) != ServerSessionGUID {
		t.Fatalf("常量 %q 和真服那 36 字节 %q 不一样，客户端会拒", ServerSessionGUID, string(want))
	}

	s := &Session{srv: &Server{}}
	got := s.firstHello()
	if string(got) != ServerSessionGUID {
		t.Fatalf("默认首包发的是 %q，要固定身份 ID", string(got))
	}
	if len(got) != 36 {
		t.Fatalf("首包 %d 字节，要 36 字节", len(got))
	}

	// 对照模式：随机那份长度对、内容不该相同（真客户端会拒，所以只当实验用）
	r := &Session{srv: &Server{Config: Config{SessionGUID: "random"}}}
	if got := r.firstHello(); len(got) != 36 || string(got) == ServerSessionGUID {
		t.Fatalf("random 模式不对劲：%q", string(got))
	}
	// 手填一份就用那份（换服、换实例时不用改代码）
	c := &Session{srv: &Server{Config: Config{SessionGUID: "00000000-0000-4000-8000-000000000000"}}}
	if got := string(c.firstHello()); got != "00000000-0000-4000-8000-000000000000" {
		t.Fatalf("--session-guid 没生效：%q", got)
	}
	t.Logf("身份 ID 和真服首包一致：%s（36 字节）", ServerSessionGUID)
}
