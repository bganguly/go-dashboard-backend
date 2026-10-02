package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/bganguly/go-dashboard/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AggregateService struct {
	db           *pgxpool.Pool
	orderService *OrderService
}

func NewAggregateService(db *pgxpool.Pool, os *OrderService) *AggregateService {
	return &AggregateService{db: db, orderService: os}
}

func (s *AggregateService) GetExactTotal(ctx context.Context,
	from, to, q, status, regionCode string,
	minTotal, maxTotal *float64) (int64, error) {
	return s.orderService.ExactCountUncapped(ctx, q, status, regionCode, from, to, minTotal, maxTotal)
}

func (s *AggregateService) GetDailyAggregates(ctx context.Context,
	from, to, q, status, regionCode string,
	minTotal, maxTotal *float64,
	topCategories int) ([]model.DailyAggregateDTO, error) {

	hasQ := strings.TrimSpace(q) != ""
	isMultiToken := hasQ && strings.Contains(strings.TrimSpace(q), " ")
	hasStatus := strings.TrimSpace(status) != ""
	hasRegion := strings.TrimSpace(regionCode) != ""
	hasTotal := minTotal != nil || maxTotal != nil

	var rows []aggRow
	var err error

	switch {
	case !hasQ && !hasStatus && !hasRegion && !hasTotal:
		rows, err = s.queryDailySummary(ctx, from, to, regionCode)
	case hasQ && isMultiToken && !hasTotal:
		rows, err = s.queryMultiTokenViaCte(ctx, from, to, q, status, regionCode)
		if err == nil && len(rows) == 0 {
			rows, err = s.queryViaSearchText(ctx, from, to, q, status, regionCode, minTotal, maxTotal)
		}
	case hasQ:
		rows, err = s.queryViaSearchText(ctx, from, to, q, status, regionCode, minTotal, maxTotal)
	case hasStatus && !hasRegion && !hasTotal:
		rows, err = s.queryStatusCategorySummary(ctx, from, to, status)
	case (hasStatus || hasRegion) && !hasTotal:
		rows, err = s.queryFilterCategorySummary(ctx, from, to, status, regionCode)
	default:
		rows, err = s.queryOrderCategoryFacts(ctx, from, to, status, regionCode, minTotal, maxTotal)
	}
	if err != nil {
		return nil, err
	}

	if topCategories <= 0 {
		topCategories = 5
	}
	return buildAggregateResult(rows, topCategories), nil
}

type aggRow struct {
	day      string
	category string
	orders   int64
	revenue  float64
	items    int64
}

func (s *AggregateService) queryDailySummary(ctx context.Context, from, to, regionCode string) ([]aggRow, error) {
	where := `WHERE date BETWEEN $1::date AND $2::date`
	args := []any{from, to}
	if regionCode != "" {
		parts := splitTrim(regionCode)
		quoted := make([]string, len(parts))
		for i, c := range parts {
			quoted[i] = fmt.Sprintf("'%s'", strings.ReplaceAll(c, "'", "''"))
		}
		where += ` AND "regionCode" = ANY(ARRAY[` + strings.Join(quoted, ",") + `])`
	}
	sql := `SELECT date::text AS day, "categoryName" AS category,
	               SUM("totalOrders")::bigint AS total_orders, CAST(SUM("totalRevenue") AS FLOAT8) AS total_revenue,
	               SUM("totalItems")::bigint AS total_items
	        FROM daily_summary ` + where + ` GROUP BY date, "categoryName" ORDER BY date`
	return s.queryAggRows(ctx, sql, args...)
}

func (s *AggregateService) queryStatusCategorySummary(ctx context.Context, from, to, status string) ([]aggRow, error) {
	parts := splitTrim(status)
	quoted := make([]string, len(parts))
	for i, st := range parts {
		quoted[i] = fmt.Sprintf("'%s'::\"OrderStatus\"", strings.ReplaceAll(st, "'", "''"))
	}
	sql := `SELECT date::text AS day, "categoryName" AS category,
	               SUM("totalOrders")::bigint AS total_orders, CAST(SUM("totalRevenue") AS FLOAT8) AS total_revenue,
	               SUM("totalItems")::bigint AS total_items
	        FROM daily_status_category_summary
	        WHERE status = ANY(ARRAY[` + strings.Join(quoted, ",") + `])
	        AND date BETWEEN $1::date AND $2::date
	        GROUP BY date, "categoryName" ORDER BY date`
	return s.queryAggRows(ctx, sql, from, to)
}

