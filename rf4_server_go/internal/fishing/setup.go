package fishing

import (
	"rf4server/internal/wire"
)

// 14/14「有鱼咬钩」推送：这一帧就是少爷之前反映「日志说派发了但游戏里没出鱼」的那一帧，
// 所以每个数字都得能说清是从哪来的。
//
// 帧形状（凭据：真服那条 317B，解剖全文 D:\rf4log\dump_14_14.txt）：
//
//	推送头(11B) + 参数表"524"{ 竿GUID, obj27 } + 尾巴 01
//	obj27（fgogccpglhb，25 个字段）= 这条鱼的全部事实 + 搏鱼参数
//	obj27#21 = 参数表"27"{ obj26, obj26 } = 这个鱼种的搏斗行为组（两个组：105 和 301）
//
// 规矩：#00 这个 GUID 是我们生成的，客户端在 14/11（搏鱼过程上报）和 14/5（留鱼）里
// 原样回显，鱼卡片 obj25#16 也填它 —— 三处对不上号，界面就认不出是同一条鱼。

// 物件类型号（混淆类名注释一份，省得下一个人去猜）
const (
	// TypeFishSetup 咬钩事实块（fgogccpglhb），14/14 推送的主体
	TypeFishSetup = 27
	// TypeFishEntry 搏斗行为组条目（oiikpeaajln），塞在 obj27#21 那个参数表里
	TypeFishEntry = 26
	// TypeFishItem 鱼卡片/鱼护里那条鱼（ecknbkpeeji），14/5 应答的主体
	TypeFishItem = 25
)

// Main/Sub 这条推送的命令号（写成常量免得路由和拼帧两边各写一遍字面量）
const (
	MainFishing byte = 14
	SubBite     byte = 14
)

// 搏鱼参数：真服那条 14/14（ruffe_n、53 克）的原始位模式（float32 塞在 u32 里）。
// 逐条抄自 data/fish_setup_push_14_14.hex 的解剖结果（D:\rf4log\dump_14_14_new.txt），
// 没一个是拍的；这一帧和 14/5 那条鱼卡片是同一次会话里的，两条能互相对答案。
// 这些数只管「鱼在水里怎么挣扎」，跟账号数据无关，所以先整片照抄，
// 哪天要按鱼种调，只改这一处。
const (
	// setupStaminaPerGram 体力池兜底比例：真服两条样本换算下来是 5.80/克 和 2.39/克，
	// 说明这格本来按鱼种给（含义待查），这边拿 53 克那条的比例当默认值。
	setupStaminaPerGram float64 = 2.3880400747623083
	// setupForcePerGram 拉力比例：两条真服样本都对得上（63 克 -> 0.61781895、53 克 -> 0.51975244），
	// 字段表注释也写了「本地公式 weight*k*2f，正比于体重」，所以这格按体重线性发是站得住的。
	setupForcePerGram float64 = 0.009806650025503976

	setup06 uint32 = 0x3f1014e4 // 初始体力比率 0.5628187656402588（字段表：Clamp01）
	setup09 uint32 = 0x3e99999a // 搏鱼参数 B 0.3
	setup10 uint32 = 0x3eeed307 // 搏鱼参数 C 0.46645376086235046
	setup11 uint32 = 0x3eaff370 // 搏鱼曲线参数 0.34365415573120117
	setup12 uint32 = 0x00000000 // 搏鱼曲线参数（真服就是 0）
	setup13 uint32 = 0x3ecccccd // 搏鱼曲线参数 0.4
	setup14 uint32 = 0x3f800000 // 搏斗强度 1.0
	setup15 uint32 = 0x3e99999a // 搏鱼曲线参数 0.3
	setup16 uint32 = 0x3f800000 // 搏鱼曲线参数 1.0
	setup17 uint32 = 0x51200000 // 真服这个数换算成浮点是 4.29e10，含义待查，照抄位模式
	setup18 uint32 = 0x3f800000 // 阶段化曲线那组参数的头一个
	setup19 uint32 = 0x3f800000 // 保留字段
	setup20 uint32 = 0x41a00000 // 20.0（字段表：典型范围 [0.997804, 0.998654]）
)

// fightEntry 一条搏斗行为组（obj26，19 个字段）。
// group 是行为组号，nums 是后面 18 个 float 的位模式（顺序就是字段 #01..#18）。
type fightEntry struct {
	group uint16
	nums  [18]uint32
}

// fightEntries 真服那条推送里的两个搏斗行为组（105 和 301），逐字节照抄。
// 301 那组最后两个是整数 4500 / 5000（不是浮点），照抄。
// 另一条早期抓包（data/fish_templates.json 里那条 317B）里这两个组的中间几个数不一样，
// 说明这两组也是按鱼种给的：现在所有鱼先共用 ruffe_n 这一组，等解剖多了改成按鱼种查表。
var fightEntries = []fightEntry{
	{group: 105, nums: [18]uint32{
		0x3f800000, 0x3e4ccccd, 0x3ecccccd, 0x3e83f694,
		0x3edbf04c, 0x3f400000, 0x3fa00000, 0x3e050e7f,
		0x3ec795be, 0xbf000000, 0x3f000000, 0x00000000,
		0x00000000, 0x7f7fffff, 0x00000000, 0x00000000,
		0x00000000, 0x00000000,
	}},
	{group: 301, nums: [18]uint32{
		0x43ae0000, 0x40000000, 0x40000000, 0x3e6ed307,
		0x3eeed307, 0x3f800000, 0x40000000, 0x3fc795bf,
		0x3fc795bf, 0x00000000, 0x00000000, 0x00000000,
		0x00000000, 0x00000000, 4500, 5000,
		0x00000000, 0x00000000,
	}},
}

