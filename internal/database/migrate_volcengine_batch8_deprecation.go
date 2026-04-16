package database

import (
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// MigrateVolcengineBatch8Deprecation 将火山引擎第八批下线模型标记为 offline
//
// 来源：https://www.volcengine.com/docs/82379/1350667
//   - EOM（停止新购）：2026-04-09 10:00:00 (UTC+8)
//   - EOS（服务下线）：2026-05-11 14:00:00 (UTC+8)
//
// 下线模型及替代方案：
//
//	deepseek-r1-250528              → doubao-seed-2-0-lite-260215
//	deepseek-v3-250324              → doubao-seed-2-0-lite-260215
//	deepseek-v3-1-terminus          → doubao-seed-2-0-lite-260215
//	doubao-1-5-pro-32k-character-250228 → doubao-seed-character-251128
//	doubao-seed-1-6-lite-251015     → doubao-seed-2-0-mini-260215
//	doubao-seed-1-8-251215          → doubao-seed-1-8-251228
//	doubao-seedance-1-0-lite-i2v-250428 → doubao-seedance-2-0-fast-260128
//	doubao-seedance-1-0-lite-t2v-250428 → doubao-seedance-2-0-fast-260128
//	doubao-seedream-3-0-t2i-250415  → doubao-seedream-5-0-lite-260128
//
// 幂等设计：仅更新 status != 'offline' 的记录，重复执行无副作用。
func MigrateVolcengineBatch8Deprecation(db *gorm.DB) error {
	if db == nil {
		return nil
	}

	// 第八批即将下线的火山引擎模型（EOS: 2026-05-11）
	deprecatedModels := []string{
		"deepseek-r1-250528",
		"deepseek-v3-250324",
		"deepseek-v3-1-terminus",
		"doubao-1-5-pro-32k-character-250228",
		"doubao-seed-1-6-lite-251015",
		"doubao-seed-1-8-251215",
		"doubao-seedance-1-0-lite-i2v-250428",
		"doubao-seedance-1-0-lite-t2v-250428",
		"doubao-seedream-3-0-t2i-250415",
	}

	result := db.Model(&model.AIModel{}).
		Where("model_name IN ? AND status != ?", deprecatedModels, "offline").
		Update("status", "offline")

	if result.Error != nil {
		logger.L.Warn("volcengine batch8 deprecation migration failed",
			zap.Error(result.Error),
		)
		return result.Error
	}

	if result.RowsAffected > 0 {
		logger.L.Info("volcengine batch8: deprecated models marked offline",
			zap.Int64("count", result.RowsAffected),
			zap.Strings("models", deprecatedModels),
		)
	}

	return nil
}
