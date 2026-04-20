package database

import (
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/logger"
)

// RunExtraParamsCleanupMigration 清理 ai_models.extra_params 中的脏数据
//
// 背景：历史数据中 extra_params 可能被错误写入 JSON 字符串（如 "eyJ..." base64 或 JWT），
// 前端 Object.entries(string) 会把每个字符当 entry 展开，UI 呈现为
// {0:"e",1:"y",2:"J",...} 的伪对象，导致编辑弹窗的"自定义参数"区出现大量单字符键值对。
//
// 迁移策略（幂等）：
//  1. 只清理 JSON_TYPE 为 'STRING' 或 'ARRAY' 的脏数据，置为 NULL
//  2. 保留正常的 OBJECT / NULL 数据不变
//  3. 单次扫描 ai_models 表，记录清理数量到日志
//
// 注意：此迁移仅处理明显脏数据（字符串被 JSON 序列化后存入 JSON 列），
// 不影响合法的 Record<string, unknown> 对象。
func RunExtraParamsCleanupMigration(db *gorm.DB) {
	start := time.Now()

	// 统计脏数据数量（JSON_TYPE = 'STRING' 或 'ARRAY'）
	var dirtyCount int64
	if err := db.Table("ai_models").
		Where("extra_params IS NOT NULL AND (JSON_TYPE(extra_params) = 'STRING' OR JSON_TYPE(extra_params) = 'ARRAY')").
		Count(&dirtyCount).Error; err != nil {
		logger.L.Warn("extra_params cleanup: count failed (likely JSON_TYPE unsupported or empty table)",
			zap.Error(err))
		return
	}

	if dirtyCount == 0 {
		logger.L.Info("extra_params cleanup: no dirty data found, skipped",
			zap.Duration("duration", time.Since(start)))
		return
	}

	// 清理：置为 NULL（保留字段，允许管理员重新填写）
	res := db.Exec(`
		UPDATE ai_models
		SET extra_params = NULL
		WHERE extra_params IS NOT NULL
		  AND (JSON_TYPE(extra_params) = 'STRING' OR JSON_TYPE(extra_params) = 'ARRAY')
	`)

	if res.Error != nil {
		logger.L.Error("extra_params cleanup: update failed",
			zap.Error(res.Error),
			zap.Int64("dirty_count", dirtyCount))
		return
	}

	logger.L.Info("extra_params cleanup: complete",
		zap.Int64("dirty_count", dirtyCount),
		zap.Int64("rows_affected", res.RowsAffected),
		zap.Duration("duration", time.Since(start)))
}
