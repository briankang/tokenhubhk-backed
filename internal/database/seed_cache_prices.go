package database

import (
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/logger"
)

// RunCachePriceMigration 幂等地为已有模型回填缓存定价字段
//
// 策略：
//   - 按模型名前缀匹配，仅处理 supports_cache = 0 的行（已启用的跳过，不覆盖管理员配置）
//   - 价格基于 input_cost_rmb 按比例推算（input_cost_rmb = 0 时缓存价也置 0，管理员可事后手动填）
//   - 适用场景：生产数据库升级后首次启动，为历史模型补充缓存定价
//
// 各供应商缓存机制：
//
//	auto     — 全自动，无需额外参数（OpenAI/DeepSeek/Moonshot/智谱/火山引擎/Gemini隐式）
//	explicit — 需在请求中传 cache_control 参数（Anthropic）
//	both     — 隐式(auto)和显式(explicit)双模式（阿里云百炼）
func RunCachePriceMigration(db *gorm.DB) {
	type patchRule struct {
		// SQL LIKE 模式，匹配 ai_models.model_name
		Pattern string
		// 缓存机制类型: auto / explicit / both
		CacheMechanism string
		// 触发缓存的最小Token门槛（仅 explicit/both 有意义）
		CacheMinTokens int
		// 相对于 input_cost_rmb 的比例
		CacheInputRatio         float64 // auto/explicit 缓存命中价系数
		CacheExplicitInputRatio float64 // both 模式的显式命中价系数
		CacheWriteRatio         float64 // 写入溢价系数（Anthropic / 阿里云显式）
	}

	rules := []patchRule{
		// ── OpenAI ──────────────────────────────────────────────────────
		// 全自动缓存，命中享50%折扣，1024 token起步
		{Pattern: "gpt-4o", CacheMechanism: "auto", CacheMinTokens: 1024, CacheInputRatio: 0.5},
		{Pattern: "gpt-4o-%", CacheMechanism: "auto", CacheMinTokens: 1024, CacheInputRatio: 0.5},
		{Pattern: "gpt-4o-mini%", CacheMechanism: "auto", CacheMinTokens: 1024, CacheInputRatio: 0.5},
		{Pattern: "o1%", CacheMechanism: "auto", CacheMinTokens: 1024, CacheInputRatio: 0.5},
		{Pattern: "o3%", CacheMechanism: "auto", CacheMinTokens: 1024, CacheInputRatio: 0.5},
		{Pattern: "o4%", CacheMechanism: "auto", CacheMinTokens: 1024, CacheInputRatio: 0.5},

		// ── Anthropic ────────────────────────────────────────────────────
		// 需显式 cache_control 参数，命中享90%折扣，写入溢价25%
		// claude-3-5-sonnet / claude-3-7-sonnet 等：1024 token起步
		{Pattern: "claude-3-5-sonnet%", CacheMechanism: "explicit", CacheMinTokens: 1024, CacheInputRatio: 0.1, CacheWriteRatio: 1.25},
		{Pattern: "claude-3-7-sonnet%", CacheMechanism: "explicit", CacheMinTokens: 1024, CacheInputRatio: 0.1, CacheWriteRatio: 1.25},
		{Pattern: "claude-sonnet%", CacheMechanism: "explicit", CacheMinTokens: 1024, CacheInputRatio: 0.1, CacheWriteRatio: 1.25},
		// claude-3-5-haiku / claude-3-haiku / claude-3-opus：4096 token起步
		{Pattern: "claude-3-5-haiku%", CacheMechanism: "explicit", CacheMinTokens: 4096, CacheInputRatio: 0.1, CacheWriteRatio: 1.25},
		{Pattern: "claude-haiku%", CacheMechanism: "explicit", CacheMinTokens: 4096, CacheInputRatio: 0.1, CacheWriteRatio: 1.25},
		{Pattern: "claude-3-haiku%", CacheMechanism: "explicit", CacheMinTokens: 4096, CacheInputRatio: 0.1, CacheWriteRatio: 1.25},
		{Pattern: "claude-3-opus%", CacheMechanism: "explicit", CacheMinTokens: 4096, CacheInputRatio: 0.1, CacheWriteRatio: 1.25},
		{Pattern: "claude-opus%", CacheMechanism: "explicit", CacheMinTokens: 4096, CacheInputRatio: 0.1, CacheWriteRatio: 1.25},
		{Pattern: "claude-3-sonnet%", CacheMechanism: "explicit", CacheMinTokens: 1024, CacheInputRatio: 0.1, CacheWriteRatio: 1.25},

		// ── DeepSeek ─────────────────────────────────────────────────────
		// 全自动磁盘缓存，命中约节省90%（$0.028 vs $0.27/M）
		{Pattern: "deepseek-chat%", CacheMechanism: "auto", CacheMinTokens: 0, CacheInputRatio: 0.1},
		{Pattern: "deepseek-reasoner%", CacheMechanism: "auto", CacheMinTokens: 0, CacheInputRatio: 0.1},
		{Pattern: "deepseek-v%", CacheMechanism: "auto", CacheMinTokens: 0, CacheInputRatio: 0.1},
		{Pattern: "deepseek-r%", CacheMechanism: "auto", CacheMinTokens: 0, CacheInputRatio: 0.1},

		// ── 火山引擎 / 豆包 ─────────────────────────────────────────────
		// 透明前缀缓存，命中约节省60%
		{Pattern: "doubao%", CacheMechanism: "auto", CacheMinTokens: 0, CacheInputRatio: 0.4},

		// ── 阿里云百炼 / 通义千问 ────────────────────────────────────────
		// 双模式：隐式(auto)命中20%价，显式(explicit)命中10%价，写入125%
		{Pattern: "qwen%", CacheMechanism: "both", CacheMinTokens: 1024, CacheInputRatio: 0.2, CacheExplicitInputRatio: 0.1, CacheWriteRatio: 1.25},

		// ── Moonshot / Kimi ──────────────────────────────────────────────
		// 全自动，命中约节省75%
		{Pattern: "moonshot%", CacheMechanism: "auto", CacheMinTokens: 0, CacheInputRatio: 0.25},
		{Pattern: "kimi%", CacheMechanism: "auto", CacheMinTokens: 0, CacheInputRatio: 0.25},

		// ── Google Gemini ────────────────────────────────────────────────
		// 隐式缓存（全自动），约享25%折扣（即命中价约为原价75%）
		{Pattern: "gemini%", CacheMechanism: "auto", CacheMinTokens: 0, CacheInputRatio: 0.75},

		// ── 智谱 GLM ─────────────────────────────────────────────────────
		// 全自动多轮缓存，命中价约为原价20%
		{Pattern: "glm%", CacheMechanism: "auto", CacheMinTokens: 0, CacheInputRatio: 0.2},
	}

	totalUpdated := int64(0)
	for _, r := range rules {
		result := db.Exec(`
			UPDATE ai_models
			SET
				supports_cache               = 1,
				cache_mechanism              = ?,
				cache_min_tokens             = ?,
				cache_input_price_rmb        = CASE WHEN input_cost_rmb > 0 THEN input_cost_rmb * ? ELSE 0 END,
				cache_explicit_input_price_rmb = CASE WHEN input_cost_rmb > 0 THEN input_cost_rmb * ? ELSE 0 END,
				cache_write_price_rmb        = CASE WHEN input_cost_rmb > 0 THEN input_cost_rmb * ? ELSE 0 END
			WHERE model_name LIKE ?
			  AND supports_cache = 0
			  AND deleted_at IS NULL
		`,
			r.CacheMechanism,
			r.CacheMinTokens,
			r.CacheInputRatio,
			r.CacheExplicitInputRatio,
			r.CacheWriteRatio,
			r.Pattern,
		)
		if result.Error != nil {
			logger.L.Warn("cache price migration: update failed",
				zap.String("pattern", r.Pattern),
				zap.Error(result.Error),
			)
			continue
		}
		totalUpdated += result.RowsAffected
	}

	if totalUpdated > 0 {
		logger.L.Info("cache price migration: complete", zap.Int64("models_updated", totalUpdated))
	} else {
		logger.L.Info("cache price migration: no models needed updating (already up-to-date)")
	}
}
