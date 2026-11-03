package fishing

import (
	"encoding/hex"
	"strings"

	"rf4server/internal/profile"
	"rf4server/internal/wire"
)

// 起鱼结算 14/4：应答是物件 801（混淆类名 caagocpofin，11 个字段）。
//
// 真服那两条样本（都是同一次会话里录的，凭据 -cmd 14/4 就是第一条）：
//
//	没钓上来那条 325B：#00 null  #01 0  #02 0  #03 obj5003(三个空清单)
//	                   #04/#05/#06 null  #07 obj307 玩家档案  #08/#09 空清单  #10 空 GUID 数组
//	钓上来那条 1833B：#00 换成参数表"174"{ obj813 结算块 }、#06 换成参数表"121"{ obj312 技能经验 }、
//	                   #08 多了四条装备记录（搏斗之后线/钩的耐久变了）、#10 是 1 个 GUID
//
// 这一版走「没钓上来」那个最小形状 + 我们自己的档案，理由是：
// #00 那个 obj813 里十个数字的含义一个都没查实（十个都是「典型取值」那种清单），
// 硬填反而容易让客户端弹个奇怪的结算面板；#07 档案是真数据（银币/等级/技能点从 MySQL 现取），
// #08/#10 我们有什么就发什么，没有就跟真服那条一样留空。哪天解剖出 obj813 的含义，
// 只改 LandReply 这一个函数。

// TypeEnd 起鱼结算那个物件
const TypeEnd = 801

// EndInput 回 14/4 要的东西。除档案外都可以不填，不填就跟真服那条 325B 一模一样。
type EndInput struct {
	// Profile 玩家档案（#07）。必填：银币/等级/技能点要跟着这次钓鱼的结果走。
	Profile profile.Data
	// GearRecords 搏斗之后变了的那些装备记录（#08）。传 nil 就是空清单。
	GearRecords [][]byte
	// FishGUIDs 这次上钩过的那几条鱼的实例 GUID（#10）。没钩上就留空。
	FishGUIDs []string
}

// EndReply 拼 14/4 的应答帧（含 9 字节应答头）。
func EndReply(t *wire.Table, callID uint32, in EndInput) ([]byte, error) {
	o, err := t.NewObj(TypeEnd)
	if err != nil {
		return nil, err
	}
	// #00 结算块：先照真服那条「没钓上来」发 null
	if err := o.SetValAt(0, wire.Null{}); err != nil {
		return nil, err
	}
	if err := o.SetU32At(1, 0); err != nil {
		return nil, err
	}
	if err := o.SetU32At(2, 0); err != nil {
		return nil, err
	}
	// #03 战队/比赛那三个清单：真服两条样本里都是空的，照抄
	cup, err := emptyCup(t)
	if err != nil {
		return nil, err
	}
	if err := o.SetValAt(3, cup); err != nil {
		return nil, err
	}
	// #04/#05/#06 三个 null（#06 是技能经验块，含义待查，先 null）
	for _, idx := range []int{4, 5, 6} {
		if err := o.SetValAt(idx, wire.Null{}); err != nil {
			return nil, err
		}
	}
	// #07 玩家档案：等级/银币/技能点从 MySQL 现取，这一格是整帧里唯一会变的数
	prof, err := profile.Build(t, in.Profile)
	if err != nil {
		return nil, err
	}
	if err := o.SetValAt(7, prof); err != nil {
		return nil, err
	}
	// #08/#09 两个物件清单：#08 放搏斗后更新的装备，#09 真服两条都是空
	if err := o.SetValAt(8, wire.Args{Code: "61", Items: wire.RawItems(in.GearRecords)}); err != nil {
		return nil, err
	}
	if err := o.SetValAt(9, wire.Args{Code: "61"}); err != nil {
		return nil, err
	}
	// #10 GUID 数组
	arr, err := guidArray(in.FishGUIDs)
	if err != nil {
		return nil, err
	}
	if err := o.SetValAt(10, arr); err != nil {
		return nil, err
	}
	body, err := o.Encode()
	if err != nil {
		return nil, err
	}
	return append(wire.ResponseHead(callID), body...), nil
}

// emptyCup 拼 #03 那个 obj5003：里面三个清单（一个 obj5012 套空参数表"210"、
// 一个空"202"、一个空"201"），真服两条样本里都长这样，逐字节照抄。
func emptyCup(t *wire.Table) (*wire.Obj, error) {
	flag, err := t.NewObj(profile.TypeProfileFlagEnum) // 5012：就一个字段，里面塞空参数表"210"
	if err != nil {
		return nil, err
	}
	if err := flag.SetValAt(0, wire.Args{Code: "210"}); err != nil {
		return nil, err
	}
	o, err := t.NewObj(5003)
	if err != nil {
		return nil, err
	}
	if err := o.SetValAt(0, flag); err != nil {
		return nil, err
	}
	if err := o.SetValAt(1, wire.Args{Code: "202"}); err != nil {
		return nil, err
	}
	if err := o.SetValAt(2, wire.Args{Code: "201"}); err != nil {
		return nil, err
	}
	return o, nil
}

// guidArray GUID 字符串列表 -> 帧里的 GUID 数组。不合法的填一串零，条数不能少
// （和 itemsync 那层同一个处理法，客户端是按条数读的）。
func guidArray(guids []string) (wire.GUIDArray, error) {
	out := wire.GUIDArray{}
	for _, g := range guids {
		h := strings.ToLower(strings.ReplaceAll(g, "-", ""))
		if len(h) != 32 || !isHexStr(h) {
			h = "00000000000000000000000000000000" // 填一串零占位：条数不能少，客户端按条数读
		}
		raw, err := hex.DecodeString(h)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, raw)
	}
	return out, nil
}
