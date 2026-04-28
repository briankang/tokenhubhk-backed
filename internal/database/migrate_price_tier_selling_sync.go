package database

import (
	"encoding/json"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunPriceTierSellingSyncMigration mirrors supplier cost tiers into platform
// selling tiers and fills missing per-tier cache prices for existing models.
func RunPriceTierSellingSyncMigration(db *gorm.DB) {
	if db == nil {
		return
	}
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	var items []model.AIModel
	if err := db.Preload("Pricing").Where("deleted_at IS NULL").Find(&items).Error; err != nil {
		log.Warn("price tier selling sync: list models failed", zap.Error(err))
		return
	}

	updatedModels := int64(0)
	updatedPricings := int64(0)
	createdPricings := int64(0)
	skipped := int64(0)

	for _, item := range items {
		costData, ok := decodeSyncPriceTiers(item.PriceTiers)
		if !ok || len(costData.Tiers) == 0 {
			skipped++
			continue
		}

		costTiers := clonePriceTiers(costData.Tiers)
		normalizeAndFillCostTiers(&item, costTiers)
		inputRatio, outputRatio, thinkingRatio := platformTierRatios(&item, costTiers)
		attachSellingOverrides(costTiers, inputRatio, outputRatio, thinkingRatio)

		costData.Tiers = costTiers
		if costData.Currency == "" {
			costData.Currency = "CNY"
		}
		if costData.UpdatedAt.IsZero() {
			costData.UpdatedAt = time.Now()
		}
		costJSON, err := marshalPriceTiersData(costData)
		if err != nil {
			log.Warn("price tier selling sync: marshal cost tiers failed",
				zap.Uint("model_id", item.ID), zap.String("model", item.ModelName), zap.Error(err))
			continue
		}
		if err := db.Model(&model.AIModel{}).Where("id = ?", item.ID).Update("price_tiers", costJSON).Error; err != nil {
			log.Warn("price tier selling sync: update model tiers failed",
				zap.Uint("model_id", item.ID), zap.String("model", item.ModelName), zap.Error(err))
			continue
		}
		updatedModels++

		sellData := model.PriceTiersData{
			Tiers:     buildPlatformSellingTiers(costTiers, inputRatio, outputRatio, thinkingRatio),
			Currency:  firstNonEmpty(costData.Currency, item.Currency, "CREDIT"),
			UnitLabel: costData.UnitLabel,
			UpdatedAt: time.Now(),
			SourceURL: costData.SourceURL,
		}
		sellJSON, err := marshalPriceTiersData(sellData)
		if err != nil {
			log.Warn("price tier selling sync: marshal selling tiers failed",
				zap.Uint("model_id", item.ID), zap.String("model", item.ModelName), zap.Error(err))
			continue
		}

		pricing := item.Pricing
		if pricing == nil {
			pricing = &model.ModelPricing{ModelID: item.ID}
		}
		if pricing.InputPriceRMB <= 0 && len(sellData.Tiers) > 0 {
			pricing.InputPriceRMB = sellData.Tiers[0].InputPrice
			pricing.InputPricePerToken = int64(pricing.InputPriceRMB * 10000)
		}
		if pricing.OutputPriceRMB <= 0 && len(sellData.Tiers) > 0 {
			pricing.OutputPriceRMB = sellData.Tiers[0].OutputPrice
			pricing.OutputPricePerToken = int64(pricing.OutputPriceRMB * 10000)
		}
		if pricing.OutputPriceThinkingRMB <= 0 && len(sellData.Tiers) > 0 && sellData.Tiers[0].OutputPriceThinking > 0 {
			pricing.OutputPriceThinkingRMB = sellData.Tiers[0].OutputPriceThinking
			pricing.OutputPriceThinkingPerToken = int64(pricing.OutputPriceThinkingRMB * 10000)
		}
		pricing.Currency = firstNonEmpty(pricing.Currency, item.Currency, "CREDIT")
		pricing.PriceTiers = sellJSON

		if pricing.ID == 0 {
			if err := db.Create(pricing).Error; err != nil {
				log.Warn("price tier selling sync: create pricing failed",
					zap.Uint("model_id", item.ID), zap.String("model", item.ModelName), zap.Error(err))
				continue
			}
			createdPricings++
		} else if err := db.Save(pricing).Error; err != nil {
			log.Warn("price tier selling sync: update pricing failed",
				zap.Uint("model_id", item.ID), zap.String("model", item.ModelName), zap.Error(err))
			continue
		} else {
			updatedPricings++
		}
	}

	log.Info("price tier selling sync migration complete",
		zap.Int64("models_updated", updatedModels),
		zap.Int64("pricings_updated", updatedPricings),
		zap.Int64("pricings_created", createdPricings),
		zap.Int64("models_skipped", skipped))
}

func decodeSyncPriceTiers(raw model.JSON) (model.PriceTiersData, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return model.PriceTiersData{}, false
	}
	if data, ok := decodeLegacySyncPriceTiers(raw); ok {
		return data, true
	}
	var data model.PriceTiersData
	if err := json.Unmarshal(raw, &data); err == nil && len(data.Tiers) > 0 {
		return data, true
	}
	var tiers []model.PriceTier
	if err := json.Unmarshal(raw, &tiers); err == nil && len(tiers) > 0 {
		return model.PriceTiersData{Tiers: tiers, Currency: "CNY", UpdatedAt: time.Now()}, true
	}
	return model.PriceTiersData{}, false
}

