// Command rf4mapdefs 从 rpc_table.json 中的 1/12 真服样本生成按水域索引的结构化定义。
// 录制帧只用于离线校准；运行时由 internal/mapworld + wiredef 重新编码 obj205。
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"rf4server/internal/assetpaths"
	"rf4server/internal/mapworld"
	"rf4server/internal/wire"
	"rf4server/internal/wiredef"
)

func main() {
	out := flag.String("out", "", "输出 JSON 路径")
	flag.Parse()
	if *out == "" {
		panic("用法：go run ./cmd/rf4mapdefs -out ..\\rf4_server\\data\\map_places_1_12.json")
	}
	tp, err := assetpaths.File("wire_tables.json")
	must(err)
	table, err := wire.LoadTable(tp)
	must(err)
	rp, err := assetpaths.File("rpc_table.json")
	must(err)
	b, err := os.ReadFile(rp)
	must(err)
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
	must(json.Unmarshal(b, &tab))
	defs := map[string]wiredef.Node{}
	for _, rec := range tab.BySig {
		if rec.Main != 1 || rec.Sub != 12 || len(rec.Responses) == 0 {
			continue
		}
		req, err := base64.StdEncoding.DecodeString(rec.ReqPay)
		must(err)
		level := mapworld.LevelFromRequest(table, req)
		if level == "" {
			continue
		}
		frame, err := base64.StdEncoding.DecodeString(rec.Responses[0].Pay)
		must(err)
		if len(frame) < 10 {
			panic(fmt.Sprintf("%s 应答太短", level))
		}
		v, used, err := table.DecodeValue(frame, 9)
		must(err)
		if used != len(frame) {
			panic(fmt.Sprintf("%s 只解到 %d/%d", level, used, len(frame)))
		}
		def, err := wiredef.FromValue(v)
		must(err)
		back, err := wiredef.Build(table, def)
		must(err)
		if string(back) != string(frame[9:]) {
			panic(fmt.Sprintf("%s 定义回拼不等于真服", level))
		}
		defs[level] = def
	}
	if len(defs) != 18 {
		keys := make([]string, 0, len(defs))
		for k := range defs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		panic(fmt.Sprintf("只抽到 %d 片：%v", len(defs), keys))
	}
	data, err := json.MarshalIndent(struct {
		Source string                  `json:"source"`
		Maps   map[string]wiredef.Node `json:"maps"`
	}{Source: "rpc_table.json 1/12 真服应答解剖；运行时按字段表现现拼", Maps: defs}, "", " ")
	must(err)
	must(os.WriteFile(*out, data, 0o644))
	fmt.Printf("%s：18 片水域定义，%dB\n", *out, len(data))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
