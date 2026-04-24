package database

import (
	"encoding/json"
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/service/pricescraper"
)

// RunAliyunDeprecationMigration disables models listed in Alibaba Cloud Bailian deprecation notices.
func RunAliyunDeprecationMigration(db *gorm.DB) error {
	if db == nil {
		return nil
	}

	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	deprecated := pricescraper.GetAliyunDeprecatedModels()
	if len(deprecated) == 0 {
		return nil
	}

	names := make([]string, 0, len(deprecated))
	for name := range deprecated {
		names = append(names, name)
	}

	var suppliers []model.Supplier
	if err := db.Where("code IN ?", []string{"aliyun_dashscope", "alibaba"}).Find(&suppliers).Error; err != nil {
		return err
	}
	if len(suppliers) == 0 {
		return nil
	}

	supplierIDs := make([]uint, 0, len(suppliers))
	for _, sup := range suppliers {
		supplierIDs = append(supplierIDs, sup.ID)
	}

	result := db.Model(&model.AIModel{}).
		Where("supplier_id IN ? AND LOWER(model_name) IN ?", supplierIDs, names).
		Updates(map[string]interface{}{
			"status":          "offline",
			"is_active":       false,
			"supplier_status": "Deprecated",
		})
	if result.Error != nil {
		return result.Error
	}

	if err := removeDeprecatedAliyunChannelModels(db, supplierIDs, deprecated); err != nil {
		return err
	}

	if result.RowsAffected > 0 {
		log.Info("aliyun deprecation: models marked offline", zap.Int64("count", result.RowsAffected))
	}
	return nil
}

func removeDeprecatedAliyunChannelModels(db *gorm.DB, supplierIDs []uint, deprecated map[string]pricescraper.ModelDeprecation) error {
	var channels []model.Channel
	if err := db.Where("supplier_id IN ?", supplierIDs).Find(&channels).Error; err != nil {
		return err
	}

	for _, ch := range channels {
		if len(ch.Models) == 0 {
			continue
		}
		var names []string
		if err := json.Unmarshal(ch.Models, &names); err != nil {
			continue
		}

		filtered := make([]string, 0, len(names))
		changed := false
		for _, name := range names {
			if _, ok := deprecated[strings.ToLower(strings.TrimSpace(name))]; ok {
				changed = true
				continue
			}
			filtered = append(filtered, name)
		}
		if !changed {
			continue
		}

		raw, err := json.Marshal(filtered)
		if err != nil {
			return err
		}
		if err := db.Model(&model.Channel{}).Where("id = ?", ch.ID).Update("models", model.JSON(raw)).Error; err != nil {
			return err
		}
	}
	return nil
}
