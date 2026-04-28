package database

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunHotModelVisibilityMigration controls the first public model set from trending_models.
// Models with popularity_stars > 4 are enabled and published; all others stay offline.
func RunHotModelVisibilityMigration(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable(&model.AIModel{}) || !db.Migrator().HasTable(&model.TrendingModel{}) {
		return nil
	}

	var names []string
	if err := db.Model(&model.TrendingModel{}).
		Where("is_active = ? AND popularity_stars > ?", true, 4).
		Pluck("LOWER(model_name)", &names).Error; err != nil {
		return fmt.Errorf("query hot trending models: %w", err)
	}

	hotNames := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		normalized := strings.ToLower(strings.TrimSpace(name))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		hotNames = append(hotNames, normalized)
	}
	if len(hotNames) == 0 {
		logHotModelVisibilityInfo("hot model visibility migration: skipped, no >4-star reference models")
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		disable := tx.Model(&model.AIModel{}).
			Where("LOWER(model_name) NOT IN ?", hotNames).
			Updates(map[string]interface{}{
				"is_active": false,
				"status":    "offline",
			})
		if disable.Error != nil {
			return fmt.Errorf("disable non-hot models: %w", disable.Error)
		}

		enable := tx.Model(&model.AIModel{}).
			Where("LOWER(model_name) IN ?", hotNames).
			Updates(map[string]interface{}{
				"is_active": true,
				"status":    "online",
			})
		if enable.Error != nil {
			return fmt.Errorf("enable hot models: %w", enable.Error)
		}

		logHotModelVisibilityInfo("hot model visibility migration: complete",
			zap.Int("reference_models", len(hotNames)),
			zap.Int64("enabled_models", enable.RowsAffected),
			zap.Int64("disabled_models", disable.RowsAffected),
		)
		return nil
	})
}

func logHotModelVisibilityInfo(msg string, fields ...zap.Field) {
	if logger.L != nil {
		logger.L.Info(msg, fields...)
	}
}
