package database

import (
	"encoding/json"
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/service/modeldiscovery"
)

// RunModelCapabilityDefaultsMigration 收敛供应商默认能力，并按模型级规则重算现有模型能力。
//
// 供应商 default_features 只作为“新模型同步初值”，因此采用保守配置；
// 实际是否支持 Thinking / Vision / Web Search / Function Call / JSON Mode 以 ai_models.features 为准。
func RunModelCapabilityDefaultsMigration(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	defaults := map[string]map[string]bool{
		"aliyun_dashscope": {
			"supports_function_call": true,
			"supports_json_mode":     true,
		},
		"volcengine": {
			"supports_function_call": true,
			"supports_json_mode":     true,
		},
		"baidu_qianfan": {
			"supports_function_call": true,
			"supports_json_mode":     true,
		},
		"tencent_hunyuan": {
			"supports_function_call": true,
		},
		"wangsu_aigw": {},
		"talkingdata": {},
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		for code, feats := range defaults {
			raw, err := json.Marshal(feats)
			if err != nil {
				return err
			}
			if err := tx.Model(&model.Supplier{}).
				Where("code = ? AND deleted_at IS NULL", code).
				Update("default_features", model.JSON(raw)).Error; err != nil {
				return fmt.Errorf("update supplier default features %s: %w", code, err)
			}
		}

		type row struct {
			model.AIModel
			SupplierCode string
		}
		var rows []row
		if err := tx.Model(&model.AIModel{}).
			Select("ai_models.*, suppliers.code AS supplier_code").
			Joins("JOIN suppliers ON suppliers.id = ai_models.supplier_id AND suppliers.deleted_at IS NULL").
			Where("ai_models.deleted_at IS NULL").
			Find(&rows).Error; err != nil {
			return fmt.Errorf("query ai models: %w", err)
		}

		updated := 0
		for _, r := range rows {
			features := make(map[string]interface{})
			if len(r.Features) > 0 {
				_ = json.Unmarshal(r.Features, &features)
			}
			before, _ := json.Marshal(features)
			modeldiscovery.InferFeaturesForModel(r.SupplierCode, r.ModelName, r.ModelType, r.InputModalities, r.TaskTypes, features)
			if modeldiscovery.MatchStreamOnly(r.ModelName) {
				features["requires_stream"] = true
			}
			after, _ := json.Marshal(features)
			if string(before) == string(after) {
				continue
			}
			if len(features) == 0 {
				after = nil
			}
			if err := tx.Model(&model.AIModel{}).Where("id = ?", r.ID).Update("features", model.JSON(after)).Error; err != nil {
				return fmt.Errorf("update model features %s: %w", r.ModelName, err)
			}
			updated++
		}
		log.Info("model capability defaults migration complete", zap.Int("models_updated", updated))
		return nil
	}); err != nil {
		return err
	}
	return nil
}
