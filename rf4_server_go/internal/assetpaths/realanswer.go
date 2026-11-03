package assetpaths

// 这份文件只提供一件事：把「真服某条命令的应答原文」取出来当考卷。
// 它是凭据（2026-10-31 抓真服包统计出来的 data/rpc_table_real.json），不是录制剧本，
// 所以不违反「运行期一帧录制都不发」那条规矩 —— 而且只有 _test.go 和 cmd/ 工具会调它。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
)

// RealAnswer 取真服对某条命令（形如 "4/4"）的应答原始字节。
// 取不到就返回错误，让调用方（测试）当场说清楚是哪条命令没样本，别默默跳过。
func RealAnswer(cmd string) ([]byte, error) {
	path, err := File("rpc_table_real.json")
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读考卷 %s 失败: %w", path, err)
	}
	var tab struct {
		Fallback map[string]struct {
			Responses []struct {
				Pay  string `json:"pay"`  // base64：完整应答帧（含 9 字节帧头）
				Name string `json:"name"` // 类名（凭据里有就带着，没有就算了）
			} `json:"responses"`
		} `json:"fallback"`
	}
	if err := json.Unmarshal(b, &tab); err != nil {
		return nil, fmt.Errorf("解析考卷失败: %w", err)
	}
	rs := tab.Fallback[cmd].Responses
	if len(rs) == 0 {
		return nil, fmt.Errorf("考卷里没有 %s 的应答样本", cmd)
	}
	pay, err := base64.StdEncoding.DecodeString(rs[0].Pay)
	if err != nil {
		return nil, fmt.Errorf("%s 的应答不是合法 base64: %w", cmd, err)
	}
	return pay, nil
}

// RealAnswerCommands 列出考卷里都有哪些命令，给人查进度用
func RealAnswerCommands() ([]string, error) {
	path, err := File("rpc_table_real.json")
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读考卷 %s 失败: %w", path, err)
	}
	var tab struct {
		Fallback map[string]json.RawMessage `json:"fallback"`
	}
	if err := json.Unmarshal(b, &tab); err != nil {
		return nil, fmt.Errorf("解析考卷失败: %w", err)
	}
	out := make([]string, 0, len(tab.Fallback))
	for k := range tab.Fallback {
		out = append(out, k)
	}
	return out, nil
}
