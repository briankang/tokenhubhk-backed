package database

import (
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/logger"
)

// RunQwqRequiresStreamMigration 为阿里云 qwq/qvq 系列模型回填 features.requires_stream=true
//
// 背景：阿里云的 qwq-* / qvq-* 推理模型仅支持流式调用，非流式请求会返回
// HTTP 400 `only support stream mode`。Handler 通过 AIModel.RequiresStream()
// 读取 features.requires_stream 字段，为 true 时自动将非流式请求升级为流式。
//
// 但历史数据中这些模型的 features 字段可能未正确标记，本次迁移补齐。
//
// 迁移策略（幂等）：
//   1. 使用 JSON_SET 仅在 features 为 NULL 或 features.requires_stream 缺失/为 false 时写入 true
//   2. 不覆盖管理员显式设置的 features.requires_stream=false（尊重手动配置）
//   3. 按前缀 qwq% / qvq% 批量匹配
func RunQwqRequiresStreamMigration(db *gorm.DB) {
	start := time.Now()

	patterns := []string{"qwq%", "qvq%"}
	var totalAffected int64

	for _, p := range patterns {
		res := db.Exec(`
			UPDATE ai_models
			SET features = JSON_SET(
				COALESCE(features, JSON_OBJECT()),
				'$.requires_stream', true
			)
			WHERE model_name LIKE ?
			  AND (
			    features IS NULL
			    OR JSON_EXTRACT(features, '$.requires_stream') IS NULL
			    OR JSON_EXTRACT(features, '$.requires_stream') = false
			    OR JSON_EXTRACT(features, '$.requires_stream') = 0
			  )
		`, p)
		if res.Error != nil {
			logger.L.Warn("qwq requires_stream migration: update failed",
				zap.String("pattern", p), zap.Error(res.Error))
			continue
		}
		totalAffected += res.RowsAffected
	}

	logger.L.Info("qwq requires_stream migration: complete",
		zap.Int64("rows_affected", totalAffected),
		zap.Duration("duration", time.Since(start)))
}
