package handler

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/bganguly/go-dashboard/internal/model"
	"github.com/bganguly/go-dashboard/internal/service"
	"github.com/gin-gonic/gin"
)

type OrderHandler struct {
	svc *service.OrderService
}

func NewOrderHandler(svc *service.OrderService) *OrderHandler {
	return &OrderHandler{svc: svc}
}

func (h *OrderHandler) List(c *gin.Context) {
	q := c.Query("q")
	page := queryInt(c, "page", 0) + 1
	pageSize := queryInt(c, "size", 20)
	sortRaw := queryStr(c, "sort", "placedAt")
	sort, dir := parseSortParam(sortRaw, queryStr(c, "dir", "desc"))
	status := c.Query("status")
	regionCode := c.Query("regionCode")
	from := c.Query("from")
	to := c.Query("to")
	minTotal := queryFloat(c, "minTotal")
	maxTotal := queryFloat(c, "maxTotal")

	cursorIDStr := c.Query("cursorId")
	cursorPlacedAt := c.Query("cursorPlacedAt")
	cursorDir := c.Query("cursorDir")

	useCursor := cursorIDStr != "" && cursorPlacedAt != "" &&
		sort == "placedAt" && (dir == "desc" || dir == "")
	if useCursor {
		cursorID, err := strconv.Atoi(cursorIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cursorId"})
			return
		}
		forward := cursorDir != "prev"
		result, err := h.svc.ListOrdersByCursor(c.Request.Context(),
			q, page, pageSize, status, regionCode, from, to, minTotal, maxTotal,
			cursorID, cursorPlacedAt, forward)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
		return
	}

	log.Printf("[run /api/orders from api explorer] raw=%q page=%d size=%d sort=%q dir=%q q=%q status=%q regionCode=%q from=%q to=%q",
		c.Request.URL.RawQuery, page, pageSize, sort, dir, q, status, regionCode, from, to)
	result, err := h.svc.ListOrders(c.Request.Context(),
		q, page, pageSize, sort, dir, status, regionCode, from, to, minTotal, maxTotal)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *OrderHandler) Count(c *gin.Context) {
	q := c.Query("q")
	status := c.Query("status")
	regionCode := c.Query("regionCode")
	from := c.Query("from")
	to := c.Query("to")
	minTotal := queryFloat(c, "minTotal")
	maxTotal := queryFloat(c, "maxTotal")

	total, err := h.svc.ExactCountUncapped(c.Request.Context(),
		q, status, regionCode, from, to, minTotal, maxTotal)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"total": total})
}

func (h *OrderHandler) Create(c *gin.Context) {
	var req model.CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	result, err := h.svc.CreateOrder(c.Request.Context(), req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, result)
}

func queryInt(c *gin.Context, key string, def int) int {
	v := c.Query(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func queryStr(c *gin.Context, key, def string) string {
	v := c.Query(key)
	if v == "" {
		return def
	}
	return v
}

func parseSortParam(raw, fallbackDir string) (string, string) {
	if i := strings.LastIndex(raw, ","); i != -1 {
		return raw[:i], raw[i+1:]
	}
	return raw, fallbackDir
}

func queryFloat(c *gin.Context, key string) *float64 {
	v := c.Query(key)
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil
	}
	return &f
}
