package mapworld

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"rf4server/internal/assetpaths"
	"rf4server/internal/wire"
)

func testLibrary(t *testing.T) *Library {
	t.Helper()
	p, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatal(err)
	}
	table, err := wire.LoadTable(p)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := Load(table)
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

// TestMapDefinitionsBuild 验证 18 片水域都能按当前类表现拼成 obj205，
// 防止 map_defs 或 wire_tables 改了一边、另一边没跟着改。
func TestMapDefinitionsBuild(t *testing.T) {
	lib := testLibrary(t)
	if len(lib.Names()) != 18 {
		t.Fatalf("水域数应为 18，实际 %d", len(lib.Names()))
	}
	for _, level := range lib.Names() {
		body, err := lib.body(level, 0, false)
		if err != nil {
			t.Fatalf("%s 拼不出来: %v", level, err)
		}
		if len(body) < 5 || body[0] != 1 || body[1] != 205 || body[2] != 0 || body[3] != 0 || body[4] != 0 {
			t.Fatalf("%s 不是 obj205，前 5 字节=%x", level, body[:min(5, len(body))])
		}
	}
}

// TestMapDefinitionsMatchCapturedAnswers 用第二份独立凭据 rpc_table.json 对答案：
// 请求中的 level_* 要能读出，未经时间重锚的结构化定义必须逐字节等于对应真服应答体。
func TestMapDefinitionsMatchCapturedAnswers(t *testing.T) {
	lib := testLibrary(t)
	p, err := assetpaths.File("rpc_table.json")
	if err != nil {
		t.Skipf("没有历史抓包凭据，跳过地图定义逐字节对账: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("读不到历史抓包凭据，跳过地图定义逐字节对账: %v", err)
	}
	var tab struct {
		BySig map[string]struct {
			Main      int    `json:"main"`
			Sub       int    `json:"sub"`
			ReqPay    string `json:"req_pay"`
			Responses []struct {
				Pay string `json:"pay"`
			} `json:"responses"`
		} `json:"by_sig"`
	}
	if err := json.Unmarshal(b, &tab); err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, rec := range tab.BySig {
		if rec.Main != 1 || rec.Sub != 12 || len(rec.Responses) == 0 {
			continue
		}
		req, err := base64.StdEncoding.DecodeString(rec.ReqPay)
		if err != nil {
			t.Fatal(err)
		}
		level := LevelFromRequest(lib.table, req)
		if !lib.Has(level) {
			continue
		}
		real, err := base64.StdEncoding.DecodeString(rec.Responses[0].Pay)
		if err != nil {
			t.Fatal(err)
		}
		if len(real) < 9 {
			t.Fatalf("%s 真服应答只有 %dB", level, len(real))
		}
		got, err := lib.body(level, 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, real[9:]) {
			t.Fatalf("%s 定义和真服应答不同：现拼 %dB，真服体 %dB", level, len(got), len(real)-9)
		}
		matched++
	}
	if matched < 18 {
		t.Fatalf("只对上 %d 片水域，预期至少 18", matched)
	}
}
