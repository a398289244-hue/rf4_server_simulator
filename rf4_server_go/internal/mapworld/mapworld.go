// Package mapworld 负责 1/12 game/place：按客户端请求的 level_* 水域，
// 从结构化地图定义现拼 obj205，并把天气日号/小时平移到当前游戏时间。
package mapworld

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"rf4server/internal/assetpaths"
	"rf4server/internal/wire"
	"rf4server/internal/wiredef"
)

const (
	DefaultLevel       = "level_001_lake"
	worldDayEpochUnix  = int64(1438387200) // 2015-08-01 00:00:00 UTC
	realSecPerGameHour = int64(150)        // 150 真实秒 = 1 游戏小时
	forecastClass      = "bleinjfhebp"     // obj204：第 0 格是游戏日号
	hourPlanClass      = "odnlikfnhnl"     // obj201：第 0/1 格是游戏日号/小时
	requestArgOffset   = 11                // 请求壳 9B + main/sub 2B
)

// Library 启动时读入全部水域定义；每次请求重新物化一棵树，避免并发改天气时间。
type Library struct {
	table *wire.Table
	maps  map[string]wiredef.Node
	names []string
}

type fileShape struct {
	Maps map[string]wiredef.Node `json:"maps"`
}

type irField struct {
	F   string          `json:"f"`
	V   json.RawMessage `json:"v"`
	Sub *irNode         `json:"-"`
}

type irNode struct {
	T      string    `json:"t"`
	Class  string    `json:"class,omitempty"`
	TypeID *uint32   `json:"type_id,omitempty"`
	Code   *string   `json:"code,omitempty"`
	Fields []irField `json:"fields,omitempty"`
	Items  []irNode  `json:"items,omitempty"`
}

// Load 从凭据目录读取 map_places_1_12.json。它是从真服 obj205 解剖出的结构化定义，
// 运行时只把字段值交给 wiredef 按当前类表编码，不发送录制帧原文。
func Load(table *wire.Table) (*Library, error) {
	if table == nil {
		return nil, fmt.Errorf("mapworld: 协议字段表没载入")
	}
	path, err := assetpaths.File("map_places_1_12.json")
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读地图定义 %s: %w", path, err)
	}
	var f fileShape
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("解析地图定义 %s: %w", path, err)
	}
	if len(f.Maps) == 0 {
		return nil, fmt.Errorf("地图定义 %s 没有水域", path)
	}
	l := &Library{table: table, maps: f.Maps}
	for name := range f.Maps {
		l.names = append(l.names, name)
	}
	sort.Strings(l.names)
	if _, ok := f.Maps[DefaultLevel]; !ok {
		return nil, fmt.Errorf("地图定义缺默认水域 %s", DefaultLevel)
	}
	return l, nil
}

// Names 返回已载入水域名（只读副本，供开机日志）。
func (l *Library) Names() []string { return append([]string(nil), l.names...) }

// Has 判断某片水域是否有定义。
func (l *Library) Has(level string) bool {
	if l == nil {
		return false
	}
	_, ok := l.maps[level]
	return ok
}

// Reply 按当前时刻现拼完整应答。未知水域回默认出生水域，避免客户端切图时拿空对象。
func (l *Library) Reply(level string, callID uint32, now time.Time) ([]byte, string, error) {
	if l == nil {
		return nil, "", fmt.Errorf("mapworld: 地图库没装")
	}
	if _, ok := l.maps[level]; !ok {
		level = DefaultLevel
	}
	body, err := l.body(level, now.Unix(), true)
	if err != nil {
		return nil, level, err
	}
	return append(wire.ResponseHead(callID), body...), level, nil
}

// body 构造 obj205 帧体。reanchor=false 只给逐字节凭据测试使用。
func (l *Library) body(level string, nowUnix int64, reanchor bool) ([]byte, error) {
	stored, ok := l.maps[level]
	if !ok {
		return nil, fmt.Errorf("地图表缺 %s", level)
	}
	root := cloneDef(stored)
	if reanchor {
		reanchorDefinition(&root, nowUnix)
	}
	return wiredef.Build(l.table, root)
}

func cloneDef(n wiredef.Node) wiredef.Node {
	out := n
	out.Items = make([]wiredef.Node, len(n.Items))
	for i := range n.Items {
		out.Items[i] = cloneDef(n.Items[i])
	}
	out.Fields = make([]wiredef.FieldNode, len(n.Fields))
	for i := range n.Fields {
		out.Fields[i] = n.Fields[i]
		out.Fields[i].Val = cloneDef(n.Fields[i].Val)
	}
	out.Nums = append([]uint32(nil), n.Nums...)
	out.Hexes = append([]string(nil), n.Hexes...)
	return out
}

