package admin

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/response"
	cachesvc "tokenhub-server/internal/service/cache"
)

// CacheHandler 缓存管理接口处理器（管理员专用）
type CacheHandler struct {
	cacheSvc *cachesvc.CacheService
	warmer   *cachesvc.CacheWarmer
}

// NewCacheHandler 创建缓存管理处理器实例
func NewCacheHandler(cacheSvc *cachesvc.CacheService, warmer *cachesvc.CacheWarmer) *CacheHandler {
	return &CacheHandler{cacheSvc: cacheSvc, warmer: warmer}
}

// Register 注册缓存管理路由到管理员路由组
func (h *CacheHandler) Register(rg *gin.RouterGroup) {
	cache := rg.Group("/cache")
	{
		cache.POST("/clear-all", h.ClearAll)
		cache.POST("/clear/:prefix", h.ClearByPrefix)
		cache.POST("/warm", h.Warm)
		cache.GET("/stats", h.Stats)
	}
}

// ClearAll 清除所有接口缓存
// POST /api/v1/admin/cache/clear-all
func (h *CacheHandler) ClearAll(c *gin.Context) {
	ctx := c.Request.Context()
	deleted, err := h.cacheSvc.ClearAll(ctx)
	if err != nil {
		logger.L.Error("清除所有缓存失败", zap.Error(err))
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "清除缓存失败: "+err.Error())
		return
	}

	logger.L.Info("管理员清除所有缓存", zap.Int64("deleted", deleted))
	response.Success(c, gin.H{
		"deleted": deleted,
		"message": "所有缓存已清除",
	})
}

// ClearByPrefix 按前缀清除缓存
// POST /api/v1/admin/cache/clear/:prefix
// prefix 示例: suppliers, models, pricings, channel-groups, payment-methods
func (h *CacheHandler) ClearByPrefix(c *gin.Context) {
	prefix := c.Param("prefix")
	if prefix == "" {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "prefix 参数不能为空")
		return
	}

	// 构建缓存匹配模式
	pattern := "cache:*" + prefix + "*"

	ctx := c.Request.Context()
	deleted, err := h.cacheSvc.DeleteByPattern(ctx, pattern)
	if err != nil {
		logger.L.Error("按前缀清除缓存失败", zap.String("prefix", prefix), zap.Error(err))
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "清除缓存失败: "+err.Error())
		return
	}

	logger.L.Info("管理员按前缀清除缓存",
		zap.String("prefix", prefix),
		zap.String("pattern", pattern),
		zap.Int64("deleted", deleted))

	response.Success(c, gin.H{
		"prefix":  prefix,
		"pattern": pattern,
		"deleted": deleted,
		"message": "缓存已清除",
	})
}

// Warm 手动触发缓存预热
// POST /api/v1/admin/cache/warm
func (h *CacheHandler) Warm(c *gin.Context) {
	ctx := context.Background()

	// 异步执行预热，但等待完成
	h.warmer.WarmAll(ctx)

	logger.L.Info("管理员手动触发缓存预热")
	response.Success(c, gin.H{
		"message": "缓存预热完成",
	})
}

// Stats 获取缓存统计信息
// GET /api/v1/admin/cache/stats
func (h *CacheHandler) Stats(c *gin.Context) {
	ctx := c.Request.Context()
	stats, err := h.cacheSvc.GetStats(ctx)
	if err != nil {
		logger.L.Error("获取缓存统计失败", zap.Error(err))
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "获取统计失败: "+err.Error())
		return
	}

	response.Success(c, stats)
}
