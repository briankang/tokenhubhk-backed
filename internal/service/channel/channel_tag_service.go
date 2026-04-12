package channel

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// TagStats 标签统计信息
type TagStats struct {
	TagID        uint   `json:"tag_id"`
	TagName      string `json:"tag_name"`
	ChannelCount int64  `json:"channel_count"`
	ActiveCount  int64  `json:"active_count"`
}

// ChannelTagService 渠道标签服务，提供标签的增删改查操作
type ChannelTagService struct {
	db *gorm.DB
}

// NewChannelTagService 创建渠道标签服务实例
func NewChannelTagService(db *gorm.DB) *ChannelTagService {
	if db == nil {
		panic("ChannelTagService: db is nil")
	}
	return &ChannelTagService{db: db}
}

// Create 创建新的渠道标签，校验名称唯一性
func (s *ChannelTagService) Create(ctx context.Context, tag *model.ChannelTag) error {
	if tag == nil {
		return fmt.Errorf("tag is nil")
	}
	if tag.Name == "" {
		return fmt.Errorf("tag name is required")
	}

	// 检查名称唯一性
	var count int64
	s.db.WithContext(ctx).Model(&model.ChannelTag{}).Where("name = ?", tag.Name).Count(&count)
	if count > 0 {
		return fmt.Errorf("tag name already exists")
	}

	if err := s.db.WithContext(ctx).Create(tag).Error; err != nil {
		return fmt.Errorf("failed to create tag: %w", err)
	}

	logger.L.Info("channel tag created", zap.Uint("id", tag.ID), zap.String("name", tag.Name))
	return nil
}

// Update 根据 ID 更新标签信息，修改名称时校验唯一性
func (s *ChannelTagService) Update(ctx context.Context, id uint, updates map[string]interface{}) error {
	if id == 0 {
		return fmt.Errorf("tag id is required")
	}
	if len(updates) == 0 {
		return fmt.Errorf("no fields to update")
	}

	delete(updates, "id")
	delete(updates, "created_at")

	// 修改名称时检查唯一性
	if name, ok := updates["name"]; ok {
		var count int64
		s.db.WithContext(ctx).Model(&model.ChannelTag{}).Where("name = ? AND id != ?", name, id).Count(&count)
		if count > 0 {
			return fmt.Errorf("tag name already exists")
		}
	}

	result := s.db.WithContext(ctx).Model(&model.ChannelTag{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to update tag: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("tag not found")
	}

	logger.L.Info("channel tag updated", zap.Uint("id", id))
	return nil
}

// Delete 删除标签，同时清除多对多关联关系
func (s *ChannelTagService) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("tag id is required")
	}

	// 先删除关联关系
	if err := s.db.WithContext(ctx).Exec("DELETE FROM channel_tags_relation WHERE channel_tag_id = ?", id).Error; err != nil {
		return fmt.Errorf("failed to clear tag associations: %w", err)
	}

	result := s.db.WithContext(ctx).Delete(&model.ChannelTag{}, id)
	if result.Error != nil {
		return fmt.Errorf("failed to delete tag: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("tag not found")
	}

	logger.L.Info("channel tag deleted", zap.Uint("id", id))
	return nil
}

// List 查询所有标签，按排序顺序返回
func (s *ChannelTagService) List(ctx context.Context) ([]model.ChannelTag, error) {
	var tags []model.ChannelTag
	if err := s.db.WithContext(ctx).Order("sort_order ASC, id ASC").Find(&tags).Error; err != nil {
		return nil, fmt.Errorf("failed to list tags: %w", err)
	}
	return tags, nil
}

// GetStats 获取指定标签的统计信息（关联渠道数、活跃渠道数）
func (s *ChannelTagService) GetStats(ctx context.Context, tagID uint) (*TagStats, error) {
	if tagID == 0 {
		return nil, fmt.Errorf("tag id is required")
	}

	var tag model.ChannelTag
	if err := s.db.WithContext(ctx).First(&tag, tagID).Error; err != nil {
		return nil, fmt.Errorf("tag not found: %w", err)
	}

	stats := &TagStats{
		TagID:   tagID,
		TagName: tag.Name,
	}

	// 拥有该标签的渠道总数
	s.db.WithContext(ctx).
		Table("channel_tags_relation").
		Where("channel_tag_id = ?", tagID).
		Count(&stats.ChannelCount)

	// 拥有该标签的活跃渠道数
	s.db.WithContext(ctx).
		Table("channel_tags_relation ctr").
		Joins("JOIN channels c ON c.id = ctr.channel_id").
		Where("ctr.channel_tag_id = ? AND c.status = ? AND c.deleted_at IS NULL", tagID, "active").
		Count(&stats.ActiveCount)

	return stats, nil
}
