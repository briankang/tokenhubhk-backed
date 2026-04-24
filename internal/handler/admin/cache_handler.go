package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/response"
	cachesvc "tokenhub-server/internal/service/cache"
	"tokenhub-server/internal/service/usercache"
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
		cache.POST("/clear-channel-routes", h.ClearChannelRouteCache)
		cache.POST("/clear-user-cache", h.ClearUserCache)
		cache.POST("/clear-stats-cache", h.ClearStatsCache)
		cache.POST("/clear-public-cache", h.ClearPublicCache)
		cache.POST("/clear-pricing-cache", h.ClearPricingCache)
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

// ClearChannelRouteCache 一键清理渠道路由缓存
// POST /api/v1/admin/cache/clear-channel-routes
// 清理 custom_channel:* 和 custom_channel_routes:* 两类缓存
func (h *CacheHandler) ClearChannelRouteCache(c *gin.Context) {
	ctx := c.Request.Context()
	var totalDeleted int64

	// 清理 custom_channel:* 缓存（渠道配置）
	d1, err1 := h.cacheSvc.DeleteByPattern(ctx, "custom_channel:*")
	if err1 != nil {
		logger.L.Error("清除渠道配置缓存失败", zap.Error(err1))
	} else {
		totalDeleted += d1
	}

	// 清理 custom_channel_routes:* 缓存（路由规则）
	d2, err2 := h.cacheSvc.DeleteByPattern(ctx, "custom_channel_routes:*")
	if err2 != nil {
		logger.L.Error("清除路由规则缓存失败", zap.Error(err2))
	} else {
		totalDeleted += d2
	}

	if err1 != nil && err2 != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "清除渠道路由缓存失败")
		return
	}

	logger.L.Info("管理员一键清除渠道路由缓存", zap.Int64("deleted", totalDeleted))
	response.Success(c, gin.H{
		"deleted": totalDeleted,
		"message": "渠道路由缓存已清除",
	})
}

// ClearUserCache 清除用户维度缓存（profile/balance/apikeys/notif）
// POST /api/v1/admin/cache/clear-user-cache?user_id=123
// 不传 user_id 时清除全部用户的缓存。
func (h *CacheHandler) ClearUserCache(c *gin.Context) {
	ctx := c.Request.Context()
	userIDStr := c.Query("user_id")

	if userIDStr != "" {
		uid, err := strconv.ParseUint(userIDStr, 10, 64)
		if err != nil || uid == 0 {
			response.ErrorMsg(c, http.StatusBadRequest, 40001, "user_id 无效")
			return
		}
		usercache.InvalidateAll(ctx, uint(uid))
		logger.L.Info("管理员清除单个用户缓存", zap.Uint64("user_id", uid))
		response.Success(c, gin.H{"user_id": uid, "message": "用户缓存已清除"})
		return
	}

	// 全量清理
	deleted, err := usercache.InvalidatePatternAll(ctx)
	if err != nil {
		logger.L.Error("清除全部用户缓存失败", zap.Error(err))
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "清除缓存失败: "+err.Error())
		return
	}

	logger.L.Info("管理员清除全部用户缓存", zap.Int("deleted", deleted))
	response.Success(c, gin.H{"deleted": deleted, "message": "全部用户缓存已清除"})
}

// ClearStatsCache 清除 admin:stats:* 共享报表缓存
// POST /api/v1/admin/cache/clear-stats-cache
func (h *CacheHandler) ClearStatsCache(c *gin.Context) {
	ctx := c.Request.Context()
	deleted, err := h.cacheSvc.DeleteByPattern(ctx, "admin:stats:*")
	if err != nil {
		logger.L.Error("清除统计缓存失败", zap.Error(err))
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "清除缓存失败: "+err.Error())
		return
	}
	logger.L.Info("管理员清除统计缓存", zap.Int64("deleted", deleted))
	response.Success(c, gin.H{"deleted": deleted, "message": "统计报表缓存已清除"})
}

// ClearPublicCache 清除公开路由缓存（param-support / suppliers / channel-groups / cache_middleware 缓存）
// POST /api/v1/admin/cache/clear-public-cache
func (h *CacheHandler) ClearPublicCache(c *gin.Context) {
	ctx := c.Request.Context()
	var total int64
	for _, pattern := range []string{"public:*", "cache:/api/v1/public/*"} {
		d, err := h.cacheSvc.DeleteByPattern(ctx, pattern)
		if err != nil {
			logger.L.Error("清除公开缓存失败", zap.String("pattern", pattern), zap.Error(err))
			continue
		}
		total += d
	}
	logger.L.Info("管理员清除公开缓存", zap.Int64("deleted", total))
	response.Success(c, gin.H{"deleted": total, "message": "公开数据缓存已清除"})
}

// ClearPricingCache 清除定价缓存（pricing:*）
// POST /api/v1/admin/cache/clear-pricing-cache?model_id=123
// 不传 model_id 时清除所有定价缓存。
func (h *CacheHandler) ClearPricingCache(c *gin.Context) {
	ctx := c.Request.Context()
	modelIDStr := c.Query("model_id")

	pattern := "pricing:*"
	if modelIDStr != "" {
		mid, err := strconv.ParseUint(modelIDStr, 10, 64)
		if err != nil || mid == 0 {
			response.ErrorMsg(c, http.StatusBadRequest, 40001, "model_id 无效")
			return
		}
		// 定价缓存 key 格式含 model_id 作为某一段，使用宽匹配
		pattern = fmt.Sprintf("pricing:*:%d:*", mid)
	}

	deleted, err := h.cacheSvc.DeleteByPattern(ctx, pattern)
	if err != nil {
		logger.L.Error("清除定价缓存失败", zap.String("pattern", pattern), zap.Error(err))
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "清除缓存失败: "+err.Error())
		return
	}

	logger.L.Info("管理员清除定价缓存", zap.String("pattern", pattern), zap.Int64("deleted", deleted))
	response.Success(c, gin.H{"pattern": pattern, "deleted": deleted, "message": "定价缓存已清除"})
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
