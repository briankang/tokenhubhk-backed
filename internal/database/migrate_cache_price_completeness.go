package database

import (
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunPriceSourceColumnsMigration ensures ai_models can keep original USD prices.
func RunPriceSourceColumnsMigration(db *gorm.DB) {
	if db == nil || !db.Migrator().HasTable(&model.AIModel{}) {
		return
	}
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	fields := []string{
		"PriceSourceCurrency",
		"PriceSourceExchangeRate",
		"InputCostUSD",
		"OutputCostUSD",
		"OutputCostThinkingUSD",
		"CacheInputPriceUSD",
		"CacheExplicitInputPriceUSD",
		"CacheWritePriceUSD",
		"CacheStoragePriceUSD",
	}
	for _, field := range fields {
		if db.Migrator().HasColumn(&model.AIModel{}, field) {
			continue
		}
		if err := db.Migrator().AddColumn(&model.AIModel{}, field); err != nil {
			log.Warn("price source column migration failed",
				zap.String("field", field),
				zap.Error(err),
			)
			return
		}
	}
}

// RunCachePriceCompletenessMigration backfills missing cache prices and clears false cache flags.
//
// Rules:
//   - both: implicit hit = 20%, explicit hit = 10%, write = 125% of input.
//   - explicit: hit = 10%, write = 125% of input.
//   - auto: provider-specific default where known, otherwise conservative 50%.
//   - rows with no base input price or cache_mechanism=none are not billable cache rows.
func RunCachePriceCompletenessMigration(db *gorm.DB) {
	if db == nil {
		return
	}
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	RunPriceSourceColumnsMigration(db)
	start := time.Now()

	fill := db.Exec(`
		UPDATE ai_models
		SET
			cache_input_price_rmb = CASE
				WHEN COALESCE(cache_input_price_rmb, 0) > 0 THEN cache_input_price_rmb
				WHEN cache_mechanism = 'both' THEN ROUND(input_cost_rmb * 0.20, 6)
				WHEN cache_mechanism = 'explicit' THEN ROUND(input_cost_rmb * 0.10, 6)
				WHEN LOWER(model_name) LIKE 'qwen%' THEN ROUND(input_cost_rmb * 0.20, 6)
				WHEN LOWER(model_name) LIKE 'glm%' THEN ROUND(input_cost_rmb * 0.20, 6)
				WHEN LOWER(model_name) LIKE 'deepseek%' THEN ROUND(input_cost_rmb * 0.10, 6)
				WHEN LOWER(model_name) LIKE 'doubao%' THEN ROUND(input_cost_rmb * 0.40, 6)
				WHEN LOWER(model_name) LIKE 'moonshot%' OR LOWER(model_name) LIKE 'kimi%' THEN ROUND(input_cost_rmb * 0.25, 6)
				WHEN LOWER(model_name) LIKE 'gemini%' THEN ROUND(input_cost_rmb * 0.75, 6)
				ELSE ROUND(input_cost_rmb * 0.50, 6)
			END,
			cache_explicit_input_price_rmb = CASE
				WHEN cache_mechanism <> 'both' THEN cache_explicit_input_price_rmb
				WHEN COALESCE(cache_explicit_input_price_rmb, 0) > 0 THEN cache_explicit_input_price_rmb
				ELSE ROUND(input_cost_rmb * 0.10, 6)
			END,
			cache_write_price_rmb = CASE
				WHEN cache_mechanism NOT IN ('both', 'explicit') THEN cache_write_price_rmb
				WHEN COALESCE(cache_write_price_rmb, 0) > 0 THEN cache_write_price_rmb
				ELSE ROUND(input_cost_rmb * 1.25, 6)
			END,
			cache_min_tokens = CASE
				WHEN COALESCE(cache_min_tokens, 0) > 0 THEN cache_min_tokens
				WHEN cache_mechanism IN ('both', 'explicit') THEN 1024
				ELSE cache_min_tokens
			END
		WHERE deleted_at IS NULL
		  AND supports_cache = 1
		  AND COALESCE(input_cost_rmb, 0) > 0
		  AND (
			COALESCE(cache_input_price_rmb, 0) = 0
			OR (cache_mechanism IN ('both', 'explicit') AND COALESCE(cache_write_price_rmb, 0) = 0)
			OR (cache_mechanism = 'both' AND COALESCE(cache_explicit_input_price_rmb, 0) = 0)
		  )
	`)
	if fill.Error != nil {
		log.Warn("cache price completeness fill failed", zap.Error(fill.Error))
		return
	}

	clear := db.Exec(`
		UPDATE ai_models
		SET
			supports_cache = 0,
			cache_mechanism = 'none',
			cache_min_tokens = 0,
			cache_input_price_rmb = 0,
			cache_explicit_input_price_rmb = 0,
			cache_write_price_rmb = 0,
			cache_storage_price_rmb = 0
		WHERE deleted_at IS NULL
		  AND supports_cache = 1
		  AND (
			cache_mechanism IS NULL
			OR cache_mechanism = ''
			OR cache_mechanism = 'none'
			OR (
				COALESCE(input_cost_rmb, 0) <= 0
				AND COALESCE(cache_input_price_rmb, 0) <= 0
				AND COALESCE(cache_explicit_input_price_rmb, 0) <= 0
				AND COALESCE(cache_write_price_rmb, 0) <= 0
				AND COALESCE(cache_storage_price_rmb, 0) <= 0
			)
		  )
	`)
	if clear.Error != nil {
		log.Warn("cache price completeness cleanup failed", zap.Error(clear.Error))
		return
	}

	log.Info("cache price completeness migration complete",
		zap.Int64("filled", fill.RowsAffected),
		zap.Int64("cleared_false_flags", clear.RowsAffected),
		zap.Duration("duration", time.Since(start)),
	)
}

// RunUSDPriceSourceBackfillMigration keeps exact USD source prices for Wangsu gateway models.
func RunUSDPriceSourceBackfillMigration(db *gorm.DB) {
	if db == nil {
		return
	}
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	RunPriceSourceColumnsMigration(db)

	var sup model.Supplier
	if err := db.Where("code = ? AND access_type = ?", "wangsu_aigw", "api").First(&sup).Error; err != nil {
		return
	}

	updated := int64(0)
	for _, m := range wangsuModels {
		updates := map[string]any{
			"price_source_currency":          "USD",
			"price_source_exchange_rate":     USDCNYSnapshot,
			"input_cost_usd":                 round6(m.InputUSDPerM),
			"output_cost_usd":                round6(m.OutputUSDPerM),
			"cache_input_price_usd":          round6(m.CacheReadUSDPerM),
			"cache_explicit_input_price_usd": round6(m.CacheExplicitReadUSDPerM),
			"cache_write_price_usd":          round6(m.CacheWriteUSDPerM),
			"cache_storage_price_usd":        round6(m.CacheStorageUSDPerMHour),
		}
		if m.OutputUSDPerM > 0 && m.SupportsThinking {
			updates["output_cost_thinking_usd"] = round6(m.OutputUSDPerM)
		}
		res := db.Model(&model.AIModel{}).
			Where("supplier_id = ? AND model_name = ?", sup.ID, m.ModelName).
			Updates(updates)
		if res.Error != nil {
			log.Warn("wangsu USD source backfill failed",
				zap.String("model", m.ModelName),
				zap.Error(res.Error),
			)
			continue
		}
		updated += res.RowsAffected
	}

	for _, m := range wangsuImageModels {
		res := db.Model(&model.AIModel{}).
			Where("supplier_id = ? AND model_name = ?", sup.ID, m.ModelName).
			Updates(map[string]any{
				"price_source_currency":          "USD",
				"price_source_exchange_rate":     USDCNYSnapshot,
				"input_cost_usd":                 round6(m.OfficialUSD),
				"output_cost_usd":                0,
				"cache_input_price_usd":          0,
				"cache_explicit_input_price_usd": 0,
				"cache_write_price_usd":          0,
				"cache_storage_price_usd":        0,
			})
		if res.Error != nil {
			log.Warn("wangsu image USD source backfill failed",
				zap.String("model", m.ModelName),
				zap.Error(res.Error),
			)
			continue
		}
		updated += res.RowsAffected
	}

	for _, m := range wangsuVideoModels {
		res := db.Model(&model.AIModel{}).
			Where("supplier_id = ? AND model_name = ?", sup.ID, m.ModelName).
			Updates(map[string]any{
				"price_source_currency":          "USD",
				"price_source_exchange_rate":     USDCNYSnapshot,
				"input_cost_usd":                 round6(m.OfficialUSD),
				"output_cost_usd":                0,
				"cache_input_price_usd":          0,
				"cache_explicit_input_price_usd": 0,
				"cache_write_price_usd":          0,
				"cache_storage_price_usd":        0,
			})
		if res.Error != nil {
			log.Warn("wangsu video USD source backfill failed",
				zap.String("model", m.ModelName),
				zap.Error(res.Error),
			)
			continue
		}
		updated += res.RowsAffected
	}

	log.Info("wangsu USD source backfill complete", zap.Int64("updated", updated))
}
