package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/bganguly/go-dashboard/internal/cache"
	internaldmb "github.com/bganguly/go-dashboard/internal/db"
	"github.com/bganguly/go-dashboard/internal/handler"
	appMigrate "github.com/bganguly/go-dashboard/internal/migrate"
	"github.com/bganguly/go-dashboard/internal/service"
	"github.com/gin-gonic/gin"
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

	// Migrations and cache warmup run in background so Cloud Run startup probe
	// sees the port bound immediately. The DB schema is already live from Spring
	// Boot; these migrations add Go-specific tables and backfill derived data.
	go func() {
		if err := appMigrate.Run(pool, migrationsDir); err != nil {
			log.Printf("migrations: %v", err)
		} else {
			log.Printf("migrations: complete")
			go warmupCache(ctx, aggSvc, aggCache)
		}
	}()

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

func warmupCache(ctx context.Context, aggSvc *service.AggregateService, aggCache *cache.AggregatesCache) {
	time.Sleep(2 * time.Second)
	now := time.Now()
	ranges := [][2]string{
		{now.AddDate(0, -1, 0).Format("2006-01-02"), now.Format("2006-01-02")},
		{now.AddDate(0, -3, 0).Format("2006-01-02"), now.Format("2006-01-02")},
		{now.AddDate(0, -6, 0).Format("2006-01-02"), now.Format("2006-01-02")},
		{now.AddDate(-1, 0, 0).Format("2006-01-02"), now.Format("2006-01-02")},
	}
	for _, r := range ranges {
		from, to := r[0], r[1]
		key := cache.Key(from, to, 5)
		if _, ok := aggCache.Get(key); ok {
			continue
		}
		data, err := aggSvc.GetDailyAggregates(ctx, from, to, "", "", "", nil, nil, 5)
		if err != nil {
			continue
		}
		total, err := aggSvc.GetExactTotal(ctx, from, to, "", "", "", nil, nil)
		if err != nil {
			continue
		}
		aggCache.Put(key, map[string]any{
			"data":                   data,
			"totalOrders":            service.AdjustCount(total),
			"totalOrdersApproximate": service.IsApproximateCount(total),
		})
	}
}

func resolveMigrationsDir() string {
	if d := os.Getenv("MIGRATIONS_DIR"); d != "" {
		return d
	}
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), "../../migrations")
}

