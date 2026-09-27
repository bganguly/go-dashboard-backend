package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type StatsService struct {
	db *pgxpool.Pool
}

func NewStatsService(db *pgxpool.Pool) *StatsService {
	return &StatsService{db: db}
}

func (s *StatsService) GetSeedStats(ctx context.Context) (map[string]any, error) {
	var customers, products int64
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM customers`).Scan(&customers); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM products`).Scan(&products); err != nil {
		return nil, err
	}
	return map[string]any{
		"customerCount": customers,
		"productCount":  products,
	}, nil
}
