package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bganguly/go-dashboard/internal/model"
	"github.com/jackc/pgx/v5"
)

const isoFmt = "2006-01-02T15:04:05.000Z"

type orderRow struct {
	id        int
	status    string
	total     float64
	currency  string
	notes     *string
	placedAt  time.Time
	cID       int
	email     string
	firstName string
	lastName  string
	phone     *string
	rID       int
	rCode     string
	rName     string
}

func collectOrderRows(rows pgx.Rows) ([]orderRow, error) {
	defer rows.Close()
	var out []orderRow
	for rows.Next() {
		var r orderRow
		if err := rows.Scan(&r.id, &r.status, &r.total, &r.currency, &r.notes, &r.placedAt,
			&r.cID, &r.email, &r.firstName, &r.lastName, &r.phone,
			&r.rID, &r.rCode, &r.rName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *OrderService) toResult(ctx context.Context, rows []orderRow, page, pageSize int,
	total int64, totalPages int, approximate bool) (model.OrderListResult, error) {

	if len(rows) == 0 {
		return model.OrderListResult{
			Data: []model.OrderDTO{}, Page: page, PageSize: pageSize,
			Total: total, TotalPages: totalPages, Approximate: approximate,
		}, nil
	}

	ids := make([]int, len(rows))
	for i, r := range rows {
		ids[i] = r.id
	}
	itemsByOrder, err := s.fetchItems(ctx, ids)
	if err != nil {
		return model.OrderListResult{}, err
	}

	data := make([]model.OrderDTO, len(rows))
	for i, r := range rows {
		items := itemsByOrder[r.id]
		if items == nil {
			items = []model.OrderItemDTO{}
		}
		data[i] = model.OrderDTO{
			ID:       r.id,
			Status:   r.status,
			Total:    r.total,
			Currency: r.currency,
			Notes:    r.notes,
			PlacedAt: r.placedAt.UTC().Format(isoFmt),
			Customer: model.CustomerSummaryDTO{ID: r.cID, Email: r.email, FirstName: r.firstName, LastName: r.lastName},
			Region:   model.RegionDTO{ID: r.rID, Code: r.rCode, Name: r.rName},
			Items:    items,
		}
	}
	return model.OrderListResult{
		Data: data, Page: page, PageSize: pageSize,
		Total: total, TotalPages: totalPages, Approximate: approximate,
	}, nil
}

func (s *OrderService) fetchItems(ctx context.Context, orderIDs []int) (map[int][]model.OrderItemDTO, error) {
	if len(orderIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(orderIDs))
	args := make([]any, len(orderIDs))
	for i, id := range orderIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	sql := `SELECT oi.id, oi."orderId", oi."productId", oi.quantity, oi."unitPrice", oi.discount,
	               p.sku, p.name AS p_name
	        FROM order_items oi
	        JOIN products p ON p.id = oi."productId"
	        WHERE oi."orderId" = ANY(ARRAY[` + strings.Join(placeholders, ",") + `]::int[])`

	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := map[int][]model.OrderItemDTO{}
	for rows.Next() {
		var (
			id, orderID, productID, qty int
			unitPrice, discount         float64
			sku, pName                  string
		)
		if err := rows.Scan(&id, &orderID, &productID, &qty, &unitPrice, &discount, &sku, &pName); err != nil {
			return nil, err
		}
		result[orderID] = append(result[orderID], model.OrderItemDTO{
			ID:        id,
			ProductID: productID,
			Quantity:  qty,
			UnitPrice: unitPrice,
			Discount:  discount,
			Product:   model.ProductSummaryDTO{ID: productID, SKU: sku, Name: pName},
		})
	}
	return result, rows.Err()
}

func (s *OrderService) readCountCache(ctx context.Context, key string) (int64, error) {
	var total int64
	err := s.db.QueryRow(ctx,
		`SELECT total FROM count_cache WHERE cache_key=$1 AND cached_at > NOW() - INTERVAL '30 days'`,
		key).Scan(&total)
	return total, err
}

func (s *OrderService) writeCountCache(ctx context.Context, key string, total int64) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO count_cache (cache_key,total,cached_at) VALUES ($1,$2,NOW())
		 ON CONFLICT (cache_key) DO UPDATE SET total=$2, cached_at=NOW()`,
		key, total)
	return err
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max1(v int) int {
	if v < 1 {
		return 1
	}
	return v
}

func reverseSlice[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func nilStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
