package support

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/logger"
)

// BudgetLevel 预算档位
type BudgetLevel int

const (
	BudgetNormal    BudgetLevel = iota // 预算充足，用主力模型 glm-4
	BudgetEconomy                      // 预算偏紧，降级 qwen-plus
	BudgetEmergency                    // 预算耗尽，固定话术 + 强制转工单
)

func (b BudgetLevel) String() string {
	switch b {
	case BudgetNormal:
		return "normal"
	case BudgetEconomy:
		return "economy"
	default:
		return "emergency"
	}
}

// BudgetGuard 月度预算守护（Redis 原子扣减）
//
// Redis key: support:budget:month:{YYYYMM} (int64, 单位：积分)
// 月初自然过期（key 带月份后缀，不设显式 TTL 也能保证下月切新 key）
type BudgetGuard struct {
	redis             *goredis.Client
	monthlyTotal      int64   // 月度总预算（积分）
	economyThresholdP int     // 降级阈值百分比（默认 30）
	emergencyThreshP  int     // 熔断阈值百分比（默认 5）
}

// NewBudgetGuard 构造
// monthlyTotalCredits: 总预算（如 5000000 = ¥500）
func NewBudgetGuard(redis *goredis.Client, monthlyTotalCredits int64) *BudgetGuard {
	return &BudgetGuard{
		redis:             redis,
		monthlyTotal:      monthlyTotalCredits,
		economyThresholdP: 30,
		emergencyThreshP:  5,
	}
}

// SetThresholds 修改阈值
func (g *BudgetGuard) SetThresholds(economyPct, emergencyPct int) {
	g.economyThresholdP = economyPct
	g.emergencyThreshP = emergencyPct
}

// Check 查询当前预算档位（不扣减）
func (g *BudgetGuard) Check(ctx context.Context) BudgetLevel {
	if g.redis == nil || g.monthlyTotal <= 0 {
		return BudgetNormal
	}
	key := g.monthKey()
	usedStr, err := g.redis.Get(ctx, key).Result()
	if err != nil {
		// key 不存在 → 本月未使用 → NORMAL
		return BudgetNormal
	}
	var used int64
	_, _ = fmt.Sscanf(usedStr, "%d", &used)
	remaining := g.monthlyTotal - used
	return g.classify(remaining)
}

// Deduct 扣减预算（tokens 估算转为积分）
// 规则：每次对话按 modelCostRMBPerM × tokensTotal / 1e6 扣减
// 简化实现：直接传入已算好的 credits 数
func (g *BudgetGuard) Deduct(ctx context.Context, credits int64) error {
	if g.redis == nil || credits <= 0 {
		return nil
	}
	key := g.monthKey()
	pipe := g.redis.TxPipeline()
	incr := pipe.IncrBy(ctx, key, credits)
	// 第一次扣减时设 35 天 TTL（跨月后自动过期）
	pipe.Expire(ctx, key, 35*24*time.Hour)
	if _, err := pipe.Exec(ctx); err != nil {
		logger.L.Warn("budget deduct failed", zap.Error(err))
		return err
	}
	used := incr.Val()
	logger.L.Debug("budget deducted",
		zap.Int64("added", credits),
		zap.Int64("used_month_total", used),
		zap.Int64("monthly_limit", g.monthlyTotal),
	)
	return nil
}

// UsedAndRemaining 返回 (used, remaining, total)
func (g *BudgetGuard) UsedAndRemaining(ctx context.Context) (int64, int64, int64) {
	if g.redis == nil || g.monthlyTotal <= 0 {
		return 0, g.monthlyTotal, g.monthlyTotal
	}
	key := g.monthKey()
	usedStr, err := g.redis.Get(ctx, key).Result()
	if err != nil {
		return 0, g.monthlyTotal, g.monthlyTotal
	}
	var used int64
	_, _ = fmt.Sscanf(usedStr, "%d", &used)
	remaining := g.monthlyTotal - used
	if remaining < 0 {
		remaining = 0
	}
	return used, remaining, g.monthlyTotal
}

func (g *BudgetGuard) classify(remainingCredits int64) BudgetLevel {
	if g.monthlyTotal <= 0 {
		return BudgetNormal
	}
	pct := float64(remainingCredits) * 100.0 / float64(g.monthlyTotal)
	if pct <= float64(g.emergencyThreshP) {
		return BudgetEmergency
	}
	if pct <= float64(g.economyThresholdP) {
		return BudgetEconomy
	}
	return BudgetNormal
}

func (g *BudgetGuard) monthKey() string {
	now := time.Now()
	return fmt.Sprintf("support:budget:month:%04d%02d", now.Year(), int(now.Month()))
}
