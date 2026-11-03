// Package profile 拼「玩家档案」：物件 307（混淆类名 ghfbofgdflm，26 个字段）。
//
// 为什么这一块最关键：客户端登录之后卡在「载入个人资料」，等的就是这个物件。它有两个出口，
// 内容一模一样，只是外面那层壳不同（凭据：2026-11-02 那次 Node 版成功进场的完整收发流水）：
//
//	3/1  客户端主动问「我是谁」 -> 应答帧：9 字节应答头 + obj307（真服那帧共 262 字节 = 9 + 253）
//	2/1  服务器主动推「玩家状态」-> 推送帧：11 字节推送头 + 参数表"524"{obj307} + 尾巴 01（共 273 字节）
//	0/1  装完 RC4 后服务器开口的第一条 -> 推送帧：空参数表"524"，19 字节（不含 obj307）
//
// 实测过的事：装完弹之后服务器一句不说，客户端就一条请求都不发，界面停在「载入个人资料」；
// 先把 0/1、2/1 发出去，客户端才开始问 1/1 -> 1/2 -> … -> 3/1 那一串。
//
// 数据来源规矩（少爷定的）：账号、昵称、等级、银币、金币、未分配技能点全从 MySQL 现取；
// 经验按等级曲线算（曲线在 curve.go，MySQL users 表没有 xp 这一列）；
// 剩下几个含义还没查实的字段用真服那帧里的常量顶住，每个常量边上都写了凭据，
// 哪天查实了只改这一处。运行期一帧录制都不发。
package profile

import (
	"strconv"
	"time"

	"rf4server/internal/wire"
)

// 物件类型号。写字面量不如写名字，后面读代码的人不用去查 307 是谁。
const (
	// TypeProfile 玩家档案本体（ghfbofgdflm），3/1 应答和 2/1 推送里都是它
	TypeProfile = 307
	// TypeProfileFloats 档案字段 #12 那个数值块（pnbphpdmppe），9 个字段
	TypeProfileFloats = 308
	// TypeProfileRig 档案字段 #13 那个「手上这套钓具」块（kkmhfbeldpg），8 个字段
	TypeProfileRig = 322
	// TypeRigRod 钓具块里的竿信息（ifoifbocoom），5 个字段
	TypeRigRod = 315
	// TypeProfileRecord 档案字段 #24 那个记录条目（dnijhkbdfan），3 个字段
	TypeProfileRecord = 745
	// TypeProfileFlagEnum 档案字段 #25 那个占位枚举（fkcmcjlagdh），1 个字段
	TypeProfileFlagEnum = 5012
)

// RegionCN 大区编号：真服那帧 #18 写的是 4 = CN（0=GL 1=RU 2=DE 3=EN 4=CN…）。
const RegionCN byte = 4

// SchoolMaskDone 档案 #15「训练营/学校任务进度」位掩码。真服那帧是 256，
// 咱们这条档没走过训练营流程，照抄 256（少这位客户端会当成「任务列表读不到」）。
const SchoolMaskDone uint16 = 256

// UnknownField16 档案 #16 这个 u32：真服两帧里都是 9，含义还没查实，先照抄。
const UnknownField16 uint32 = 9

// ---- 下面这几个常量是档案里「数值块」obj308 的内容，全部来自真服 3/1 那帧 ----
// （解剖结果在 D:\rf4log\dump_3_1.txt，逐条能对上）。
// 这一版没搞清每个数字管什么，先把值发对，客户端就能把面板解出来。
const (
	floats00 uint32 = 0x3f800000 // #00 float 1.0
	floats01 uint32 = 0x3e740d2c // #01 float 0.23833149671554565
	floats03 uint32 = 0x3baebcf7 // #03 float 0.005332584958523512
	floats05 uint32 = 0x41480000 // #05 float 12.5
	floats06 uint32 = 0x3f800000 // #06 float 1.0
)

// ---- 手上那套钓具（obj322 里那层 obj315），同样照抄真服 3/1 那帧 ----
// 等「装配」那块搬过来，就改成按这个号自己装的竿/轮现算，改的只是 withDefaults 一处。
const (
	// rigBlockID obj322#00。真服那帧 10901；录制那位在线上那版是 10905，
	// 看着像「钓组配置号」，含义待查，先照抄真服这份。
	rigBlockID uint32 = 10901
	// rigMaxCast obj315#00：最远抛投距离（米），按 float32 存进 u32。真服那帧 52.97186279296875。
	rigMaxCast uint32 = 0x4253e330
	// rigRodTypeID obj315#01：竿类型。1=Spinning 纺车/路亚（真服那帧就是 1）
	rigRodTypeID byte = 1
	// rigReelTypeID obj315#03：卷线器类型。1=第一种（真服那帧就是 1）
	rigReelTypeID byte = 1
	// rigRodID / rigReelID 竿和轮的编号（systemID 那串）
	rigRodID  = "spin_10032_S78MH"
	rigReelID = "spin_5063"
	// rigField02 / rigField03 / rigField05 obj322 另外三个数：真服那帧是 4 / 3 / 5
	rigField02 uint32 = 4
	rigField03 uint32 = 3
	rigField05 uint32 = 5
)

