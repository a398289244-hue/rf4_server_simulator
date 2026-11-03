// Package assetpaths 只管一件事：找到那几份真服凭据 JSON 在哪。
// 数据不复制进 Go 仓库，永远指向 Node 版那份 data/，两边共用一套，免得答案不一致。
package assetpaths

import (
	"os"
	"path/filepath"
)

// Dir 返回凭据数据目录。
// 找法：环境变量 RF4_DATA_DIR 优先 -> 当前项目 data/ -> 旧版同级目录 rf4_server/data。
func Dir() (string, error) {
	// 环境变量：手动指定时用（比如以后把凭据单独放 D 盘）
	if env := os.Getenv("RF4_DATA_DIR"); env != "" {
		abs, err := filepath.Abs(env)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	// 默认：本仓库在 rf4_server_go，凭据在隔壁 rf4_server/data
	here, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := here; ; {
		// 先找项目自己的 data/，再兼容旧版同级的 rf4_server/data。
		// go test 的工作目录是包目录，所以要逐级向上查。
		for _, cand := range []string{
			filepath.Join(d, "data"),
			filepath.Join(d, "rf4_server", "data"),
		} {
			if _, err := os.Stat(filepath.Join(cand, "wire_tables.json")); err == nil {
				return cand, nil
			}
		}
		up := filepath.Dir(d)
		if up == d {
			break
		}
		d = up
	}
	return "", os.ErrNotExist
}

// File 按名字取某份凭据的完整路径，例如 File("wire_tables.json")。
func File(name string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}
