// Package logging 只管一件事：把日志写成 Node 版那个样子，少爷在 IDEA 里看着眼熟。
// 格式：[HH:MM:SS.mmm][类别] 正文，一个类别一个文件：logs/<类别>-YYYY-MM-DD.log。
// 注意：时间戳用的是 UTC，比北京时间早 8 小时（Node 版就这样，两边对照日志时才不会看晕）。
package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Logger 按类别分文件的日志器。所有方法都可以多协程同时调（游戏服和 HTTP 同时写）。
type Logger struct {
	dir    string              // 日志目录，例如 ./logs
	echo   bool                // true = 同时往控制台打一份（IDEA 控制台就是要看这个）
	mu     sync.Mutex          // 保护 files/closed，避免两个连接同时开文件
	files  map[string]*os.File // 类别 -> 已打开的日志文件
	closed bool                // Close 过了就不再写：否则某个后台 goroutine 收工晚了会把文件重新打开，
	// Windows 上那份句柄没关干净，测试删临时目录就会报「正在被另一进程使用」
}

// New 建日志器。dir 不存在会自动建；echo 一般给 true。
func New(dir string, echo bool) (*Logger, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	return &Logger{dir: abs, echo: echo, files: map[string]*os.File{}}, nil
}

// Line 写一行。cat 是类别（game/db/api/gateway/server），msg 是人话正文。
func (l *Logger) Line(cat, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return // 已经收工了：这条就当没听见，别为它把日志文件再打开一次
	}
	// 时间戳：UTC，时:分:秒.毫秒，跟 Node 版一字一样
	stamp := time.Now().UTC().Format("15:04:05.000")
	line := fmt.Sprintf("[%s][%s] %s\n", stamp, cat, strings.TrimRight(msg, "\n"))
	if f, err := l.fileFor(cat); err == nil {
		_, _ = io.WriteString(f, line)
	}
	if l.echo {
		os.Stdout.WriteString(line)
	}
}

// Boot 开机日志，内容和 Line 一样，只是单独留个名字，方便以后想加前缀时只改一处。
func (l *Logger) Boot(cat, msg string) { l.Line(cat, msg) }

// Close 关掉所有打开的文件
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	for _, f := range l.files {
		_ = f.Close()
	}
	l.files = map[string]*os.File{}
}

// fileFor 按类别+日期取文件句柄（跨天自动换文件）
func (l *Logger) fileFor(cat string) (*os.File, error) {
	name := fmt.Sprintf("%s-%s.log", cat, time.Now().UTC().Format("2006-01-02"))
	if f, ok := l.files[name]; ok {
		return f, nil
	}
	f, err := os.OpenFile(filepath.Join(l.dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	l.files[name] = f
	return f, nil
}