func (s *AggregateService) queryFilterCategorySummary(ctx context.Context, from, to, status, regionCode string) ([]aggRow, error) {
	var extra []string
	if status != "" {
		parts := splitTrim(status)
		quoted := make([]string, len(parts))
		for i, st := range parts {
			quoted[i] = fmt.Sprintf("'%s'::\"OrderStatus\"", strings.ReplaceAll(st, "'", "''"))
		}
		extra = append(extra, `status = ANY(ARRAY[`+strings.Join(quoted, ",")+`])`)
	}
	if regionCode != "" {
		parts := splitTrim(regionCode)
		quoted := make([]string, len(parts))
		for i, c := range parts {
			quoted[i] = fmt.Sprintf("'%s'", strings.ReplaceAll(c, "'", "''"))
		}
		extra = append(extra, `"regionCode" = ANY(ARRAY[`+strings.Join(quoted, ",")+`])`)
	}
	where := `WHERE date BETWEEN $1::date AND $2::date`
	if len(extra) > 0 {
		where += ` AND ` + strings.Join(extra, ` AND `)
	}
	sql := `SELECT date::text AS day, "categoryName" AS category,
	               SUM("totalOrders")::bigint AS total_orders, CAST(SUM("totalRevenue") AS FLOAT8) AS total_revenue,
	               SUM("totalItems")::bigint AS total_items
	        FROM daily_filter_category_summary ` + where + ` GROUP BY date, "categoryName" ORDER BY date`
	return s.queryAggRows(ctx, sql, from, to)
}

func (s *AggregateService) queryOrderCategoryFacts(ctx context.Context, from, to, status, regionCode string, minTotal, maxTotal *float64) ([]aggRow, error) {
	qa := &queryArgs{}
	fromP := qa.Add(from)
	toP := qa.Add(to)
	var extra []string
	if status != "" {
		parts := splitTrim(status)
		quoted := make([]string, len(parts))
		for i, st := range parts {
			quoted[i] = fmt.Sprintf("'%s'::\"OrderStatus\"", strings.ReplaceAll(st, "'", "''"))
		}
		extra = append(extra, `status = ANY(ARRAY[`+strings.Join(quoted, ",")+`])`)
	}
	if regionCode != "" {
		parts := splitTrim(regionCode)
		quoted := make([]string, len(parts))
		for i, c := range parts {
			quoted[i] = fmt.Sprintf("'%s'", strings.ReplaceAll(c, "'", "''"))
		}
		extra = append(extra, `"regionCode" = ANY(ARRAY[`+strings.Join(quoted, ",")+`])`)
	}
	if minTotal != nil {
		p := qa.Add(*minTotal)
		extra = append(extra, `"orderTotal" >= `+p)
	}
	if maxTotal != nil {
		p := qa.Add(*maxTotal)
		extra = append(extra, `"orderTotal" <= `+p)
	}
	where := `WHERE date BETWEEN ` + fromP + `::date AND ` + toP + `::date`
	if len(extra) > 0 {
		where += ` AND ` + strings.Join(extra, ` AND `)
	}
	sql := `SELECT date::text AS day, "categoryName" AS category,
	               COUNT(DISTINCT "orderId")::bigint AS total_orders, CAST(SUM("totalRevenue") AS FLOAT8) AS total_revenue,
	               SUM("totalItems")::bigint AS total_items
	        FROM order_category_facts ` + where + ` GROUP BY date, "categoryName" ORDER BY date`
	return s.queryAggRows(ctx, sql, qa.Args()...)
}

