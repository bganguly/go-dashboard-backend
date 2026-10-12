package service

import (
	"fmt"
	"strings"
)

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