// Data 拼一帧档案要的全部输入。每一项都标了来源，别在这儿藏计算。
type Data struct {
	// UserID 数字账号 id（obj307#00）。来源 MySQL users.user_id。
	// 客户端拿它对「这是不是我」，填错就认不出自己。
	UserID int64
	// Nickname 游戏里显示的名字（#01）。来源 users.nickname。
	Nickname string
	// Level 角色等级（#02）。来源 users.level。经验、技能点都按它推。
	Level int
	// SilverMinor 银币，最小单位（#07）。1 银币 = 100 minor，来源 users.silver_minor。
	SilverMinor int64
	// GoldMinor 金币，最小单位（#08）。来源 users.gold_minor。
	GoldMinor int64
	// SkillPoints 未分配技能点（#06）。来源 skill_points.granted 减去已学节点花掉的。
	// 少爷把 return2 的发放点手动改成 100 了，所以必须现取，不能按等级曲线硬算。
	SkillPoints int
	// Avatar 头像 URL（#09）。真服那帧是空串，客户端怎么解 URL 还没验证，
	// 所以调用方这边先一律传空串，别把 users.avatar 直接塞进来。
	Avatar string
	// Region 大区（#18）：0 = 按 CN 填，其余照传。
	Region byte

	// XP 累计经验（#03）。0 = 按等级曲线补 BaseXP(Level)。
	// MySQL users 表没有 xp 这一列，所以这一版就是曲线值；哪天要精确就把真值存进库再填这儿。
	XP uint64
	// XPInLevel 本级内已获得经验（#04）。0 = 刚升级的状态，客户端经验条显示满格起点。
	XPInLevel uint64
	// XPToNext 升到下一级需要的经验（#05）。0 = 按等级曲线补 NextLevelReq(Level)。
	XPToNext uint64

	// StateStamp 状态时刻（obj308#07 那个 FILETIME）。0 = 现算当前时间。
	// 真服两帧里这个数都落在抓包那一刻附近，可以确定是「这帧状态是什么时候的」，
	// 所以平时就该发当前时间；测试里要逐字节对答案时手动钉住它。
	StateStamp uint64

	// RigRodID / RigReelID 手上那套钓具的编号。留空 = 用上面那组真服常量。
	RigRodID  string
	RigReelID string
}

// withDefaults 把「没填」的字段补成该发的值。补的规则全写在这儿，Build 里就只剩填字段。
func (d Data) withDefaults() Data {
	d.Level = clampLevel(d.Level)
	if d.XP == 0 {
		d.XP = BaseXP(d.Level)
	}
	if d.XPToNext == 0 {
		d.XPToNext = NextLevelReq(d.Level)
	}
	if d.XPInLevel > d.XPToNext {
		// 本级内经验不该超过升级门槛，超了客户端那条进度条会画出去
		d.XPInLevel = d.XPToNext
	}
	if d.SkillPoints < 0 {
		d.SkillPoints = 0
	}
	if d.Region == 0 {
		d.Region = RegionCN
	}
	if d.StateStamp == 0 {
		d.StateStamp = wire.Filetime(time.Now())
	}
	if d.RigRodID == "" {
		d.RigRodID = rigRodID
	}
	if d.RigReelID == "" {
		d.RigReelID = rigReelID
	}
	return d
}

