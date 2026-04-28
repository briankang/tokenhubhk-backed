package database

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunWangsuOfficialPricingMigration 强制将所有网宿模型的成本价/售价/缓存价/阶梯价
// 统一更新为 wangsu_model_capabilities.go 中的官网权威数据。
//
// 调用时机：每次 RunDataMigrations（启动时不运行，管理员触发升级接口时执行）。
// 幂等性：不做"已相同则跳过"检查，直接 UPDATE 到目标值；重复运行总是一致的结果。
//
// 覆盖字段（AIModel）：
//   - InputCostRMB / OutputCostRMB / InputPricePerToken / OutputPricePerToken
//   - CacheInputPriceRMB / CacheWritePriceRMB / SupportsCache / CacheMechanism / CacheMinTokens
//   - PriceTiers（阶梯模型）
//   - Description（注明权威来源 + 映射关系）
//
// 覆盖字段（ModelPricing）：
//   - InputPriceRMB / OutputPriceRMB / InputPricePerToken / OutputPricePerToken
//   - PriceTiers
//
// 售价计算：official × contract_discount × WangsuSellMarkup（当前 1.30×）。已有管理员手动设定的折扣会被覆盖回默认，
// 如管理员已自定义需谨慎（本期默认触发但可通过 force=false 在未来扩展跳过逻辑）。
func RunWangsuOfficialPricingMigration(db *gorm.DB) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	if db == nil {
		log.Warn("wangsu_official_pricing: db is nil, skip")
		return
	}
	RunPriceSourceColumnsMigration(db)

	// 定位网宿供应商
	var sup model.Supplier
	if err := db.Where("code = ? AND access_type = ?", "wangsu_aigw", "api").First(&sup).Error; err != nil {
		log.Info("wangsu_official_pricing: wangsu_aigw 供应商不存在，跳过迁移")
		return
	}

	updated := 0
	notFound := 0
	for _, m := range wangsuModels {
		var ai model.AIModel
		if err := db.Where("supplier_id = ? AND model_name = ?", sup.ID, m.ModelName).First(&ai).Error; err != nil {
			notFound++
			continue
		}

		// 1. 默认（非阶梯）官网价。折扣只写入 discount 字段，由 buildPriceSummary 计算折后成本。
		inputCostRMB := round6(m.InputUSDPerM * USDCNYSnapshot)
		outputCostRMB := round6(m.OutputUSDPerM * USDCNYSnapshot)
		cacheReadCostRMB := round6(m.CacheReadUSDPerM * USDCNYSnapshot)
		cacheExplicitReadCostRMB := round6(m.CacheExplicitReadUSDPerM * USDCNYSnapshot)
		cacheWriteCostRMB := round6(m.CacheWriteUSDPerM * USDCNYSnapshot)
		cacheStorageCostRMB := round6(m.CacheStorageUSDPerMHour * USDCNYSnapshot)

		// 2. 阶梯成本价（AIModel.PriceTiers）
		var costTiersJSON model.JSON
		if len(m.PriceTiersUSD) > 0 {
			costTiers := make([]map[string]any, 0, len(m.PriceTiersUSD))
			for _, t := range m.PriceTiersUSD {
				costTiers = append(costTiers, map[string]any{
					"label":             t.Label,
					"max_input_tokens":  t.MaxInputTokens,
					"input_price_rmb":   round6(t.InputUSDPerM * USDCNYSnapshot),
					"output_price_rmb":  round6(t.OutputUSDPerM * USDCNYSnapshot),
					"cache_read_rmb":    round6(t.CacheReadUSDPerM * USDCNYSnapshot),
					"cache_write_rmb":   round6(t.CacheWriteUSDPerM * USDCNYSnapshot),
					"source_usd_input":  t.InputUSDPerM,
					"source_usd_output": t.OutputUSDPerM,
				})
			}
			if b, err := json.Marshal(costTiers); err == nil {
				costTiersJSON = model.JSON(b)
			}
		}

		// 3. Description 重写（透明化官网来源 + 映射）
		desc := fmt.Sprintf("%s - 经网宿网关代理。官网价 $%.4f/$%.4f × 汇率 %.2f；合同折扣 %.3f 单独体现在折后成本",
			m.DisplayName, m.InputUSDPerM, m.OutputUSDPerM, USDCNYSnapshot, m.Discount)
		if m.MappedFrom != "" {
			desc += fmt.Sprintf("。官方对标：%s", m.MappedFrom)
		}
		if len(m.PriceTiersUSD) > 0 {
			desc += "。含阶梯定价（≤200K / >200K）"
		}

		// 4. 更新 AIModel
		updates := map[string]any{
			"input_cost_rmb":                 inputCostRMB,
			"output_cost_rmb":                outputCostRMB,
			"input_price_per_token":          int64(math.Round(inputCostRMB * 10000)),
			"output_price_per_token":         int64(math.Round(outputCostRMB * 10000)),
			"cache_input_price_rmb":          cacheReadCostRMB,
			"cache_explicit_input_price_rmb": cacheExplicitReadCostRMB,
			"cache_write_price_rmb":          cacheWriteCostRMB,
			"cache_storage_price_rmb":        cacheStorageCostRMB,
			"price_source_currency":          "USD",
			"price_source_exchange_rate":     USDCNYSnapshot,
			"input_cost_usd":                 round6(m.InputUSDPerM),
			"output_cost_usd":                round6(m.OutputUSDPerM),
			"cache_input_price_usd":          round6(m.CacheReadUSDPerM),
			"cache_explicit_input_price_usd": round6(m.CacheExplicitReadUSDPerM),
			"cache_write_price_usd":          round6(m.CacheWriteUSDPerM),
			"cache_storage_price_usd":        round6(m.CacheStorageUSDPerMHour),
			"supports_cache":                 m.SupportsCache,
			"cache_mechanism":                pickStr(m.CacheMechanism, "none"),
			"cache_min_tokens":               m.CacheMinTokens,
			"discount":                       m.Discount,
			"description":                    desc,
			"max_output_tokens":              m.MaxOutputTokens,
			"max_input_tokens":               m.ContextWindow,
			"context_window":                 m.ContextWindow,
			"model_type":                     m.ModelType,
		}
		if len(m.InputModalities) > 0 {
			updates["input_modalities"] = jsonStringSlice(m.InputModalities)
		}
		if len(m.OutputModalities) > 0 {
			updates["output_modalities"] = jsonStringSlice(m.OutputModalities)
		}
		if m.SupportsThinking && m.OutputUSDPerM > 0 {
			updates["output_cost_thinking_usd"] = round6(m.OutputUSDPerM)
		}
		if costTiersJSON != nil {
			updates["price_tiers"] = costTiersJSON
		} else {
			// 无阶梯时清空旧 PriceTiers（避免遗留脏数据）
			updates["price_tiers"] = nil
		}
		if err := db.Model(&model.AIModel{}).Where("id = ?", ai.ID).Updates(updates).Error; err != nil {
			log.Warn("wangsu_official_pricing: 更新 AIModel 失败",
				zap.String("model", m.ModelName), zap.Error(err))
			continue
		}

		// 5. 更新/创建 ModelPricing（售价 = 官网价 × 合同折扣 × 平台默认加价）
		sellIn := round6(inputCostRMB * m.Discount * WangsuSellMarkup)
		sellOut := round6(outputCostRMB * m.Discount * WangsuSellMarkup)

		var sellTiersJSON model.JSON
		if len(m.PriceTiersUSD) > 0 {
			sellTiers := make([]map[string]any, 0, len(m.PriceTiersUSD))
			for _, t := range m.PriceTiersUSD {
				tIn := round6(t.InputUSDPerM * USDCNYSnapshot * m.Discount * WangsuSellMarkup)
				tOut := round6(t.OutputUSDPerM * USDCNYSnapshot * m.Discount * WangsuSellMarkup)
				sellTiers = append(sellTiers, map[string]any{
					"label":            t.Label,
					"max_input_tokens": t.MaxInputTokens,
					"input_price_rmb":  tIn,
					"output_price_rmb": tOut,
				})
			}
			if b, err := json.Marshal(sellTiers); err == nil {
				sellTiersJSON = model.JSON(b)
			}
		}

		var existing model.ModelPricing
		pricingErr := db.Where("model_id = ?", ai.ID).First(&existing).Error
		now := time.Now()
		if pricingErr != nil {
			// 不存在 → 创建
			mp := model.ModelPricing{
				ModelID:             ai.ID,
				InputPricePerToken:  int64(math.Round(sellIn * 10000)),
				InputPriceRMB:       sellIn,
				OutputPricePerToken: int64(math.Round(sellOut * 10000)),
				OutputPriceRMB:      sellOut,
				Currency:            "CREDIT",
				EffectiveFrom:       &now,
				PriceTiers:          sellTiersJSON,
			}
			if err := db.Create(&mp).Error; err != nil {
				log.Warn("wangsu_official_pricing: 创建 ModelPricing 失败",
					zap.String("model", m.ModelName), zap.Error(err))
			}
		} else {
			// 存在 → 更新
			pUpdates := map[string]any{
				"input_price_per_token":  int64(math.Round(sellIn * 10000)),
				"input_price_rmb":        sellIn,
				"output_price_per_token": int64(math.Round(sellOut * 10000)),
				"output_price_rmb":       sellOut,
				"effective_from":         now,
			}
			if sellTiersJSON != nil {
				pUpdates["price_tiers"] = sellTiersJSON
			} else {
				pUpdates["price_tiers"] = nil
			}
			if err := db.Model(&model.ModelPricing{}).Where("id = ?", existing.ID).Updates(pUpdates).Error; err != nil {
				log.Warn("wangsu_official_pricing: 更新 ModelPricing 失败",
					zap.String("model", m.ModelName), zap.Error(err))
			}
		}

		updated++
	}

	log.Info("wangsu_official_pricing: 完成",
		zap.Int("updated", updated),
		zap.Int("not_found", notFound),
		zap.Int("total", len(wangsuModels)))
}
