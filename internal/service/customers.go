package service

import (
	"context"
	"strings"

	"github.com/bganguly/go-dashboard/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CustomerService struct {
	db *pgxpool.Pool
}

func NewCustomerService(db *pgxpool.Pool) *CustomerService {
	return &CustomerService{db: db}
}

func (s *CustomerService) ListCustomers(ctx context.Context,
	cursor *int, limit int, q string, regionID *int) (model.CustomerListResult, error) {

	limit = clamp(limit, 1, 100)
	qa := &queryArgs{}
	var clauses []string

	if cursor != nil {
		p := qa.Add(*cursor)
		clauses = append(clauses, `c.id > `+p)
	}
	if strings.TrimSpace(q) != "" {
		p := qa.Add("%" + strings.TrimSpace(q) + "%")
		clauses = append(clauses, `(c."firstName" || ' ' || c."lastName" || ' ' || c.email) ILIKE `+p)
	}
	if regionID != nil {
		p := qa.Add(*regionID)
		clauses = append(clauses, `c."regionId" = `+p)
	}

	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	lp := qa.Add(limit + 1)

	sql := `SELECT c.id, c.email, c."firstName", c."lastName", c.phone, c."createdAt",
	               r.id AS r_id, r.code, r.name AS r_name
	        FROM customers c
	        JOIN regions r ON r.id = c."regionId"
	        ` + where + ` ORDER BY c.id LIMIT ` + lp

	rows, err := s.db.Query(ctx, sql, qa.Args()...)
	if err != nil {
		return model.CustomerListResult{}, err
	}
	defer rows.Close()

	var data []model.CustomerDTO
	for rows.Next() {
		var (
			id, rID           int
			email, fn, ln     string
			phone, createdAt  *string
			rCode, rName      string
		)
		if err := rows.Scan(&id, &email, &fn, &ln, &phone, &createdAt, &rID, &rCode, &rName); err != nil {
			return model.CustomerListResult{}, err
		}
		data = append(data, model.CustomerDTO{
			ID:        id,
			Email:     email,
			FirstName: fn,
			LastName:  ln,
			Phone:     phone,
			Region:    model.RegionDTO{ID: rID, Code: rCode, Name: rName},
			CreatedAt: createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return model.CustomerListResult{}, err
	}

	hasMore := len(data) > limit
	if hasMore {
		data = data[:limit]
	}
	var nextCursor *int
	if hasMore && len(data) > 0 {
		id := data[len(data)-1].ID
		nextCursor = &id
	}
	if data == nil {
		data = []model.CustomerDTO{}
	}
	return model.CustomerListResult{Data: data, NextCursor: nextCursor, HasMore: hasMore}, nil
}
