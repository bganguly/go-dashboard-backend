package service

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/bganguly/go-dashboard/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
	countCap        = 10_000
	countSentinel   = int64(countCap + 1)
)

type OrderService struct {
	db *pgxpool.Pool
}

func NewOrderService(db *pgxpool.Pool) *OrderService {
	return &OrderService{db: db}
}

func IsApproximateCount(n int64) bool { return n == countSentinel }
func AdjustCount(n int64) int64 {
	if n == countSentinel {
		return countCap
	}
	return n
}

// ListOrders — offset-based pagination with optional reverse-scan for last page.
func (s *OrderService) ListOrders(ctx context.Context,
	q string, page, pageSize int, sort, dir string,
	status, regionCode, from, to string,
	minTotal, maxTotal *float64) (model.OrderListResult, error) {

	pageSize = clamp(pageSize, 1, maxPageSize)
	page = max1(page)

	safeSort := safeOrderSort(sort)
	safeDir := safeOrderDir(dir)

	qa := &queryArgs{}
	where, needsRegionJoin := buildOrderWhere(q, status, regionCode, from, to, minTotal, maxTotal, qa)

	rawTotal, err := s.exactCount(ctx, q, status, regionCode, from, to, minTotal, maxTotal)
	if err != nil {
		return model.OrderListResult{}, err
	}
	approximate := rawTotal == countSentinel
	total := AdjustCount(rawTotal)
	totalPages := int(math.Ceil(float64(total) / float64(pageSize)))

	orderBy := buildOrderBy(safeSort, safeDir)

	useReverseScan := totalPages > 1 && page == totalPages
	limit := pageSize
	offset := (page - 1) * pageSize
	effectiveOrderBy := orderBy
	if useReverseScan {
		limit = int(total) - (totalPages-1)*pageSize
		offset = 0
		effectiveOrderBy = flipOrderBy(orderBy)
	}

	_ = needsRegionJoin
	regionJoin := ""
	if needsRegionJoin {
		regionJoin = ""
	}
	_ = regionJoin

	lp := qa.Add(limit)
	op := qa.Add(offset)
	dataSql := `SELECT o.id, o.status, o.total, o.currency, o.notes, o."placedAt",
	       c.id AS c_id, c.email, c."firstName", c."lastName", c.phone,
	       r.id AS r_id, r.code AS r_code, r.name AS r_name
	FROM orders o
	JOIN customers c ON c.id = o."customerId"
	JOIN regions r ON r.id = o."regionId"
	` + where + " ORDER BY " + effectiveOrderBy + " LIMIT " + lp + " OFFSET " + op

	rows, err := s.db.Query(ctx, dataSql, qa.Args()...)
	if err != nil {
		return model.OrderListResult{}, err
	}
	orderRows, err := collectOrderRows(rows)
	if err != nil {
		return model.OrderListResult{}, err
	}
	if useReverseScan {
		reverseSlice(orderRows)
	}

	return s.toResult(ctx, orderRows, page, pageSize, total, totalPages, approximate)
}