func walkDef(n *wiredef.Node, fn func(*wiredef.Node)) {
	if n == nil {
		return
	}
	fn(n)
	for i := range n.Fields {
		walkDef(&n.Fields[i].Val, fn)
	}
	for i := range n.Items {
		walkDef(&n.Items[i], fn)
	}
}

func defField(n *wiredef.Node, idx int) *wiredef.Node {
	for i := range n.Fields {
		if n.Fields[i].Idx == idx {
			return &n.Fields[i].Val
		}
	}
	return nil
}

// reanchorDefinition 把真服样本里的天气日号/小时整体平移到当前游戏时间。
// 只改 obj204#0（逐日预报）和 obj201#0/#1（逐小时计划），其余地图定义原样保留。
func reanchorDefinition(root *wiredef.Node, nowUnix int64) {
	secs := nowUnix - worldDayEpochUnix
	day := secs / 3600
	hour := (secs / realSecPerGameHour) % 24

	var forecasts []*wiredef.Node
	walkDef(root, func(n *wiredef.Node) {
		if n.Kind == "obj" && n.TypeID == 204 {
			if f := defField(n, 0); f != nil && f.Kind == "u32" {
				forecasts = append(forecasts, f)
			}
		}
	})
	if len(forecasts) > 0 {
		minDay := uint64(^uint64(0))
		for _, f := range forecasts {
			if f.N < minDay {
				minDay = f.N
			}
		}
		delta := day - 1 - int64(minDay)
		for _, f := range forecasts {
			f.N = uint64(int64(f.N) + delta)
		}
	}

	type hourlyPair struct{ day, hour *wiredef.Node }
	var hourly []hourlyPair
	walkDef(root, func(n *wiredef.Node) {
		if n.Kind != "obj" || n.TypeID != 201 {
			return
		}
		d, h := defField(n, 0), defField(n, 1)
		if d != nil && h != nil && d.Kind == "u32" && h.Kind == "u32" {
			hourly = append(hourly, hourlyPair{d, h})
		}
	})
	if len(hourly) == 0 {
		return
	}
	minAbs := int64(1<<62 - 1)
	abs := make([]int64, len(hourly))
	for i, p := range hourly {
		abs[i] = int64(p.day.N)*24 + int64(p.hour.N)
		if abs[i] < minAbs {
			minAbs = abs[i]
		}
	}
	delta := day*24 + hour - minAbs
	for i, p := range hourly {
		shifted := abs[i] + delta
		p.day.N = uint64(shifted / 24)
		p.hour.N = uint64(shifted % 24)
	}
}

// LevelFromRequest 从 args 里找第一个 level_* 字符串；不认死参数表编码和嵌套层数。
func LevelFromRequest(table *wire.Table, plain []byte) string {
	if table == nil || len(plain) <= requestArgOffset {
		return ""
	}
	for off := requestArgOffset; off < len(plain); {
		v, next, err := table.DecodeValue(plain, off)
		if err != nil || next <= off {
			break
		}
		found := ""
		wire.Walk(v, func(it wire.WalkItem) {
			if found == "" && it.Kind == "str" && strings.HasPrefix(it.S, "level_") {
				found = it.S
			}
		})
		if found != "" {
			return found
		}
		off = next
	}
	return ""
}

func materialize(n *irNode) error {
	if n == nil {
		return nil
	}
	for i := range n.Fields {
		f := &n.Fields[i]
		if f.F != "val" || f.Sub != nil {
			continue
		}
		if string(f.V) == "null" {
			continue
		}
		if err := json.Unmarshal(f.V, &f.Sub); err != nil {
			return fmt.Errorf("地图内嵌值解析: %w", err)
		}
		if err := materialize(f.Sub); err != nil {
			return err
		}
	}
	for i := range n.Items {
		if err := materialize(&n.Items[i]); err != nil {
			return err
		}
	}
	return nil
}

func walkObjects(n *irNode, fn func(*irNode)) {
	if n == nil {
		return
	}
	if n.T == "obj" {
		fn(n)
	}
	for i := range n.Fields {
		walkObjects(n.Fields[i].Sub, fn)
	}
	for i := range n.Items {
		walkObjects(&n.Items[i], fn)
	}
}