// Build 造出 obj307。填法完全按类表（字段顺序、写法都在 wire_tables.json 里），
// 少填一个字段就退回类表里的空值 —— 但真服那些空串字段发 null 和发空串不一样，
// 所以下面把每个会出现在帧里的字段都显式填了，测试再逐字节对一遍。
func Build(t *wire.Table, d Data) (*wire.Obj, error) {
	d = d.withDefaults()

	o, err := t.NewObj(TypeProfile)
	if err != nil {
		return nil, err
	}

	// #00~#02 我是谁：账号 id、昵称、等级
	if err := o.SetU32At(0, uint32(d.UserID)); err != nil {
		return nil, err
	}
	if err := o.SetShortStrAt(1, d.Nickname); err != nil {
		return nil, err
	}
	if err := o.SetU32At(2, uint32(d.Level)); err != nil {
		return nil, err
	}
	// #03~#05 经验三件套：累计、本级内、升下一级要多少
	if err := o.SetU64At(3, d.XP); err != nil {
		return nil, err
	}
	if err := o.SetU64At(4, d.XPInLevel); err != nil {
		return nil, err
	}
	if err := o.SetU64At(5, d.XPToNext); err != nil {
		return nil, err
	}
	// #06 未分配技能点（面板上那个「剩余点数」就读这一格）
	if err := o.SetU32At(6, uint32(d.SkillPoints)); err != nil {
		return nil, err
	}
	// #07/#08 钱包（最小单位）
	if err := o.SetU64At(7, uint64(d.SilverMinor)); err != nil {
		return nil, err
	}
	if err := o.SetU64At(8, uint64(d.GoldMinor)); err != nil {
		return nil, err
	}
	// #09 头像 URL：真服发的是「长度为 0 的串」，不是 null
	if err := o.SetStr16At(9, d.Avatar); err != nil {
		return nil, err
	}
	// #10 一个 0..2 的枚举、#11 一个时刻：真服那帧都是 0
	if err := o.SetByteAt(10, 0); err != nil {
		return nil, err
	}
	if err := o.SetU64At(11, 0); err != nil {
		return nil, err
	}
	// #12 数值块、#13 手上那套钓具：先进两个子构造器把物件造好，再挂上来
	nums, err := floatBlock(t, d.StateStamp)
	if err != nil {
		return nil, err
	}
	if err := o.SetValAt(12, nums); err != nil {
		return nil, err
	}
	rig, err := rigBlock(t, d)
	if err != nil {
		return nil, err
	}
	if err := o.SetValAt(13, rig); err != nil {
		return nil, err
	}
	// #14~#19 一堆杂项：训练营掩码、大区、几个还没查实但真服有固定值的数
	if err := o.SetByteAt(14, 0); err != nil {
		return nil, err
	}
	if err := o.SetU16At(15, SchoolMaskDone); err != nil {
		return nil, err
	}
	if err := o.SetU32At(16, UnknownField16); err != nil {
		return nil, err
	}
	if err := o.SetStr16At(17, ""); err != nil {
		return nil, err
	}
	if err := o.SetByteAt(18, d.Region); err != nil {
		return nil, err
	}
	if err := o.SetU64At(19, 0); err != nil {
		return nil, err
	}
	// #20/#22 两个字符串数组：真服是「0 项」，不是 null
	if err := o.SetValAt(20, wire.BytesArray{}); err != nil {
		return nil, err
	}
	// #21 一个空串（同样不是 null）
	if err := o.SetShortStrAt(21, ""); err != nil {
		return nil, err
	}
	if err := o.SetValAt(22, wire.BytesArray{}); err != nil {
		return nil, err
	}
	// #23 记录条目数组：真服是参数表 "181"、0 项
	if err := o.SetValAt(23, wire.Args{Code: "181"}); err != nil {
		return nil, err
	}
	// #24/#25 两个占位子对象
	record, err2 := recordBlock(t)
	if err2 != nil {
		return nil, err2
	}
	if err := o.SetValAt(24, record); err != nil {
		return nil, err
	}
	flag, err3 := flagBlock(t)
	if err3 != nil {
		return nil, err3
	}
	if err := o.SetValAt(25, flag); err != nil {
		return nil, err
	}
	return o, nil
}

// floatBlock 档案 #12 那个数值块（obj308）：七个浮点按 float32 的位模式存进 u32，
// #07 是「这帧状态是什么时候的」（FILETIME），#08 是 0。
// 值全部来自真服 3/1 那帧（D:\rf4log\dump_3_1.txt 里 #12 那一段，逐条能对上）。
func floatBlock(t *wire.Table, stamp uint64) (*wire.Obj, error) {
	o, err := t.NewObj(TypeProfileFloats)
	if err != nil {
		return nil, err
	}
	for _, p := range []struct {
		idx int
		v   uint32
	}{
		{0, floats00}, {1, floats01}, {2, 0}, {3, floats03}, {4, 0}, {5, floats05}, {6, floats06},
	} {
		if err := o.SetU32At(p.idx, p.v); err != nil {
			return nil, err
		}
	}
	if err := o.SetU64At(7, stamp); err != nil {
		return nil, err
	}
	if err := o.SetU32At(8, 0); err != nil {
		return nil, err
	}
	return o, nil
}