// ListOrdersByCursor — keyset pagination for default placedAt DESC sort.
func (s *OrderService) ListOrdersByCursor(ctx context.Context,
	q string, page, pageSize int,
	status, regionCode, from, to string,
	minTotal, maxTotal *float64,
	cursorID int, cursorPlacedAt string, forward bool) (model.OrderListResult, error) {

	pageSize = clamp(pageSize, 1, maxPageSize)

	qa := &queryArgs{}
	where, _ := buildOrderWhere(q, status, regionCode, from, to, minTotal, maxTotal, qa)

	rawTotal, err := s.exactCount(ctx, q, status, regionCode, from, to, minTotal, maxTotal)
	if err != nil {
		return model.OrderListResult{}, err
	}
	approximate := rawTotal == countSentinel
	total := AdjustCount(rawTotal)
	totalPages := int(math.Ceil(float64(total) / float64(pageSize)))

	cpArg := qa.Add(cursorPlacedAt)
	ciArg := qa.Add(cursorID)
	var cursorClause string
	if forward {
		cursorClause = fmt.Sprintf(`(o."placedAt", o.id) < (%s::timestamptz, %s)`, cpArg, ciArg)
	} else {
		cursorClause = fmt.Sprintf(`(o."placedAt", o.id) > (%s::timestamptz, %s)`, cpArg, ciArg)
	}

	combinedWhere := where
	if combinedWhere == "" {
		combinedWhere = "WHERE " + cursorClause
	} else {
		combinedWhere += " AND " + cursorClause
	}

	var orderBy string
	if forward {
		orderBy = `o."placedAt" DESC, o.id DESC`
	} else {
		orderBy = `o."placedAt" ASC, o.id ASC`
	}

	lp := qa.Add(pageSize)
	dataSql := `SELECT o.id, o.status, o.total, o.currency, o.notes, o."placedAt",
	       c.id AS c_id, c.email, c."firstName", c."lastName", c.phone,
	       r.id AS r_id, r.code AS r_code, r.name AS r_name
	FROM orders o
	JOIN customers c ON c.id = o."customerId"
	JOIN regions r ON r.id = o."regionId"
	` + combinedWhere + " ORDER BY " + orderBy + " LIMIT " + lp

	rows, err := s.db.Query(ctx, dataSql, qa.Args()...)
	if err != nil {
		return model.OrderListResult{}, err
	}
	orderRows, err := collectOrderRows(rows)
	if err != nil {
		return model.OrderListResult{}, err
	}
	if !forward {
		reverseSlice(orderRows)
	}

	return s.toResult(ctx, orderRows, page, pageSize, total, totalPages, approximate)
}

// exactCount — capped count used by list endpoints.
func (s *OrderService) exactCount(ctx context.Context,
	q, status, regionCode, from, to string,
	minTotal, maxTotal *float64) (int64, error) {

	cacheKey := buildCountCacheKey(q, status, regionCode, from, to, minTotal, maxTotal)

	hit, rcErr := s.readCountCache(ctx, cacheKey)
	if rcErr == nil {
		return hit, nil
	}
	qa := &queryArgs{}
	where, needsRegionJoin := buildOrderWhere(q, status, regionCode, from, to, minTotal, maxTotal, qa)
	regionJoin := ""
	if needsRegionJoin {
		regionJoin = `JOIN regions r ON r.id = o."regionId" `
	}

	if hasShortToken(q) {
		capArg := qa.Add(countSentinel)
		cappedSQL := fmt.Sprintf(`SELECT COUNT(*) FROM (SELECT 1 FROM orders o %s%s LIMIT %s) _cap`,
			regionJoin, where, capArg)
		var capped int64
		if err := s.db.QueryRow(ctx, cappedSQL, qa.Args()...).Scan(&capped); err != nil {
			return 0, err
		}
		if capped < countSentinel {
			_ = s.writeCountCache(ctx, cacheKey, capped)
		}
		return capped, nil
	}

	countSQL := `SELECT COUNT(*) FROM orders o ` + regionJoin + where
	var n int64
	if err := s.db.QueryRow(ctx, countSQL, qa.Args()...).Scan(&n); err != nil {
		return 0, err
	}
	if err := s.writeCountCache(ctx, cacheKey, n); err != nil {
		log.Printf("[count] writeCountCache err: %v", err)
	}
	return n, nil
}

