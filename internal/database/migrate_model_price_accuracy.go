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

const priceAccuracyMinMargin = 0.15

// RunModelPriceAccuracyMigration raises missing/low-margin platform selling prices
// to the official price layer, preserving supplier/model discounts only as cost.
func RunModelPriceAccuracyMigration(db *gorm.DB) {
	if db == nil {
		return
	}
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	var items []model.AIModel
	if err := db.Preload("Supplier").Preload("Pricing").Where("deleted_at IS NULL").Find(&items).Error; err != nil {
		log.Warn("model price accuracy: list models failed", zap.Error(err))
		return
	}

	created := int64(0)
	updated := int64(0)
	tiersUpdated := int64(0)

	for _, item := range items {
		if item.InputCostRMB <= 0 && item.OutputCostRMB <= 0 && item.OutputCostThinkingRMB <= 0 {
			continue
		}

		pricing := item.Pricing
		if pricing == nil {
			pricing = &model.ModelPricing{ModelID: item.ID, Currency: priceAccuracyFirstNonEmpty(item.Currency, "CREDIT")}
		}

		changed := false
		if shouldRaiseSellingToOfficial(pricing.InputPriceRMB, item.InputCostRMB, effectiveDiscountForPriceAccuracy(item)) {
			pricing.InputPriceRMB = priceAccuracyRound6(item.InputCostRMB)
			pricing.InputPricePerToken = int64(math.Round(pricing.InputPriceRMB * 10000))
			changed = true
		}
		if shouldRaiseSellingToOfficial(pricing.OutputPriceRMB, item.OutputCostRMB, effectiveDiscountForPriceAccuracy(item)) {
			pricing.OutputPriceRMB = priceAccuracyRound6(item.OutputCostRMB)
			pricing.OutputPricePerToken = int64(math.Round(pricing.OutputPriceRMB * 10000))
			changed = true
		}
		if shouldRaiseSellingToOfficial(pricing.OutputPriceThinkingRMB, item.OutputCostThinkingRMB, effectiveDiscountForPriceAccuracy(item)) {
			pricing.OutputPriceThinkingRMB = priceAccuracyRound6(item.OutputCostThinkingRMB)
			pricing.OutputPriceThinkingPerToken = int64(math.Round(pricing.OutputPriceThinkingRMB * 10000))
			changed = true
		}
		if pricing.Currency == "" {
			pricing.Currency = priceAccuracyFirstNonEmpty(item.Currency, "CREDIT")
			changed = true
		}

		if tierJSON, ok := officialSellingTiersJSON(item); ok {
			pricing.PriceTiers = tierJSON
			tiersUpdated++
			changed = true
		}

		if !changed {
			continue
		}
		now := time.Now()
		pricing.EffectiveFrom = &now
		if pricing.ID == 0 {
			if err := db.Create(pricing).Error; err != nil {
				log.Warn("model price accuracy: create pricing failed", zap.String("model", item.ModelName), zap.Error(err))
				continue
			}
			created++
		} else if err := db.Save(pricing).Error; err != nil {
			log.Warn("model price accuracy: update pricing failed", zap.String("model", item.ModelName), zap.Error(err))
			continue
		} else {
			updated++
		}
	}

	log.Info("model price accuracy migration complete",
		zap.Int64("created", created),
		zap.Int64("updated", updated),
		zap.Int64("tiers_updated", tiersUpdated))
}

func shouldRaiseSellingToOfficial(selling, official, discount float64) bool {
	if official <= 0 {
		return false
	}
	if selling <= 0 {
		return true
	}
	effective := official * discount
	if selling < effective {
		return true
	}
	margin := (selling - effective) / selling
	return margin < priceAccuracyMinMargin && selling < official
}

func effectiveDiscountForPriceAccuracy(item model.AIModel) float64 {
	if item.Discount > 0 && item.Discount <= 1 {
		return item.Discount
	}
	if item.Supplier.Discount > 0 && item.Supplier.Discount <= 1 {
		return item.Supplier.Discount
	}
	return 1
}

