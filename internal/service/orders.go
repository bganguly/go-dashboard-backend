package service

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/bganguly/go-dashboard/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
	countCap        = 10_000
	countSentinel   = int64(countCap + 1)
	isoFmt          = "2006-01-02T15:04:05.000Z"
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

	t0 := time.Now()
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

	result, err := s.toResult(ctx, orderRows, page, pageSize, total, totalPages, approximate)
	log.Printf("[orders] ListOrders total=%dms q=%q from=%s to=%s page=%d", time.Since(t0).Milliseconds(), q, from, to, page)
	return result, err
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

// ExactCount — capped count used by list endpoints.
func (s *OrderService) exactCount(ctx context.Context,
	q, status, regionCode, from, to string,
	minTotal, maxTotal *float64) (int64, error) {

	log.Printf("[count] exactCount entered")
	cacheKey := buildCountCacheKey(q, status, regionCode, from, to, minTotal, maxTotal)

	// count_cache first — single PK lookup (~1ms on hit). Covers every code
	// path below including rollup results written on the previous call.
	tRC := time.Now()
	hit, rcErr := s.readCountCache(ctx, cacheKey)
	log.Printf("[count] readCountCache took %dms err=%v", time.Since(tRC).Milliseconds(), rcErr)
	if rcErr == nil {
		log.Printf("[count] cache HIT key=%s val=%d", cacheKey, hit)
		return hit, nil
	}

	log.Printf("[count] cache MISS key=%s", cacheKey)
	qa := &queryArgs{}
	where, needsRegionJoin := buildOrderWhere(q, status, regionCode, from, to, minTotal, maxTotal, qa)
	regionJoin := ""
	if needsRegionJoin {
		regionJoin = `JOIN regions r ON r.id = o."regionId" `
	}

	t0 := time.Now()
	if hasShortToken(q) {
		capArg := qa.Add(countSentinel)
		cappedSQL := fmt.Sprintf(`SELECT COUNT(*) FROM (SELECT 1 FROM orders o %s%s LIMIT %s) _cap`,
			regionJoin, where, capArg)
		var capped int64
		if err := s.db.QueryRow(ctx, cappedSQL, qa.Args()...).Scan(&capped); err != nil {
			return 0, err
		}
		log.Printf("[count] capped COUNT %dms val=%d (sentinel=%v)", time.Since(t0).Milliseconds(), capped, capped >= countSentinel)
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
	log.Printf("[count] full COUNT %dms val=%d", time.Since(t0).Milliseconds(), n)
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
	if hit, err := s.readCountCache(ctx, cacheKey); err == nil {
		return hit, nil
	}

	qa := &queryArgs{}
	where, needsRegionJoin := buildOrderWhere(q, status, regionCode, from, to, minTotal, maxTotal, qa)
	regionJoin := ""
	if needsRegionJoin {
		regionJoin = `JOIN regions r ON r.id = o."regionId" `
	}

	countSQL := `SELECT COUNT(*) FROM orders o ` + regionJoin + where
	var n int64
	if err := s.db.QueryRow(ctx, countSQL, qa.Args()...).Scan(&n); err != nil {
		return 0, err
	}
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

	// Invalidate count_cache rows that cover all orders (q="") or whose q token
	// appears in the new order's search_text.
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

// --- helpers ---

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

func buildOrderWhere(q, status, regionCode, from, to string,
	minTotal, maxTotal *float64, qa *queryArgs) (string, bool) {

	var clauses []string
	needsRegionJoin := false

	if q != "" {
		q = strings.TrimSpace(q)
		tokens := strings.Fields(q)
		for _, tok := range tokens {
			p := qa.Add("%" + tok + "%")
			clauses = append(clauses, `o.search_text ILIKE `+p)
		}
	}
	if status != "" {
		parts := splitTrim(status)
		quoted := make([]string, len(parts))
		for i, s := range parts {
			quoted[i] = fmt.Sprintf("'%s'::\"OrderStatus\"", strings.ReplaceAll(s, "'", "''"))
		}
		clauses = append(clauses, `o.status = ANY(ARRAY[`+strings.Join(quoted, ",")+`])`)
	}
	if regionCode != "" {
		needsRegionJoin = true
		parts := splitTrim(regionCode)
		quoted := make([]string, len(parts))
		for i, c := range parts {
			quoted[i] = fmt.Sprintf("'%s'", strings.ReplaceAll(c, "'", "''"))
		}
		clauses = append(clauses, `r.code = ANY(ARRAY[`+strings.Join(quoted, ",")+`])`)
	}
	if from != "" {
		p := qa.Add(from)
		clauses = append(clauses, `o."placedAt" >= `+p+`::timestamptz`)
	}
	if to != "" {
		p := qa.Add(to)
		clauses = append(clauses, `o."placedAt" <= (`+p+`::date + interval '1 day' - interval '1 second')`)
	}
	if minTotal != nil {
		p := qa.Add(*minTotal)
		clauses = append(clauses, `o.total >= `+p)
	}
	if maxTotal != nil {
		p := qa.Add(*maxTotal)
		clauses = append(clauses, `o.total <= `+p)
	}

	if len(clauses) == 0 {
		return "", needsRegionJoin
	}
	return "WHERE " + strings.Join(clauses, " AND "), needsRegionJoin
}

func buildCountCacheKey(q, status, regionCode, from, to string, minTotal, maxTotal *float64) string {
	minStr, maxStr := "", ""
	if minTotal != nil {
		minStr = fmt.Sprintf("%g", *minTotal)
	}
	if maxTotal != nil {
		maxStr = fmt.Sprintf("%g", *maxTotal)
	}
	return fmt.Sprintf("q=%s&status=%s&regionCode=%s&from=%s&to=%s&minTotal=%s&maxTotal=%s",
		strings.ToLower(strings.TrimSpace(q)), status, regionCode, from, to, minStr, maxStr)
}

func buildOrderBy(sort, dir string) string {
	switch sort {
	case "customer":
		return `c."firstName" ` + dir + `, c."lastName" ` + dir + `, o."placedAt" DESC`
	case "total":
		return `o.total ` + dir + `, o."placedAt" DESC`
	case "status":
		return `o.status ` + dir + `, o."placedAt" DESC`
	case "id":
		return `o.id ` + dir
	default:
		return `o."placedAt" ` + dir
	}
}

func flipOrderBy(ob string) string {
	ob = strings.ReplaceAll(ob, " DESC", "\x00")
	ob = strings.ReplaceAll(ob, " ASC", " DESC")
	ob = strings.ReplaceAll(ob, "\x00", " ASC")
	return ob
}

func safeOrderSort(s string) string {
	switch s {
	case "placedAt", "total", "status", "customer", "id":
		return s
	}
	return "placedAt"
}

func safeOrderDir(d string) string {
	if strings.EqualFold(d, "asc") {
		return "ASC"
	}
	return "DESC"
}

func hasShortToken(q string) bool {
	for _, t := range strings.Fields(q) {
		if len(t) < 3 {
			return true
		}
	}
	return false
}

func splitTrim(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
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
