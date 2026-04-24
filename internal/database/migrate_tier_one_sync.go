package database

import (
	"encoding/json"
	"math"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunTierOneSyncMigration 将 ai_models.price_tiers[0] 的价格同步到顶层字段
// (input_cost_rmb / output_cost_rmb / output_cost_thinking_rmb)。
//
// 背景：
//   - 爬虫（阿里云/火山引擎）写入时 price_tiers 含阶梯一详细数据，
//     但顶层 input_cost_rmb/output_cost_rmb 可能因历史路径漂移与阶梯一不一致
//   - BatchUpdateSellingPrice 判 InputCostRMB==0 跳过，导致大量只有阶梯数据
//     的模型无法批量设置售价
//   - 前端市场卡片顶部大字显示走的是 input_cost_rmb/output_cost_rmb，
//     阶梯一必须对齐顶层以保证展示一致
//
// 迁移策略（幂等）：
//   - 仅当 tier[0].input_price > 0 且与顶层 input_cost_rmb 差异 > 0.0001 时才更新
//   - output_cost_thinking_rmb 为 0 时用 tier[0].output_price_thinking 填充（若非 0）
//   - 不覆盖管理员已手动调整（若 abs(diff) <= 0.0001 视为已对齐）
func RunTierOneSyncMigration(db *gorm.DB) {
	start := time.Now()

	// 只拉有阶梯数据的行，减少扫描
	var rows []model.AIModel
	if err := db.
		Select("id, input_cost_rmb, output_cost_rmb, output_cost_thinking_rmb, price_tiers").
		Where("price_tiers IS NOT NULL AND JSON_LENGTH(price_tiers) > 0").
		Find(&rows).Error; err != nil {
		logger.L.Warn("tier-one sync migration: load failed", zap.Error(err))
		return
	}

	updated := 0
	for _, row := range rows {
		if len(row.PriceTiers) == 0 {
			continue
		}
		var data model.PriceTiersData
		if err := json.Unmarshal(row.PriceTiers, &data); err != nil {
			continue
		}
		if len(data.Tiers) == 0 {
			continue
		}
		t0 := data.Tiers[0]

		updates := map[string]interface{}{}
		if t0.InputPrice > 0 && math.Abs(t0.InputPrice-row.InputCostRMB) > 0.0001 {
			updates["input_cost_rmb"] = t0.InputPrice
		}
		if t0.OutputPrice > 0 && math.Abs(t0.OutputPrice-row.OutputCostRMB) > 0.0001 {
			updates["output_cost_rmb"] = t0.OutputPrice
		}
		// thinking: 仅在顶层为 0 时回填（尊重手动配置）
		if t0.OutputPriceThinking > 0 && row.OutputCostThinkingRMB == 0 {
			updates["output_cost_thinking_rmb"] = t0.OutputPriceThinking
		}

		if len(updates) == 0 {
			continue
		}
		if err := db.Model(&model.AIModel{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
			logger.L.Debug("tier-one sync: update row failed",
				zap.Uint("model_id", row.ID), zap.Error(err))
			continue
		}
		updated++
	}

	logger.L.Info("tier-one sync migration: complete",
		zap.Int("models_scanned", len(rows)),
		zap.Int("models_updated", updated),
		zap.Duration("duration", time.Since(start)))
}
