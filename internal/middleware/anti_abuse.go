package middleware

import (
	"context"
	"crypto/md5"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/guard"
)

// AntiAbuseMiddleware 反滥用系统中间件
// 1. 实现简单的设备指纹 (IP + UA)
// 2. 基于 TotalRecharged 区分 Free 用户 vs 正常用户
// 3. 执行多维限速 (RPM / TPM / 并发)，配置完全动态化
func AntiAbuseMiddleware(db *gorm.DB, guardSvc *guard.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 管理员路由豁免
		if strings.HasPrefix(c.Request.URL.Path, "/api/v1/admin/") {
			c.Next()
			return
		}

		// 1. 获取用户信息
		userIDVal, exists := c.Get("userId")
		if !exists {
			c.Next()
			return
		}
		userID := userIDVal.(uint)

		// 2. 生成设备指纹 (MD5(IP + UA))
		ip := c.ClientIP()
		ua := c.GetHeader("User-Agent")
		fingerprint := fmt.Sprintf("%x", md5.Sum([]byte(ip+ua)))
		c.Set("fingerprint", fingerprint)

		// 3. 获取用户余额与充值信息
		var balance model.UserBalance
		err := db.WithContext(c.Request.Context()).
			Select("id, total_recharged").
			Where("user_id = ?", userID).
			First(&balance).Error
		if err != nil {
			c.Next() // 容错：查不到余额不拦截
			return
		}

		// 4. 获取全局风控与限额配置
		ctx := c.Request.Context()
		gCfg := guardSvc.GetConfig(ctx)

		// 获取付费门槛配置
		paidThreshold := int64(100000) // 默认 10 元
		var quotaCfg model.QuotaConfig
		if err := db.Where("is_active = ?", true).First(&quotaCfg).Error; err == nil {
			paidThreshold = quotaCfg.PaidThresholdCredits
		}

		_ = balance.TotalRecharged >= paidThreshold
		isPaid, err := LoadPaidUserStatus(ctx, db, userID)
		if err != nil {
			c.Next()
			return
		}

		// 5. 设置限速参数 (从配置加载)
		var rpmLimit int
		var tpmLimit int64
		var maxConc int

		if isPaid {
			// 付费用户：优先使用个性化配置，若无则使用全局付费用户默认值
			var userQuota model.UserQuotaConfig
			if err := db.Where("user_id = ?", userID).First(&userQuota).Error; err == nil {
				rpmLimit = userQuota.CustomRPM
				tpmLimit = int64(userQuota.CustomTPM)
				maxConc = userQuota.MaxConcurrent
			}

			if rpmLimit <= 0 {
				rateCfg := LoadRateLimiterConfig()
				rpmLimit = rateCfg.UserRPM
				if rpmLimit <= 0 {
					rpmLimit = 60
				}
			}
			if maxConc <= 0 {
				maxConc = 10 // 付费默认并发
			}
			if tpmLimit <= 0 {
				tpmLimit = 200000 // 付费默认 TPM
			}
		} else {
			// Free 用户：使用全局免费用户配置
			rpmLimit = gCfg.FreeUserRPM
			tpmLimit = int64(gCfg.FreeUserTPM)
			maxConc = gCfg.FreeUserConcurrency
		}

		// 6. 执行限速检查
		redis := pkgredis.Client
		if redis == nil {
			c.Next() // fail-open
			return
		}

		// --- A. RPM (每分钟请求数) ---
		rpmKey := fmt.Sprintf("abuse:rpm:%d", userID)
		if !checkRPM(ctx, redis, rpmKey, rpmLimit, c) {
			return
		}

		// --- B. 并发检查 ---
		concKey := fmt.Sprintf("abuse:conc:%d", userID)
		currConc, _ := redis.Get(ctx, concKey).Int()
		if maxConc > 0 && currConc >= maxConc {
			response.ErrorMsg(c, http.StatusTooManyRequests, errcode.ErrRateLimit.Code,
				fmt.Sprintf("Concurrent limit exceeded (%d/%d). Please upgrade or reduce requests.", currConc, maxConc))
			c.Abort()
			return
		}

		// --- C. TPM (Token 消耗速率) ---
		if tpmLimit > 0 {
			tpmKey := fmt.Sprintf("abuse:tpm:%d:%d", userID, time.Now().Unix()/60)
			currentTPM, _ := redis.Get(ctx, tpmKey).Int64()
			if currentTPM >= tpmLimit {
				response.ErrorMsg(c, http.StatusTooManyRequests, errcode.ErrRateLimit.Code,
					"Minute token consumption limit exceeded. Please recharge to unlock higher limits.")
				c.Abort()
				return
			}
		}

		c.Set("isPaidUser", isPaid)
		c.Next()
	}
}

// checkRPM 检查每分钟请求数
func checkRPM(ctx context.Context, redis *goredis.Client, key string, limit int, c *gin.Context) bool {
	// 使用简单的 INCR + EXPIRE (1分钟窗口)
	count, err := redis.Incr(ctx, key).Result()
	if err == nil && count == 1 {
		redis.Expire(ctx, key, 60*time.Second)
	}

	if int(count) > limit {
		response.ErrorMsg(c, http.StatusTooManyRequests, errcode.ErrRateLimit.Code,
			fmt.Sprintf("Request frequency too high (%d/%d rpm). Please upgrade to normal user.", count, limit))
		c.Abort()
		return false
	}
	return true
}