type legacySyncPriceTier struct {
	Name           string  `json:"name"`
	Label          string  `json:"label"`
	InputMin       int64   `json:"input_min"`
	InputMax       int64   `json:"input_max"`
	MinInputTokens int64   `json:"min_input_tokens"`
	MaxInputTokens int64   `json:"max_input_tokens"`
	InputPriceRMB  float64 `json:"input_price_rmb"`
	OutputPriceRMB float64 `json:"output_price_rmb"`
	CacheReadRMB   float64 `json:"cache_read_rmb"`
	CacheWriteRMB  float64 `json:"cache_write_rmb"`
}

func decodeLegacySyncPriceTiers(raw model.JSON) (model.PriceTiersData, bool) {
	var wrapped struct {
		Tiers     []legacySyncPriceTier `json:"tiers"`
		Currency  string                `json:"currency"`
		UnitLabel string                `json:"unit_label"`
		SourceURL string                `json:"source_url"`
		UpdatedAt time.Time             `json:"updated_at"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && hasLegacySyncTierFields(wrapped.Tiers) {
		return legacySyncTiersToData(wrapped.Tiers, wrapped.Currency, wrapped.UnitLabel, wrapped.SourceURL, wrapped.UpdatedAt), true
	}

	var tiers []legacySyncPriceTier
	if err := json.Unmarshal(raw, &tiers); err == nil && hasLegacySyncTierFields(tiers) {
		return legacySyncTiersToData(tiers, "CNY", "", "", time.Now()), true
	}
	return model.PriceTiersData{}, false
}

func hasLegacySyncTierFields(tiers []legacySyncPriceTier) bool {
	for _, tier := range tiers {
		if tier.InputPriceRMB > 0 || tier.OutputPriceRMB > 0 || tier.CacheReadRMB > 0 || tier.CacheWriteRMB > 0 || tier.MaxInputTokens > 0 {
			return true
		}
	}
	return false
}

func legacySyncTiersToData(in []legacySyncPriceTier, currency, unitLabel, sourceURL string, updatedAt time.Time) model.PriceTiersData {
	out := make([]model.PriceTier, 0, len(in))
	var previousMax int64
	for i, old := range in {
		name := firstNonEmpty(old.Name, old.Label)
		min := old.InputMin
		if old.MinInputTokens > 0 {
			min = old.MinInputTokens
		}
		if min == 0 && i > 0 && previousMax > 0 {
			min = previousMax
		}
		max := old.InputMax
		if old.MaxInputTokens > 0 {
			max = old.MaxInputTokens
		}
		tier := model.PriceTier{
			Name:               name,
			InputMin:           min,
			InputMinExclusive:  i > 0 && min > 0,
			OutputMin:          0,
			OutputMinExclusive: true,
			InputPrice:         old.InputPriceRMB,
			OutputPrice:        old.OutputPriceRMB,
			CacheInputPrice:    old.CacheReadRMB,
			CacheWritePrice:    old.CacheWriteRMB,
		}
		if max > 0 {
			maxCopy := max
			tier.InputMax = &maxCopy
			previousMax = max
		}
		tier.Normalize()
		out = append(out, tier)
	}
	model.SortTiers(out)
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}
	return model.PriceTiersData{
		Tiers:     out,
		Currency:  firstNonEmpty(currency, "CNY"),
		UnitLabel: unitLabel,
		UpdatedAt: updatedAt,
		SourceURL: sourceURL,
	}
}

func clonePriceTiers(in []model.PriceTier) []model.PriceTier {
	out := make([]model.PriceTier, len(in))
	copy(out, in)
	return out
}

func normalizeAndFillCostTiers(item *model.AIModel, tiers []model.PriceTier) {
	cacheInputRatio := ratioOrZero(item.CacheInputPriceRMB, item.InputCostRMB)
	cacheWriteRatio := ratioOrZero(item.CacheWritePriceRMB, item.InputCostRMB)
	for i := range tiers {
		tiers[i].Normalize()
		if item.SupportsCache && item.CacheInputPriceRMB > 0 && tiers[i].CacheInputPrice <= 0 {
			if cacheInputRatio > 0 && tiers[i].InputPrice > 0 {
				tiers[i].CacheInputPrice = round6(tiers[i].InputPrice * cacheInputRatio)
			} else {
				tiers[i].CacheInputPrice = round6(item.CacheInputPriceRMB)
			}
		}
		if item.SupportsCache && item.CacheWritePriceRMB > 0 && tiers[i].CacheWritePrice <= 0 {
			if cacheWriteRatio > 0 && tiers[i].InputPrice > 0 {
				tiers[i].CacheWritePrice = round6(tiers[i].InputPrice * cacheWriteRatio)
			} else {
				tiers[i].CacheWritePrice = round6(item.CacheWritePriceRMB)
			}
		}
	}
	model.SortTiers(tiers)
}

func platformTierRatios(item *model.AIModel, tiers []model.PriceTier) (float64, float64, float64) {
	inputRatio := 1.0
	outputRatio := 1.0
	thinkingRatio := 1.0
	if item.Pricing != nil {
		inputRatio = firstPositiveRatio(item.Pricing.InputPriceRMB, item.InputCostRMB, inputRatioFromTiers(tiers, "input"), inputRatio)
		outputRatio = firstPositiveRatio(item.Pricing.OutputPriceRMB, item.OutputCostRMB, inputRatioFromTiers(tiers, "output"), outputRatio)
		thinkingRatio = firstPositiveRatio(item.Pricing.OutputPriceThinkingRMB, item.OutputCostThinkingRMB, inputRatioFromTiers(tiers, "thinking"), thinkingRatio)
		return inputRatio, outputRatio, thinkingRatio
	}
	inputRatio = firstPositiveRatio(0, 0, inputRatioFromTiers(tiers, "input"), inputRatio)
	outputRatio = firstPositiveRatio(0, 0, inputRatioFromTiers(tiers, "output"), outputRatio)
	thinkingRatio = firstPositiveRatio(0, 0, inputRatioFromTiers(tiers, "thinking"), thinkingRatio)
	return inputRatio, outputRatio, thinkingRatio
}

func inputRatioFromTiers(tiers []model.PriceTier, kind string) float64 {
	for _, tier := range tiers {
		switch kind {
		case "input":
			if tier.SellingInputPrice != nil && tier.InputPrice > 0 && *tier.SellingInputPrice > 0 {
				return *tier.SellingInputPrice / tier.InputPrice
			}
		case "output":
			if tier.SellingOutputPrice != nil && tier.OutputPrice > 0 && *tier.SellingOutputPrice > 0 {
				return *tier.SellingOutputPrice / tier.OutputPrice
			}
		case "thinking":
			if tier.SellingOutputThinkingPrice != nil && tier.OutputPriceThinking > 0 && *tier.SellingOutputThinkingPrice > 0 {
				return *tier.SellingOutputThinkingPrice / tier.OutputPriceThinking
			}
		}
	}
	return 0
}

func firstPositiveRatio(numerator, denominator, fallback, defaultValue float64) float64 {
	if numerator > 0 && denominator > 0 {
		return numerator / denominator
	}
	if fallback > 0 {
		return fallback
	}
	return defaultValue
}

func ratioOrZero(numerator, denominator float64) float64 {
	if numerator > 0 && denominator > 0 {
		return numerator / denominator
	}
	return 0
}

func attachSellingOverrides(tiers []model.PriceTier, inputRatio, outputRatio, thinkingRatio float64) {
	for i := range tiers {
		if tiers[i].InputPrice > 0 && (tiers[i].SellingInputPrice == nil || *tiers[i].SellingInputPrice <= 0) {
			v := round6(tiers[i].InputPrice * inputRatio)
			tiers[i].SellingInputPrice = &v
		}
		if tiers[i].OutputPrice > 0 && (tiers[i].SellingOutputPrice == nil || *tiers[i].SellingOutputPrice <= 0) {
			v := round6(tiers[i].OutputPrice * outputRatio)
			tiers[i].SellingOutputPrice = &v
		}
		if tiers[i].OutputPriceThinking > 0 && (tiers[i].SellingOutputThinkingPrice == nil || *tiers[i].SellingOutputThinkingPrice <= 0) {
			v := round6(tiers[i].OutputPriceThinking * thinkingRatio)
			tiers[i].SellingOutputThinkingPrice = &v
		}
	}
}

func buildPlatformSellingTiers(costTiers []model.PriceTier, inputRatio, outputRatio, thinkingRatio float64) []model.PriceTier {
	tiers := make([]model.PriceTier, len(costTiers))
	for i, tier := range costTiers {
		sellInput := tierValueOrRatio(tier.SellingInputPrice, tier.InputPrice, inputRatio)
		sellOutput := tierValueOrRatio(tier.SellingOutputPrice, tier.OutputPrice, outputRatio)
		sellThinking := tierValueOrRatio(tier.SellingOutputThinkingPrice, tier.OutputPriceThinking, thinkingRatio)

		tiers[i] = tier
		tiers[i].InputPrice = sellInput
		tiers[i].OutputPrice = sellOutput
		tiers[i].OutputPriceThinking = sellThinking
		tiers[i].CacheInputPrice = sellingCacheTierPrice(tier.CacheInputPrice, tier.InputPrice, sellInput, inputRatio)
		tiers[i].CacheWritePrice = sellingCacheTierPrice(tier.CacheWritePrice, tier.InputPrice, sellInput, inputRatio)
		tiers[i].SellingInputPrice = &sellInput
		tiers[i].SellingOutputPrice = &sellOutput
		if sellThinking > 0 {
			tiers[i].SellingOutputThinkingPrice = &sellThinking
		} else {
			tiers[i].SellingOutputThinkingPrice = nil
		}
	}
	model.SortTiers(tiers)
	return tiers
}

func tierValueOrRatio(override *float64, base, ratio float64) float64 {
	if override != nil && *override > 0 {
		return round6(*override)
	}
	if base > 0 {
		return round6(base * ratio)
	}
	return 0
}

func sellingCacheTierPrice(costCache, costInput, sellInput, fallbackRatio float64) float64 {
	if costCache <= 0 {
		return 0
	}
	if costInput > 0 && sellInput > 0 {
		return round6(costCache * sellInput / costInput)
	}
	return round6(costCache * fallbackRatio)
}

func marshalPriceTiersData(data model.PriceTiersData) (model.JSON, error) {
	for i := range data.Tiers {
		data.Tiers[i].Normalize()
	}
	model.SortTiers(data.Tiers)
	if data.UpdatedAt.IsZero() {
		data.UpdatedAt = time.Now()
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return model.JSON(b), nil
}