// rigBlock 档案 #13「手上这套钓具」（obj322，里面套一层 obj315 竿信息）。
// 这一版填的是真服那帧的常量，等装配域搬过来就按这个号自己装的竿/轮现算。
func rigBlock(t *wire.Table, d Data) (*wire.Obj, error) {
	rod, err := t.NewObj(TypeRigRod)
	if err != nil {
		return nil, err
	}
	if err := rod.SetU32At(0, rigMaxCast); err != nil { // 最远抛投（米，float32 位模式）
		return nil, err
	}
	if err := rod.SetByteAt(1, rigRodTypeID); err != nil { // 竿类型：1=Spinning
		return nil, err
	}
	if err := rod.SetShortStrAt(2, d.RigRodID); err != nil { // 竿编号
		return nil, err
	}
	if err := rod.SetByteAt(3, rigReelTypeID); err != nil { // 卷线器类型
		return nil, err
	}
	if err := rod.SetShortStrAt(4, d.RigReelID); err != nil { // 轮编号
		return nil, err
	}

	o, err := t.NewObj(TypeProfileRig)
	if err != nil {
		return nil, err
	}
	if err := o.SetU32At(0, rigBlockID); err != nil {
		return nil, err
	}
	if err := o.SetU32At(1, 0); err != nil {
		return nil, err
	}
	if err := o.SetU32At(2, rigField02); err != nil {
		return nil, err
	}
	if err := o.SetU32At(3, rigField03); err != nil {
		return nil, err
	}
	if err := o.SetValAt(4, rod); err != nil {
		return nil, err
	}
	if err := o.SetU32At(5, rigField05); err != nil {
		return nil, err
	}
	if err := o.SetU32At(6, 0); err != nil {
		return nil, err
	}
	if err := o.SetU32At(7, 0); err != nil {
		return nil, err
	}
	return o, nil
}

// recordBlock 档案 #24 那个占位记录（obj745）：真服那帧三个字段全是 0。
func recordBlock(t *wire.Table) (*wire.Obj, error) {
	o, err := t.NewObj(TypeProfileRecord)
	if err != nil {
		return nil, err
	}
	if err := o.SetByteAt(0, 0); err != nil {
		return nil, err
	}
	if err := o.SetU32At(1, 0); err != nil {
		return nil, err
	}
	if err := o.SetU64At(2, 0); err != nil {
		return nil, err
	}
	return o, nil
}

// flagBlock 档案 #25 那个占位枚举（obj5012）：里面是个 0 项的参数表 "210"。
func flagBlock(t *wire.Table) (*wire.Obj, error) {
	o, err := t.NewObj(TypeProfileFlagEnum)
	if err != nil {
		return nil, err
	}
	if err := o.SetValAt(0, wire.Args{Code: "210"}); err != nil {
		return nil, err
	}
	return o, nil
}

// Body 只拼 obj307 的字节（不带任何帧头）。
func Body(t *wire.Table, d Data) ([]byte, error) {
	o, err := Build(t, d)
	if err != nil {
		return nil, err
	}
	return o.Encode()
}

// Reply 3/1 的应答帧：9 字节应答头（marker=-2 + call_id）+ obj307。
// 真服那帧 262 字节，扣掉 9 字节头是 253 字节档案本体。
func Reply(t *wire.Table, callID uint32, d Data) ([]byte, error) {
	body, err := Body(t, d)
	if err != nil {
		return nil, err
	}
	return append(wire.ResponseHead(callID), body...), nil
}

// StatePush 2/1「玩家状态」推送帧：11 字节推送头 + 参数表"524"{obj307} + 尾巴 01。
// 推送头里那个序号真服常年写 1（录制里 2/1、2/4、14/14 全是 1），这边照抄，
// 别自作聪明递增 —— 客户端按这个位置对号，写错了它整帧丢掉。
func StatePush(t *wire.Table, d Data) ([]byte, error) {
	body, err := Body(t, d)
	if err != nil {
		return nil, err
	}
	return append(wire.PushHead(1, 2, 1), wire.WrapArgs524([][]byte{body})...), nil
}

// HelloPush 0/1：装完 RC4 之后服务器开的第一句（19 字节，正文是空参数表"524"）。
// 凭据：Node 版成功进场那场，11:51:31.570 那条 19B，
// hex=01ffffffff 01000000 0001 0303 353234 0000 01。
// 客户端在等这一句才肯开始问后面的东西 —— 少了它，界面就一直停在「载入个人资料」。
func HelloPush() []byte {
	return append(wire.PushHead(1, 0, 1), wire.WrapArgs524(nil)...)
}

// Summary 给日志用的一行人话：这条档案发了谁、几级、多少钱、剩几点技能。
// 卡「载入个人资料」那种事，日志里这一行就是第一现场，别再让人去 hex 里数偏移。
// 经验那两个数已经补过默认值，所以要先经过 withDefaults 再打印才准。
func (d Data) Summary() string {
	d = d.withDefaults()
	return "账号id " + strconv.FormatInt(d.UserID, 10) +
		" 昵称 " + d.Nickname +
		" 等级 " + strconv.Itoa(d.Level) +
		" 银币 " + Money(d.SilverMinor) +
		" 金币 " + Money(d.GoldMinor) +
		" 可用技能点 " + strconv.Itoa(d.SkillPoints) +
		" 经验 " + strconv.FormatUint(d.XP, 10) + "/" + strconv.FormatUint(d.XPToNext, 10)
}