func officialSellingTiersJSON(item model.AIModel) (model.JSON, bool) {
	data, ok := priceAccuracyDecodeTiers(item.PriceTiers)
	if !ok || len(data.Tiers) == 0 {
		return nil, false
	}
	changed := false
	tiers := priceAccuracyCloneTiers(data.Tiers)
	for i := range tiers {
		tiers[i].Normalize()
		if shouldRaiseTierSellingToOfficial(tiers[i].SellingInputPrice, tiers[i].InputPrice, item) {
			v := priceAccuracyRound6(tiers[i].InputPrice)
			tiers[i].SellingInputPrice = &v
			changed = true
		}
		if shouldRaiseTierSellingToOfficial(tiers[i].SellingOutputPrice, tiers[i].OutputPrice, item) {
			v := priceAccuracyRound6(tiers[i].OutputPrice)
			tiers[i].SellingOutputPrice = &v
			changed = true
		}
		if shouldRaiseTierSellingToOfficial(tiers[i].SellingOutputThinkingPrice, tiers[i].OutputPriceThinking, item) {
			v := priceAccuracyRound6(tiers[i].OutputPriceThinking)
			tiers[i].SellingOutputThinkingPrice = &v
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	raw, err := priceAccuracyMarshalTiers(model.PriceTiersData{
		Tiers:     buildOfficialSellingTiers(tiers),
		Currency:  priceAccuracyFirstNonEmpty(data.Currency, item.Currency, "CREDIT"),
		UnitLabel: data.UnitLabel,
		UpdatedAt: time.Now(),
		SourceURL: data.SourceURL,
	})
	if err != nil {
		return nil, false
	}
	return raw, true
}

func shouldRaiseTierSellingToOfficial(current *float64, official float64, item model.AIModel) bool {
	if official <= 0 {
		return false
	}
	selling := 0.0
	if current != nil {
		selling = *current
	}
	return shouldRaiseSellingToOfficial(selling, official, effectiveDiscountForPriceAccuracy(item))
}

func buildOfficialSellingTiers(costTiers []model.PriceTier) []model.PriceTier {
	tiers := make([]model.PriceTier, len(costTiers))
	for i, tier := range costTiers {
		tiers[i] = tier
		if tier.SellingInputPrice != nil && *tier.SellingInputPrice > 0 {
			tiers[i].InputPrice = priceAccuracyRound6(*tier.SellingInputPrice)
		}
		if tier.SellingOutputPrice != nil && *tier.SellingOutputPrice > 0 {
			tiers[i].OutputPrice = priceAccuracyRound6(*tier.SellingOutputPrice)
		}
		if tier.SellingOutputThinkingPrice != nil && *tier.SellingOutputThinkingPrice > 0 {
			tiers[i].OutputPriceThinking = priceAccuracyRound6(*tier.SellingOutputThinkingPrice)
		}
		if tier.InputPrice > 0 && tiers[i].InputPrice > 0 {
			ratio := tiers[i].InputPrice / tier.InputPrice
			tiers[i].CacheInputPrice = priceAccuracyRound6(tier.CacheInputPrice * ratio)
			tiers[i].CacheWritePrice = priceAccuracyRound6(tier.CacheWritePrice * ratio)
		}
	}
	model.SortTiers(tiers)
	return tiers
}

func priceAccuracyDecodeTiers(raw model.JSON) (model.PriceTiersData, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return model.PriceTiersData{}, false
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

func priceAccuracyCloneTiers(in []model.PriceTier) []model.PriceTier {
	out := make([]model.PriceTier, len(in))
	copy(out, in)
	return out
}

func priceAccuracyMarshalTiers(data model.PriceTiersData) (model.JSON, error) {
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

func priceAccuracyRound6(value float64) float64 {
	return math.Round(value*1e6) / 1e6
}

func priceAccuracyFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
