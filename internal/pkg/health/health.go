// Package health 提供 Kubernetes 健康检查端点的语义化分层实现。
//
// 设计原则（2026-04-21）：
//
//	/health   —— 兼容旧 probe 配置，等价于 /livez（只要进程活着就返回 200）
//	/livez    —— liveness 探针：永远返回 200，除非进程死锁 / 主协程退出
//	            —— 用于 K8s livenessProbe，失败意味着 Pod 需要重启
//	/readyz   —— readiness 探针：浅层 Ping DB + Redis，失败返回 503
//	            —— 用于 K8s readinessProbe，失败意味着 Pod 从 Service/ALB upstream 摘除
//	            —— Pod 不会被重启，依赖恢复后自动回到服务
//
// 这样做的关键收益：
//  1. DB/Redis 瞬时抖动 → 只摘流量不重启 Pod（避免连锁重启）
//  2. Pod 半死状态（依赖不可用）时不再接流量 → 用户看到的是其他健康 Pod 的响应，不是 502
//  3. liveness 不检查依赖 → 不会因 DB 抖动触发 CrashLoopBackOff
package health

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/logger"
)

// probeTimeout 单次依赖 Ping 的超时（必须远小于 K8s readiness timeoutSeconds）
const probeTimeout = 1500 * time.Millisecond

// State 本进程的运行状态快照（原子），由 Set* 函数切换
type State struct {
	dbHealthy    atomic.Bool
	redisHealthy atomic.Bool
}

// Global 全局健康状态，供 ready handler 读取 + 后台探针写入
var Global = &State{}

// LivenessHandler 返回仅检查进程存活的 handler
// 不依赖 DB/Redis，只要主协程能处理请求就返回 200
func LivenessHandler(role string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
			"role":   role,
			"mode":   "liveness",
		})
	}
}

// ReadinessHandler 返回浅层检查 DB + Redis 可用性的 handler
// 任一失败返回 503，K8s 据此将 Pod 从 Service/ALB 摘除
//
// 参数 db/rds 可为 nil（部分角色不依赖 DB/Redis），nil 组件视为 healthy
func ReadinessHandler(role string, db *gorm.DB, rds *goredis.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), probeTimeout)
		defer cancel()

		status := gin.H{
			"role":  role,
			"mode":  "readiness",
			"db":    "skipped",
			"redis": "skipped",
		}
		httpStatus := http.StatusOK

		if db != nil {
			if err := pingDB(ctx, db); err != nil {
				status["db"] = "unhealthy: " + err.Error()
				httpStatus = http.StatusServiceUnavailable
				Global.dbHealthy.Store(false)
				if logger.L != nil {
					logger.L.Warn("readyz: db ping failed", zap.Error(err))
				}
			} else {
				status["db"] = "ok"
				Global.dbHealthy.Store(true)
			}
		}

		if rds != nil {
			if err := rds.Ping(ctx).Err(); err != nil {
				status["redis"] = "unhealthy: " + err.Error()
				httpStatus = http.StatusServiceUnavailable
				Global.redisHealthy.Store(false)
				if logger.L != nil {
					logger.L.Warn("readyz: redis ping failed", zap.Error(err))
				}
			} else {
				status["redis"] = "ok"
				Global.redisHealthy.Store(true)
			}
		}

		if httpStatus == http.StatusOK {
			status["status"] = "ready"
		} else {
			status["status"] = "not_ready"
		}
		c.JSON(httpStatus, status)
	}
}

// pingDB 通过真实 SQL 查询探测 DB 可用性。
//
// 重要：这里不使用 sqlDB.PingContext()，因为后者在连接池中存在已建立
// 连接时可能只做本地健康检查（不真正往 DB 发包），无法发现"MySQL 进程
// 已停止但 TCP 尚未 RST"的半死状态。改用 `SELECT 1` 强制走完整的 SQL
// 往返，捕获 `invalid connection` / `i/o timeout` 等真实 DB 故障。
func pingDB(ctx context.Context, db *gorm.DB) error {
	var n int
	return db.WithContext(ctx).Raw("SELECT 1").Scan(&n).Error
}

// IsReady 供外部代码查询（如 graceful shutdown 判断是否还有流量）
func (s *State) IsReady() bool {
	return s.dbHealthy.Load() && s.redisHealthy.Load()
}