// ExactCountUncapped — used by GET /api/orders/count; always writes to count_cache.
func (s *OrderService) ExactCountUncapped(ctx context.Context,
	q, status, regionCode, from, to string,
	minTotal, maxTotal *float64) (int64, error) {

	cacheKey := buildCountCacheKey(q, status, regionCode, from, to, minTotal, maxTotal)
	t0 := time.Now()
	if hit, err := s.readCountCache(ctx, cacheKey); err == nil {
		log.Printf("[COUNT] count_cache HIT key=%s in %dms", cacheKey, time.Since(t0).Milliseconds())
		return hit, nil
	}
	log.Printf("[COUNT] count_cache MISS key=%s lookup=%dms", cacheKey, time.Since(t0).Milliseconds())

	qa := &queryArgs{}
	where, needsRegionJoin := buildOrderWhere(q, status, regionCode, from, to, minTotal, maxTotal, qa)
	regionJoin := ""
	if needsRegionJoin {
		regionJoin = `JOIN regions r ON r.id = o."regionId" `
	}

	countSQL := `SELECT COUNT(*) FROM orders o ` + regionJoin + where
	var n int64
	t1 := time.Now()
	if err := s.db.QueryRow(ctx, countSQL, qa.Args()...).Scan(&n); err != nil {
		return 0, err
	}
	log.Printf("[COUNT] COUNT(*) done in %dms result=%d", time.Since(t1).Milliseconds(), n)
	_ = s.writeCountCache(ctx, cacheKey, n)
	return n, nil
}

// CreateOrder — inserts order + items, updates daily_order_count, invalidates cache.
func (s *OrderService) CreateOrder(ctx context.Context, req model.CreateOrderRequest) (map[string]any, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var customerExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM customers WHERE id = $1)`, req.CustomerID).Scan(&customerExists); err != nil || !customerExists {
		return nil, fmt.Errorf("customer not found: %d", req.CustomerID)
	}
	var regionExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM regions WHERE id = $1)`, req.RegionID).Scan(&regionExists); err != nil || !regionExists {
		return nil, fmt.Errorf("region not found: %d", req.RegionID)
	}

	currency := req.Currency
	if currency == "" {
		currency = "USD"
	}

	var orderID int
	var placedAt time.Time
	err = tx.QueryRow(ctx,
		`INSERT INTO orders ("customerId","regionId",currency,notes,status,total,"placedAt","updatedAt")
		 VALUES ($1,$2,$3,$4,'PENDING',0,NOW(),NOW()) RETURNING id,"placedAt"`,
		req.CustomerID, req.RegionID, currency, nilStr(req.Notes),
	).Scan(&orderID, &placedAt)
	if err != nil {
		return nil, err
	}

	var grandTotal float64
	for _, item := range req.Items {
		var productExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM products WHERE id = $1)`, item.ProductID).Scan(&productExists); err != nil || !productExists {
			return nil, fmt.Errorf("product not found: %d", item.ProductID)
		}
		discount := item.Discount
		lineTotal := item.UnitPrice * float64(item.Quantity) * (1 - discount)
		grandTotal += lineTotal
		_, err = tx.Exec(ctx,
			`INSERT INTO order_items ("orderId","productId",quantity,"unitPrice",discount)
			 VALUES ($1,$2,$3,$4,$5)`,
			orderID, item.ProductID, item.Quantity, item.UnitPrice, discount)
		if err != nil {
			return nil, err
		}
	}

	if _, err := tx.Exec(ctx,
		`UPDATE orders SET total=$1,"updatedAt"=NOW() WHERE id=$2`,
		grandTotal, orderID); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO daily_order_count (date,"totalOrders") VALUES ($1::date,1)
		 ON CONFLICT (date) DO UPDATE SET "totalOrders"=daily_order_count."totalOrders"+1`,
		placedAt.Format("2006-01-02")); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	go func() {
		bgCtx := context.Background()
		var searchText *string
		_ = s.db.QueryRow(bgCtx,
			`SELECT search_text FROM orders WHERE id = $1`, orderID).Scan(&searchText)
		_, _ = s.db.Exec(bgCtx,
			`DELETE FROM count_cache WHERE
			 substring(cache_key from 'q=([^&]*)') = ''
			 OR ($1::text IS NOT NULL AND $1::text ILIKE '%' || substring(cache_key from 'q=([^&]*)') || '%')`,
			searchText)
	}()

	return map[string]any{
		"id":       orderID,
		"status":   "PENDING",
		"total":    grandTotal,
		"placedAt": placedAt.UTC().Format(isoFmt),
	}, nil
}
