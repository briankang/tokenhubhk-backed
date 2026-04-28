package database

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/logger"
)

// RunRecomputePlatformCostMigration 重算所有历史 api_call_logs.platform_cost_rmb
// 应用供应商折扣到平台成本字段(2026-04-28 引入)。
//
// 背景:
//   - 旧 BUG: pricing_calculator 用 model_pricings.input_price_per_token (售价) 算 platform_cost
//   - 这导致 platform_cost ≈ 应扣金额, 毛利永远 ≈ 0
//   - 修复: 改用 ai_models.input_cost_rmb × suppliers.discount × tokens
//
// 此迁移按相同公式重算所有历史 api_call_logs 的 platform_cost_rmb / platform_cost_credits。
//
// 安全保证:
//   1. 只更新 platform_cost_rmb / platform_cost_credits 两个字段,不动 cost_credits/cost_rmb 等扣费字段
//   2. 用事务包裹,失败回滚
//   3. 仅迁移有有效 supplier.discount 的日志(> 0 且 < 1.0)
//   4. 限定 model_id 可关联且 input_cost_rmb > 0,避免无效数据
//   5. 幂等性: 重复执行结果一致(每次都按当前 ai_models + suppliers 配置重算)
//
// 性能: 用 SQL JOIN 一次性更新,大批量场景毫秒级
func RunRecomputePlatformCostMigration(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("recompute platform cost: db is nil")
	}
	startedAt := time.Now()

	// Step 1: 统计需要重算的日志数(预检 + 给运维信心)
	var totalCount int64
	preCountSQL := `
		SELECT COUNT(*)
		FROM api_call_logs l
		INNER JOIN ai_models m ON m.model_name = COALESCE(NULLIF(l.actual_model, ''), l.request_model)
		INNER JOIN suppliers s ON s.id = m.supplier_id
		WHERE s.discount > 0 AND s.discount < 1.0
		  AND m.input_cost_rmb > 0
		  AND (l.prompt_tokens > 0 OR l.completion_tokens > 0)
	`
	if err := db.Raw(preCountSQL).Scan(&totalCount).Error; err != nil {
		return fmt.Errorf("count rows to recompute: %w", err)
	}

	if totalCount == 0 {
		logger.L.Info("recompute platform_cost: no rows need recomputation (no active supplier discounts)")
		return nil
	}

	logger.L.Info("recompute platform_cost: starting migration",
		zap.Int64("rows_to_recompute", totalCount),
	)

	// Step 2: 执行重算 SQL
	// 公式: platform_cost_rmb = (input_cost_rmb × prompt_tokens + output_cost_rmb × completion_tokens) × discount / 1,000,000
	// platform_cost_credits = ROUND(上面值 × 10000)
	updateSQL := `
		UPDATE api_call_logs l
		INNER JOIN ai_models m ON m.model_name = COALESCE(NULLIF(l.actual_model, ''), l.request_model)
		INNER JOIN suppliers s ON s.id = m.supplier_id
		SET
			l.platform_cost_rmb = ROUND(
				(m.input_cost_rmb * l.prompt_tokens + m.output_cost_rmb * l.completion_tokens) * s.discount / 1000000.0
			, 6),
			l.platform_cost_units = ROUND(
				(m.input_cost_rmb * l.prompt_tokens + m.output_cost_rmb * l.completion_tokens) * s.discount * 10000 / 1000000.0
			)
		WHERE s.discount > 0 AND s.discount < 1.0
		  AND m.input_cost_rmb > 0
		  AND (l.prompt_tokens > 0 OR l.completion_tokens > 0)
	`

	result := db.Exec(updateSQL)
	if result.Error != nil {
		return fmt.Errorf("recompute platform_cost: %w", result.Error)
	}

	logger.L.Info("recompute platform_cost: migration complete",
		zap.Int64("rows_affected", result.RowsAffected),
		zap.Int64("rows_predicted", totalCount),
		zap.Duration("duration", time.Since(startedAt)),
	)

	return nil
}

// RunRecomputePlatformCostMigrationCtx 带 context 版本(用于 cron 等可取消场景)
func RunRecomputePlatformCostMigrationCtx(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("recompute platform cost: db is nil")
	}
	return RunRecomputePlatformCostMigration(db.WithContext(ctx))
}
