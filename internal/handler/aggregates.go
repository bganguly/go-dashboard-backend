package handler

import (
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/bganguly/go-dashboard/internal/service"
	"github.com/gin-gonic/gin"
)

type AggregateHandler struct {
	svc   *service.AggregateService
	cache *service.AggregatesCache
}

func NewAggregateHandler(svc *service.AggregateService, cache *service.AggregatesCache) *AggregateHandler {
	return &AggregateHandler{svc: svc, cache: cache}
}

func (h *AggregateHandler) Get(c *gin.Context) {
	handlerStart := time.Now()
	host, _ := os.Hostname()
	log.Printf("[AGG] request instance=%s", host)
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

	noFilters := q == "" && status == "" && regionCode == "" && minTotal == nil && maxTotal == nil

	if noFilters && includeData && includeTotal {
		ck := service.AggregateCacheKey(from, to, topCategories)
		if cached, ok := h.cache.Get(ck); ok {
			log.Printf("[AGG] cache HIT key=%s total=%dms", ck, time.Since(handlerStart).Milliseconds())
			c.JSON(http.StatusOK, cached)
			return
		}
		log.Printf("[AGG] cache MISS key=%s", ck)
	}

	type dataResult struct {
		v   any
		err error
	}
	dataCh := make(chan dataResult, 1)
	totalCh := make(chan dataResult, 1)

	var wg sync.WaitGroup
	if includeData {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t0 := time.Now()
			v, err := h.svc.GetDailyAggregates(c.Request.Context(),
				from, to, q, status, regionCode, minTotal, maxTotal, topCategories)
			log.Printf("[AGG] GetDailyAggregates done in %dms err=%v", time.Since(t0).Milliseconds(), err)
			dataCh <- dataResult{v, err}
		}()
	}
	if includeTotal {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t0 := time.Now()
			v, err := h.svc.GetExactTotal(c.Request.Context(),
				from, to, q, status, regionCode, minTotal, maxTotal)
			log.Printf("[AGG] GetExactTotal done in %dms err=%v", time.Since(t0).Milliseconds(), err)
			totalCh <- dataResult{v, err}
		}()
	}
	wg.Wait()
	log.Printf("[AGG] parallel queries done total=%dms", time.Since(handlerStart).Milliseconds())

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

	if noFilters && includeData && includeTotal {
		h.cache.Put(service.AggregateCacheKey(from, to, topCategories), map[string]any(body))
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
