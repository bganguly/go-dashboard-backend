package handler

import (
	"net/http"

	"github.com/bganguly/go-dashboard/internal/service"
	"github.com/gin-gonic/gin"
)

type RegionHandler struct {
	svc *service.RegionService
}

func NewRegionHandler(svc *service.RegionService) *RegionHandler {
	return &RegionHandler{svc: svc}
}

func (h *RegionHandler) List(c *gin.Context) {
	result, err := h.svc.ListRegions(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}
