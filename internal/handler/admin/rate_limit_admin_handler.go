package admin

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"

	"tokenhub-server/internal/middleware"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
	ratelimitsvc "tokenhub-server/internal/service/ratelimit"
)

// RateLimitAdminHandler 限流监控管理 Handler
// 提供活跃限流桶快照 + 429 事件历史 + 一键解除能力
type RateLimitAdminHandler struct {
	recorder *ratelimitsvc.EventRecorder
}

// NewRateLimitAdminHandler 构造器
func NewRateLimitAdminHandler(recorder *ratelimitsvc.EventRecorder) *RateLimitAdminHandler {
	return &RateLimitAdminHandler{recorder: recorder}
}

// Register 注册路由
func (h *RateLimitAdminHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/rate-limits/active", h.ListActiveBuckets)
	rg.GET("/rate-limits/events", h.ListEvents)
	rg.DELETE("/rate-limits/active/:key", h.ReleaseBucket)
	rg.POST("/rate-limits/reset", h.BatchReset)
}

// ActiveBucket 活跃限流桶条目
type ActiveBucket struct {
	Key         string  `json:"key"`
	KeyB64      string  `json:"key_b64"`
	SubjectType string  `json:"subject_type"`
	SubjectID   string  `json:"subject_id"`
	Rule        string  `json:"rule"`
	Used        int64   `json:"used"`
	Limit       int     `json:"limit"`
	Ratio       float64 `json:"ratio"`
	TTLSeconds  int64   `json:"ttl_seconds"`
}

// scanPatterns SCAN 扫描的 key 前缀列表
var scanPatterns = []string{
	"rl:ip:*",
	"rl:user:*",
	"rl:apikey:*",
	"rl:member_rpm:*",
	"rl:strict:*",
	"tpm:user:*",
	"rl:global:qps:*",
	"apikey:blocked:*",
}

