package itemdef

import (
	"context"

	"rf4server/internal/gamestore"
)

// DefDB 定义表来源（gamestore.MySQL 就有这个方法）。这里只写要的那一个方法，
// 单测造个假表就行，不用起 MySQL。
type DefDB interface {
	ItemDefRows(ctx context.Context) ([]gamestore.ItemDefRow, error)
}

// FromMySQL 把「MySQL static_item_def 整表」接成本包要的 Loader。
// 只有这一个文件认识 gamestore，别的文件只管查表，换数据源不用碰它们。
func FromMySQL(db DefDB) Loader {
	return func(ctx context.Context) ([]Row, error) {
		rows, err := db.ItemDefRows(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]Row, 0, len(rows))
		for _, r := range rows {
			out = append(out, Row{
				Key:       r.Key,
				NumericID: r.NumericID,
				Category:  r.Category,
				Kind:      r.Kind,
				Brand:     r.Brand,
				AttrsJSON: r.AttrsJSON,
			})
		}
		return out, nil
	}
}
