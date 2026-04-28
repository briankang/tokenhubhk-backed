// Package billing 提供计费相关的辅助服务（对账、异常检测等）。
package billing

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/service/audit"
)

// AnomalyChecker 计费异常巡检器
//
// 每小时遍历近 1 小时的 api_call_logs，按 5 条 SQL 规则检查异常并写入 audit_logs：
//  1. 定价缺失：cost_credits=0 且 prompt_tokens>100 且 status_code=200
//  2. 缓存字段反常：cache_read_tokens > prompt_tokens
//  3. 流式 usage 缺失（实际全 0）：completion_tokens=0 且 finish_reason 为 stop/length
//  4. 单 model 平均单价偏离 7 天均值 > 5σ（突变）
//  5. 缓存节省未计算：cached_tokens > 0 但 cache_savings_rmb = 0（已超过 5 分钟仍 0）
//
// 命中即写一条 audit_logs warning 提醒管理员复核。
type AnomalyChecker struct {
	db       *gorm.DB
	auditSvc *audit.AuditService
}

// NewAnomalyChecker 构造函数；db 必填，auditSvc 可空（空则只记日志）
func NewAnomalyChecker(db *gorm.DB, auditSvc *audit.AuditService) *AnomalyChecker {
	return &AnomalyChecker{db: db, auditSvc: auditSvc}
}

// AnomalyResult 异常检查汇总
type AnomalyResult struct {
	WindowStart       time.Time `json:"window_start"`
	WindowEnd         time.Time `json:"window_end"`
	PricingMissing    int       `json:"pricing_missing"`     // 规则1
	CacheFieldInvalid int       `json:"cache_field_invalid"` // 规则2
	StreamUsageMissing int      `json:"stream_usage_missing"` // 规则3
	UnitPriceShift    int       `json:"unit_price_shift"`    // 规则4
	CacheSavingsMissing int     `json:"cache_savings_missing"` // 规则5
	TotalAlerts       int       `json:"total_alerts"`
}

// Run 执行一次异常巡检（覆盖最近 1 小时窗口）
func (c *AnomalyChecker) Run(ctx context.Context) (*AnomalyResult, error) {
	if c.db == nil {
		return nil, fmt.Errorf("anomaly checker: db is nil")
	}
	now := time.Now()
	windowStart := now.Add(-1 * time.Hour)
	windowEnd := now

	res := &AnomalyResult{
		WindowStart: windowStart,
		WindowEnd:   windowEnd,
	}

	// 规则 1：定价缺失（成功 2xx + 大于 100 prompt 但 cost=0）
	var rule1 int64
	if err := c.db.WithContext(ctx).Table("api_call_logs").
		Where("created_at BETWEEN ? AND ?", windowStart, windowEnd).
		Where("cost_credits = 0 AND prompt_tokens > 100 AND status_code = 200").
		Count(&rule1).Error; err == nil {
		res.PricingMissing = int(rule1)
		if rule1 > 0 {
			c.writeAlert(ctx, "billing_anomaly_pricing_missing", "定价缺失",
				fmt.Sprintf("近1小时有 %d 条成功请求 cost_credits=0 但 prompt_tokens>100，疑似定价配置缺失或损坏", rule1))
		}
	}

	// 规则 2：缓存字段反常（cache_read_tokens > prompt_tokens 不可能）
	var rule2 int64
	if err := c.db.WithContext(ctx).Table("api_call_logs").
		Where("created_at BETWEEN ? AND ?", windowStart, windowEnd).
		Where("cache_read_tokens > prompt_tokens AND prompt_tokens > 0").
		Count(&rule2).Error; err == nil {
		res.CacheFieldInvalid = int(rule2)
		if rule2 > 0 {
			c.writeAlert(ctx, "billing_anomaly_cache_invalid", "缓存字段反常",
				fmt.Sprintf("近1小时有 %d 条 cache_read_tokens>prompt_tokens，provider 适配层字段映射可能错误", rule2))
		}
	}

	// 规则 3：流式 usage 缺失（应有完成但 completion_tokens=0；排除 estimator 兜底已生效的）
	var rule3 int64
	if err := c.db.WithContext(ctx).Table("api_call_logs").
		Where("created_at BETWEEN ? AND ?", windowStart, windowEnd).
		Where("completion_tokens = 0 AND status_code = 200 AND prompt_tokens > 0").
		Where("usage_source = ?", "provider"). // 排除已被 estimator 修正的
		Count(&rule3).Error; err == nil {
		res.StreamUsageMissing = int(rule3)
		if rule3 > 0 {
			c.writeAlert(ctx, "billing_anomaly_stream_usage_missing", "流式 usage 缺失",
				fmt.Sprintf("近1小时有 %d 条成功响应 completion_tokens=0（usage_source=provider），上游 SSE chunk 可能未返回 usage 字段", rule3))
		}
	}

	// 规则 4：单 model 平均 token 价偏离 7 天均值（仅检查近1小时调用 ≥5 次的 model）
	rule4 := c.checkUnitPriceShift(ctx, windowStart, windowEnd)
	res.UnitPriceShift = rule4

	// 规则 5：缓存节省未计算（cache_read_tokens>0 但 savings=0，超 5min 仍未补全）
	var rule5 int64
	cacheCheckBefore := windowEnd.Add(-5 * time.Minute)
	if err := c.db.WithContext(ctx).Table("api_call_logs").
		Where("created_at BETWEEN ? AND ?", windowStart, cacheCheckBefore).
		Where("cache_read_tokens > 0 AND cache_savings_rmb = 0").
		Count(&rule5).Error; err == nil {
		res.CacheSavingsMissing = int(rule5)
		if rule5 > 0 {
			c.writeAlert(ctx, "billing_anomaly_cache_savings_missing", "缓存节省未计算",
				fmt.Sprintf("近1小时有 %d 条 cache_read_tokens>0 但 cache_savings_rmb=0，CalculateWithCache 异步计算可能失败", rule5))
		}
	}

	res.TotalAlerts = res.PricingMissing + res.CacheFieldInvalid + res.StreamUsageMissing + res.UnitPriceShift + res.CacheSavingsMissing
	if res.TotalAlerts > 0 {
		logger.L.Warn("billing anomaly checker: alerts",
			zap.Int("total_alerts", res.TotalAlerts),
			zap.Int("pricing_missing", res.PricingMissing),
			zap.Int("cache_invalid", res.CacheFieldInvalid),
			zap.Int("stream_missing", res.StreamUsageMissing),
			zap.Int("unit_price_shift", res.UnitPriceShift),
			zap.Int("cache_savings_missing", res.CacheSavingsMissing))
	}
	return res, nil
}

