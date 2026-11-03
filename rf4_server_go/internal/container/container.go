package container

// Package container 管 21/1：客户端点开「工坊/仓库」那类界面时，要这个容器里有哪些格子、哪些货。
//
// 一帧的形状（拿真服那条 21/1 应答逐字段拆出来的，和 22/1 是同一类东西）：
//
//	应答 = 01 feffffff call_id | 参数表 code="144"{ obj505 × N }
//	obj505 = #00 物件 GUID | #01 成色(f32) | #02 一个 u32 | #03 一串带名字的 u32（槽位/数量）
//
// 库里 container_items 存的就是这些段（head/cond_bits/n1_bits/tag/args），
// 是当年从真服那帧拆出来落库的，现在属于「这个号自己的数据」：能直接 SELECT 直接 UPDATE。
// 运行期这边只是把它们拼回去，不发录制原文。
//
// 这个号一个容器行都没有也照样回一帧空列表（语法借同一个容器名别人的那行），
// 不回客户端会一直转圈 —— 这个坑之前踩过。

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"rf4server/internal/gamestore"
	"rf4server/internal/wire"
)

// argOffContainer 容器元素里各段的固定长度（凭据：真服那帧）
const (
	headLen   = 5  // 元素头：01 + 类型号(4 字节小端)
	guidLen   = 16 // 物件 GUID
	condBits  = 4  // 成色 f32 位模式
	n1BitsLen = 4  // 另一个 u32
)

// Source 容器数据来源（gamestore.MySQL 就有这两个方法）
type Source interface {
	Container(ctx context.Context, username, container string) (gamestore.ContainerRows, error)
	ContainerSyntax(ctx context.Context, container string) (gamestore.ContainerMeta, error)
}

// Stats 一次拼装的统计，日志要用（「这个号几件、语法借的谁的」都得说清）
type Stats struct {
	Name    string // 客户端要的容器名
	Items   int    // 回了几件
	SrcUser string // 语法来自哪一行（不是自己的就写出来，省得以为是自己的货）
	Syntax  string // 参数表名 + 元素类型号，例如 args"144"/obj505
	Reason  string // 为什么是空的 / 哪里跳过了几件
	Bytes   int
}

// Service 容器服务。
type Service struct {
	tbl *wire.Table // 参数表/u32 数组的编码要用字段表里的写法
	src Source
}

// New 造服务
func New(t *wire.Table, src Source) *Service { return &Service{tbl: t, src: src} }

// Frame 拼一帧 21/1 应答。
//
// 没这一格的语法也不报错：先借「同名容器别人那一行」的语法，借不到就用工坊默认，
// 总之回一帧合法的（客户端要的是答案，不是我们的困扰）。
func (s *Service) Frame(ctx context.Context, user, name string, cid uint32) ([]byte, Stats, error) {
	st := Stats{Name: name}
	if s == nil || s.tbl == nil {
		return nil, st, errors.New("container: 协议字段表没载入，容器帧拼不出来")
	}
	if s.src == nil {
		return nil, st, errors.New("container: 没接上容器数据（containers/container_items）")
	}

	rows, err := s.src.Container(ctx, user, name)
	switch {
	case err == nil:
		st.SrcUser = user
	case errors.Is(err, gamestore.ErrNotFound):
		// 这个号还没有这个容器：语法借别人那一行（表里 return 那个号有 workshop_02）
		meta, e := s.src.ContainerSyntax(ctx, name)
		if e != nil {
			st.Reason = "库里没有这个容器的语法，回空列表"
			rows = gamestore.ContainerRows{Container: name, Meta: defaultMeta(name)}
		} else {
			rows = gamestore.ContainerRows{Container: name, Meta: meta}
			st.SrcUser = meta.Username
			st.Reason = "这个号还没这个容器，语法借的"
		}
	default:
		return nil, st, fmt.Errorf("读 %s 的容器 %s 失败: %w", user, name, err)
	}

	meta := rows.Meta
	if meta.Field == "" {
		meta = defaultMeta(name)
		rows.Meta = meta
		st.Reason = "语法行里没写参数表名，用了工坊默认"
	}
	st.Syntax = fmt.Sprintf("args%q/元素类型号 %d", meta.Field, meta.ElemTag+256)

	elems := make([][]byte, 0, len(rows.Items))
	var skipped int
	for _, it := range rows.Items {
		el, err := element(it)
		if err != nil {
			skipped++
			st.Reason = appendReason(st.Reason, "第 "+strconv.Itoa(it.Pos)+" 格读不动: "+err.Error())
			continue
		}
		elems = append(elems, el)
	}
	st.Items = len(elems)
	if skipped > 0 {
		st.Reason = appendReason(st.Reason, strconv.Itoa(skipped)+" 件跳过")
	}

	body, err := wire.Args{Code: meta.Field, Items: wire.RawItems(elems)}.Encode()
	if err != nil {
		return nil, st, fmt.Errorf("container: 容器参数表拼不出来: %w", err)
	}
	frame := append(wire.ResponseHead(cid), body...)
	st.Bytes = len(frame)
	return frame, st, nil
}

// element 把一行的各段拼成一个容器元素。
// 各段存的是 hex 原文（当年从真服那帧拆出来的），长度不对就报出来，别默默拼歪。
func element(it gamestore.ContainerItem) ([]byte, error) {
	head, err := hex.DecodeString(it.Head)
	if err != nil {
		return nil, fmt.Errorf("head 不是十六进制: %w", err)
	}
	if len(head) != headLen {
		return nil, fmt.Errorf("head 要 %d 字节，实际 %d", headLen, len(head))
	}
	guid, err := hex.DecodeString(it.GUID)
	if err != nil {
		return nil, fmt.Errorf("guid 不是十六进制: %w", err)
	}
	if len(guid) != guidLen {
		return nil, fmt.Errorf("guid 要 %d 字节，实际 %d", guidLen, len(guid))
	}
	cond, err := hex.DecodeString(it.CondBits)
	if err != nil || len(cond) != condBits {
		return nil, fmt.Errorf("成色那 4 字节不对: %q", it.CondBits)
	}
	n1, err := hex.DecodeString(it.N1Bits)
	if err != nil || len(n1) != n1BitsLen {
		return nil, fmt.Errorf("另一个 u32 那 4 字节不对: %q", it.N1Bits)
	}
	out := make([]byte, 0, headLen+guidLen+condBits+n1BitsLen+16)
	out = append(out, head...)
	out = append(out, guid...)
	out = append(out, cond...)
	out = append(out, n1...)
	if it.Tag != "" {
		// 那串 u32 是「带名字的 u32 数组」（协议里 marker 07 + 短串名字 + 条数 + 各项）
		arr, err := wire.TypedU32Array{Code: it.Tag, Items: it.Args}.Encode()
		if err != nil {
			return nil, fmt.Errorf("那串 u32 拼不出来: %w", err)
		}
		out = append(out, arr...)
	}
	return out, nil
}

// defaultMeta 借不到语法行时的兜底：工坊 workshop_02 那一组的语法
func defaultMeta(name string) gamestore.ContainerMeta {
	field := "144"
	if name != "workshop_02" {
		// 别的容器名（仓库那种）参数表名不一样，宁可回一个空列表也别拼错名字
		field = "144"
	}
	return gamestore.ContainerMeta{Container: name, Tag: 3, Field: field, ElemTag: 249, Seeded: "代码里的工坊默认语法"}
}

func appendReason(old, add string) string {
	if old == "" {
		return add
	}
	return old + "；" + add
}
