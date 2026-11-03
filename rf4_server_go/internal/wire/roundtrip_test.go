package wire

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"rf4server/internal/assetpaths"
)

// realPay 从 rpc_table_real.json 里取某条命令真服应答的原始字节。
// 这张表是我们 2026-10-31 挂代理抓真服流量后生成的，里面每条命令都存着「真服当时实际回了什么字节」，
// 是这套后端所有逐字节自测的标准答案。
func realPay(t *testing.T, cmd string) []byte {
	t.Helper()
	p, err := assetpaths.File("rpc_table_real.json")
	if err != nil {
		t.Skipf("没有历史抓包凭据，跳过逐字节回环: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("读不到历史抓包凭据，跳过逐字节回环: %v", err)
	}
	var tab struct {
		Fallback map[string]struct {
			Responses []struct {
				Pay string `json:"pay"` // base64
			} `json:"responses"`
		} `json:"fallback"`
	}
	if err := json.Unmarshal(b, &tab); err != nil {
		t.Fatalf("解析 rpc_table_real.json 失败: %v", err)
	}
	rs := tab.Fallback[cmd].Responses
	if len(rs) == 0 {
		t.Fatalf("凭据里没有 %s 这条应答", cmd)
	}
	pay, err := base64.StdEncoding.DecodeString(rs[0].Pay)
	if err != nil {
		t.Fatalf("%s 的 pay 不是 base64: %v", cmd, err)
	}
	return pay
}

// loadTable 读协议字段表，路径由 assetpaths 给
func loadTable(t *testing.T) *Table {
	t.Helper()
	p, err := assetpaths.File("wire_tables.json")
	if err != nil {
		t.Fatalf("找不到凭据目录: %v", err)
	}
	tt, err := LoadTable(p)
	if err != nil {
		t.Fatalf("读字段表失败: %v", err)
	}
	return tt
}

// TestRoundtripTree 是 wire 包的命根子测试：
// 把真服那条 5522B 技能树应答解码成对象，再一字不动地编码回去，必须逐字节相等。
// 这条过了，说明「字段顺序、每个字段的写法、marker 原样保留」全都对；差一个字节都算错。
func TestRoundtripTree(t *testing.T) {
	tt := loadTable(t)
	pay := realPay(t, "5/1")
	if len(pay) != 5522 {
		t.Fatalf("录制里 5/1 应答长度应是 5522B，读到 %dB", len(pay))
	}
	v, off, err := tt.DecodeValue(pay, 9) // 9 = 应答帧头长度：01 | -2 | call_id
	if err != nil {
		t.Fatalf("解码真服 311 失败: %v", err)
	}
	if off != len(pay) {
		t.Errorf("解码只吃到第 %d 字节，应答全长 %d 字节，说明有字段写法解错了或表少了字段", off, len(pay))
	}
	o, ok := v.(*Obj)
	if !ok {
		t.Fatalf("解出来的不是对象，是 %T", v)
	}
	if o.TypeID != 311 {
		t.Errorf("typeID 应为 311，实际 %d", o.TypeID)
	}
	if o.Unknown {
		t.Errorf("311 在字段表里认不出来（类名 %q），逐字节复现无从谈起", o.Class)
	}
	back, err := o.Encode()
	if err != nil {
		t.Fatalf("再编码失败: %v", err)
	}
	if !bytes.Equal(back, pay[9:]) {
		i := 0
		for i < len(back) && i < len(pay[9:]) && back[i] == pay[9:][i] {
			i++
		}
		t.Fatalf("逐字节不等：第 %d 字节开始不同，我=%x 真服=%x", i, safeSlice(back, i, 12), safeSlice(pay[9:], i, 12))
	}
}

// TestRoundtripFrames 把录制里几类代表帧都往返一遍：
// 档案推送(2/1)、背包推送(4/22)、购买应答(1/32 obj800)、快捷栏应答(1/147 obj5011)。
// 这些帧覆盖的写法比技能树多（guid、字节块、长串、字典），能提前发现哪个 op 搬错了。
func TestRoundtripFrames(t *testing.T) {
	tt := loadTable(t)
	cases := []struct {
		name string // 用例名，失败时看得懂在测哪条
		cmd  string // 凭据表里的命令号
		off  int    // payload 里值从第几个字节开始（应答 9，推送 11 + args 外壳）
	}{
		{"5/1 技能树应答", "5/1", 9},
		{"4/5 背包清单应答", "4/5", 9},
		{"4/4 使用物品应答", "4/4", 9},
		{"17/2 装配应答", "17/2", 9},
		{"11/1 快捷栏列表应答", "11/1", 9},
		{"1/14 世界信息应答", "1/14", 9},
		{"28/1 社交应答", "28/1", 9},
		{"3/1 进入世界应答", "3/1", 9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pay := realPay(t, c.cmd)
			v, off, err := tt.DecodeValue(pay, c.off)
			if err != nil {
				t.Fatalf("解码失败: %v", err)
			}
			if off != len(pay) {
				t.Errorf("只解到第 %d 字节，全长 %d 字节：多半是某个字段写法不对", off, len(pay))
			}
			back, err := v.Encode()
			if err != nil {
				t.Fatalf("编码失败: %v", err)
			}
			if !bytes.Equal(back, pay[c.off:]) {
				i := 0
				for i < len(back) && i < len(pay[c.off:]) && back[i] == pay[c.off:][i] {
					i++
				}
				t.Fatalf("第 %d 字节起不同：我=%x 真服=%x", i, safeSlice(back, i, 16), safeSlice(pay[c.off:], i, 16))
			}
		})
	}
}

// safeSlice 截一段字节用来打错误信息，越界不 panic
func safeSlice(b []byte, from, n int) []byte {
	if from > len(b) {
		return nil
	}
	end := from + n
	if end > len(b) {
		end = len(b)
	}
	return b[from:end]
}
