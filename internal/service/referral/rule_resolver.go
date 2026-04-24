package referral

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// CommissionSource 返佣比例来源
const (
	SourceRule     = "rule"     // 命中 CommissionRule
	SourceOverride = "override" // 命中 UserCommissionOverride
	SourceConfig   = "config"   // 走全局默认 ReferralConfig
)

// ResolvedRate 一次决策的完整结果
type ResolvedRate struct {
	Rate         float64 `json:"rate"`          // 最终生效比例
	PlatformRate float64 `json:"platform_rate"` // 平台默认（ReferralConfig.CommissionRate）
	Source       string  `json:"source"`        // rule / override / config
	RuleID       uint    `json:"rule_id,omitempty"`
	OverrideID   uint    `json:"override_id,omitempty"`
	LifetimeCap  int64   `json:"lifetime_cap"` // 终身上限（继承规则 > override > config）
}

// RuleResolver 返佣比例决策器（带 Redis 缓存）
//
// 决策优先级（高→低）：
//  1. CommissionRule：匹配 (inviterID, modelID) 的最高 priority 活跃规则
//  2. UserCommissionOverride：inviter 级别的活跃覆盖
//  3. ReferralConfig.CommissionRate：全局默认
//
// 缓存设计：
//   - Key: comm_rule:<inviterID>:<modelID>
//   - Value: ResolvedRate JSON
//   - TTL: 10 分钟
//   - Redis 故障 fail-open 直接走 DB，不影响功能
type RuleResolver struct {
	db           *gorm.DB
	redis        *goredis.Client
	ttl          time.Duration
	redisTimeout time.Duration
}

// Default 全局 Resolver 单例（bootstrap 初始化后赋值）
var Default *RuleResolver

// NewRuleResolver 构造 Resolver；redis 可为 nil（降级每次查 DB）
func NewRuleResolver(db *gorm.DB, redis *goredis.Client) *RuleResolver {
	if db == nil {
		panic("rule resolver: db is nil")
	}
	return &RuleResolver{
		db:           db,
		redis:        redis,
		ttl:          10 * time.Minute,
		redisTimeout: 800 * time.Millisecond,
	}
}

// cacheKey 构造缓存键
func cacheKey(inviterID, modelID uint) string {
	return fmt.Sprintf("comm_rule:%d:%d", inviterID, modelID)
}

// Resolve 返回 (inviterID, modelID) 的生效返佣比例
// modelID 为 0 时跳过 CommissionRule 查询，直接走 override/config
func (r *RuleResolver) Resolve(ctx context.Context, inviterID, modelID uint) (*ResolvedRate, error) {
	if inviterID == 0 {
		return nil, errors.New("rule resolver: inviterID is zero")
	}

	// 1. 查 Redis 缓存
	if r.redis != nil && modelID > 0 {
		key := cacheKey(inviterID, modelID)
		rctx, rcancel := context.WithTimeout(ctx, r.redisTimeout)
		raw, err := r.redis.Get(rctx, key).Result()
		rcancel()
		if err == nil && raw != "" {
			var cached ResolvedRate
			if jerr := json.Unmarshal([]byte(raw), &cached); jerr == nil {
				return &cached, nil
			}
		} else if err != nil && !errors.Is(err, goredis.Nil) {
			if logger.L != nil {
				logger.L.Debug("rule resolver: redis get failed, fallback to db",
					zap.Uint("inviter_id", inviterID),
					zap.Uint("model_id", modelID),
					zap.Error(err),
				)
			}
		}
	}

	// 2. 从 DB 决策
	resolved, err := r.resolveFromDB(ctx, inviterID, modelID)
	if err != nil {
		return nil, err
	}

	// 3. 回填 Redis
	if r.redis != nil && modelID > 0 {
		if data, merr := json.Marshal(resolved); merr == nil {
			setCtx, setCancel := context.WithTimeout(ctx, r.redisTimeout)
			_ = r.redis.Set(setCtx, cacheKey(inviterID, modelID), string(data), r.ttl).Err()
			setCancel()
		}
	}
	return resolved, nil
}

