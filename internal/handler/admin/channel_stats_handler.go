package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/response"
)

// ChannelStatsHandler 渠道统计接口处理器（管理员专用）
// 提供渠道级别的请求成功率、平均延迟、熔断状态等监控数据
type ChannelStatsHandler struct {
	db    *gorm.DB
	redis *goredis.Client
}

// NewChannelStatsHandler 创建渠道统计处理器实例
func NewChannelStatsHandler(db *gorm.DB, redis *goredis.Client) *ChannelStatsHandler {
	if db == nil {
		panic("admin channel_stats handler: db is nil")
	}
	return &ChannelStatsHandler{db: db, redis: redis}
}

// Register 注册渠道统计路由到管理员路由组
func (h *ChannelStatsHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/channel-stats", h.GetChannelStats)
	rg.GET("/channel-stats/models", h.GetModelStats)
}

// channelStatsRow 数据库聚合查询的行结构（内部使用）
type channelStatsRow struct {
	ChannelID    uint    `json:"channel_id"`
	TotalReqs    int64   `json:"total_requests"`
	SuccessCount int64   `json:"success_count"`
	SuccessRate  float64 `json:"success_rate"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	P99Latency   int     `json:"p99_latency_ms"` // 近似值，使用 MAX
	LastError    string  `json:"last_error,omitempty"`
}

// channelHealthInfo Redis 中存储的健康检查结果
type channelHealthInfo struct {
	ChannelID  uint      `json:"channel_id"`
	Healthy    bool      `json:"healthy"`
	LatencyMs  int64     `json:"latency_ms"`
	StatusCode int       `json:"status_code,omitempty"`
	Error      string    `json:"error,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
}

// ChannelStatsItem 单个渠道的完整统计信息（响应结构）
type ChannelStatsItem struct {
	ChannelID     uint    `json:"channel_id"`
	ChannelName   string  `json:"channel_name"`
	ChannelStatus string  `json:"channel_status"`
	TotalRequests int64   `json:"total_requests"`
	SuccessCount  int64   `json:"success_count"`
	SuccessRate   float64 `json:"success_rate"`
	AvgLatencyMs  float64 `json:"avg_latency_ms"`
	P99LatencyMs  int     `json:"p99_latency_ms"`
	LastError     string  `json:"last_error,omitempty"`

	// 健康检查相关（来自 Redis）
	HealthCheckHealthy   *bool      `json:"health_check_healthy,omitempty"`
	HealthCheckLatencyMs *int64     `json:"health_check_latency_ms,omitempty"`
	HealthCheckError     string     `json:"health_check_error,omitempty"`
	HealthCheckAt        *time.Time `json:"health_check_at,omitempty"`
}

// channelBasicInfo 渠道基础信息（JOIN channels 表）
type channelBasicInfo struct {
	ID     uint   `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// modelStatsRow 按模型聚合的统计行
type modelStatsRow struct {
	ChannelID    uint    `json:"channel_id"`
	ModelName    string  `json:"model_name"`
	TotalReqs    int64   `json:"total_requests"`
	SuccessCount int64   `json:"success_count"`
	SuccessRate  float64 `json:"success_rate"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	MaxLatencyMs int     `json:"max_latency_ms"`
	LastError    string  `json:"last_error,omitempty"`
}

