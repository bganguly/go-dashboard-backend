package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/bganguly/go-dashboard/internal/cache"
	internaldmb "github.com/bganguly/go-dashboard/internal/db"
	"github.com/bganguly/go-dashboard/internal/handler"
	appMigrate "github.com/bganguly/go-dashboard/internal/migrate"
	"github.com/bganguly/go-dashboard/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	ctx := context.Background()
	pool, err := internaldmb.NewPool(ctx)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()

	migrationsDir := resolveMigrationsDir()
	if err := appMigrate.Run(pool, migrationsDir); err != nil {
		log.Fatalf("migrations: %v", err)
	}

	aggCache := cache.NewAggregatesCache()
	orderSvc := service.NewOrderService(pool, aggCache)
	aggSvc := service.NewAggregateService(pool, orderSvc)
	customerSvc := service.NewCustomerService(pool)
	regionSvc := service.NewRegionService(pool)
	statsSvc := service.NewStatsService(pool)

	orderH := handler.NewOrderHandler(orderSvc)
	aggH := handler.NewAggregateHandler(aggSvc, aggCache)
	customerH := handler.NewCustomerHandler(customerSvc)
	regionH := handler.NewRegionHandler(regionSvc)
	runtimeH := handler.NewRuntimeHandler(statsSvc)

	r := gin.Default()

	allowOrigin := os.Getenv("CORS_ORIGIN")
	if allowOrigin == "" {
		allowOrigin = "http://localhost:5173"
	}
	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", allowOrigin)
		c.Header("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type,Authorization")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	})

	api := r.Group("/api")
	{
		api.GET("/runtime", runtimeH.Runtime)
		api.GET("/status", runtimeH.Status)
		api.GET("/seed-stats", runtimeH.SeedStats)

		api.GET("/orders", orderH.List)
		api.POST("/orders", orderH.Create)
		api.GET("/orders/count", orderH.Count)

		api.GET("/aggregates", aggH.Get)
		api.GET("/customers", customerH.List)
		api.GET("/regions", regionH.List)
	}

	// Cache warmup — runs in background, doesn't block startup.
	go warmupCache(ctx, aggSvc, aggCache)
	go warmupCountCache(ctx, pool)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("go-dashboard-backend listening on :%s (Go %s, GOMAXPROCS=%d)",
		port, runtime.Version(), runtime.GOMAXPROCS(0))
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func resolveMigrationsDir() string {
	if d := os.Getenv("MIGRATIONS_DIR"); d != "" {
		return d
	}
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), "../../migrations")
}

const defaultFrom = "2020-01-01"

func warmupCache(ctx context.Context, aggSvc *service.AggregateService, aggCache *cache.AggregatesCache) {
	time.Sleep(2 * time.Second)
	now := time.Now()
	to := now.Format("2006-01-02")

	// Warm the same date ranges Spring Boot warms: 30/90/180/365d rolling + all-time.
	// The React frontend sends topCategories=4 with each of these from-dates; warming
	// them here ensures the first real request hits the cache instead of going to the DB.
	froms := []string{
		now.AddDate(0, 0, -30).Format("2006-01-02"),
		now.AddDate(0, 0, -90).Format("2006-01-02"),
		now.AddDate(0, 0, -180).Format("2006-01-02"),
		now.AddDate(0, 0, -365).Format("2006-01-02"),
		defaultFrom,
	}

	for _, from := range froms {
		key := cache.Key(from, to, 4)
		if _, ok := aggCache.Get(key); ok {
			continue
		}
		data, err := aggSvc.GetDailyAggregates(ctx, from, to, "", "", "", nil, nil, 4)
		if err != nil {
			log.Printf("[warmup] aggregates err from=%s: %v", from, err)
			continue
		}
		total, err := aggSvc.GetExactTotal(ctx, from, to, "", "", "", nil, nil)
		if err != nil {
			log.Printf("[warmup] total err from=%s: %v", from, err)
			continue
		}
		aggCache.Put(key, map[string]any{
			"data":                   data,
			"totalOrders":            service.AdjustCount(total),
			"totalOrdersApproximate": service.IsApproximateCount(total),
		})
		log.Printf("[warmup] cached from=%s to=%s topN=4", from, to)
	}
}

func warmupCountCache(ctx context.Context, pool *pgxpool.Pool) {
	time.Sleep(3 * time.Second)
	rows, err := pool.Query(ctx,
		`WITH recent AS (
		   SELECT c."firstName", c."lastName"
		   FROM orders o JOIN customers c ON c.id = o."customerId"
		   ORDER BY o."placedAt" DESC LIMIT 40
		 ) SELECT DISTINCT "firstName", "lastName" FROM recent`)
	if err != nil {
		return
	}
	defer rows.Close()

	seen := map[string]bool{}
	var tokens []string
	for rows.Next() {
		var first, last string
		if err := rows.Scan(&first, &last); err != nil {
			continue
		}
		for _, t := range []string{strings.ToLower(strings.TrimSpace(first)), strings.ToLower(strings.TrimSpace(last))} {
			if t != "" && !seen[t] {
				seen[t] = true
				tokens = append(tokens, t)
			}
		}
	}
	rows.Close()

	to := time.Now().Format("2006-01-02")
	for _, tok := range tokens {
		key := fmt.Sprintf("q=%s&status=&regionCode=&from=%s&to=%s&minTotal=&maxTotal=", tok, defaultFrom, to)
		var existing int64
		err := pool.QueryRow(ctx,
			`SELECT total FROM count_cache WHERE cache_key=$1 AND cached_at > NOW() - INTERVAL '30 days'`,
			key).Scan(&existing)
		if err == nil {
			continue
		}
		var count int64
		if err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM orders o
			 WHERE o."placedAt" >= $1::timestamptz
			   AND o."placedAt" <= ($2::date + interval '1 day' - interval '1 second')
			   AND o.search_text ILIKE $3`,
			defaultFrom, to, "%"+tok+"%").Scan(&count); err != nil {
			continue
		}
		_, _ = pool.Exec(ctx,
			`INSERT INTO count_cache (cache_key,total,cached_at) VALUES ($1,$2,NOW())
			 ON CONFLICT (cache_key) DO UPDATE SET total=$2, cached_at=NOW()`,
			key, count)
	}
}


