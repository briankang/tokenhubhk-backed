package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/response"

	"gorm.io/gorm"
)

// setupInitialized 缓存初始化状态，避免每次请求都查询数据库
var (
	setupInitialized bool
	setupMu          sync.RWMutex
	setupLastCheck   time.Time
)

// SetupGuard 安装守卫中间件
// - 未初始化时：只允许访问 /api/v1/setup/* 和 /health 等公开路径
// - 已初始化后：/api/v1/setup/* 中除了 status 以外的端点返回 403
func SetupGuard(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// 始终放行健康检查和静态资源
		if path == "/health" || !strings.HasPrefix(path, "/api/") {
			c.Next()
			return
		}

		// setup/status 始终放行（前端需要随时查询初始化状态）
		if path == "/api/v1/setup/status" {
			c.Next()
			return
		}

		initialized := checkSetupInitialized(db)

		isSetupPath := strings.HasPrefix(path, "/api/v1/setup/")

		if !initialized {
			// 未初始化：只允许 setup 路径
			if isSetupPath {
				c.Next()
				return
			}
			// 非 setup 路径返回 503，提示需要先完成初始化
			response.ErrorMsg(c, http.StatusServiceUnavailable, 503, "系统尚未初始化，请先完成安装向导")
			c.Abort()
			return
		}

		// 已初始化：拒绝 setup 路径（status 除外，已在上面放行）
		if isSetupPath {
			response.ErrorMsg(c, http.StatusForbidden, 403, "系统已完成初始化")
			c.Abort()
			return
		}

		c.Next()
	}
}

// checkSetupInitialized 检查系统是否已初始化（带缓存，30秒刷新一次）
func checkSetupInitialized(db *gorm.DB) bool {
	setupMu.RLock()
	// 已确认初始化的状态是永久的，不再查询
	if setupInitialized {
		setupMu.RUnlock()
		return true
	}
	// 30秒内不重复查询数据库
	if time.Since(setupLastCheck) < 30*time.Second {
		result := setupInitialized
		setupMu.RUnlock()
		return result
	}
	setupMu.RUnlock()

	// 查询数据库
	setupMu.Lock()
	defer setupMu.Unlock()

	// 双重检查
	if setupInitialized {
		return true
	}

	var cfg model.SystemConfig
	err := db.Where("`key` = ?", "initialized").First(&cfg).Error
	if err == nil && cfg.Value == "true" {
		setupInitialized = true
	}
	setupLastCheck = time.Now()
	return setupInitialized
}

// ResetSetupCache 重置安装状态缓存（用于测试）
func ResetSetupCache() {
	setupMu.Lock()
	defer setupMu.Unlock()
	setupInitialized = false
	setupLastCheck = time.Time{}
}
