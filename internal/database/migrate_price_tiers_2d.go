package database

import (
	"encoding/json"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunPriceTiers2DMigration 幂等地为已有模型补齐二维阶梯价格数据
//
// 迁移策略：
//  1. 遍历 ai_models 表，对 price_tiers 字段：
//     - 为空/NULL：注入默认阶梯 (0, +∞] × (0, +∞]（基于 input_cost_rmb / output_cost_rmb）
//     - 非空：对每条 tier 调 Normalize() 同步新旧字段（MinTokens ↔ InputMin）
//  2. 同样处理 model_pricings 表
//  3. 写入 zap 日志记录处理结果
//
// 此迁移在 bootstrap 阶段调用，确保后续计费引擎能统一走阶梯路径。
func RunPriceTiers2DMigration(db *gorm.DB) {
	start := time.Now()

	modelsUpdated := migrateAIModelsPriceTiers(db)
	pricingsUpdated := migrateModelPricingsPriceTiers(db)

	logger.L.Info("price tiers 2D migration: complete",
		zap.Int("ai_models_updated", modelsUpdated),
		zap.Int("model_pricings_updated", pricingsUpdated),
		zap.Duration("duration", time.Since(start)),
	)
}

// migrateAIModelsPriceTiers 处理 ai_models 表
func migrateAIModelsPriceTiers(db *gorm.DB) int {
	type row struct {
		ID           uint
		ModelName    string
		InputCostRMB float64
		OutputCostRMB float64
		PriceTiers   []byte
	}

	var rows []row
	if err := db.Table("ai_models").
		Select("id, model_name, input_cost_rmb, output_cost_rmb, price_tiers").
		Find(&rows).Error; err != nil {
		logger.L.Warn("migrate price_tiers: list ai_models failed", zap.Error(err))
		return 0
	}

	updated := 0
	for _, r := range rows {
		data := decodeOrInitTiersData(r.PriceTiers)
		changed := false

		// 1. 空列表 → 注入默认阶梯
		if len(data.Tiers) == 0 {
			if r.InputCostRMB > 0 || r.OutputCostRMB > 0 {
				data.Tiers = []model.PriceTier{
					model.DefaultTier(r.InputCostRMB, r.OutputCostRMB),
				}
				changed = true
			}
		} else {
			// 2. 现有 tier 调 Normalize 同步字段
			for i := range data.Tiers {
				before := data.Tiers[i]
				data.Tiers[i].Normalize()
				if normalizeChanged(before, data.Tiers[i]) {
					changed = true
				}
			}
			// 按 InputMin 升序
			model.SortTiers(data.Tiers)
		}

		if !changed {
			continue
		}

		if data.Currency == "" {
			data.Currency = "CNY"
		}
		data.UpdatedAt = time.Now()

		raw, err := json.Marshal(data)
		if err != nil {
			logger.L.Warn("migrate price_tiers: marshal failed",
				zap.Uint("model_id", r.ID),
				zap.String("model_name", r.ModelName),
				zap.Error(err))
			continue
		}

		if err := db.Table("ai_models").
			Where("id = ?", r.ID).
			Update("price_tiers", raw).Error; err != nil {
			logger.L.Warn("migrate price_tiers: update failed",
				zap.Uint("model_id", r.ID),
				zap.Error(err))
			continue
		}
		updated++
	}
	return updated
}

// migrateModelPricingsPriceTiers 处理 model_pricings 表
func migrateModelPricingsPriceTiers(db *gorm.DB) int {
	type row struct {
		ID         uint
		ModelID    uint
		PriceTiers []byte
	}

	var rows []row
	if err := db.Table("model_pricings").
		Select("id, model_id, price_tiers").
		Find(&rows).Error; err != nil {
		logger.L.Warn("migrate price_tiers: list model_pricings failed", zap.Error(err))
		return 0
	}

	updated := 0
	for _, r := range rows {
		if len(r.PriceTiers) == 0 {
			continue // 空则留空（计费路径会 fallback 到模型级 PriceTiers）
		}

		data := decodeOrInitTiersData(r.PriceTiers)
		if len(data.Tiers) == 0 {
			continue
		}

		changed := false
		for i := range data.Tiers {
			before := data.Tiers[i]
			data.Tiers[i].Normalize()
			if normalizeChanged(before, data.Tiers[i]) {
				changed = true
			}
		}
		model.SortTiers(data.Tiers)

		if !changed {
			continue
		}

		raw, err := json.Marshal(data)
		if err != nil {
			logger.L.Warn("migrate price_tiers model_pricings: marshal failed",
				zap.Uint("id", r.ID), zap.Error(err))
			continue
		}

		if err := db.Table("model_pricings").
			Where("id = ?", r.ID).
			Update("price_tiers", raw).Error; err != nil {
			logger.L.Warn("migrate price_tiers model_pricings: update failed",
				zap.Uint("id", r.ID), zap.Error(err))
			continue
		}
		updated++
	}
	return updated
}

// decodeOrInitTiersData 解析 JSON，失败返回空结构
func decodeOrInitTiersData(raw []byte) model.PriceTiersData {
	var data model.PriceTiersData
	if len(raw) == 0 || string(raw) == "null" {
		return data
	}
	_ = json.Unmarshal(raw, &data)
	return data
}

// normalizeChanged 判断 Normalize 是否改变了字段
func normalizeChanged(before, after model.PriceTier) bool {
	return before.InputMin != after.InputMin ||
		!ptrInt64Equal(before.InputMax, after.InputMax) ||
		before.OutputMin != after.OutputMin ||
		!ptrInt64Equal(before.OutputMax, after.OutputMax) ||
		before.OutputMinExclusive != after.OutputMinExclusive
}

func ptrInt64Equal(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
