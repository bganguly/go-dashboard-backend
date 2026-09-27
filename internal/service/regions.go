package service

import (
	"context"
	"sort"

	"github.com/bganguly/go-dashboard/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RegionService struct {
	db *pgxpool.Pool
}

func NewRegionService(db *pgxpool.Pool) *RegionService {
	return &RegionService{db: db}
}

func (s *RegionService) ListRegions(ctx context.Context) ([]model.RegionDTO, error) {
	rows, err := s.db.Query(ctx, `SELECT id, code, name FROM regions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.RegionDTO
	for rows.Next() {
		var r model.RegionDTO
		if err := rows.Scan(&r.ID, &r.Code, &r.Name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	if out == nil {
		out = []model.RegionDTO{}
	}
	return out, nil
}