func (s *AggregateService) queryViaSearchText(ctx context.Context, from, to, q, status, regionCode string, minTotal, maxTotal *float64) ([]aggRow, error) {
	qa := &queryArgs{}
	fromP := qa.Add(from)
	toP := qa.Add(to)
	tokens := strings.Fields(strings.TrimSpace(q))
	var clauses []string
	for _, tok := range tokens {
		p := qa.Add("%" + tok + "%")
		clauses = append(clauses, `o.search_text ILIKE `+p)
	}
	if status != "" {
		parts := splitTrim(status)
		quoted := make([]string, len(parts))
		for i, st := range parts {
			quoted[i] = fmt.Sprintf("'%s'::\"OrderStatus\"", strings.ReplaceAll(st, "'", "''"))
		}
		clauses = append(clauses, `o.status = ANY(ARRAY[`+strings.Join(quoted, ",")+`])`)
	}
	if regionCode != "" {
		parts := splitTrim(regionCode)
		quoted := make([]string, len(parts))
		for i, c := range parts {
			quoted[i] = fmt.Sprintf("'%s'", strings.ReplaceAll(c, "'", "''"))
		}
		clauses = append(clauses, `r.code = ANY(ARRAY[`+strings.Join(quoted, ",")+`])`)
	}
	if minTotal != nil {
		p := qa.Add(*minTotal)
		clauses = append(clauses, `CAST(o.total AS FLOAT8) >= `+p)
	}
	if maxTotal != nil {
		p := qa.Add(*maxTotal)
		clauses = append(clauses, `CAST(o.total AS FLOAT8) <= `+p)
	}
	where := `WHERE o."placedAt"::date BETWEEN ` + fromP + `::date AND ` + toP + `::date`
	if len(clauses) > 0 {
		where += ` AND ` + strings.Join(clauses, ` AND `)
	}
	sql := `SELECT o."placedAt"::date::text AS day, cat.name AS category,
	               COUNT(DISTINCT o.id)::bigint AS total_orders,
	               COALESCE(CAST(SUM(oi.quantity * CAST(oi."unitPrice" AS FLOAT8) * (1 - CAST(oi.discount AS FLOAT8))) AS FLOAT8), 0) AS total_revenue,
	               COALESCE(SUM(oi.quantity), 0)::bigint AS total_items
	        FROM orders o
	        JOIN customers c ON c.id = o."customerId"
	        JOIN regions r ON r.id = o."regionId"
	        JOIN order_items oi ON oi."orderId" = o.id
	        JOIN products p ON p.id = oi."productId"
	        JOIN categories cat ON cat.id = p."categoryId"
	        ` + where + ` GROUP BY o."placedAt"::date, cat.name ORDER BY o."placedAt"::date`
	return s.queryAggRows(ctx, sql, qa.Args()...)
}

func (s *AggregateService) queryMultiTokenViaCte(ctx context.Context, from, to, q, status, regionCode string) ([]aggRow, error) {
	qa := &queryArgs{}
	fromP := qa.Add(from)
	toP := qa.Add(to)
	tokens := strings.Fields(strings.TrimSpace(q))
	var tokenClauses []string
	for _, tok := range tokens {
		p := qa.Add("%" + tok + "%")
		tokenClauses = append(tokenClauses, `("firstName" || ' ' || "lastName") ILIKE `+p)
	}
	var extra []string
	if status != "" {
		parts := splitTrim(status)
		quoted := make([]string, len(parts))
		for i, st := range parts {
			quoted[i] = fmt.Sprintf("'%s'::\"OrderStatus\"", strings.ReplaceAll(st, "'", "''"))
		}
		extra = append(extra, `dcs.status = ANY(ARRAY[`+strings.Join(quoted, ",")+`])`)
	}
	if regionCode != "" {
		parts := splitTrim(regionCode)
		quoted := make([]string, len(parts))
		for i, c := range parts {
			quoted[i] = fmt.Sprintf("'%s'", strings.ReplaceAll(c, "'", "''"))
		}
		extra = append(extra, `dcs."regionCode" = ANY(ARRAY[`+strings.Join(quoted, ",")+`])`)
	}
	extraWhere := ""
	if len(extra) > 0 {
		extraWhere = ` AND ` + strings.Join(extra, ` AND `)
	}
	sql := `WITH matching_customers AS (
	  SELECT id FROM customers WHERE ` + strings.Join(tokenClauses, ` AND `) + `
	)
	SELECT dcs.date::text AS day, dcs."categoryName" AS category,
	       SUM(dcs."totalOrders")::bigint AS total_orders,
	       CAST(SUM(dcs."totalRevenue") AS FLOAT8) AS total_revenue,
	       SUM(dcs."totalItems")::bigint AS total_items
	FROM daily_customer_category_summary dcs
	WHERE dcs."customerId" IN (SELECT id FROM matching_customers)
	AND dcs.date BETWEEN ` + fromP + `::date AND ` + toP + `::date` +
		extraWhere + ` GROUP BY dcs.date, dcs."categoryName" ORDER BY dcs.date`
	return s.queryAggRows(ctx, sql, qa.Args()...)
}

