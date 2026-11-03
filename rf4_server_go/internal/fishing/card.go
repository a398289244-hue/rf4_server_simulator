package fishing

import (
	"time"

	"rf4server/internal/itemsync"
	"rf4server/internal/wire"
)

// 鱼卡片 / 鱼护里那条鱼：物件 25（混淆类名 ecknbkpeeji，29 个字段）。
//
// 它是「一条具体的鱼」的档案，三个地方要用同一份字节，所以只能有一个构造函数：
//
//	14/5（留鱼）应答 = obj800{ #00 = 参数表"61"{ 这条 obj25 } }   凭据：真服那条 174B
//	写进 objects 表  = 同一串字节，下次登录背包/鱼护里就是它
//	22/x 卖鱼        = 以后按这条算钱，不再另拼一份
//
// 凭据来源：cmd/rf4dump -cmd 14/5（真服那条 53 克的 ruffe_n），逐字段抄的，
// 每个没查实的字段都在注释里标了「待查」，没查实的就照真服那个值发。

// ContainerKeepnet 容器类型：5 = 软箱/鱼护。真服那条 #18 就是 5。
const ContainerKeepnet byte = 5

// ContainerBag 容器类型：0 = 背包。
const ContainerBag byte = 0

// CardInput 拼一条鱼记录要的全部输入。0 值/空串的含义写在每一项旁边。
type CardInput struct {
	// Key 卡片上显示的鱼种编号。真服那条是 "ruffe_n"，而 14/14 推送里同一条鱼写的是 "ruffe"
	// —— 一个是显示用的种名（带水域变体后缀），一个是名册里的种名。填错客户端顶多显示不出中文名。
	Key string
	// Grams 体重（克）。#01
	Grams int64
	// LengthM 体长（米，float32 塞 u32）。#02
	LengthM float64
	// Class 大小档（1 小 2 中 3 大 4 特大）。#03。0 当中档处理。
	Class byte
	// HookedFiletime 钓获时刻（FILETIME，1601 年起算 100ns）。#05 和 #23 都用它；
	// 传 0 = 用当前时间现算。
	HookedFiletime uint64
	// GameDay/GameHour/GameMin 钓获那一刻的游戏内时间（#06 那个 obj600）。
	// 真服那条是 98626 天 / 0 时 / 19 分（天数是从某个锚点起算的，锚点还没查实），
	// 所以这边填 0 也合法：客户端只是把这几格显示出来。
	GameDay  uint32
	GameHour uint32
	GameMin  uint32
	// BaitKey 用的饵的编号（#08，真服是 "worm_01"）。填错只是卡片上饵名不对。
	BaitKey string
	// BaitType 饵类型枚举（#09）。真服那条是 0，含义待查。
	BaitType byte
	// Place 钓点/水域 id（#10，真服是 "level_002_kirzah_river"）。
	Place string
	// XP 这条鱼给的经验（#14，u16）。真服那条是 0，Go 版也先不给经验（users 表还没有 xp 列）。
	XP uint16
	// SetupGUID 这次咬钩的实例 GUID（#16）。必须和 14/14 推送 #00、客户端 keep 回来的那个一致。
	SetupGUID string
	// ParentGUID 装在谁里面（#17 可空）。鱼护还没建模成一个物品，先留空。
	ParentGUID string
	// Container 容器类型（#18）。留空(0)按鱼护 5 处理，因为 keep 完就是进鱼护。
	Container byte
	// HasContainer 调用方有没有明确指定 Container（不然 0 会被误当成背包）。
	HasContainer bool
}

