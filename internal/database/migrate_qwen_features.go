package database

import (
	"encoding/json"
	"strings"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// RunFixQwenFeaturesMigration 修复 Qwen 3 系列及其他模型的能力标记（supports_thinking, supports_web_search）
// 1. 设置 aliyun_dashscope 供应商的 DefaultFeatures
// 2. 批量更新现有 Qwen 3 Plus/Max 模型的能力位
func RunFixQwenFeaturesMigration(db *gorm.DB) {
	logger.L.Info("Running migration: fix qwen features...")

	// 1. 更新 aliyun_dashscope 供应商的默认能力
	// 这样以后同步的新模型会默认开启这些
	defaultFeatures := map[string]interface{}{
		"supports_web_search": true,
		"supports_vision":     true,
	}
	featBytes, _ := json.Marshal(defaultFeatures)
	
	if err := db.Model(&model.Supplier{}).
		Where("code = ?", "aliyun_dashscope").
		Update("default_features", model.JSON(featBytes)).Error; err != nil {
		logger.L.Error("Update aliyun_dashscope default features failed", zap.Error(err))
	}

	// 2. 识别并更新现有模型的能力位
	// 规则：qwen3.*plus, qwen3.*max, qwq, qvq 支持 thinking
	// qwen 全系支持 web_search
	
	var models []model.AIModel
	if err := db.Where("model_name LIKE ?", "qwen%").
		Or("model_name LIKE ?", "qwq%").
		Or("model_name LIKE ?", "qvq%").
		Find(&models).Error; err != nil {
		logger.L.Error("Scan qwen models failed", zap.Error(err))
		return
	}

	for _, m := range models {
		name := strings.ToLower(m.ModelName)
		features := make(map[string]interface{})
		if len(m.Features) > 0 {
			_ = json.Unmarshal(m.Features, &features)
		}

		dirty := false

		// 联网搜索：Qwen 全系支持
		if _, exists := features["supports_web_search"]; !exists {
			features["supports_web_search"] = true
			dirty = true
		}

		// 深度思考：Qwen 3 Plus/Max, QwQ, QvQ 支持
		isThinkingModel := (strings.Contains(name, "qwen3") && (strings.Contains(name, "plus") || strings.Contains(name, "max"))) ||
			strings.Contains(name, "qwq") || strings.Contains(name, "qvq")
		
		if isThinkingModel {
			if v, ok := features["supports_thinking"].(bool); !ok || !v {
				features["supports_thinking"] = true
				dirty = true
			}
		}

		if dirty {
			newFeatBytes, _ := json.Marshal(features)
			if err := db.Model(&model.AIModel{}).Where("id = ?", m.ID).
				Update("features", model.JSON(newFeatBytes)).Error; err != nil {
				logger.L.Error("Update model features failed", zap.String("model", m.ModelName), zap.Error(err))
			}
		}
	}

	logger.L.Info("Migration: fix qwen features complete.")
}