func (s *AggregateService) queryAggRows(ctx context.Context, sql string, args ...any) ([]aggRow, error) {
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []aggRow
	for rows.Next() {
		var r aggRow
		if err := rows.Scan(&r.day, &r.category, &r.orders, &r.revenue, &r.items); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func buildAggregateResult(rows []aggRow, topN int) []model.DailyAggregateDTO {
	type dayEntry struct {
		cats map[string][3]float64 // [orders, revenue, items]
	}
	byDay := map[string]*dayEntry{}
	dayOrder := []string{}

	for _, r := range rows {
		if _, ok := byDay[r.day]; !ok {
			byDay[r.day] = &dayEntry{cats: map[string][3]float64{}}
			dayOrder = append(dayOrder, r.day)
		}
		prev := byDay[r.day].cats[r.category]
		byDay[r.day].cats[r.category] = [3]float64{
			prev[0] + float64(r.orders),
			prev[1] + r.revenue,
			prev[2] + float64(r.items),
		}
	}

	catTotals := map[string]float64{}
	for _, de := range byDay {
		for cat, v := range de.cats {
			catTotals[cat] += v[0]
		}
	}
	type kv struct{ k string; v float64 }
	sorted := make([]kv, 0, len(catTotals))
	for k, v := range catTotals {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].v > sorted[j].v })
	topSet := map[string]bool{}
	for i, kv := range sorted {
		if i >= topN {
			break
		}
		topSet[kv.k] = true
	}

	result := make([]model.DailyAggregateDTO, 0, len(dayOrder))
	for _, day := range dayOrder {
		de := byDay[day]
		cats := map[string]model.CategoryAggregateDTO{}
		var othO, othR, othI float64
		for cat, v := range de.cats {
			o, r, i := v[0], v[1], v[2]
			if topSet[cat] {
				avg := 0.0
				if o > 0 {
					avg = r / o
				}
				cats[cat] = model.CategoryAggregateDTO{
					TotalOrders:   int64(o),
					TotalRevenue:  r,
					TotalItems:    int64(i),
					AvgOrderValue: math.Round(avg*100) / 100,
				}
			} else {
				othO += o; othR += r; othI += i
			}
		}
		if othO > 0 {
			avg := 0.0
			if othO > 0 {
				avg = othR / othO
			}
			cats["Others"] = model.CategoryAggregateDTO{
				TotalOrders:   int64(othO),
				TotalRevenue:  othR,
				TotalItems:    int64(othI),
				AvgOrderValue: math.Round(avg*100) / 100,
			}
		}
		var totO, totR, totI float64
		for _, v := range cats {
			totO += float64(v.TotalOrders)
			totR += v.TotalRevenue
			totI += float64(v.TotalItems)
		}
		result = append(result, model.DailyAggregateDTO{
			Date:       day,
			Categories: cats,
			Totals:     model.TotalsDTO{TotalOrders: int64(totO), TotalRevenue: totR, TotalItems: int64(totI)},
		})
	}
	return result
}