// resolveFromDB 未走缓存时的 DB 决策路径
func (r *RuleResolver) resolveFromDB(ctx context.Context, inviterID, modelID uint) (*ResolvedRate, error) {
	now := time.Now()

	// 加载全局默认配置
	var cfg model.ReferralConfig
	if err := r.db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error; err != nil {
		// 无活跃配置：返回 0 比例，CommissionCalculator 会短路跳过
		return &ResolvedRate{Rate: 0, PlatformRate: 0, Source: SourceConfig}, nil
	}
	platformRate := cfg.CommissionRate
	if platformRate <= 0 && cfg.PersonalCashbackRate > 0 {
		platformRate = cfg.PersonalCashbackRate
	}

	out := &ResolvedRate{
		Rate:         platformRate,
		PlatformRate: platformRate,
		Source:       SourceConfig,
		LifetimeCap:  cfg.LifetimeCapCredits,
	}

	// 1) 最高优先级：CommissionRule
	if modelID > 0 {
		var rule model.CommissionRule
		err := r.db.WithContext(ctx).
			Table("commission_rules").
			Joins("JOIN commission_rule_users cru ON cru.rule_id = commission_rules.id AND cru.user_id = ?", inviterID).
			Joins("JOIN commission_rule_models crm ON crm.rule_id = commission_rules.id AND crm.model_id = ?", modelID).
			Where("commission_rules.is_active = ? AND commission_rules.effective_from <= ?", true, now).
			Where("commission_rules.effective_to IS NULL OR commission_rules.effective_to > ?", now).
			Where("commission_rules.deleted_at IS NULL").
			Order("commission_rules.priority ASC, commission_rules.id DESC").
			First(&rule).Error
		if err == nil {
			out.Rate = rule.CommissionRate
			out.Source = SourceRule
			out.RuleID = rule.ID
			return out, nil
		}
	}

	// 2) 次优先级：UserCommissionOverride
	var override model.UserCommissionOverride
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND is_active = ? AND effective_from <= ?", inviterID, true, now).
		Where("effective_to IS NULL OR effective_to > ?", now).
		First(&override).Error
	if err == nil {
		out.Rate = override.CommissionRate
		out.Source = SourceOverride
		out.OverrideID = override.ID
		if override.LifetimeCapCredits != nil {
			out.LifetimeCap = *override.LifetimeCapCredits
		}
	}

	return out, nil
}

// InvalidateByUserModel 失效单个 (user, model) 键
func (r *RuleResolver) InvalidateByUserModel(ctx context.Context, userID, modelID uint) {
	if r.redis == nil || userID == 0 || modelID == 0 {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, r.redisTimeout)
	defer cancel()
	_ = r.redis.Del(cctx, cacheKey(userID, modelID)).Err()
}

// InvalidateByRule 规则变更时批量失效该规则下所有 (user, model) 组合
// users 和 models 为规则关联的 ID 集合
func (r *RuleResolver) InvalidateByRule(ctx context.Context, users []uint, models []uint) {
	if r.redis == nil || len(users) == 0 || len(models) == 0 {
		return
	}
	keys := make([]string, 0, len(users)*len(models))
	for _, u := range users {
		for _, m := range models {
			keys = append(keys, cacheKey(u, m))
		}
	}
	// 分批 DEL 避免单次过大
	const batch = 500
	for i := 0; i < len(keys); i += batch {
		end := i + batch
		if end > len(keys) {
			end = len(keys)
		}
		cctx, cancel := context.WithTimeout(ctx, r.redisTimeout)
		_ = r.redis.Del(cctx, keys[i:end]...).Err()
		cancel()
	}
}

// InvalidateByUser 失效某用户的全部模型缓存（用 SCAN + DEL）
// 适用于 UserCommissionOverride 或 ReferralConfig 改动
func (r *RuleResolver) InvalidateByUser(ctx context.Context, userID uint) {
	if r.redis == nil || userID == 0 {
		return
	}
	pattern := fmt.Sprintf("comm_rule:%d:*", userID)
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	iter := r.redis.Scan(cctx, 0, pattern, 500).Iterator()
	batch := make([]string, 0, 500)
	for iter.Next(cctx) {
		batch = append(batch, iter.Val())
		if len(batch) >= 500 {
			_ = r.redis.Del(cctx, batch...).Err()
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		_ = r.redis.Del(cctx, batch...).Err()
	}
}

// InvalidateAll 失效全部缓存（ReferralConfig 全局改动时用，通过模式匹配）
func (r *RuleResolver) InvalidateAll(ctx context.Context) {
	if r.redis == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	iter := r.redis.Scan(cctx, 0, "comm_rule:*", 1000).Iterator()
	batch := make([]string, 0, 1000)
	for iter.Next(cctx) {
		batch = append(batch, iter.Val())
		if len(batch) >= 1000 {
			_ = r.redis.Del(cctx, batch...).Err()
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		_ = r.redis.Del(cctx, batch...).Err()
	}
}
