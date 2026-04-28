package database

import (
	"encoding/json"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// legacyPriceTier is used only by this one-time data migration. Runtime code uses model.PriceTier.
type legacyPriceTier struct {
	Name                       string   `json:"name"`
	Variant                    string   `json:"variant,omitempty"`
	InputMin                   int64    `json:"input_min"`
	InputMinExclusive          bool     `json:"input_min_exclusive,omitempty"`
	InputMax                   *int64   `json:"input_max,omitempty"`
	InputMaxExclusive          bool     `json:"input_max_exclusive,omitempty"`
	OutputMin                  int64    `json:"output_min"`
	OutputMinExclusive         bool     `json:"output_min_exclusive,omitempty"`
	OutputMax                  *int64   `json:"output_max,omitempty"`
	OutputMaxExclusive         bool     `json:"output_max_exclusive,omitempty"`
	MinTokens                  int64    `json:"min_tokens,omitempty"`
	MaxTokens                  *int64   `json:"max_tokens,omitempty"`
	InputPrice                 float64  `json:"input_price"`
	OutputPrice                float64  `json:"output_price"`
	CacheInputPrice            float64  `json:"cache_input_price,omitempty"`
	CacheWritePrice            float64  `json:"cache_write_price,omitempty"`
	OutputPriceThinking        float64  `json:"output_price_thinking,omitempty"`
	SellingInputPrice          *float64 `json:"selling_input_price,omitempty"`
	SellingOutputPrice         *float64 `json:"selling_output_price,omitempty"`
	SellingOutputThinkingPrice *float64 `json:"selling_output_thinking_price,omitempty"`
}

type legacyPriceTiersData struct {
	Tiers     []legacyPriceTier `json:"tiers"`
	Currency  string            `json:"currency"`
	UnitLabel string            `json:"unit_label,omitempty"`
	UpdatedAt time.Time         `json:"updated_at"`
	SourceURL string            `json:"source_url"`
}

// RunPriceTiers2DMigration migrates old tier JSON into the current input/output range schema.
// It also rewrites rows so min_tokens/max_tokens are physically removed from stored JSON.
func RunPriceTiers2DMigration(db *gorm.DB) {
	start := time.Now()
	modelsUpdated := migrateAIModelsPriceTiers(db)
	pricingsUpdated := migrateModelPricingsPriceTiers(db)
	logger.L.Info("price tiers current-schema migration complete",
		zap.Int("ai_models_updated", modelsUpdated),
		zap.Int("model_pricings_updated", pricingsUpdated),
		zap.Duration("duration", time.Since(start)),
	)
}

func migrateAIModelsPriceTiers(db *gorm.DB) int {
	type row struct {
		ID            uint
		ModelName     string
		InputCostRMB  float64
		OutputCostRMB float64
		PriceTiers    []byte
	}

	var rows []row
	if err := db.Table("ai_models").Select("id, model_name, input_cost_rmb, output_cost_rmb, price_tiers").Find(&rows).Error; err != nil {
		logger.L.Warn("migrate price_tiers: list ai_models failed", zap.Error(err))
		return 0
	}

	updated := 0
	for _, r := range rows {
		data, changed := decodeAndUpgradeTiersData(r.PriceTiers)
		if len(data.Tiers) == 0 && (r.InputCostRMB > 0 || r.OutputCostRMB > 0) {
			data.Tiers = []model.PriceTier{model.DefaultTier(r.InputCostRMB, r.OutputCostRMB)}
			changed = true
		}
		if !changed {
			continue
		}
		if data.Currency == "" {
			data.Currency = "CNY"
		}
		data.UpdatedAt = time.Now()
		if writePriceTiers(db, "ai_models", r.ID, data) {
			updated++
		}
	}
	return updated
}

func migrateModelPricingsPriceTiers(db *gorm.DB) int {
	type row struct {
		ID         uint
		ModelID    uint
		PriceTiers []byte
	}

	var rows []row
	if err := db.Table("model_pricings").Select("id, model_id, price_tiers").Find(&rows).Error; err != nil {
		logger.L.Warn("migrate price_tiers: list model_pricings failed", zap.Error(err))
		return 0
	}

	updated := 0
	for _, r := range rows {
		data, changed := decodeAndUpgradeTiersData(r.PriceTiers)
		if !changed || len(data.Tiers) == 0 {
			continue
		}
		if writePriceTiers(db, "model_pricings", r.ID, data) {
			updated++
		}
	}
	return updated
}

func decodeAndUpgradeTiersData(raw []byte) (model.PriceTiersData, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return model.PriceTiersData{}, false
	}

	var legacy legacyPriceTiersData
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return model.PriceTiersData{}, false
	}

	out := model.PriceTiersData{
		Tiers:     make([]model.PriceTier, 0, len(legacy.Tiers)),
		Currency:  legacy.Currency,
		UnitLabel: legacy.UnitLabel,
		UpdatedAt: legacy.UpdatedAt,
		SourceURL: legacy.SourceURL,
	}
	changed := false
	for _, old := range legacy.Tiers {
		tier := model.PriceTier{
			Name:                       old.Name,
			Variant:                    old.Variant,
			InputMin:                   old.InputMin,
			InputMinExclusive:          old.InputMinExclusive,
			InputMax:                   old.InputMax,
			InputMaxExclusive:          old.InputMaxExclusive,
			OutputMin:                  old.OutputMin,
			OutputMinExclusive:         old.OutputMinExclusive,
			OutputMax:                  old.OutputMax,
			OutputMaxExclusive:         old.OutputMaxExclusive,
			InputPrice:                 old.InputPrice,
			OutputPrice:                old.OutputPrice,
			CacheInputPrice:            old.CacheInputPrice,
			CacheWritePrice:            old.CacheWritePrice,
			OutputPriceThinking:        old.OutputPriceThinking,
			SellingInputPrice:          old.SellingInputPrice,
			SellingOutputPrice:         old.SellingOutputPrice,
			SellingOutputThinkingPrice: old.SellingOutputThinkingPrice,
		}

		if (old.MinTokens > 0 || old.MaxTokens != nil) && tier.InputMin == 0 && !tier.InputMinExclusive && tier.InputMax == nil {
			tier.InputMin = old.MinTokens
			tier.InputMax = old.MaxTokens
			changed = true
		}
		if old.MinTokens > 0 || old.MaxTokens != nil {
			changed = true
		}
		before := tier
		tier.Normalize()
		if normalizeChanged(before, tier) {
			changed = true
		}
		out.Tiers = append(out.Tiers, tier)
	}
	model.SortTiers(out.Tiers)
	return out, changed
}

func writePriceTiers(db *gorm.DB, table string, id uint, data model.PriceTiersData) bool {
	raw, err := json.Marshal(data)
	if err != nil {
		logger.L.Warn("migrate price_tiers: marshal failed", zap.String("table", table), zap.Uint("id", id), zap.Error(err))
		return false
	}
	if err := db.Table(table).Where("id = ?", id).Update("price_tiers", raw).Error; err != nil {
		logger.L.Warn("migrate price_tiers: update failed", zap.String("table", table), zap.Uint("id", id), zap.Error(err))
		return false
	}
	return true
}

func normalizeChanged(before, after model.PriceTier) bool {
	return before.InputMin != after.InputMin ||
		!ptrInt64Equal(before.InputMax, after.InputMax) ||
		before.InputMinExclusive != after.InputMinExclusive ||
		before.OutputMin != after.OutputMin ||
		!ptrInt64Equal(before.OutputMax, after.OutputMax) ||
		before.OutputMinExclusive != after.OutputMinExclusive ||
		before.Name != after.Name
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
