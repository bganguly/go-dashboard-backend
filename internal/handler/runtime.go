package handler

import (
	"net/http"
	"os"

	"github.com/bganguly/go-dashboard/internal/service"
	"github.com/gin-gonic/gin"
)

type RuntimeHandler struct {
	stats *service.StatsService
}

func NewRuntimeHandler(stats *service.StatsService) *RuntimeHandler {
	return &RuntimeHandler{stats: stats}
}

func (h *RuntimeHandler) Runtime(c *gin.Context) {
	runtime := os.Getenv("BACKEND_RUNTIME")
	if runtime == "" {
		runtime = "go"
	}
	c.JSON(http.StatusOK, gin.H{"runtime": runtime})
}

func (h *RuntimeHandler) Status(c *gin.Context) {
	runtime := os.Getenv("BACKEND_RUNTIME")
	if runtime == "" {
		runtime = "go"
	}
	c.JSON(http.StatusOK, gin.H{
		"runtime":    runtime,
		"typesense":  false,
		"searchMode": "postgres-ilike",
	})
}

func (h *RuntimeHandler) SeedStats(c *gin.Context) {
	stats, err := h.stats.GetSeedStats(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, stats)
}
