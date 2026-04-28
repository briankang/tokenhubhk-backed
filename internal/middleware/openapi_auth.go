package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/pkg/safego"
)

const (
	// openAPIRateLimitPrefix 是 Open API 限流 Redis 前缀
	openAPIRateLimitPrefix = "openapi:ratelimit:"
	// openAPIRateWindow 限流窗口: 1分钟
	openAPIRateWindow = 60 * time.Second
	// openAPIRateMax 每个 API Key 每分钟最大请求数
	openAPIRateMax = 60
)

// apiKeyCacheTTL API Key Redis 缓存有效期（减轻多副本下的 DB 查询压力）
const apiKeyCacheTTL = 5 * time.Minute

const apiKeyLastUsedWriteInterval = time.Minute

type apiKeyLastUsedCacheEntry struct {
	expiresAt time.Time
}

var apiKeyLastUsedCache sync.Map
var apiKeyLookupLocks sync.Map

// OpenAPIAuth 返回 Open API 专用 Bearer Token (API Key) 认证中间件。
// 从 Authorization: Bearer <api_key> 提取 API Key，验证有效性并注入 user_id/tenant_id。
// 使用 Redis 缓存 API Key 元数据，减轻多 Gateway Pod 高并发时的 DB 查询压力。
func OpenAPIAuth(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 提取 Bearer Token
		rawKey := extractBearerToken(c)
		if rawKey == "" {
			response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
			c.Abort()
			return
		}

		// SHA256 哈希查找 API Key
		hash := sha256.Sum256([]byte(rawKey))
		hashStr := hex.EncodeToString(hash[:])

		var apiKey model.ApiKey
		cacheKey := "apikey:hash:" + hashStr

		// 1. 先查 Redis 缓存
		if err := pkgredis.GetJSON(c.Request.Context(), cacheKey, &apiKey); err == nil && apiKey.ID > 0 {
			// 缓存命中
		} else {
			unlock := lockAPIKeyLookup(hashStr)
			// 同一个 Key 的冷启动缓存击穿只允许一个请求查 DB，其它请求等待后复查 Redis。
			if err := pkgredis.GetJSON(c.Request.Context(), cacheKey, &apiKey); err == nil && apiKey.ID > 0 {
				unlock()
			} else {
				// 2. 缓存未命中，查 DB
				err := db.Where("key_hash = ?", hashStr).First(&apiKey).Error
				if err != nil {
					unlock()
					if errors.Is(err, gorm.ErrRecordNotFound) {
						response.Error(c, http.StatusUnauthorized, errcode.ErrApiKeyInvalid)
						c.Abort()
						return
					}
					response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
					c.Abort()
					return
				}
				// 3. 写入 Redis 缓存
				_ = pkgredis.SetJSON(c.Request.Context(), cacheKey, &apiKey, apiKeyCacheTTL)
				unlock()
			}
		}

		// 检查 Key 是否激活
		if !apiKey.IsActive {
			response.Error(c, http.StatusUnauthorized, errcode.ErrApiKeyInvalid)
			c.Abort()
			return
		}

		// 检查 Key 是否过期
		if apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now()) {
			response.Error(c, http.StatusUnauthorized, errcode.ErrApiKeyExpired)
			c.Abort()
			return
		}

		// 注入用户信息到 Gin context
		c.Set("userId", apiKey.UserID)
		c.Set("tenantId", apiKey.TenantID)
		c.Set("apiKeyId", apiKey.ID)
		EnsurePaidUserContext(c, db, apiKey.UserID)

		// 注入 API Key 高级配置到 context，供后续路由使用
		if apiKey.CustomChannelID != nil {
			c.Set("customChannelID", *apiKey.CustomChannelID) // 关联的自定义渠道ID
		}
		c.Set("allowedModels", apiKey.AllowedModels) // 允许的模型列表
		c.Set("creditLimit", apiKey.CreditLimit)     // 积分限额
		c.Set("creditUsed", apiKey.CreditUsed)       // 已用积分
		c.Set("rateLimitRPM", apiKey.RateLimitRPM)   // Key级别RPM
		c.Set("rateLimitTPM", apiKey.RateLimitTPM)   // Key级别TPM

		// 检查积分限额：如果设置了限额且已用积分达到限额，返回 402
		if apiKey.CreditLimit > 0 && apiKey.CreditUsed >= apiKey.CreditLimit {
			response.Error(c, http.StatusPaymentRequired, errcode.ErrInsufficientBalance)
			c.Abort()
			return
		}

		if shouldUpdateAPIKeyLastUsed(c.Request.Context(), fmt.Sprintf("%d", apiKey.ID)) {
			// 异步更新 lastUsedAt — safego 防 panic，短超时防连接池饥饿
			safego.Go("openapi-update-last-used", func() {
				bgCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = db.WithContext(bgCtx).Model(&model.ApiKey{}).
					Where("id = ?", apiKey.ID).
					Update("last_used_at", time.Now()).Error
			})
		}

		c.Next()
	}
}

func lockAPIKeyLookup(hashStr string) func() {
	raw, _ := apiKeyLookupLocks.LoadOrStore(hashStr, &sync.Mutex{})
	mu := raw.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func shouldUpdateAPIKeyLastUsed(ctx context.Context, id string) bool {
	if id == "" {
		return false
	}
	now := time.Now()
	if raw, ok := apiKeyLastUsedCache.Load(id); ok {
		if entry, ok := raw.(apiKeyLastUsedCacheEntry); ok && now.Before(entry.expiresAt) {
			return false
		}
	}
	apiKeyLastUsedCache.Store(id, apiKeyLastUsedCacheEntry{expiresAt: now.Add(apiKeyLastUsedWriteInterval)})

	if pkgredis.Client == nil {
		return true
	}
	redisKey := "apikey:last_used:update:" + id
	ok, err := pkgredis.Client.SetNX(ctx, redisKey, now.Unix(), apiKeyLastUsedWriteInterval).Result()
	if err != nil {
		return true
	}
	return ok
}

// OpenAPIRateLimit 返回 Open API 专用限流中间件，基于 API Key 的滑动窗口限流 (60 req/min)。
func OpenAPIRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		redis := pkgredis.Client
		if redis == nil {
			// Redis 不可用时放行 (fail-open)
			c.Next()
			return
		}

		apiKeyID, exists := c.Get("apiKeyId")
		if !exists {
			c.Next()
			return
		}

		key := fmt.Sprintf("%s%v", openAPIRateLimitPrefix, apiKeyID)
		ctx := context.Background()
		now := time.Now().UnixMilli()

		pipe := redis.Pipeline()
		// 移除过期条目
		pipe.ZRemRangeByScore(ctx, key, "0", fmt.Sprintf("%d", now-openAPIRateWindow.Milliseconds()))
		// 统计当前窗口请求数
		countCmd := pipe.ZCard(ctx, key)
		// 添加当前请求
		pipe.ZAdd(ctx, key, goredis.Z{Score: float64(now), Member: fmt.Sprintf("%d:%d", now, time.Now().UnixNano()%1000000)})
		// 设置 key 过期时间
		pipe.Expire(ctx, key, openAPIRateWindow)

		if _, err := pipe.Exec(ctx); err != nil {
			// Redis 错误时放行
			c.Next()
			return
		}

		count := countCmd.Val()
		if count >= openAPIRateMax {
			response.Error(c, http.StatusTooManyRequests, errcode.ErrRateLimit)
			c.Abort()
			return
		}

		c.Next()
	}
}

// extractBearerToken 从 Authorization 头提取 Bearer Token
func extractBearerToken(c *gin.Context) string {
	auth := c.GetHeader("Authorization")
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