// CardRecord 拼一条 obj25 并编成字节。这串字节既能直接发给客户端，也是 objects.elem 该存的东西。
func CardRecord(t *wire.Table, in CardInput) ([]byte, error) {
	o, err := t.NewObj(TypeFishItem)
	if err != nil {
		return nil, err
	}
	class := in.Class
	if class == 0 {
		class = ClassMedium
	}
	stamp := in.HookedFiletime
	if stamp == 0 {
		stamp = wire.Filetime(time.Now().UTC())
	}

	// #00 种名、#01 体重、#02 体长、#03 大小档、#04 真服那条是 1（含义待查，照抄）
	if err := o.SetShortStrAt(0, in.Key); err != nil {
		return nil, err
	}
	if err := o.SetU32At(1, uint32(in.Grams)); err != nil {
		return nil, err
	}
	if err := o.SetU32At(2, wire.F32Bits(in.LengthM)); err != nil {
		return nil, err
	}
	if err := o.SetByteAt(3, class); err != nil {
		return nil, err
	}
	if err := o.SetByteAt(4, 1); err != nil {
		return nil, err
	}
	// #05 钓获时刻（FILETIME）
	if err := o.SetU64At(5, stamp); err != nil {
		return nil, err
	}
	// #06 游戏内时间（obj600：天/时/分）
	clock, err := gameClock(t, in)
	if err != nil {
		return nil, err
	}
	if err := o.SetValAt(6, clock); err != nil {
		return nil, err
	}
	// #07 「中档以上」那个 flag，字段表注释就是 cat>=2
	if err := o.SetByteAt(7, TrophyFlag(class)); err != nil {
		return nil, err
	}
	// #08 饵、#09 饵类型、#10~#12 三个串（后两个真服是空串，不是 null）
	if err := o.SetShortStrAt(8, in.BaitKey); err != nil {
		return nil, err
	}
	if err := o.SetByteAt(9, in.BaitType); err != nil {
		return nil, err
	}
	if err := o.SetShortStrAt(10, in.Place); err != nil {
		return nil, err
	}
	// #11/#12 两个「还没填上的文本」：真服发的是 null(0xff)，不是空串
	if err := o.SetOptShortStrAt(11, "", false); err != nil {
		return nil, err
	}
	if err := o.SetOptShortStrAt(12, "", false); err != nil {
		return nil, err
	}
	// #13 奖杯/记录条目：真服是 null
	if err := o.SetValAt(13, wire.Null{}); err != nil {
		return nil, err
	}
	// #14 经验、#15 一个 0 字节
	if err := o.SetU16At(14, in.XP); err != nil {
		return nil, err
	}
	if err := o.SetByteAt(15, 0); err != nil {
		return nil, err
	}
	// #16 咬钩实例 GUID（和 14/14 那帧同一个数）、#17 父容器（可空）
	if err := o.SetGUIDAt(16, in.SetupGUID); err != nil {
		return nil, err
	}
	// #17 是「可空 GUID」（OPTGUID）：没父容器就发空，跟 GUID16 是两种写法
	if err := o.SetOptGUIDAt(17, in.ParentGUID); err != nil {
		return nil, err
	}
	// #18 容器类型（0=背包 5=鱼护）
	cont := ContainerKeepnet
	if in.HasContainer {
		cont = in.Container
	}
	if err := o.SetByteAt(18, cont); err != nil {
		return nil, err
	}
	// #19~#21 三个 u32：真服是 0 / 1.0 / 1.0（后两个字段表说是「物品好坏」那两格，都拉满）
	if err := o.SetU32At(19, 0); err != nil {
		return nil, err
	}
	if err := o.SetU32At(20, 0x3f800000); err != nil {
		return nil, err
	}
	if err := o.SetU32At(21, 0x3f800000); err != nil {
		return nil, err
	}
	// #22 创建时间（.NET ticks，真服那条是 0）、#23 更新时刻（FILETIME，和 #05 同一个数）
	if err := o.SetU64At(22, 0); err != nil {
		return nil, err
	}
	if err := o.SetU64At(23, stamp); err != nil {
		return nil, err
	}
	// #24~#28 状态位 + 两个可空子对象 + 剩余数量 + 状态掩码：真服全是 0 / null
	if err := o.SetU32At(24, 0); err != nil {
		return nil, err
	}
	if err := o.SetValAt(25, wire.Null{}); err != nil {
		return nil, err
	}
	if err := o.SetValAt(26, wire.Null{}); err != nil {
		return nil, err
	}
	if err := o.SetU32At(27, 0); err != nil {
		return nil, err
	}
	if err := o.SetU16At(28, 0); err != nil {
		return nil, err
	}
	return o.Encode()
}

// gameClock 拼 #06 那个游戏内时间块（obj600，三个 u32）。
func gameClock(t *wire.Table, in CardInput) (*wire.Obj, error) {
	o, err := t.NewObj(600)
	if err != nil {
		return nil, err
	}
	if err := o.SetU32At(0, in.GameDay); err != nil {
		return nil, err
	}
	if err := o.SetU32At(1, in.GameHour); err != nil {
		return nil, err
	}
	if err := o.SetU32At(2, in.GameMin); err != nil {
		return nil, err
	}
	return o, nil
}

// KeepReply 回 14/5：obj800，#00 里就放那一条鱼。形状和 4/7 删东西那种回包是同一个物件，
// 所以直接借 itemsync 那层拼，别在这儿手搓一遍。
func KeepReply(t *wire.Table, callID uint32, card []byte) ([]byte, error) {
	return itemsync.Frame(t, callID, [][]byte{card}, nil, nil)
}

// KeepPush422 拼 4/22 那条推送：真服在留鱼之后会紧接着推一帧「装备更新了」。
//
// 凭据：那次会话 t=17740 那条 993B，正文就是 参数表"524"{ 六条物件记录 }，
// 六条全是这位玩家自己身上的装备（线、轮、竿、配件），跟别人的号没关系。
// 所以我们发自己名下的那几件就行 —— 发别人的记录才会出「删了又回来」的影子物品。
func KeepPush422(t *wire.Table, records [][]byte) ([]byte, error) {
	if len(records) == 0 {
		return nil, nil // 一件装备都没有就没必要推，调用方自己决定要不要记日志
	}
	return append(wire.PushHead(1, 4, 22), wire.WrapArgs524(records)...), nil
}
