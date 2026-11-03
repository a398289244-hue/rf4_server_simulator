package staticdef

// 这两条测试是「静态定义帧 == 真服原文」的证据：把 cmd/rf4def 抽出来的定义 JSON 现拼一遍，
// 逐字节和考卷里那条真服应答比。哪天定义或拼帧跑偏了，这里立刻红。
//
//	1/13 -> 4784 字节（水域 + 120 条天气计划）
//	28/1 -> 9825 字节（大区/赛季 浏览器）

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"rf4server/internal/assetpaths"
	"rf4server/internal/wire"
)

// 定义文件清单：命令号 -> data/ 里的 JSON。加了新定义帧就往这儿添一行。
var defFiles = map[string]string{
	"1/13": "map_weather_1_13.json",
	"28/1": "region_explorer_28_1.json",
}

func loadTable(t *testing.T) *wire.Table {
	t.Helper()
	path, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatalf("找不到凭据目录：%v", err)
	}
	table, err := wire.LoadTable(path)
	if err != nil {
		t.Fatalf("读字段表失败：%v", err)
	}
	return table
}

func TestStaticFramesMatchRealFrames(t *testing.T) {
	table := loadTable(t)
	for cmd, file := range defFiles {
		path, err := assetpaths.File(file)
		if err != nil {
			t.Fatalf("%v", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Skipf("没有定义文件 %s（先跑 cmd/rf4def -cmd %s -out %s）", file, cmd, file)
		}
		real, err := assetpaths.RealAnswer(cmd)
		if err != nil {
			t.Fatalf("%v", err)
		}
		lib, err := Load(table, map[string]string{cmd: file})
		if err != nil {
			t.Fatalf("载入 %s 的定义失败：%v", cmd, err)
		}
		callID := binary.LittleEndian.Uint32(real[5:9])
		got, err := lib.Reply(cmd, callID)
		if err != nil {
			t.Fatalf("拼 %s 失败：%v", cmd, err)
		}
		if !bytes.Equal(got, real) {
			i := 0
			for i < len(got) && i < len(real) && got[i] == real[i] {
				i++
			}
			t.Fatalf("%s 现拼结果和真服不一样：我这边 %d 字节，真服 %d 字节，第 %d 字节起不同", cmd, len(got), len(real), i)
		}
		t.Logf("%s 现拼 %d 字节 == 真服原文（定义文件 %s）", cmd, len(got), file)
	}
}

// TestMissingDefSaysSo 没装的定义要当场报错，不能让路由拿着空帧发给客户端
func TestMissingDefSaysSo(t *testing.T) {
	lib, err := Load(loadTable(t), map[string]string{"28/2": "region_explorer_28_1.json"})
	if err != nil {
		t.Skipf("没有定义文件：%v", err)
	}
	if !lib.Has("28/2") || lib.Has("9/9") {
		t.Fatalf("Has 判断不对：28/2=%v 9/9=%v", lib.Has("28/2"), lib.Has("9/9"))
	}
	if _, err := lib.Reply("9/9", 1); err == nil {
		t.Fatalf("没装 9/9 却拼出一帧来了，应该是错误")
	}
}
