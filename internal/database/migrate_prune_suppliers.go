package database

import (
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// ActiveChineseSupplierCodes 是生产环境当前保留的真实接入供应商。
// 其余供应商多为历史模板、调研样例或 UI 测试数据，迁移时会从管理后台软删除。
var ActiveChineseSupplierCodes = []string{
	"aliyun_dashscope",
	"volcengine",
	"baidu_qianfan",
	"tencent_hunyuan",
	"wangsu_aigw",
	"talkingdata",
}

// RunPruneUnusedSuppliersMigration 清理非真实接入供应商及其目录数据。
//
// 只清理运营配置类数据：suppliers / model_categories / ai_models / channels 及其派生配置；
// 不清理 api_call_logs、channel_logs、daily_stats 等历史账务和审计数据。
func RunPruneUnusedSuppliersMigration(db *gorm.DB) error {
	if db == nil {
		return nil
	}

	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var staleSuppliers []model.Supplier
		if err := tx.Where("code NOT IN ?", ActiveChineseSupplierCodes).Find(&staleSuppliers).Error; err != nil {
			return fmt.Errorf("query stale suppliers: %w", err)
		}
		if len(staleSuppliers) == 0 {
			log.Info("supplier prune: no stale suppliers")
			return pruneChannelGroups(tx, log)
		}

		supplierIDs := make([]uint, 0, len(staleSuppliers))
		for _, sup := range staleSuppliers {
			supplierIDs = append(supplierIDs, sup.ID)
		}

		var modelIDs []uint
		if err := tx.Model(&model.AIModel{}).Where("supplier_id IN ?", supplierIDs).Pluck("id", &modelIDs).Error; err != nil {
			return fmt.Errorf("query stale models: %w", err)
		}
		var channelIDs []uint
		if err := tx.Model(&model.Channel{}).Where("supplier_id IN ?", supplierIDs).Pluck("id", &channelIDs).Error; err != nil {
			return fmt.Errorf("query stale channels: %w", err)
		}

		if len(modelIDs) > 0 {
			if err := tx.Where("model_id IN ?", modelIDs).Delete(&model.ModelPricing{}).Error; err != nil {
				return fmt.Errorf("delete model pricings: %w", err)
			}
			if err := tx.Where("model_id IN ?", modelIDs).Delete(&model.ModelLabel{}).Error; err != nil {
				return fmt.Errorf("delete model labels: %w", err)
			}
			if err := tx.Where("model_id IN ?", modelIDs).Delete(&model.AgentPricing{}).Error; err != nil {
				return fmt.Errorf("delete agent pricings: %w", err)
			}
			if err := tx.Where("model_id IN ?", modelIDs).Delete(&model.UserModelDiscount{}).Error; err != nil {
				return fmt.Errorf("delete user model discounts: %w", err)
			}
			if err := tx.Where("id IN ?", modelIDs).Delete(&model.AIModel{}).Error; err != nil {
				return fmt.Errorf("delete ai models: %w", err)
			}
		}

		if len(channelIDs) > 0 {
			if err := tx.Where("channel_id IN ?", channelIDs).Delete(&model.ChannelModel{}).Error; err != nil {
				return fmt.Errorf("delete channel models: %w", err)
			}
			if err := tx.Exec("DELETE FROM channel_tags_relation WHERE channel_id IN ?", channelIDs).Error; err != nil {
				return fmt.Errorf("delete channel tag relations: %w", err)
			}
			if err := tx.Where("id IN ?", channelIDs).Delete(&model.Channel{}).Error; err != nil {
				return fmt.Errorf("delete channels: %w", err)
			}
		}

		if err := tx.Where("supplier_id IN ?", supplierIDs).Delete(&model.ModelCategory{}).Error; err != nil {
			return fmt.Errorf("delete categories: %w", err)
		}
		if err := tx.Where("id IN ?", supplierIDs).Delete(&model.Supplier{}).Error; err != nil {
			return fmt.Errorf("delete suppliers: %w", err)
		}

		log.Info("supplier prune: stale suppliers soft deleted",
			zap.Int("suppliers", len(supplierIDs)),
			zap.Int("models", len(modelIDs)),
			zap.Int("channels", len(channelIDs)),
		)

		return pruneChannelGroups(tx, log)
	})
}

func pruneChannelGroups(tx *gorm.DB, log *zap.Logger) error {
	var activeChannels []model.Channel
	if err := tx.Select("id").Find(&activeChannels).Error; err != nil {
		return fmt.Errorf("query active channels: %w", err)
	}
	active := make(map[uint]bool, len(activeChannels))
	for _, ch := range activeChannels {
		active[ch.ID] = true
	}

	var groups []model.ChannelGroup
	if err := tx.Find(&groups).Error; err != nil {
		return fmt.Errorf("query channel groups: %w", err)
	}
	activeGroups := make(map[uint]bool, len(groups))
	for _, group := range groups {
		var ids []uint
		_ = json.Unmarshal(group.ChannelIDs, &ids)
		filtered := make([]uint, 0, len(ids))
		for _, id := range ids {
			if active[id] {
				filtered = append(filtered, id)
			}
		}
		if len(filtered) == 0 {
			if err := tx.Delete(&model.ChannelGroup{}, group.ID).Error; err != nil {
				return fmt.Errorf("delete empty channel group %d: %w", group.ID, err)
			}
			continue
		}
		activeGroups[group.ID] = true
		raw, _ := json.Marshal(filtered)
		if string(raw) != string(group.ChannelIDs) {
			if err := tx.Model(&model.ChannelGroup{}).Where("id = ?", group.ID).Update("channel_ids", model.JSON(raw)).Error; err != nil {
				return fmt.Errorf("update channel group %d: %w", group.ID, err)
			}
		}
	}

	var rules []model.BackupRule
	if err := tx.Find(&rules).Error; err != nil {
		return fmt.Errorf("query backup rules: %w", err)
	}
	for _, rule := range rules {
		if !activeGroups[rule.PrimaryGroupID] {
			if err := tx.Delete(&model.BackupRule{}, rule.ID).Error; err != nil {
				return fmt.Errorf("delete backup rule %d: %w", rule.ID, err)
			}
			continue
		}
		var ids []uint
		_ = json.Unmarshal(rule.BackupGroupIDs, &ids)
		filtered := make([]uint, 0, len(ids))
		for _, id := range ids {
			if activeGroups[id] {
				filtered = append(filtered, id)
			}
		}
		raw, _ := json.Marshal(filtered)
		if string(raw) != string(rule.BackupGroupIDs) {
			if err := tx.Model(&model.BackupRule{}).Where("id = ?", rule.ID).Update("backup_group_ids", model.JSON(raw)).Error; err != nil {
				return fmt.Errorf("update backup rule %d: %w", rule.ID, err)
			}
		}
	}

	log.Info("supplier prune: channel groups normalized", zap.Time("at", time.Now()))
	return nil
}
