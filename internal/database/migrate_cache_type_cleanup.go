package database

import (
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/logger"
)

// RunCacheTypeCleanup 修正历史 seed 数据中非 LLM/VLM 模型错误启用缓存定价的问题
//
// 背景：seed_cache_prices.go 早期版本对所有有价格的模型一律启用 supports_cache=true，
// 导致 ImageGeneration / VideoGeneration / TTS / ASR / Rerank / Embedding 等类型
// 错误地携带缓存定价字段。
//
// 规则：仅 ModelType ∈ {LLM, VLM, Vision} 且 pricing_unit = per_million_tokens 的模型
// 才允许启用缓存；其他类型强制清零所有缓存字段。
//
// 幂等性：可重复执行，仅影响"类型不符"的行。
func RunCacheTypeCleanup(db *gorm.DB) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	start := time.Now()

	// 统计待修复的行数
	var beforeCount int64
	db.Raw(`
		SELECT COUNT(*)
		FROM ai_models
		WHERE supports_cache = 1
		  AND (
			  model_type NOT IN ('LLM', 'VLM', 'Vision')
			  OR pricing_unit <> 'per_million_tokens'
		  )
	`).Scan(&beforeCount)

	if beforeCount == 0 {
		log.Info("cache type cleanup: no dirty data found, skipped",
			zap.Duration("duration", time.Since(start)))
		return
	}

	log.Info("cache type cleanup: starting",
		zap.Int64("rows_to_fix", beforeCount))

	// 批量清理
	result := db.Exec(`
		UPDATE ai_models
		SET supports_cache = 0,
			cache_mechanism = 'none',
			cache_min_tokens = 0,
			cache_input_price_rmb = 0,
			cache_explicit_input_price_rmb = 0,
			cache_write_price_rmb = 0,
			cache_storage_price_rmb = 0
		WHERE supports_cache = 1
		  AND (
			  model_type NOT IN ('LLM', 'VLM', 'Vision')
			  OR pricing_unit <> 'per_million_tokens'
		  )
	`)
	if result.Error != nil {
		log.Error("cache type cleanup: failed",
			zap.Error(result.Error))
		return
	}

	log.Info("cache type cleanup: complete",
		zap.Int64("rows_affected", result.RowsAffected),
		zap.Duration("duration", time.Since(start)))
}
