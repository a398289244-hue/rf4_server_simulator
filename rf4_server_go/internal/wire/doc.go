// Package wire 是这个后端的协议底座：只管字节，不管业务。
//
// 里面三层东西：
//
//	Table   —— 真服协议字段表（wire_tables.json）：每个 wire 类有哪些字段、每个字段怎么写(op)
//	Value   —— 「一个值」的抽象：u32/u64/串/字节块/guid/args/对象……每种带自己的 marker
//	Obj     —— 对象记录：01 | typeID(u32) | 按类表顺序排的字段，字段字节由该字段的 op 决定
//
// 字节格式（都是小端）：
//
//	对象      01 | typeID(u32) | 字段1 | 字段2 ...           字段怎么写看类表 ops
//	args      03 | 编码短串("524"/"121"/"122") | 条数(u16) | 各项
//	短串      长度(1字节, 0xff=null) | UTF-8 字节
//	u32       0x0d/0x10/0x13 | 4 字节        u64  0x11/0x12 | 8 字节
//	串        0x14(1字节长度) / 0x15(2字节长度,0xffff=null) / 0x16(4字节长度,0x7fffffff=null)
//	guid      0x0c | 16 字节；0x17 = 空 guid
//	空/缺省   0x00 = null，0xff = elemnull
//
// 依据：data/wire_tables.json（协议逆向表）+ 45 秒真服录制逐字节反推。
// 本包里出现「技能」「物品」这类业务词就算写错了。
package wire
