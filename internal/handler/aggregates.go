package handler

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/bganguly/go-dashboard/internal/service"
	"github.com/gin-gonic/gin"
)

type AggregateHandler struct {
	svc *service.AggregateService
}

func NewAggregateHandler(svc *service.AggregateService) *AggregateHandler {
	return &AggregateHandler{svc: svc}
}

func (h *AggregateHandler) Get(c *gin.Context) {
	from := c.Query("from")
	to := c.Query("to")
	if from == "" || to == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "from and to are required"})
		return
	}
	q := c.Query("q")
	status := c.Query("status")
	regionCode := c.Query("regionCode")
	minTotal := queryFloat(c, "minTotal")
	maxTotal := queryFloat(c, "maxTotal")
	topCategories := queryInt(c, "topCategories", 5)
	includeData := queryBool(c, "includeData", true)
	includeTotal := queryBool(c, "includeTotal", true)

	type dataResult struct {
		v   any
		err error
	}
	dataCh := make(chan dataResult, 1)
	totalCh := make(chan dataResult, 1)

	t0 := time.Now()
	var wg sync.WaitGroup
	if includeData {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tData := time.Now()
			v, err := h.svc.GetDailyAggregates(c.Request.Context(),
				from, to, q, status, regionCode, minTotal, maxTotal, topCategories)
			log.Printf("[agg] GetDailyAggregates %dms", time.Since(tData).Milliseconds())
			dataCh <- dataResult{v, err}
		}()
	}
	if includeTotal {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tTotal := time.Now()
			v, err := h.svc.GetExactTotal(c.Request.Context(),
				from, to, q, status, regionCode, minTotal, maxTotal)
			log.Printf("[agg] GetExactTotal %dms", time.Since(tTotal).Milliseconds())
			totalCh <- dataResult{v, err}
		}()
	}
	wg.Wait()
	log.Printf("[agg] total handler %dms", time.Since(t0).Milliseconds())

	body := gin.H{}
	if includeData {
		r := <-dataCh
		if r.err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": r.err.Error()})
			return
		}
		body["data"] = r.v
	}
	if includeTotal {
		r := <-totalCh
		if r.err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": r.err.Error()})
			return
		}
		raw := r.v.(int64)
		body["totalOrders"] = service.AdjustCount(raw)
		body["totalOrdersApproximate"] = service.IsApproximateCount(raw)
	}

	c.JSON(http.StatusOK, body)
}

func queryBool(c *gin.Context, key string, def bool) bool {
	v := c.Query(key)
	if v == "" {
		return def
	}
	return v != "false" && v != "0"
}
