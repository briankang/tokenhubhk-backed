package database

import (
	"encoding/json"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunExtraParamsFeatureFlagsCleanup 清理 ai_models.extra_params 中的能力标记脏数据
//
// 背景：历史上 UI 把模型能力标记（如"支持 stop"）误写成 {"stop": true} 存入 extra_params。
// 当 v1/chat/completions 的 mergeExtraParams 把这些参数合并到请求体 JSON 时，
// 上游供应商会收到 `"stop": true`（bool 而非 string[]），导致 400 反序列化错误。
//
// 实测（2026-04-20）DB 中脏数据分布：
//   - 301 行 extra_params.stop = true
//   - 19  行 extra_params.voice = true (TTS 模型)
//   - 2   行 extra_params.dimensions = true (embedding)
//   - 1   行 extra_params.n = true, size = true (seedream-5-0)
//
// 迁移策略（幂等）：
//  1. 扫描所有 JSON_TYPE(extra_params) = 'OBJECT' 的行
//  2. 对每行，仅删除 {key, value} 满足「key 在 BogusFlagKeys 白名单 && value 是 bool」的条目
//  3. 若清洗后 map 为空，将 extra_params 整体置 NULL；否则写回清洗后的 JSON
//  4. 记录清理的行数与键数到日志
//
// 安全性：
//   - 仅清理 {key: bool} 形式。合法值（string/number/object/array）不受影响
//   - 白名单只包含"值为 bool 时一定非法"的键名
//   - 不改变表结构，不改变其他列，纯行级 UPDATE
func RunExtraParamsFeatureFlagsCleanup(db *gorm.DB) {
	start := time.Now()

	type row struct {
		ID          uint            `gorm:"column:id"`
		ExtraParams json.RawMessage `gorm:"column:extra_params"`
	}
	var rows []row
	if err := db.Table("ai_models").
		Select("id, extra_params").
		Where("extra_params IS NOT NULL AND JSON_TYPE(extra_params) = 'OBJECT'").
		Find(&rows).Error; err != nil {
		logger.L.Warn("extra_params feature flags cleanup: scan failed", zap.Error(err))
		return
	}

	if len(rows) == 0 {
		logger.L.Info("extra_params feature flags cleanup: no OBJECT rows, skipped",
			zap.Duration("duration", time.Since(start)))
		return
	}

	var rowsCleaned int64
	var keysRemoved int64
	var rowsNulled int64

	for _, r := range rows {
		var m map[string]interface{}
		if err := json.Unmarshal(r.ExtraParams, &m); err != nil {
			continue
		}

		dirty := false
		for k, v := range m {
			if !model.IsBogusFlagKey(k) {
				continue
			}
			if _, isBool := v.(bool); !isBool {
				continue
			}
			delete(m, k)
			keysRemoved++
			dirty = true
		}

		if !dirty {
			continue
		}
		rowsCleaned++

		if len(m) == 0 {
			// 清洗后 map 为空 → 置 NULL
			if err := db.Exec("UPDATE ai_models SET extra_params = NULL WHERE id = ?", r.ID).Error; err != nil {
				logger.L.Warn("extra_params feature flags cleanup: null update failed",
					zap.Uint("id", r.ID), zap.Error(err))
				continue
			}
			rowsNulled++
		} else {
			clean, err := json.Marshal(m)
			if err != nil {
				logger.L.Warn("extra_params feature flags cleanup: marshal failed",
					zap.Uint("id", r.ID), zap.Error(err))
				continue
			}
			if err := db.Exec("UPDATE ai_models SET extra_params = ? WHERE id = ?", string(clean), r.ID).Error; err != nil {
				logger.L.Warn("extra_params feature flags cleanup: update failed",
					zap.Uint("id", r.ID), zap.Error(err))
				continue
			}
		}
	}

	logger.L.Info("extra_params feature flags cleanup: complete",
		zap.Int("scanned_rows", len(rows)),
		zap.Int64("rows_cleaned", rowsCleaned),
		zap.Int64("rows_nulled", rowsNulled),
		zap.Int64("keys_removed", keysRemoved),
		zap.Duration("duration", time.Since(start)))
}
