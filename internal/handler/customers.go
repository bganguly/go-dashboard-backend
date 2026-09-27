package handler

import (
	"net/http"
	"strconv"

	"github.com/bganguly/go-dashboard/internal/service"
	"github.com/gin-gonic/gin"
)

type CustomerHandler struct {
	svc *service.CustomerService
}

func NewCustomerHandler(svc *service.CustomerService) *CustomerHandler {
	return &CustomerHandler{svc: svc}
}

func (h *CustomerHandler) List(c *gin.Context) {
	var cursor *int
	if v := c.Query("cursor"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cursor"})
			return
		}
		cursor = &n
	}
	limit := queryInt(c, "limit", 20)
	q := c.Query("q")
	var regionID *int
	if v := c.Query("regionId"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid regionId"})
			return
		}
		regionID = &n
	}

	result, err := h.svc.ListCustomers(c.Request.Context(), cursor, limit, q, regionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}
