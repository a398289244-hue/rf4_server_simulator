// Package rig 管「往竿上装东西 / 拆下来」（17/2、17/3、17/4，顺带 17/1 17/6 17/7）。
//
// 命令形状（两处硬凭据，不是猜的）：
//
//	17/2 请求 = 参数表"524"{ 竿GUID, 物品GUID, 一个整数 }        （实机日志 2026-11-02 15:23 量出来的）
//	17/2 应答 = obj800 快照，#00 = 两条新记录，顺序是 先物品后竿   （真服那条 460B 逐字节拆出来的）
//
// 规矩（从 glance522 的 rigops.go 搬过来，Node 版已经实测过）：
//
//	鱼线(11) 写竿的第 3 格、渔轮(6) 写第 4 格（都是 OPTGUID）；
//	其余零件（浮漂/鱼钩/鱼饵/拟饵/前导…）进竿上那个 153 钓组对象的参数表"81"，
//	每条是一个 152{ 品类 BYTE, 零件GUID, 槽索引 U32 }，槽索引由「钓组类型 + 品类」那张表决定；
//	零件自己的父 GUID 要指向竿；两件记录的版本戳（创建时间那格 u64）必须严格变大，
//	不然客户端认为「没变化」，这次更新直接被丢掉 —— 之前「装了但界面不动」就是栽在这。
//
// 权威数据是 objects.elem（客户端读的就是它）：改完就把新字节写回库。
// rig / rig_component 两张表只是给少爷直接看的投影，写坏了不拦游戏，只记一行日志。
package rig

import (
	"context"

	"rf4server/internal/gamestore"
	"rf4server/internal/itemsync"
	"rf4server/internal/wire"
)

// argOff 客户端请求里参数表的起点：9 字节应答式壳 + main + sub = 第 11 字节
const argOff = 11

// Store 这一域要的数据操作（gamestore.MySQL 全都有）。
// 写成小接口是为了单测能塞假数据，也让「谁是权威」这一条只在这一处出现。
type Store interface {
	Objects(ctx context.Context, username string) ([]gamestore.Object, error)
	UpdateObjectElem(ctx context.Context, username, guid, elemHex string) (bool, error)
	SaveRig(ctx context.Context, st gamestore.RigState) error
}

// itemdefSource 只用到「按编号查品类」这一件事（internal/itemdef.Defs 加上一个小方法就符合）
type itemdefSource interface {
	CategoryOf(key string) (int, bool)
}

// Service 装配服务。
type Service struct {
	tbl *wire.Table   // 协议字段表（类、字段写法都从这儿来）
	def itemdefSource // 物品定义（按编号查品类）
	run Store         // 数据入口
	log Logger        // 日志出口（可空，装配镜像写坏了要说一声）
}

// New 造服务。
func New(t *wire.Table, defs itemdefSource, run Store) *Service {
	return &Service{tbl: t, def: defs, run: run}
}

// RemovedItem 拆下来的一件（日志要说清是哪件、从哪个槽下来的）
type RemovedItem struct {
	GUID  string // 零件/组件自己的 GUID
	Key   string // 物品编号
	Slot  int    // 竿上的槽（3 鱼线 4 渔轮）；零件这里是它的品类
	Index int    // 零件在 153 里的槽索引
	Part  bool   // 是不是钓组零件（不是就是竿上那两个专用格）
}

// Result 一次装配的结果，字段全是给日志准备的
type Result struct {
	OK        bool   // 改动生效了没有
	Reason    string // 没生效的原因
	Kind      string // "field"=写竿的专用格；"part"=进钓组零件表
	Slot      int    // 专用格号（3/4）；零件是 -1
	Cat       int    // 这件东西的品类
	PartIndex int    // 进了 153 的哪个槽索引
	RigType   int    // 这根竿现在的钓组类型
	Prev      string // 被顶掉那件的 GUID（专用格被占时才有）
	Removed   []RemovedItem
	Records   int // 回包里几条记录
	RodGUID   string
	ItemGUID  string
}

// rejected 没装成也要回一帧空 800：客户端要的是「一个答案」，不回它就一直转圈。
func (s *Service) rejected(cid uint32, res Result) ([]byte, Result, error) {
	frame, err := itemsync.EmptyFrame(s.tbl, cid)
	return frame, res, err
}
