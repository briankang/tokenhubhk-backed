package database

import (
	"fmt"
	"math"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunPriceTierInheritanceMigration copies official tier matrices to versioned
// models only when the source model is in the same supplier and base prices
// match exactly. This avoids inventing tier prices for models whose official
// page has not been verified yet.
func RunPriceTierInheritanceMigration(db *gorm.DB) {
	if db == nil {
		return
	}
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	var items []model.AIModel
	if err := db.Preload("Supplier").Preload("Pricing").Where("deleted_at IS NULL").Find(&items).Error; err != nil {
		log.Warn("price tier inheritance: list models failed", zap.Error(err))
		return
	}

	bySupplierModel := make(map[string]model.AIModel, len(items))
	for _, item := range items {
		bySupplierModel[priceTierInheritanceKey(item.SupplierID, item.ModelName)] = item
	}

	updatedModels := int64(0)
	updatedPricings := int64(0)
	for _, target := range items {
		if tiers, ok := priceTierInheritanceTencentHY20Tiers(target); ok && !hasMeaningfulPriceTiers(target.PriceTiers) {
			if priceTierInheritanceApplyOfficialTiers(db, target, tiers, "https://cloud.tencent.com/document/product/1729/97731") {
				updatedModels++
				updatedPricings++
			}
			continue
		}

		if priceTierInheritanceIsOfficialFlatAliyun(target) && hasMeaningfulPriceTiers(target.PriceTiers) {
			if priceTierInheritanceFlattenModel(db, target) {
				updatedModels++
				updatedPricings++
			}
			continue
		}

		if hasMeaningfulPriceTiers(target.PriceTiers) {
			continue
		}

		source, ok := priceTierInheritanceSource(target, bySupplierModel)
		if !ok || !priceTierInheritancePricesMatch(source, target) {
			continue
		}

		data, ok := priceAccuracyDecodeTiers(source.PriceTiers)
		if !ok || len(data.Tiers) == 0 || !priceTierInheritanceFirstTierMatches(data.Tiers[0], target) {
			continue
		}

		target.PriceTiers = priceTierInheritanceCostTiersJSON(data, target)
		if len(target.PriceTiers) == 0 {
			continue
		}
		if err := db.Model(&model.AIModel{}).Where("id = ?", target.ID).Update("price_tiers", target.PriceTiers).Error; err != nil {
			log.Warn("price tier inheritance: update model tiers failed", zap.String("model", target.ModelName), zap.Error(err))
			continue
		}
		updatedModels++

		if tierJSON, ok := officialSellingTiersJSON(target); ok {
			pricing := target.Pricing
			if pricing == nil {
				pricing = &model.ModelPricing{ModelID: target.ID, Currency: priceAccuracyFirstNonEmpty(target.Currency, "CREDIT")}
			}
			pricing.PriceTiers = tierJSON
			now := time.Now()
			pricing.EffectiveFrom = &now
			if pricing.ID == 0 {
				if err := db.Create(pricing).Error; err != nil {
					log.Warn("price tier inheritance: create pricing tiers failed", zap.String("model", target.ModelName), zap.Error(err))
					continue
				}
			} else if err := db.Save(pricing).Error; err != nil {
				log.Warn("price tier inheritance: update pricing tiers failed", zap.String("model", target.ModelName), zap.Error(err))
				continue
			}
			updatedPricings++
		}
	}

	log.Info("price tier inheritance migration complete",
		zap.Int64("updated_models", updatedModels),
		zap.Int64("updated_pricings", updatedPricings))
}

func priceTierInheritanceSource(target model.AIModel, bySupplierModel map[string]model.AIModel) (model.AIModel, bool) {
	supplier := strings.ToLower(target.Supplier.Code)
	name := strings.ToLower(target.ModelName)
	if supplier != "aliyun_dashscope" && supplier != "alibaba" {
		return model.AIModel{}, false
	}

	sourceNames := make([]string, 0, 2)
	switch name {
	case "qwen-plus-2025-12-01", "qwen-plus-2025-09-11", "qwen-plus-2025-07-28":
		sourceNames = append(sourceNames, "qwen-plus", "qwen-plus-latest")
	default:
		return model.AIModel{}, false
	}

	for _, sourceName := range sourceNames {
		source, ok := bySupplierModel[priceTierInheritanceKey(target.SupplierID, sourceName)]
		if ok && hasMeaningfulPriceTiers(source.PriceTiers) {
			return source, true
		}
	}
	return model.AIModel{}, false
}

func priceTierInheritanceTencentHY20Tiers(item model.AIModel) ([]model.PriceTier, bool) {
	if strings.ToLower(item.Supplier.Code) != "tencent_hunyuan" {
		return nil, false
	}
	name := strings.ToLower(item.ModelName)
	switch {
	case strings.Contains(name, "2.0") && strings.Contains(name, "thinking"):
		return []model.PriceTier{
			{Name: "0<Token<=32K", InputMin: 0, InputMax: priceTierInheritanceInt64Ptr(32000), OutputMin: 0, OutputMinExclusive: true, InputPrice: 3.975, OutputPrice: 15.9},
			{Name: "32K<Token<=128K", InputMin: 32000, InputMinExclusive: true, InputMax: priceTierInheritanceInt64Ptr(128000), OutputMin: 0, OutputMinExclusive: true, InputPrice: 5.3, OutputPrice: 21.2},
		}, true
	case strings.Contains(name, "2.0") && strings.Contains(name, "instruct"):
		return []model.PriceTier{
			{Name: "0<Token<=32K", InputMin: 0, InputMax: priceTierInheritanceInt64Ptr(32000), OutputMin: 0, OutputMinExclusive: true, InputPrice: 3.18, OutputPrice: 7.95},
			{Name: "32K<Token<=128K", InputMin: 32000, InputMinExclusive: true, InputMax: priceTierInheritanceInt64Ptr(128000), OutputMin: 0, OutputMinExclusive: true, InputPrice: 4.505, OutputPrice: 11.13},
		}, true
	default:
		return nil, false
	}
}

func priceTierInheritanceInt64Ptr(v int64) *int64 {
	return &v
}

func priceTierInheritanceApplyOfficialTiers(db *gorm.DB, item model.AIModel, tiers []model.PriceTier, sourceURL string) bool {
	if len(tiers) == 0 {
		return false
	}
	raw, err := priceAccuracyMarshalTiers(model.PriceTiersData{
		Tiers:     tiers,
		Currency:  priceAccuracyFirstNonEmpty(item.PriceSourceCurrency, item.Currency, "CNY"),
		UnitLabel: "CNY / 1M tokens",
		UpdatedAt: time.Now(),
		SourceURL: sourceURL,
	})
	if err != nil {
		return false
	}
	first := tiers[0]
	updates := map[string]any{
		"price_tiers":            raw,
		"input_cost_rmb":         priceAccuracyRound6(first.InputPrice),
		"output_cost_rmb":        priceAccuracyRound6(first.OutputPrice),
		"input_price_per_token":  int64(math.Round(first.InputPrice * 10000)),
		"output_price_per_token": int64(math.Round(first.OutputPrice * 10000)),
		"supports_cache":         false,
		"cache_mechanism":        "none",
	}
	if err := db.Model(&model.AIModel{}).Where("id = ?", item.ID).Updates(updates).Error; err != nil {
		log := logger.L
		if log == nil {
			log = zap.NewNop()
		}
		log.Warn("price tier inheritance: apply official tiers failed", zap.String("model", item.ModelName), zap.Error(err))
		return false
	}

	item.PriceTiers = raw
	item.InputCostRMB = first.InputPrice
	item.OutputCostRMB = first.OutputPrice
	pricing := item.Pricing
	if pricing == nil {
		pricing = &model.ModelPricing{
			ModelID:        item.ID,
			InputPriceRMB:  first.InputPrice,
			OutputPriceRMB: first.OutputPrice,
			Currency:       priceAccuracyFirstNonEmpty(item.Currency, "CREDIT"),
		}
	}
	if tierJSON, ok := officialSellingTiersJSON(item); ok {
		pricing.PriceTiers = tierJSON
	}
	now := time.Now()
	pricing.EffectiveFrom = &now
	if pricing.ID == 0 {
		if err := db.Create(pricing).Error; err != nil {
			log := logger.L
			if log == nil {
				log = zap.NewNop()
			}
			log.Warn("price tier inheritance: create official tier pricing failed", zap.String("model", item.ModelName), zap.Error(err))
			return false
		}
		return true
	}
	if err := db.Save(pricing).Error; err != nil {
		log := logger.L
		if log == nil {
			log = zap.NewNop()
		}
		log.Warn("price tier inheritance: update official tier pricing failed", zap.String("model", item.ModelName), zap.Error(err))
		return false
	}
	return true
}

func hasMeaningfulPriceTiers(raw model.JSON) bool {
	data, ok := priceAccuracyDecodeTiers(raw)
	if !ok || len(data.Tiers) == 0 {
		return false
	}
	if len(data.Tiers) > 1 {
		return true
	}
	tier := data.Tiers[0]
	return tier.InputMax != nil || tier.OutputMax != nil || tier.InputMin > 0 || tier.OutputMin > 0
}

func priceTierInheritancePricesMatch(source, target model.AIModel) bool {
	return priceTierInheritanceFloatEqual(source.InputCostRMB, target.InputCostRMB) &&
		priceTierInheritanceFloatEqual(source.OutputCostRMB, target.OutputCostRMB) &&
		priceTierInheritanceFloatEqual(source.OutputCostThinkingRMB, target.OutputCostThinkingRMB)
}

func priceTierInheritanceFirstTierMatches(tier model.PriceTier, target model.AIModel) bool {
	return priceTierInheritanceFloatEqual(tier.InputPrice, target.InputCostRMB) &&
		priceTierInheritanceFloatEqual(tier.OutputPrice, target.OutputCostRMB) &&
		(target.OutputCostThinkingRMB <= 0 || priceTierInheritanceFloatEqual(tier.OutputPriceThinking, target.OutputCostThinkingRMB))
}

func priceTierInheritanceCostTiersJSON(source model.PriceTiersData, target model.AIModel) model.JSON {
	tiers := priceAccuracyCloneTiers(source.Tiers)
	for i := range tiers {
		tiers[i].SellingInputPrice = nil
		tiers[i].SellingOutputPrice = nil
		tiers[i].SellingOutputThinkingPrice = nil
		tiers[i].Normalize()
	}
	raw, err := priceAccuracyMarshalTiers(model.PriceTiersData{
		Tiers:     tiers,
		Currency:  priceAccuracyFirstNonEmpty(source.Currency, target.PriceSourceCurrency, target.Currency, "CNY"),
		UnitLabel: source.UnitLabel,
		UpdatedAt: time.Now(),
		SourceURL: source.SourceURL,
	})
	if err != nil {
		return nil
	}
	return raw
}

func priceTierInheritanceIsOfficialFlatAliyun(item model.AIModel) bool {
	supplier := strings.ToLower(item.Supplier.Code)
	if supplier != "aliyun_dashscope" && supplier != "alibaba" {
		return false
	}
	name := strings.ToLower(item.ModelName)
	if strings.HasPrefix(name, "qwen-max") {
		return true
	}
	switch name {
	case "qwen-plus-2025-07-14", "qwen-plus-2025-04-28", "qwen-plus-2025-01-25", "qwen-plus-2025-01-12", "qwen-plus-2024-12-20":
		return true
	default:
		return false
	}
}

func priceTierInheritanceFlattenModel(db *gorm.DB, item model.AIModel) bool {
	costJSON := priceTierInheritanceSingleTierJSON(item, nil)
	if len(costJSON) == 0 {
		return false
	}
	if err := db.Model(&model.AIModel{}).Where("id = ?", item.ID).Update("price_tiers", costJSON).Error; err != nil {
		log := logger.L
		if log == nil {
			log = zap.NewNop()
		}
		log.Warn("price tier inheritance: flatten model tiers failed", zap.String("model", item.ModelName), zap.Error(err))
		return false
	}

	pricing := item.Pricing
	if pricing == nil {
		pricing = &model.ModelPricing{
			ModelID:        item.ID,
			InputPriceRMB:  item.InputCostRMB,
			OutputPriceRMB: item.OutputCostRMB,
			Currency:       priceAccuracyFirstNonEmpty(item.Currency, "CREDIT"),
		}
	}
	pricing.PriceTiers = priceTierInheritanceSingleTierJSON(item, pricing)
	now := time.Now()
	pricing.EffectiveFrom = &now
	if pricing.ID == 0 {
		if err := db.Create(pricing).Error; err != nil {
			log := logger.L
			if log == nil {
				log = zap.NewNop()
			}
			log.Warn("price tier inheritance: create flattened pricing failed", zap.String("model", item.ModelName), zap.Error(err))
			return false
		}
		return true
	}
	if err := db.Save(pricing).Error; err != nil {
		log := logger.L
		if log == nil {
			log = zap.NewNop()
		}
		log.Warn("price tier inheritance: update flattened pricing failed", zap.String("model", item.ModelName), zap.Error(err))
		return false
	}
	return true
}

func priceTierInheritanceSingleTierJSON(item model.AIModel, pricing *model.ModelPricing) model.JSON {
	input := item.InputCostRMB
	output := item.OutputCostRMB
	thinking := item.OutputCostThinkingRMB
	cacheInput := item.CacheInputPriceRMB
	cacheWrite := item.CacheWritePriceRMB
	if pricing != nil {
		input = pricing.InputPriceRMB
		output = pricing.OutputPriceRMB
		thinking = pricing.OutputPriceThinkingRMB
		if item.InputCostRMB > 0 && pricing.InputPriceRMB > 0 {
			ratio := pricing.InputPriceRMB / item.InputCostRMB
			cacheInput = priceAccuracyRound6(item.CacheInputPriceRMB * ratio)
			cacheWrite = priceAccuracyRound6(item.CacheWritePriceRMB * ratio)
		}
	}
	raw, err := priceAccuracyMarshalTiers(model.PriceTiersData{
		Tiers: []model.PriceTier{{
			Name:                "Default",
			InputMin:            0,
			OutputMin:           0,
			OutputMinExclusive:  true,
			InputPrice:          priceAccuracyRound6(input),
			OutputPrice:         priceAccuracyRound6(output),
			OutputPriceThinking: priceAccuracyRound6(thinking),
			CacheInputPrice:     priceAccuracyRound6(cacheInput),
			CacheWritePrice:     priceAccuracyRound6(cacheWrite),
		}},
		Currency:  priceAccuracyFirstNonEmpty(item.PriceSourceCurrency, item.Currency, "CNY"),
		UpdatedAt: time.Now(),
		SourceURL: "",
	})
	if err != nil {
		return nil
	}
	return raw
}

func priceTierInheritanceFloatEqual(a, b float64) bool {
	return priceAccuracyRound6(a) == priceAccuracyRound6(b)
}

func priceTierInheritanceKey(supplierID uint, modelName string) string {
	return fmt.Sprintf("%d:%s", supplierID, strings.ToLower(strings.TrimSpace(modelName)))
}
