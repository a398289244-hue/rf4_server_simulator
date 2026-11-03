package itemwire

// 这一堆混淆名字是从协议逆推表（wire_tables.json）和 glance522 的 csnames.go 抄来的，
// 千万别改名、也别改成「更好懂的英文名」—— 名字本身就是字节位置的钥匙。
//
// 为什么要按名字找字段而不是按序号：同一件「身份字段」（GUID、数字编号、创建时间…）
// 在不同 wire 类里的位置不一样（竿在 #04、线在 #03），只有名字是全类通用的。

// CS 是「同一含义、跨类通用」的字段名表
const (
	fGUID     = "cahhdnicmbn" // 物件实例 GUID
	fParent   = "odflendafcg" // 父容器/父物品 GUID（可空）
	fSlot     = "mfllbndghhe" // 容器类型：0=背包 1=帐篷 2=车 3=船 5=软箱 10=房；装配子件填 4
	fNum      = "iofbjlaadfg" // 物品数字编号（客户端按它认货）
	fWork     = "hjilekjlgbk" // 工艺质量 0..1
	fDur      = "nmidlgejlcp" // 剩余完好率 0..1
	fCreated  = "adhajflcpcd" // 创建时间（.NET ticks）
	fUpdated  = "iingicaldbk" // 更新/到期时间（FILETIME）
	fState    = "hgaijieklek" // 物品状态位（attrs 里有原值，照原值发）
	fSubtype  = "dbgaheblmei" // 状态位掩码（u16）
	fQty      = "ljnnpokapig" // 数量/容量
	fStrength = "nlmnpekipjc" // 强度
	fDrag     = "ppibbfgiffa" // 渔轮刹车曳力
	fOrigin   = "effnpjlfjai" // 购买凭证那一格（obj166）
	fRig      = "pgcabefancm" // 竿上的默认钓组（obj153）
	fLine     = "bghbekmllgp" // 钓组上的线
	fReel     = "neangpifoka" // 钓组上的轮
	fRigTip   = "kjphlnnlflh" // 钓组尖端
)

// CLASS 是几个常用 wire 类的类名（类型号从类表里查，别写死数字）
const (
	clsRod      = "cnolibojkao" // 竿
	clsReel     = "dgjinoebdop" // 渔轮
	clsLeader   = "oldfpclaabo" // 前导/子线
	clsLure     = "ioombppcnlh" // 拟饵
	clsDragSub  = "loedbndnill" // 刹车片（渔轮子件）
	clsRig      = "gbbhdkefnkn" // 钓组 obj153
	clsRigItem  = "gcfcdnfmnbh" // 钓组上的一条组件 obj152
	clsReceipt  = "pcfflcdpjgk" // 购买凭证 obj166
	clsPurchase = "jfafgmcfjkc" // 购买回执外壳 obj804（22/2 应答顶层就是它）
)

// 购买凭证 obj166 的字段名（凭据：真服 22/2 那条应答逐字节拆出来的）
const (
	rcStore   = "pgdemfofgdm" // 在哪家店买的
	rcBuyerID = "jfflfjglkhc" // 买家账号 id
	rcBalance = "oegjbfmmeoe" // 买完之后余额（最小单位）
	rcPrice   = "phdhnkpngpc" // 成交价（u64，最小单位）
	rcFlag    = "gbnihpkgjnk" // 一个 0/1 小旗子（真服那条是 0）
	rcBuyer   = "dhaenhabecc" // 买家名字
)

// 购买回执 obj804 的四个字段（类表里这四个还没逆出名字，只能按序号写，
// 序号是从真服那帧解剖出来的：见 D:\rf4log\dump_buy.txt）
const (
	P804Item    = 0 // #00 物品编号（短串）
	P804Objects = 1 // #01 参数表"61"：这次到手的物件记录
	P804Rig     = 2 // #02 参数表"61"：钓组上的物件（一般是空）
	P804GUIDs   = 3 // #03 GUID 数组（真服那条是空数组）
)

// 母物品 attrs 里「这一格是个子部件配置」时用的几个键（glance522 materialize.go 的口径）
const (
	subKey    = "nhjofmmmngl" // 子件的物品编号，如 rodblank_ef_tel_starling_13
	subKind   = "mgkfaekpnoj" // 子件的类别号（81 那种他家和我们都先不物化）
	subNested = "mlfjkaoicdn" // 渔轮嵌套配置（强度在里面）
	subDrag   = "omghfmdfjpc" // 曳力：嵌在渔轮配置里
	subBase   = "kngaaefkpka" // 强度基准值（浮点位模式）
)

// 渔轮 f2..f14 这 13 格的子部件配置键（顺序照 glance522 的 reelPartKeys）
var reelPartKeys = []string{
	"dlfjnokncfl", "mlfjkaoicdn", "fkmdfljgann", "mglgpmpeond",
	"fblnceginlm", "ofpkgghjmmf", "ndmdiaeeggg", "ddpggjlhgki",
	"oiaplokhcii", "hllnkgmfpkn", "geibgaipbda", "eokpjdaohfm", "dfjpnambjcj",
}

// 前导线 f1/f2/f3 的子部件配置键
var leaderPartKeys = []string{"mbnambppbom", "lbadjgllglo", "baadhdmaonp"}

// 竿 f0/f1/f2 的出厂部件配置键 + 算子件 GUID 用的盐（顺序必须一一对应）
var (
	rodPartKeys  = []string{"ajkflmejnih", "ofpkgghjmmf", "ddbadcmdkcl"}
	rodPartSalts = []string{"rod", "handle", "third"}
)

// lureHookKey 拟饵出厂钩列表在 attrs 里的键
const lureHookKey = "gpoaffnjbdo"

// subUnbuiltKinds 这些类别的子部件连 glance522 都还没物化（81），我们也先跳过
var subUnbuiltKinds = map[int]bool{81: true}

// equipClass2048  glance522 的经验值：这些类要额外置 2048 装配位。
// 真服录制里竿的状态位就是 attrs 原值（没加 2048），所以我们默认不加。
var equipClass2048 = map[string]bool{
	clsRod: true, clsReel: true, "hgabifpnhmk": true, "knocoeggfdk": true, "dbmbnkpkafb": true,
}

const (
	subMountFlag  = uint64(1024)       // 子件已挂到母物品上
	equipFlag2048 = uint64(2048)       // 装配位（默认不加，见上）
	f32One        = uint32(0x3f800000) // 1.0 的 f32 位模式
)

// ClsPurchase 购买回执外壳 obj804 的类名，导出给 internal/buy 拼 22/2 应答用。
const ClsPurchase = "jfafgmcfjkc"