func fieldInt(n *irNode, idx int) (int64, bool) {
	if n == nil || idx < 0 || idx >= len(n.Fields) {
		return 0, false
	}
	var v int64
	if err := json.Unmarshal(n.Fields[idx].V, &v); err != nil {
		return 0, false
	}
	return v, true
}

func setFieldInt(f *irField, v int64) { f.V, _ = json.Marshal(v) }

func reanchorWeather(root *irNode, nowUnix int64) {
	secs := nowUnix - worldDayEpochUnix
	day := secs / 3600
	hour := (secs / realSecPerGameHour) % 24

	var forecast []*irField
	walkObjects(root, func(n *irNode) {
		if n.Class == forecastClass && len(n.Fields) > 0 {
			if _, ok := fieldInt(n, 0); ok {
				forecast = append(forecast, &n.Fields[0])
			}
		}
	})
	if len(forecast) > 0 {
		minDay := int64(1<<62 - 1)
		for _, f := range forecast {
			var v int64
			_ = json.Unmarshal(f.V, &v)
			if v < minDay {
				minDay = v
			}
		}
		delta := day - 1 - minDay
		for _, f := range forecast {
			var v int64
			_ = json.Unmarshal(f.V, &v)
			setFieldInt(f, v+delta)
		}
	}

	type hourEntry struct{ day, hour *irField }
	var hourly []hourEntry
	walkObjects(root, func(n *irNode) {
		if n.Class == hourPlanClass && len(n.Fields) >= 2 {
			if _, ok := fieldInt(n, 0); ok {
				if _, ok := fieldInt(n, 1); ok {
					hourly = append(hourly, hourEntry{&n.Fields[0], &n.Fields[1]})
				}
			}
		}
	})
	if len(hourly) == 0 {
		return
	}
	minAbs := int64(1<<62 - 1)
	abs := make([]int64, len(hourly))
	for i, e := range hourly {
		var d, h int64
		_ = json.Unmarshal(e.day.V, &d)
		_ = json.Unmarshal(e.hour.V, &h)
		abs[i] = d*24 + h
		if abs[i] < minAbs {
			minAbs = abs[i]
		}
	}
	delta := day*24 + hour - minAbs
	for i, e := range hourly {
		shifted := abs[i] + delta
		setFieldInt(e.day, shifted/24)
		setFieldInt(e.hour, shifted%24)
	}
}

func toDef(n *irNode) (wiredef.Node, error) {
	if n == nil || n.T == "null" {
		return wiredef.Node{Kind: "null"}, nil
	}
	switch n.T {
	case "obj":
		if n.TypeID == nil {
			return wiredef.Node{}, fmt.Errorf("地图对象 %s 缺 type_id", n.Class)
		}
		out := wiredef.Node{Kind: "obj", TypeID: int(*n.TypeID)}
		for i := range n.Fields {
			v, err := fieldDef(&n.Fields[i])
			if err != nil {
				return out, fmt.Errorf("obj %d 第 %d 格: %w", *n.TypeID, i, err)
			}
			out.Fields = append(out.Fields, wiredef.FieldNode{Idx: i, Val: v})
		}
		return out, nil
	case "args":
		out := wiredef.Node{Kind: "args", CodeNull: n.Code == nil}
		if n.Code != nil {
			out.Code = *n.Code
		}
		for i := range n.Items {
			v, err := toDef(&n.Items[i])
			if err != nil {
				return out, err
			}
			out.Items = append(out.Items, v)
		}
		return out, nil
	default:
		return wiredef.Node{}, fmt.Errorf("地图定义不认识节点 t=%q", n.T)
	}
}

func fieldDef(f *irField) (wiredef.Node, error) {
	switch f.F {
	case "val":
		return toDef(f.Sub)
	case "str":
		var s string
		if err := json.Unmarshal(f.V, &s); err != nil {
			return wiredef.Node{}, err
		}
		return wiredef.Node{Kind: "str", S: s}, nil
	case "u32":
		var n uint32
		if err := json.Unmarshal(f.V, &n); err != nil {
			return wiredef.Node{}, err
		}
		return wiredef.Node{Kind: "u32", N: uint64(n)}, nil
	case "byte", "opt":
		var n uint8
		if err := json.Unmarshal(f.V, &n); err != nil {
			return wiredef.Node{}, err
		}
		return wiredef.Node{Kind: "byte", N: uint64(n)}, nil
	default:
		return wiredef.Node{}, fmt.Errorf("地图字段不认识 f=%q", f.F)
	}
}
