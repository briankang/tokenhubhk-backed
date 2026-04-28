package database

import (
	"encoding/json"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

const talkingDataSeed20DefaultSellingRatio = 1.5

// RunTalkingDataSeed20PricingMigration keeps Doubao Seed 2.0 official tiers,
// tier cache prices, and platform selling tiers aligned for existing rows.
func RunTalkingDataSeed20PricingMigration(db *gorm.DB) {
	if db == nil {
		return
	}
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	var supplier model.Supplier
	if err := db.Where("code = ? AND access_type = ?", "talkingdata", "api").First(&supplier).Error; err != nil {
		return
	}

	updatedModels := int64(0)
	updatedPricings := int64(0)
	for _, def := range talkingDataModelDefs() {
		if !isTalkingDataSeed20TieredModel(def) {
			continue
		}

		var item model.AIModel
		if err := db.Where("supplier_id = ? AND model_name = ? AND deleted_at IS NULL", supplier.ID, def.ModelName).First(&item).Error; err != nil {
			continue
		}

		var pricing model.ModelPricing
		hasPricing := db.Where("model_id = ? AND deleted_at IS NULL", item.ID).First(&pricing).Error == nil
		inputRatio, outputRatio := sellingRatiosForSeed20(def, &pricing, hasPricing)

		costJSON, costTiers := buildTalkingDataSeed20CostTiers(def, inputRatio, outputRatio)
		if costJSON == nil || len(costTiers) == 0 {
			continue
		}

		modelUpdates := map[string]interface{}{
			"input_cost_rmb":          def.InputCostRMB,
			"output_cost_rmb":         def.OutputCostRMB,
			"input_price_per_token":   int64(def.InputCostRMB * 10000),
			"output_price_per_token":  int64(def.OutputCostRMB * 10000),
			"supports_cache":          def.SupportsCache,
			"cache_mechanism":         def.CacheMechanism,
			"cache_min_tokens":        def.CacheMinTokens,
			"cache_input_price_rmb":   def.CacheInputPriceRMB,
			"cache_storage_price_rmb": def.CacheStoragePriceRMB,
			"price_tiers":             costJSON,
		}
		if err := db.Model(&model.AIModel{}).Where("id = ?", item.ID).Updates(modelUpdates).Error; err == nil {
			updatedModels++
		}

		sellingInput := round6(def.InputCostRMB * inputRatio)
		sellingOutput := round6(def.OutputCostRMB * outputRatio)
		sellJSON := buildTalkingDataSeed20SellingTiers(costTiers, inputRatio, outputRatio)
		if !hasPricing {
			pricing = model.ModelPricing{ModelID: item.ID}
		}
		pricing.InputPriceRMB = sellingInput
		pricing.OutputPriceRMB = sellingOutput
		pricing.InputPricePerToken = int64(sellingInput * 10000)
		pricing.OutputPricePerToken = int64(sellingOutput * 10000)
		pricing.Currency = firstNonEmpty(pricing.Currency, item.Currency, "CREDIT")
		pricing.PriceTiers = sellJSON
		if pricing.ID == 0 {
			if err := db.Create(&pricing).Error; err == nil {
				updatedPricings++
			}
		} else if err := db.Save(&pricing).Error; err == nil {
			updatedPricings++
		}
	}

	log.Info("talkingdata seed 2.0 pricing migration complete",
		zap.Int64("models_updated", updatedModels),
		zap.Int64("pricings_updated", updatedPricings))
}

func isTalkingDataSeed20TieredModel(def talkingDataModelDef) bool {
	return def.PricingUnit == model.UnitPerMillionTokens &&
		strings.HasPrefix(strings.ToLower(def.ModelName), "doubao-seed-2.0-") &&
		len(def.PriceTiers) > 0
}

func sellingRatiosForSeed20(def talkingDataModelDef, pricing *model.ModelPricing, hasPricing bool) (float64, float64) {
	inputRatio := talkingDataSeed20DefaultSellingRatio
	outputRatio := talkingDataSeed20DefaultSellingRatio
	if hasPricing && pricing != nil {
		if def.InputCostRMB > 0 && pricing.InputPriceRMB > 0 {
			inputRatio = pricing.InputPriceRMB / def.InputCostRMB
		}
		if def.OutputCostRMB > 0 && pricing.OutputPriceRMB > 0 {
			outputRatio = pricing.OutputPriceRMB / def.OutputCostRMB
		}
	}
	return inputRatio, outputRatio
}

func buildTalkingDataSeed20CostTiers(def talkingDataModelDef, inputRatio, outputRatio float64) (model.JSON, []model.PriceTier) {
	tiers := make([]model.PriceTier, len(def.PriceTiers))
	copy(tiers, def.PriceTiers)
	for i := range tiers {
		tiers[i].Normalize()
		sin := round6(tiers[i].InputPrice * inputRatio)
		sout := round6(tiers[i].OutputPrice * outputRatio)
		tiers[i].SellingInputPrice = &sin
		tiers[i].SellingOutputPrice = &sout
	}
	model.SortTiers(tiers)
	wrapped := model.PriceTiersData{
		Tiers:     tiers,
		Currency:  "CNY",
		UnitLabel: "元/百万token",
		UpdatedAt: time.Now(),
		SourceURL: "https://www.volcengine.com/docs/82379/1544106",
	}
	b, err := json.Marshal(wrapped)
	if err != nil {
		return nil, nil
	}
	return model.JSON(b), tiers
}

func buildTalkingDataSeed20SellingTiers(costTiers []model.PriceTier, inputRatio, outputRatio float64) model.JSON {
	tiers := make([]model.PriceTier, len(costTiers))
	for i, tier := range costTiers {
		sin := round6(tier.InputPrice * inputRatio)
		sout := round6(tier.OutputPrice * outputRatio)
		tiers[i] = tier
		tiers[i].InputPrice = sin
		tiers[i].OutputPrice = sout
		tiers[i].CacheInputPrice = round6(tier.CacheInputPrice * inputRatio)
		tiers[i].CacheWritePrice = round6(tier.CacheWritePrice * inputRatio)
		tiers[i].SellingInputPrice = &sin
		tiers[i].SellingOutputPrice = &sout
	}
	wrapped := model.PriceTiersData{
		Tiers:     tiers,
		Currency:  "CNY",
		UnitLabel: "元/百万token",
		UpdatedAt: time.Now(),
		SourceURL: "https://www.volcengine.com/docs/82379/1544106",
	}
	b, err := json.Marshal(wrapped)
	if err != nil {
		return nil
	}
	return model.JSON(b)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