// ListActiveBuckets 活跃限流桶快照 GET /admin/rate-limits/active
// Query: ?type=ip&q=xxx&page=1&page_size=50
func (h *RateLimitAdminHandler) ListActiveBuckets(c *gin.Context) {
	redis := pkgredis.Client
	if redis == nil {
		response.ErrorMsg(c, http.StatusServiceUnavailable, errcode.ErrInternal.Code, "redis not available")
		return
	}

	typeFilter := strings.TrimSpace(c.Query("type"))
	q := strings.TrimSpace(c.Query("q"))
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if pageSize < 1 || pageSize > 500 {
		pageSize = 50
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	// 加载当前配置作为 limit 参考
	cfg := middleware.LoadRateLimiterConfig()

	var buckets []ActiveBucket

	for _, pattern := range scanPatterns {
		keys := scanKeys(ctx, redis, pattern, 5000)
		for _, key := range keys {
			b := buildBucket(ctx, redis, key, cfg)
			if b == nil {
				continue
			}
			// 过滤
			if typeFilter != "" && b.SubjectType != typeFilter {
				continue
			}
			if q != "" && !strings.Contains(b.SubjectID, q) && !strings.Contains(b.Key, q) {
				continue
			}
			buckets = append(buckets, *b)
		}
	}

	// 按 ratio 降序（接近上限优先展示）
	sort.Slice(buckets, func(i, j int) bool {
		return buckets[i].Ratio > buckets[j].Ratio
	})

	total := len(buckets)
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}

	response.Success(c, gin.H{
		"list":      buckets[start:end],
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// scanKeys 使用 SCAN 游标遍历匹配 pattern 的所有 key（上限 maxKeys 防止卡死）
func scanKeys(ctx context.Context, redis *goredis.Client, pattern string, maxKeys int) []string {
	var result []string
	var cursor uint64
	for {
		keys, next, err := redis.Scan(ctx, cursor, pattern, 500).Result()
		if err != nil {
			return result
		}
		result = append(result, keys...)
		if len(result) >= maxKeys {
			return result[:maxKeys]
		}
		cursor = next
		if cursor == 0 {
			return result
		}
	}
}

// buildBucket 根据 key 构造 ActiveBucket 条目
func buildBucket(ctx context.Context, redis *goredis.Client, key string, cfg *middleware.RateLimiterConfig) *ActiveBucket {
	// TTL
	ttl, err := redis.TTL(ctx, key).Result()
	if err != nil || ttl <= 0 {
		return nil
	}

	// 解析 subject
	subjectType, subjectID, rule := parseKeySubject(key)

	// 计数（大部分是 ZSET，使用 ZCARD；tpm/apikey:blocked 使用 GET）
	var used int64
	keyType, _ := redis.Type(ctx, key).Result()
	switch keyType {
	case "zset":
		used, _ = redis.ZCard(ctx, key).Result()
	case "string":
		if v, err := redis.Get(ctx, key).Int64(); err == nil {
			used = v
		} else {
			used = 1
		}
	default:
		used = 0
	}

	// 默认 limit
	limit := inferLimit(subjectType, cfg)

	ratio := 0.0
	if limit > 0 {
		ratio = float64(used) / float64(limit)
	}

	return &ActiveBucket{
		Key:         key,
		KeyB64:      base64.URLEncoding.EncodeToString([]byte(key)),
		SubjectType: subjectType,
		SubjectID:   subjectID,
		Rule:        rule,
		Used:        used,
		Limit:       limit,
		Ratio:       ratio,
		TTLSeconds:  int64(ttl.Seconds()),
	}
}

// parseKeySubject 解析 Redis key 反推 subject/rule（与 middleware.parseRateLimitKey 对齐 + 扩展）
func parseKeySubject(key string) (subjectType, subjectID, rule string) {
	rule = "sliding_60s"
	switch {
	case strings.HasPrefix(key, "rl:ip:"):
		return "ip", strings.TrimPrefix(key, "rl:ip:"), rule
	case strings.HasPrefix(key, "rl:user:"):
		return "user", strings.TrimPrefix(key, "rl:user:"), rule
	case strings.HasPrefix(key, "rl:apikey:"):
		return "apikey", strings.TrimPrefix(key, "rl:apikey:"), rule
	case strings.HasPrefix(key, "rl:member_rpm:"):
		return "member", strings.TrimPrefix(key, "rl:member_rpm:"), "member_rpm"
	case strings.HasPrefix(key, "rl:strict:"):
		rest := strings.TrimPrefix(key, "rl:strict:")
		if idx := strings.LastIndex(rest, ":user:"); idx >= 0 {
			return "strict_user", rest[idx+len(":user:"):], "strict:" + rest[:idx]
		}
		if idx := strings.LastIndex(rest, ":"); idx >= 0 {
			return "strict_ip", rest[idx+1:], "strict:" + rest[:idx]
		}
		return "strict", rest, "strict"
	case strings.HasPrefix(key, "tpm:user:"):
		return "tpm", strings.TrimPrefix(key, "tpm:user:"), "tpm_per_minute"
	case strings.HasPrefix(key, "rl:global:qps:"):
		return "global", strings.TrimPrefix(key, "rl:global:qps:"), "global_qps"
	case strings.HasPrefix(key, "apikey:blocked:"):
		return "apikey_blocked", strings.TrimPrefix(key, "apikey:blocked:"), "apikey_anomaly"
	}
	return "unknown", key, rule
}

// inferLimit 根据 subjectType 推断默认 limit
func inferLimit(subjectType string, cfg *middleware.RateLimiterConfig) int {
	switch subjectType {
	case "ip":
		return cfg.IPRPM
	case "user":
		return cfg.UserRPM
	case "apikey":
		return cfg.APIKeyRPM
	case "global":
		return cfg.GlobalQPS
	}
	return 0
}

// ListEvents 429 事件历史分页 GET /admin/rate-limits/events
func (h *RateLimitAdminHandler) ListEvents(c *gin.Context) {
	if h.recorder == nil {
		response.ErrorMsg(c, http.StatusServiceUnavailable, errcode.ErrInternal.Code, "recorder not initialized")
		return
	}

	q := &model.RateLimitEventQuery{
		SubjectType: c.Query("subject_type"),
		SubjectID:   c.Query("subject_id"),
		Path:        c.Query("path"),
	}
	q.Page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	q.PageSize, _ = strconv.Atoi(c.DefaultQuery("page_size", "50"))

	if s := c.Query("start"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			q.StartDate = t
		}
	}
	if s := c.Query("end"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			q.EndDate = t
		}
	}

	list, total, err := h.recorder.List(c.Request.Context(), q)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, gin.H{
		"list":      list,
		"total":     total,
		"page":      q.Page,
		"page_size": q.PageSize,
	})
}