// fightEntriesFor 搏斗行为组列表。这一版所有鱼种共用 ruffe 那两组（我们手上只有那一条样本），
// 哪天解剖到更多鱼种的 14/14，就把这张表变成按鱼种查的表，调用处一行都不用改。
func fightEntriesFor(key string) []fightEntry { return fightEntries }

// SetupBody 拼 obj27 那个「咬钩事实」物件。字段序号和上面注释里的凭据一一对应。
func SetupBody(t *wire.Table, f Fish) (*wire.Obj, error) {
	o, err := t.NewObj(TypeFishSetup)
	if err != nil {
		return nil, err
	}

	// #00 这条鱼的实例 GUID（我们生成的，后面 keep 要原样回显）
	if err := o.SetGUIDAt(0, f.SetupGUID); err != nil {
		return nil, err
	}
	// #01 鱼种编号、#02 大小档、#03 体长(米)、#04 体重(克)
	if err := o.SetShortStrAt(1, f.Key); err != nil {
		return nil, err
	}
	class := f.Class
	if class == 0 {
		class = ClassMedium
	}
	if err := o.SetByteAt(2, class); err != nil {
		return nil, err
	}
	if err := o.SetU32At(3, wire.F32Bits(f.LengthM)); err != nil {
		return nil, err
	}
	if err := o.SetU32At(4, uint32(f.Grams)); err != nil {
		return nil, err
	}
	// #05 体力池：调用方给了就用给的（以后按鱼种查表），没给就按体重兜底
	stamina := f.Stamina
	if stamina <= 0 {
		stamina = setupStaminaPerGram * float64(f.Grams)
	}
	if err := o.SetU32At(5, wire.F32Bits(stamina)); err != nil {
		return nil, err
	}
	if err := o.SetU32At(6, setup06); err != nil {
		return nil, err
	}
	// #07 是个可选单字节，真服那条是 0
	if err := o.SetByteAt(7, 0); err != nil {
		return nil, err
	}
	// #08 拉力按体重线性给（两条真服样本都验证过这个比例）
	force := f.Force
	if force <= 0 {
		force = setupForcePerGram * float64(f.Grams)
	}
	if err := o.SetU32At(8, wire.F32Bits(force)); err != nil {
		return nil, err
	}
	// #09~#20 搏鱼曲线那一大坨：先照抄真服
	for _, p := range []struct {
		idx int
		v   uint32
	}{
		{9, setup09}, {10, setup10}, {11, setup11}, {12, setup12}, {13, setup13},
		{14, setup14}, {15, setup15}, {16, setup16}, {17, setup17}, {18, setup18},
		{19, setup19}, {20, setup20},
	} {
		if err := o.SetU32At(p.idx, p.v); err != nil {
			return nil, err
		}
	}

	// #21 这个鱼种的搏斗行为组数组（参数表"27"，每项一个 obj26）
	list := fightEntriesFor(f.Key)
	raws := make([][]byte, 0, len(list))
	for _, fe := range list {
		e, err := buildFightEntry(t, fe)
		if err != nil {
			return nil, err
		}
		raws = append(raws, e)
	}
	if err := o.SetValAt(21, wire.Args{Code: "27", Items: wire.RawItems(raws)}); err != nil {
		return nil, err
	}
	// #22 还没填上的文本名：真服发的是 null(0xff)；#23 线末端鱼钩 guid；#24 null
	if err := o.SetOptShortStrAt(22, "", false); err != nil {
		return nil, err
	}
	hook := f.HookGUID
	if hook == "" {
		hook = f.ModelGUID // 兼容旧测试/旧数据；实机会优先填真实鱼钩 GUID
	}
	if hook == "" {
		hook = f.SetupGUID
	}
	if err := o.SetGUIDAt(23, hook); err != nil {
		return nil, err
	}
	if err := o.SetValAt(24, wire.Null{}); err != nil {
		return nil, err
	}
	return o, nil
}

// buildFightEntry 拼一条 obj26：#00 是行为组号(u16)，后面 18 个都是 u32。
func buildFightEntry(t *wire.Table, fe fightEntry) ([]byte, error) {
	o, err := t.NewObj(TypeFishEntry)
	if err != nil {
		return nil, err
	}
	if err := o.SetU16At(0, fe.group); err != nil {
		return nil, err
	}
	for i, v := range fe.nums {
		if err := o.SetU32At(1+i, v); err != nil {
			return nil, err
		}
	}
	return o.Encode()
}

// SetupPush 拼整条 14/14 推送。gearGUID 是这次抛竿用的那套钓具的 GUID（14/1 里客户端给我们的）。
// 少发这个 GUID 客户端不知道该把鱼挂在哪根竿上 —— 之前「游戏里没出鱼」就怀疑过这一处。
func SetupPush(t *wire.Table, gearGUID string, f Fish) ([]byte, error) {
	obj, err := SetupBody(t, f)
	if err != nil {
		return nil, err
	}
	objBytes, err := obj.Encode()
	if err != nil {
		return nil, err
	}
	guidBytes, err := wire.GUID{Hex: gearGUID}.Encode()
	if err != nil {
		return nil, err
	}
	return append(wire.PushHead(1, MainFishing, SubBite), wire.WrapArgs524([][]byte{guidBytes, objBytes})...), nil
}