// checkUnitPriceShift 规则 4：检查单位 token 价突变
//
// 简化实现：分别取近1小时和近7天每个 model 的平均单位价（cost_credits / total_tokens × 1M），
// 若近1小时偏离 7 天均值 > 50% 则视为突变（5σ 估算复杂度高，本次用相对偏差代替）
func (c *AnomalyChecker) checkUnitPriceShift(ctx context.Context, windowStart, windowEnd time.Time) int {
	type avgPrice struct {
		ModelName string
		Avg       float64
		Cnt       int64
	}

	var hourly []avgPrice
	if err := c.db.WithContext(ctx).Table("api_call_logs").
		Select("model_name, AVG(cost_credits * 1000000.0 / NULLIF(prompt_tokens + completion_tokens, 0)) AS avg, COUNT(*) AS cnt").
		Where("created_at BETWEEN ? AND ?", windowStart, windowEnd).
		Where("prompt_tokens + completion_tokens >= 100 AND cost_credits > 0").
		Group("model_name").
		Having("cnt >= 5").
		Scan(&hourly).Error; err != nil {
		return 0
	}
	if len(hourly) == 0 {
		return 0
	}

	weekStart := windowEnd.Add(-7 * 24 * time.Hour)
	var weekly []avgPrice
	if err := c.db.WithContext(ctx).Table("api_call_logs").
		Select("model_name, AVG(cost_credits * 1000000.0 / NULLIF(prompt_tokens + completion_tokens, 0)) AS avg, COUNT(*) AS cnt").
		Where("created_at BETWEEN ? AND ?", weekStart, windowStart).
		Where("prompt_tokens + completion_tokens >= 100 AND cost_credits > 0").
		Group("model_name").
		Scan(&weekly).Error; err != nil {
		return 0
	}
	weeklyMap := make(map[string]float64, len(weekly))
	for _, w := range weekly {
		weeklyMap[w.ModelName] = w.Avg
	}

	hits := 0
	for _, h := range hourly {
		base, ok := weeklyMap[h.ModelName]
		if !ok || base <= 0 {
			continue
		}
		delta := (h.Avg - base) / base
		if delta < 0 {
			delta = -delta
		}
		if delta > 0.5 {
			hits++
			c.writeAlert(ctx, "billing_anomaly_price_shift", "单位价突变",
				fmt.Sprintf("模型 %s 近1小时均价 %.2f 积分/百万tok 偏离 7 天均值 %.2f（变化 %.1f%%）", h.ModelName, h.Avg, base, delta*100))
		}
	}
	return hits
}

// writeAlert 写入一条 audit_logs warning（异步入队）
func (c *AnomalyChecker) writeAlert(ctx context.Context, action, feature, message string) {
	if c.auditSvc == nil {
		logger.L.Warn("billing anomaly (no audit svc)", zap.String("action", action), zap.String("message", message))
		return
	}
	c.auditSvc.Enqueue(&model.AuditLog{
		Action:   action,
		Resource: "billing",
		Menu:     "计费异常",
		Feature:  feature,
		Method:   "CRON",
		Path:     "cron://billing_anomaly_check",
		Remark:   message,
	})
}
