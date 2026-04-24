// 默认用户额度配置 — 管理后台可动态调整，Redis 持久化
package balance

import (
	"context"
	"fmt"
	"strconv"

	pkgredis "tokenhub-server/internal/pkg/redis"
)

// DefaultUserQuotaConfig 默认用户额度配置（当用户无自定义 UserQuotaConfig 时使用）
type DefaultUserQuotaConfig struct {
	DailyLimit      int64 `json:"dailyLimit"`      // 日限额（积分），0 = 无限制
	MonthlyLimit    int64 `json:"monthlyLimit"`    // 月限额（积分），0 = 无限制
	MaxTokensPerReq int   `json:"maxTokensPerReq"` // 单次最大 Token
	MaxConcurrent   int   `json:"maxConcurrent"`   // 最大并发请求数
}

// 新默认值（相对历史 32768/5 大幅放宽）
const (
	defaultQuotaDailyLimit      int64 = 0       // 0 = 无限制
	defaultQuotaMonthlyLimit    int64 = 0       // 0 = 无限制
	defaultQuotaMaxTokensPerReq       = 131072  // 128K，覆盖主流长上下文模型
	defaultQuotaMaxConcurrent         = 20      // 20 并发，Dashboard + API 混合场景足够
)

// BuildDefaultUserQuotaConfig 返回内置默认值
func BuildDefaultUserQuotaConfig() *DefaultUserQuotaConfig {
	return &DefaultUserQuotaConfig{
		DailyLimit:      defaultQuotaDailyLimit,
		MonthlyLimit:    defaultQuotaMonthlyLimit,
		MaxTokensPerReq: defaultQuotaMaxTokensPerReq,
		MaxConcurrent:   defaultQuotaMaxConcurrent,
	}
}

// LoadDefaultUserQuotaConfig 从 Redis 读取动态配置，失败回退内置默认
func LoadDefaultUserQuotaConfig() *DefaultUserQuotaConfig {
	cfg := BuildDefaultUserQuotaConfig()
	if pkgredis.Client == nil {
		return cfg
	}
	ctx := context.Background()
	v, err := pkgredis.Client.HGetAll(ctx, "config:default_user_quota").Result()
	if err != nil || len(v) == 0 {
		return cfg
	}
	if s, ok := v["daily_limit"]; ok {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n >= 0 {
			cfg.DailyLimit = n
		}
	}
	if s, ok := v["monthly_limit"]; ok {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n >= 0 {
			cfg.MonthlyLimit = n
		}
	}
	if s, ok := v["max_tokens_per_req"]; ok {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			cfg.MaxTokensPerReq = n
		}
	}
	if s, ok := v["max_concurrent"]; ok {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			cfg.MaxConcurrent = n
		}
	}
	return cfg
}

// SaveDefaultUserQuotaConfig 管理后台写入 Redis，立即生效
func SaveDefaultUserQuotaConfig(cfg *DefaultUserQuotaConfig) error {
	if pkgredis.Client == nil {
		return fmt.Errorf("redis not available")
	}
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	ctx := context.Background()
	return pkgredis.Client.HSet(ctx, "config:default_user_quota", map[string]interface{}{
		"daily_limit":        cfg.DailyLimit,
		"monthly_limit":      cfg.MonthlyLimit,
		"max_tokens_per_req": cfg.MaxTokensPerReq,
		"max_concurrent":     cfg.MaxConcurrent,
	}).Err()
}
