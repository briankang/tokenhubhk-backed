// Package metrics 提供轻量级 Prometheus 指标暴露。
// 不依赖 prometheus/client_golang，直接输出 Prometheus text exposition format。
// 适用于小项目，避免引入额外依赖。
package metrics

import (
	"fmt"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Counters 全局请求计数器
var (
	HTTPRequestsTotal   atomic.Int64 // HTTP 请求总数
	HTTPRequestsErrors  atomic.Int64 // HTTP 5xx 错误数
	StreamActiveCount   atomic.Int64 // 当前活跃流式连接
	TasksPublished      atomic.Int64 // 已发布的异步任务数
	TasksCompleted      atomic.Int64 // 已完成的异步任务数
	TasksFailed         atomic.Int64 // 失败的异步任务数
	startTime           = time.Now()
)

// RequestCounterMiddleware Gin 中间件，统计 HTTP 请求数和错误数
func RequestCounterMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		HTTPRequestsTotal.Add(1)
		c.Next()
		if c.Writer.Status() >= 500 {
			HTTPRequestsErrors.Add(1)
		}
	}
}

// Handler 返回 Prometheus text format 的 /metrics 端点处理器
func Handler(db *gorm.DB, redis *goredis.Client, role string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w := c.Writer

		// 服务信息
		fmt.Fprintf(w, "# HELP tokenhub_info Service information\n")
		fmt.Fprintf(w, "# TYPE tokenhub_info gauge\n")
		fmt.Fprintf(w, "tokenhub_info{role=\"%s\",version=\"1.0.0\"} 1\n\n", role)

		// 运行时间
		fmt.Fprintf(w, "# HELP tokenhub_uptime_seconds Service uptime in seconds\n")
		fmt.Fprintf(w, "# TYPE tokenhub_uptime_seconds gauge\n")
		fmt.Fprintf(w, "tokenhub_uptime_seconds{role=\"%s\"} %.0f\n\n", role, time.Since(startTime).Seconds())

		// HTTP 请求统计
		fmt.Fprintf(w, "# HELP tokenhub_http_requests_total Total HTTP requests\n")
		fmt.Fprintf(w, "# TYPE tokenhub_http_requests_total counter\n")
		fmt.Fprintf(w, "tokenhub_http_requests_total{role=\"%s\"} %d\n\n", role, HTTPRequestsTotal.Load())

		fmt.Fprintf(w, "# HELP tokenhub_http_errors_total Total HTTP 5xx errors\n")
		fmt.Fprintf(w, "# TYPE tokenhub_http_errors_total counter\n")
		fmt.Fprintf(w, "tokenhub_http_errors_total{role=\"%s\"} %d\n\n", role, HTTPRequestsErrors.Load())

		// 异步任务统计
		fmt.Fprintf(w, "# HELP tokenhub_tasks_published_total Total async tasks published\n")
		fmt.Fprintf(w, "# TYPE tokenhub_tasks_published_total counter\n")
		fmt.Fprintf(w, "tokenhub_tasks_published_total %d\n\n", TasksPublished.Load())

		fmt.Fprintf(w, "# HELP tokenhub_tasks_completed_total Total async tasks completed\n")
		fmt.Fprintf(w, "# TYPE tokenhub_tasks_completed_total counter\n")
		fmt.Fprintf(w, "tokenhub_tasks_completed_total %d\n\n", TasksCompleted.Load())

		fmt.Fprintf(w, "# HELP tokenhub_tasks_failed_total Total async tasks failed\n")
		fmt.Fprintf(w, "# TYPE tokenhub_tasks_failed_total counter\n")
		fmt.Fprintf(w, "tokenhub_tasks_failed_total %d\n\n", TasksFailed.Load())

		// Go 运行时
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		fmt.Fprintf(w, "# HELP go_goroutines Number of goroutines\n")
		fmt.Fprintf(w, "# TYPE go_goroutines gauge\n")
		fmt.Fprintf(w, "go_goroutines %d\n\n", runtime.NumGoroutine())

		fmt.Fprintf(w, "# HELP go_memstats_alloc_bytes Current bytes allocated\n")
		fmt.Fprintf(w, "# TYPE go_memstats_alloc_bytes gauge\n")
		fmt.Fprintf(w, "go_memstats_alloc_bytes %d\n\n", mem.Alloc)

		fmt.Fprintf(w, "# HELP go_memstats_sys_bytes Total bytes of memory obtained from the OS\n")
		fmt.Fprintf(w, "# TYPE go_memstats_sys_bytes gauge\n")
		fmt.Fprintf(w, "go_memstats_sys_bytes %d\n\n", mem.Sys)

		// 数据库连接池
		if db != nil {
			if sqlDB, err := db.DB(); err == nil {
				stats := sqlDB.Stats()
				fmt.Fprintf(w, "# HELP tokenhub_db_open_connections Current open DB connections\n")
				fmt.Fprintf(w, "# TYPE tokenhub_db_open_connections gauge\n")
				fmt.Fprintf(w, "tokenhub_db_open_connections %d\n\n", stats.OpenConnections)

				fmt.Fprintf(w, "# HELP tokenhub_db_in_use DB connections currently in use\n")
				fmt.Fprintf(w, "# TYPE tokenhub_db_in_use gauge\n")
				fmt.Fprintf(w, "tokenhub_db_in_use %d\n\n", stats.InUse)

				fmt.Fprintf(w, "# HELP tokenhub_db_idle DB connections idle\n")
				fmt.Fprintf(w, "# TYPE tokenhub_db_idle gauge\n")
				fmt.Fprintf(w, "tokenhub_db_idle %d\n\n", stats.Idle)

				fmt.Fprintf(w, "# HELP tokenhub_db_wait_count Total number of connections waited for\n")
				fmt.Fprintf(w, "# TYPE tokenhub_db_wait_count counter\n")
				fmt.Fprintf(w, "tokenhub_db_wait_count %d\n\n", stats.WaitCount)

				fmt.Fprintf(w, "# HELP tokenhub_db_max_open Maximum open connections\n")
				fmt.Fprintf(w, "# TYPE tokenhub_db_max_open gauge\n")
				fmt.Fprintf(w, "tokenhub_db_max_open %d\n\n", stats.MaxOpenConnections)
			}
		}

		// Redis 连接池
		if redis != nil {
			poolStats := redis.PoolStats()
			fmt.Fprintf(w, "# HELP tokenhub_redis_hits Redis pool hits\n")
			fmt.Fprintf(w, "# TYPE tokenhub_redis_hits counter\n")
			fmt.Fprintf(w, "tokenhub_redis_hits %d\n\n", poolStats.Hits)

			fmt.Fprintf(w, "# HELP tokenhub_redis_misses Redis pool misses\n")
			fmt.Fprintf(w, "# TYPE tokenhub_redis_misses counter\n")
			fmt.Fprintf(w, "tokenhub_redis_misses %d\n\n", poolStats.Misses)

			fmt.Fprintf(w, "# HELP tokenhub_redis_total_conns Redis total connections\n")
			fmt.Fprintf(w, "# TYPE tokenhub_redis_total_conns gauge\n")
			fmt.Fprintf(w, "tokenhub_redis_total_conns %d\n\n", poolStats.TotalConns)

			fmt.Fprintf(w, "# HELP tokenhub_redis_idle_conns Redis idle connections\n")
			fmt.Fprintf(w, "# TYPE tokenhub_redis_idle_conns gauge\n")
			fmt.Fprintf(w, "tokenhub_redis_idle_conns %d\n\n", poolStats.IdleConns)
		}

		c.Status(http.StatusOK)
	}
}
