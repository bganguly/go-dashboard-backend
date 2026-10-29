package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DatasetBoundsHandler struct {
	pool *pgxpool.Pool
}

func NewDatasetBoundsHandler(pool *pgxpool.Pool) *DatasetBoundsHandler {
	return &DatasetBoundsHandler{pool: pool}
}

func (h *DatasetBoundsHandler) Get(c *gin.Context) {
	var from, to *string
	err := h.pool.QueryRow(
		context.Background(),
		`SELECT MIN("placedAt")::date::text, MAX("placedAt")::date::text FROM orders`,
	).Scan(&from, &to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	f, t := "", ""
	if from != nil {
		f = *from
	}
	if to != nil {
		t = *to
	}
	c.JSON(http.StatusOK, gin.H{"from": f, "to": t})
}
