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

	internaldmb "github.com/bganguly/go-dashboard/internal/db"
	"github.com/bganguly/go-dashboard/internal/handler"
	appMigrate "github.com/bganguly/go-dashboard/internal/migrate"
	"github.com/bganguly/go-dashboard/internal/service"
	gzip "github.com/gin-contrib/gzip"
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

	orderSvc := service.NewOrderService(pool)
	aggSvc := service.NewAggregateService(pool, orderSvc)
	aggCache := service.NewAggregatesCache()
	customerSvc := service.NewCustomerService(pool)
	regionSvc := service.NewRegionService(pool)
	statsSvc := service.NewStatsService(pool)

	orderH := handler.NewOrderHandler(orderSvc)
	aggH := handler.NewAggregateHandler(aggSvc, aggCache)
	customerH := handler.NewCustomerHandler(customerSvc)
	regionH := handler.NewRegionHandler(regionSvc)
	runtimeH := handler.NewRuntimeHandler(statsSvc)

	r := gin.Default()
	r.Use(gzip.Gzip(gzip.DefaultCompression))

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

	go warmupCountCache(ctx, pool)
	go warmupAggregatesCache(ctx, aggSvc, aggCache)

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

func warmupAggregatesCache(ctx context.Context, svc *service.AggregateService, cache *service.AggregatesCache) {
	time.Sleep(2 * time.Second)
	now := time.Now()
	ranges := [][2]string{
		{now.AddDate(0, 0, -30).Format("2006-01-02"), now.Format("2006-01-02")},
		{now.AddDate(0, 0, -60).Format("2006-01-02"), now.Format("2006-01-02")},
		{now.AddDate(0, 0, -90).Format("2006-01-02"), now.Format("2006-01-02")},
		{now.AddDate(0, 0, -180).Format("2006-01-02"), now.Format("2006-01-02")},
		{now.AddDate(-1, 0, 0).Format("2006-01-02"), now.Format("2006-01-02")},
	}
	for _, topN := range []int{3, 5} {
		for _, r := range ranges {
			from, to := r[0], r[1]
			ck := service.AggregateCacheKey(from, to, topN)
			if _, ok := cache.Get(ck); ok {
				continue
			}
			data, err := svc.GetDailyAggregates(ctx, from, to, "", "", "", nil, nil, topN)
			if err != nil {
				continue
			}
			total, err := svc.GetExactTotal(ctx, from, to, "", "", "", nil, nil)
			if err != nil {
				continue
			}
			cache.Put(ck, map[string]any{
				"data":                   data,
				"totalOrders":            service.AdjustCount(total),
				"totalOrdersApproximate": service.IsApproximateCount(total),
			})
		}
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


