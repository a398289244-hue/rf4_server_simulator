// Package archguard 不放业务，只放「架构规矩」的测试。
// 目的是把少爷定的两条铁律变成会失败的测试，而不是靠口头保证：
//  1. 运行时代码一行都不许读录制（replay_bootstrap / s2c_uuid / 剧本 tape），录制只能在 _test.go 里当标准答案；
//  2. 只有 internal/gamestore 可以碰数据库驱动和 SQL，别的包只准依赖 gamestore.Runtime 接口。
//
// 这两条以前是靠人记着，记漏一次就出「删了又回来的影子物品」那种事，所以现在交给编译器管。
package archguard

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot 从测试文件所在目录往上找到 go.mod 那一层，保证不管在哪个工作目录跑都对
func skipWalkDir(name string) bool {
	switch name {
	case ".git", ".idea", "data", "logs", "bin", "dist":
		return true
	}
	return strings.HasPrefix(name, "projectFilesBackup")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("拿不到当前目录: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("找不到仓库根，定位不了 go.mod")
	return ""
}

// forbiddenInRuntime 运行时代码里不许出现的记号。
// 注释里提到「录制」是允许的（下面会先把注释剥掉），真正读文件、抄字段的那种才算违规。
var forbiddenInRuntime = []string{
	"replay_bootstrap", // 45 秒那份录制文件
	"s2c_uuid",         // 录制里那条会话 UUID（快照），Go 版必须自己随机生成
	"script.meta",      // Node 版从录制 meta 里抄东西的写法
	"tape",             // 剧本帧序列
	"Tape",
	"Replay",
}

// TestRuntimeCodeNeverReadsRecording 铁律一：运行时不碰录制。
// 录制只许出现在两种地方：_test.go（当逐字节的标准答案）和离线小工具（cmd/ 下的 dump 工具）。
func TestRuntimeCodeNeverReadsRecording(t *testing.T) {
	root := repoRoot(t)
	var bad []string

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipWalkDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		// 测试文件和离线工具允许用录制：测试是考卷，工具是解剖台，都不会发给客户端
		if strings.HasSuffix(rel, "_test.go") || strings.HasPrefix(rel, "cmd/") {
			return nil
		}

		b, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("读 %s 失败: %v", rel, err)
			return nil
		}
		code := stripComments(string(b)) // 注释里讲历史不算违规
		for _, tok := range forbiddenInRuntime {
			if strings.Contains(code, tok) {
				bad = append(bad, rel+" 里出现了 "+tok)
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("遍历源码失败: %v", walkErr)
	}
	if len(bad) > 0 {
		t.Fatal("运行时代码碰录制了（发出去就是快照幽灵）:\n  " + strings.Join(bad, "\n  "))
	}
	t.Logf("铁律一守住了：internal 下所有运行时代码都不读录制")
}

// TestOnlyGamestoreTouchesSQL 铁律二：数据库只准 internal/gamestore 碰。
// 别的包要数据就依赖 Runtime 接口，这样以后换库、加缓存、做双跑都不用改业务。
func TestOnlyGamestoreTouchesSQL(t *testing.T) {
	root := repoRoot(t)
	var bad []string

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipWalkDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/gamestore/") || strings.HasSuffix(rel, "_test.go") || strings.HasPrefix(rel, "cmd/") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("读 %s 失败: %v", rel, err)
			return nil
		}
		code := stripComments(string(b))
		for _, tok := range []string{"database/sql", "go-sql-driver"} {
			if strings.Contains(code, tok) {
				bad = append(bad, rel+" 直接引了 "+tok)
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("遍历源码失败: %v", walkErr)
	}
	if len(bad) > 0 {
		t.Fatal("有包绕过 gamestore 直接连库（数据入口就散了）:\n  " + strings.Join(bad, "\n  "))
	}
	t.Logf("铁律二守住了：只有 internal/gamestore 引数据库驱动")
}

// stripComments 把 // 行注释和 /* */ 块注释剥掉。
// 只做「够用的粗糙处理」：逐行看，遇到不成对引号时宁可当没注释，别把代码切掉。
func stripComments(src string) string {
	src = strings.ReplaceAll(src, "/*", "\n")
	src = strings.ReplaceAll(src, "*/", "\n")
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 && balancedQuotes(line[:i]) {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// balancedQuotes 引号个数是不是偶数（偶数才说明这个 // 不在字符串里）
func balancedQuotes(s string) bool {
	return strings.Count(s, "\"")%2 == 0
}
