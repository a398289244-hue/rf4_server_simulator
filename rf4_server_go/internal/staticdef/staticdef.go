// Package staticdef 管的是「整帧内容都是静态定义」那几条应答：
//
//	1/13 -> obj206（水域 + 120 条天气计划）
//	28/1 -> obj5011（大区/赛季 浏览器那一坨）
//
// 这些帧里没有一个数字是「这个玩家自己的数据」，全是游戏定义。定义 JSON 由 cmd/rf4def
// 从真服原文解剖出来（机器生成，格式见 internal/wiredef），运行期这边按协议字段表现拼，
// 所以发出去的每个字节都是自己拼的 —— 不是把录制那帧原样吐出去。
//
// 想在游戏里改这些定义：直接编辑 data/ 里那份 JSON 再重启后端就行，不用改代码。
package staticdef

import (
	"fmt"
	"sort"

	"rf4server/internal/assetpaths"
	"rf4server/internal/wire"
	"rf4server/internal/wiredef"
)

// Library 一份「命令 -> 已载入定义」的表。启动时装好，之后只读，所以不用加锁。
type Library struct {
	table *wire.Table
	defs  map[string]wiredef.Node // 键就是 "1/13" 这种命令号
	names []string                // 装了哪些命令，排序后打日志用
}

// Load 把 spec（命令号 -> data/ 里的定义文件名）逐个读进来。
// 少一个文件就报错并说清缺哪份 —— 后端宁可起不来，也别默默少一条命令让客户端干等。
func Load(t *wire.Table, spec map[string]string) (*Library, error) {
	if t == nil {
		return nil, fmt.Errorf("staticdef: 协议字段表还没载入，定义拼不出来")
	}
	lib := &Library{table: t, defs: map[string]wiredef.Node{}}
	for cmd, file := range spec {
		path, err := assetpaths.File(file)
		if err != nil {
			return nil, err
		}
		def, err := wiredef.LoadFile(path)
		if err != nil {
			return nil, err
		}
		lib.defs[cmd] = def
		lib.names = append(lib.names, cmd)
	}
	sort.Strings(lib.names)
	return lib, nil
}

// Has 有没有这条命令的定义（路由那边用它决定要不要接活）
func (l *Library) Has(cmd string) bool {
	if l == nil {
		return false
	}
	_, ok := l.defs[cmd]
	return ok
}

// Names 已经装了哪些命令（开机日志用）
func (l *Library) Names() []string {
	if l == nil {
		return nil
	}
	return l.names
}

// Reply 按命令号现拼一帧应答：9 字节应答头 + 帧体。
// 拼不出（命令没装 / 定义写歪）就把错误带出去，调用方会写成一行人话日志。
func (l *Library) Reply(cmd string, callID uint32) ([]byte, error) {
	if l == nil { // 没装库也别 panic：回个错误，让日志说一句人话
		return nil, fmt.Errorf("staticdef: 这台后端没装静态定义库，%s 拼不出来", cmd)
	}
	def, ok := l.defs[cmd]
	if !ok {
		return nil, fmt.Errorf("staticdef: 没装 %s 的定义", cmd)
	}
	body, err := wiredef.Build(l.table, def)
	if err != nil {
		return nil, fmt.Errorf("staticdef 拼 %s 失败: %w", cmd, err)
	}
	return append(wire.ResponseHead(callID), body...), nil
}