// ReleaseBucket 解除单个限流桶 DELETE /admin/rate-limits/active/:key
// :key 参数使用 base64url 编码（避免 `:` 等特殊字符）
func (h *RateLimitAdminHandler) ReleaseBucket(c *gin.Context) {
	redis := pkgredis.Client
	if redis == nil {
		response.ErrorMsg(c, http.StatusServiceUnavailable, errcode.ErrInternal.Code, "redis not available")
		return
	}

	b64 := c.Param("key")
	rawKey, err := base64.URLEncoding.DecodeString(b64)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "invalid key encoding")
		return
	}
	key := string(rawKey)

	if !isReleasableKey(key) {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "key not in releasable namespaces")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	deleted, err := redis.Del(ctx, key).Result()
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, gin.H{
		"key":     key,
		"deleted": deleted,
	})
}

// BatchReset 批量解除限流 POST /admin/rate-limits/reset
// Body: { "keys": [...] } 或 { "subject_type": "user", "subject_id": "123" }
func (h *RateLimitAdminHandler) BatchReset(c *gin.Context) {
	redis := pkgredis.Client
	if redis == nil {
		response.ErrorMsg(c, http.StatusServiceUnavailable, errcode.ErrInternal.Code, "redis not available")
		return
	}

	var req struct {
		Keys        []string `json:"keys"`
		SubjectType string   `json:"subject_type"`
		SubjectID   string   `json:"subject_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	var targetKeys []string

	// 模式 A：显式 keys 列表
	for _, k := range req.Keys {
		if isReleasableKey(k) {
			targetKeys = append(targetKeys, k)
		}
	}

	// 模式 B：subject_type + subject_id → 扫描匹配的 key
	if req.SubjectType != "" && req.SubjectID != "" {
		patterns := patternsForSubject(req.SubjectType, req.SubjectID)
		for _, pattern := range patterns {
			keys := scanKeys(ctx, redis, pattern, 1000)
			for _, k := range keys {
				if isReleasableKey(k) {
					targetKeys = append(targetKeys, k)
				}
			}
		}
	}

	if len(targetKeys) == 0 {
		response.Success(c, gin.H{"deleted": 0, "keys": []string{}})
		return
	}

	deleted, err := redis.Del(ctx, targetKeys...).Result()
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, gin.H{
		"deleted": deleted,
		"keys":    targetKeys,
	})
}

// isReleasableKey 白名单：仅允许解除限流相关的 key，防止误删业务数据
func isReleasableKey(key string) bool {
	releasablePrefixes := []string{
		"rl:ip:", "rl:user:", "rl:apikey:", "rl:member_rpm:",
		"rl:strict:", "tpm:user:", "rl:global:qps:", "apikey:blocked:",
	}
	for _, p := range releasablePrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// patternsForSubject 根据 subject_type/subject_id 构造 SCAN 模式列表
func patternsForSubject(subjectType, subjectID string) []string {
	switch subjectType {
	case "ip":
		return []string{fmt.Sprintf("rl:ip:%s", subjectID)}
	case "user":
		return []string{
			fmt.Sprintf("rl:user:%s", subjectID),
			fmt.Sprintf("rl:member_rpm:%s", subjectID),
			fmt.Sprintf("tpm:user:%s:*", subjectID),
			fmt.Sprintf("rl:strict:*:user:%s", subjectID),
		}
	case "apikey":
		return []string{
			fmt.Sprintf("rl:apikey:%s", subjectID),
			fmt.Sprintf("apikey:blocked:%s", subjectID),
			fmt.Sprintf("apikey:anomaly:%s", subjectID),
		}
	}
	return nil
}