// ModelStatsItem 单个渠道-模型组合的统计信息（响应结构）
type ModelStatsItem struct {
	ChannelID    uint    `json:"channel_id"`
	ChannelName  string  `json:"channel_name"`
	ModelName    string  `json:"model_name"`
	TotalRequests int64  `json:"total_requests"`
	SuccessCount int64   `json:"success_count"`
	SuccessRate  float64 `json:"success_rate"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	MaxLatencyMs int     `json:"max_latency_ms"`
	LastError    string  `json:"last_error,omitempty"`
}

// GetChannelStats 获取渠道统计数据
// GET /api/v1/admin/channel-stats?hours=24&channel_id=1
// 返回各渠道的请求成功率、平均延迟、P99延迟、最后错误、健康检查状态等
func (h *ChannelStatsHandler) GetChannelStats(c *gin.Context) {
	ctx := c.Request.Context()

	// 解析时间范围参数，默认24小时
	hours, _ := strconv.Atoi(c.DefaultQuery("hours", "24"))
	if hours <= 0 || hours > 720 {
		hours = 24
	}
	since := time.Now().Add(-time.Duration(hours) * time.Hour)

	// 可选：按单个渠道 ID 过滤
	var channelIDFilter *uint
	if cidStr := c.Query("channel_id"); cidStr != "" {
		if cid, err := strconv.ParseUint(cidStr, 10, 64); err == nil && cid > 0 {
			v := uint(cid)
			channelIDFilter = &v
		}
	}

	// 1. 从 channel_logs 聚合统计数据
	stats, err := h.queryChannelLogStats(ctx, since, channelIDFilter)
	if err != nil {
		logger.L.Error("查询渠道日志统计失败", zap.Error(err))
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "查询渠道统计失败: "+err.Error())
		return
	}

	// 2. 查询所有相关渠道的基础信息
	channelIDs := make([]uint, 0, len(stats))
	for _, s := range stats {
		channelIDs = append(channelIDs, s.ChannelID)
	}

	channelInfoMap := h.loadChannelInfoMap(ctx, channelIDs, channelIDFilter)

	// 3. 从 Redis 加载健康检查结果
	healthMap := h.loadHealthResults(ctx, channelIDs, channelIDFilter)

	// 4. 组装最终响应
	items := make([]ChannelStatsItem, 0, len(stats))
	for _, s := range stats {
		item := ChannelStatsItem{
			ChannelID:     s.ChannelID,
			TotalRequests: s.TotalReqs,
			SuccessCount:  s.SuccessCount,
			SuccessRate:   s.SuccessRate,
			AvgLatencyMs:  s.AvgLatencyMs,
			P99LatencyMs:  s.P99Latency,
			LastError:     s.LastError,
		}

		// 填充渠道名称和状态
		if info, ok := channelInfoMap[s.ChannelID]; ok {
			item.ChannelName = info.Name
			item.ChannelStatus = info.Status
		}

		// 填充健康检查数据
		if hr, ok := healthMap[s.ChannelID]; ok {
			item.HealthCheckHealthy = &hr.Healthy
			item.HealthCheckLatencyMs = &hr.LatencyMs
			item.HealthCheckError = hr.Error
			item.HealthCheckAt = &hr.CheckedAt
		}

		items = append(items, item)
	}

	// 如果指定了 channel_id 但日志中无数据，仍返回渠道基础信息（含健康检查）
	if channelIDFilter != nil && len(items) == 0 {
		if info, ok := channelInfoMap[*channelIDFilter]; ok {
			item := ChannelStatsItem{
				ChannelID:     info.ID,
				ChannelName:   info.Name,
				ChannelStatus: info.Status,
			}
			if hr, ok := healthMap[*channelIDFilter]; ok {
				item.HealthCheckHealthy = &hr.Healthy
				item.HealthCheckLatencyMs = &hr.LatencyMs
				item.HealthCheckError = hr.Error
				item.HealthCheckAt = &hr.CheckedAt
			}
			items = append(items, item)
		}
	}

	response.Success(c, gin.H{
		"items":      items,
		"hours":      hours,
		"query_time": time.Now().Format(time.RFC3339),
	})
}

// queryChannelLogStats 从 channel_logs 表按渠道聚合统计指标
func (h *ChannelStatsHandler) queryChannelLogStats(ctx context.Context, since time.Time, channelID *uint) ([]channelStatsRow, error) {
	// 构建聚合查询：
	// - total_requests: 总请求数
	// - success_count: 成功请求数（status_code < 500）
	// - success_rate: 成功率百分比
	// - avg_latency_ms: 平均延迟
	// - p99_latency_ms: 近似 P99（使用 MAX）
	// - last_error: 最近的错误信息
	query := h.db.WithContext(ctx).
		Table("channel_logs").
		Select(`
			channel_id,
			COUNT(*) AS total_reqs,
			SUM(CASE WHEN status_code < 500 THEN 1 ELSE 0 END) AS success_count,
			ROUND(SUM(CASE WHEN status_code < 500 THEN 1 ELSE 0 END) * 100.0 / COUNT(*), 2) AS success_rate,
			ROUND(AVG(latency_ms), 2) AS avg_latency_ms,
			MAX(latency_ms) AS p99_latency,
			(SELECT cl2.error_message FROM channel_logs cl2
			 WHERE cl2.channel_id = channel_logs.channel_id
			   AND cl2.error_message != ''
			   AND cl2.created_at >= ?
			 ORDER BY cl2.created_at DESC LIMIT 1) AS last_error
		`, since).
		Where("created_at >= ?", since).
		Group("channel_id").
		Order("total_reqs DESC")

	if channelID != nil {
		query = query.Where("channel_id = ?", *channelID)
	}

	var rows []channelStatsRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("聚合渠道日志失败: %w", err)
	}

	return rows, nil
}

// loadChannelInfoMap 批量加载渠道基础信息，返回 ID→Info 映射
func (h *ChannelStatsHandler) loadChannelInfoMap(ctx context.Context, channelIDs []uint, singleID *uint) map[uint]channelBasicInfo {
	infoMap := make(map[uint]channelBasicInfo)

	query := h.db.WithContext(ctx).
		Table("channels").
		Select("id, name, status")

	// 如果指定了单个渠道，只查该渠道；否则查聚合结果中涉及的所有渠道
	if singleID != nil {
		query = query.Where("id = ?", *singleID)
	} else if len(channelIDs) > 0 {
		query = query.Where("id IN ?", channelIDs)
	} else {
		return infoMap
	}

	var infos []channelBasicInfo
	if err := query.Find(&infos).Error; err != nil {
		logger.L.Warn("加载渠道基础信息失败", zap.Error(err))
		return infoMap
	}

	for _, info := range infos {
		infoMap[info.ID] = info
	}
	return infoMap
}

// loadHealthResults 从 Redis 批量加载渠道健康检查结果
func (h *ChannelStatsHandler) loadHealthResults(ctx context.Context, channelIDs []uint, singleID *uint) map[uint]channelHealthInfo {
	healthMap := make(map[uint]channelHealthInfo)

	if h.redis == nil {
		return healthMap
	}

	// 确定需要查询的渠道 ID 列表
	idsToCheck := channelIDs
	if singleID != nil {
		idsToCheck = []uint{*singleID}
	}

	// 使用 Pipeline 批量获取 Redis 数据，减少网络往返
	if len(idsToCheck) == 0 {
		return healthMap
	}

	pipe := h.redis.Pipeline()
	cmds := make(map[uint]*goredis.StringCmd, len(idsToCheck))
	for _, id := range idsToCheck {
		key := fmt.Sprintf("channel:health:%d", id)
		cmds[id] = pipe.Get(ctx, key)
	}

	_, err := pipe.Exec(ctx)
	if err != nil && err != goredis.Nil {
		logger.L.Warn("批量获取渠道健康检查数据失败", zap.Error(err))
	}

	for id, cmd := range cmds {
		val, err := cmd.Result()
		if err != nil {
			continue // key 不存在或读取失败，跳过
		}
		var info channelHealthInfo
		if err := json.Unmarshal([]byte(val), &info); err != nil {
			logger.L.Warn("反序列化渠道健康数据失败",
				zap.Uint("channel_id", id),
				zap.Error(err),
			)
			continue
		}
		healthMap[id] = info
	}

	return healthMap
}

// GetModelStats 获取渠道下按模型维度的统计数据
// GET /api/v1/admin/channel-stats/models?hours=24&channel_id=1
func (h *ChannelStatsHandler) GetModelStats(c *gin.Context) {
	ctx := c.Request.Context()

	hours, _ := strconv.Atoi(c.DefaultQuery("hours", "24"))
	if hours <= 0 || hours > 720 {
		hours = 24
	}
	since := time.Now().Add(-time.Duration(hours) * time.Hour)

	var channelIDFilter *uint
	if cidStr := c.Query("channel_id"); cidStr != "" {
		if cid, err := strconv.ParseUint(cidStr, 10, 64); err == nil && cid > 0 {
			v := uint(cid)
			channelIDFilter = &v
		}
	}

	// 从 channel_logs 按 (channel_id, model_name) 聚合
	query := h.db.WithContext(ctx).
		Table("channel_logs").
		Select(`
			channel_id,
			model_name,
			COUNT(*) AS total_reqs,
			SUM(CASE WHEN status_code < 500 THEN 1 ELSE 0 END) AS success_count,
			ROUND(SUM(CASE WHEN status_code < 500 THEN 1 ELSE 0 END) * 100.0 / COUNT(*), 2) AS success_rate,
			ROUND(AVG(latency_ms), 2) AS avg_latency_ms,
			MAX(latency_ms) AS max_latency_ms,
			(SELECT cl2.error_message FROM channel_logs cl2
			 WHERE cl2.channel_id = channel_logs.channel_id
			   AND cl2.model_name = channel_logs.model_name
			   AND cl2.error_message != ''
			   AND cl2.created_at >= ?
			 ORDER BY cl2.created_at DESC LIMIT 1) AS last_error
		`, since).
		Where("created_at >= ?", since).
		Group("channel_id, model_name").
		Order("channel_id ASC, total_reqs DESC")

	if channelIDFilter != nil {
		query = query.Where("channel_id = ?", *channelIDFilter)
	}

	var rows []modelStatsRow
	if err := query.Find(&rows).Error; err != nil {
		logger.L.Error("查询渠道模型统计失败", zap.Error(err))
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "查询渠道模型统计失败: "+err.Error())
		return
	}

	// 加载渠道名称
	channelIDs := make(map[uint]bool)
	for _, r := range rows {
		channelIDs[r.ChannelID] = true
	}
	ids := make([]uint, 0, len(channelIDs))
	for id := range channelIDs {
		ids = append(ids, id)
	}
	channelInfoMap := h.loadChannelInfoMap(ctx, ids, nil)

	// 组装响应
	items := make([]ModelStatsItem, 0, len(rows))
	for _, r := range rows {
		item := ModelStatsItem{
			ChannelID:     r.ChannelID,
			ModelName:     r.ModelName,
			TotalRequests: r.TotalReqs,
			SuccessCount:  r.SuccessCount,
			SuccessRate:   r.SuccessRate,
			AvgLatencyMs:  r.AvgLatencyMs,
			MaxLatencyMs:  r.MaxLatencyMs,
			LastError:     r.LastError,
		}
		if info, ok := channelInfoMap[r.ChannelID]; ok {
			item.ChannelName = info.Name
		}
		items = append(items, item)
	}

	response.Success(c, gin.H{
		"items":      items,
		"hours":      hours,
		"query_time": time.Now().Format(time.RFC3339),
	})
}
