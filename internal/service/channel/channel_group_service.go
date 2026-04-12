package channel

import (
	"context"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// ChannelGroupService 渠道组服务，提供渠道组的增删改查和成员解析
type ChannelGroupService struct {
	db *gorm.DB
}

// NewChannelGroupService 创建渠道组服务实例
func NewChannelGroupService(db *gorm.DB) *ChannelGroupService {
	if db == nil {
		panic("ChannelGroupService: db is nil")
	}
	return &ChannelGroupService{db: db}
}

// Create 创建新的渠道组，校验名称、编码、混合模式和策略
func (s *ChannelGroupService) Create(ctx context.Context, group *model.ChannelGroup) error {
	if group == nil {
		return fmt.Errorf("group is nil")
	}
	if group.Name == "" {
		return fmt.Errorf("group name is required")
	}
	if group.Code == "" {
		return fmt.Errorf("group code is required")
	}

	// 默认值
	if group.Strategy == "" {
		group.Strategy = "Priority"
	}
	if group.MixMode == "" {
		group.MixMode = "SINGLE"
	}

	// 校验混合模式
	validModes := map[string]bool{"SINGLE": true, "FALLBACK_CHAIN": true, "SPLIT_BY_MODEL": true, "TAG_BASED": true}
	if !validModes[group.MixMode] {
		return fmt.Errorf("invalid mix_mode: %s", group.MixMode)
	}

	// 校验策略
	validStrategies := map[string]bool{"Priority": true, "Weight": true, "RoundRobin": true, "LeastLoad": true, "CostFirst": true}
	if !validStrategies[group.Strategy] {
		return fmt.Errorf("invalid strategy: %s", group.Strategy)
	}

	if err := s.db.WithContext(ctx).Create(group).Error; err != nil {
		return fmt.Errorf("failed to create channel group: %w", err)
	}

	logger.L.Info("channel group created", zap.Uint("id", group.ID), zap.String("code", group.Code))
	return nil
}

// Update 根据 ID 更新渠道组信息
func (s *ChannelGroupService) Update(ctx context.Context, id uint, updates map[string]interface{}) error {
	if id == 0 {
		return fmt.Errorf("group id is required")
	}
	if len(updates) == 0 {
		return fmt.Errorf("no fields to update")
	}

	delete(updates, "id")
	delete(updates, "created_at")

	// 更新时校验混合模式
	if mode, ok := updates["mix_mode"]; ok {
		validModes := map[string]bool{"SINGLE": true, "FALLBACK_CHAIN": true, "SPLIT_BY_MODEL": true, "TAG_BASED": true}
		if !validModes[fmt.Sprintf("%v", mode)] {
			return fmt.Errorf("invalid mix_mode: %v", mode)
		}
	}

	// 更新时校验策略
	if strat, ok := updates["strategy"]; ok {
		validStrategies := map[string]bool{"Priority": true, "Weight": true, "RoundRobin": true, "LeastLoad": true, "CostFirst": true}
		if !validStrategies[fmt.Sprintf("%v", strat)] {
			return fmt.Errorf("invalid strategy: %v", strat)
		}
	}

	result := s.db.WithContext(ctx).Model(&model.ChannelGroup{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to update channel group: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("channel group not found")
	}

	logger.L.Info("channel group updated", zap.Uint("id", id))
	return nil
}

// Delete 根据 ID 软删除渠道组
func (s *ChannelGroupService) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("group id is required")
	}

	result := s.db.WithContext(ctx).Delete(&model.ChannelGroup{}, id)
	if result.Error != nil {
		return fmt.Errorf("failed to delete channel group: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("channel group not found")
	}

	logger.L.Info("channel group deleted", zap.Uint("id", id))
	return nil
}

// GetByID 根据 ID 查询渠道组
func (s *ChannelGroupService) GetByID(ctx context.Context, id uint) (*model.ChannelGroup, error) {
	if id == 0 {
		return nil, fmt.Errorf("group id is required")
	}

	var group model.ChannelGroup
	if err := s.db.WithContext(ctx).First(&group, id).Error; err != nil {
		return nil, fmt.Errorf("failed to get channel group: %w", err)
	}
	return &group, nil
}

// List 分页查询渠道组列表
func (s *ChannelGroupService) List(ctx context.Context, page, pageSize int) ([]model.ChannelGroup, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var total int64
	if err := s.db.WithContext(ctx).Model(&model.ChannelGroup{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count channel groups: %w", err)
	}

	var groups []model.ChannelGroup
	err := s.db.WithContext(ctx).
		Order("id DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&groups).Error
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list channel groups: %w", err)
	}

	return groups, total, nil
}

// GetChannels 解析渠道组的成员渠道
// SINGLE/FALLBACK_CHAIN: 使用 ChannelIDs JSON 数组
// TAG_BASED: 使用 TagFilter 动态解析匹配的渠道
// SPLIT_BY_MODEL: 使用 ChannelIDs 作为全集
func (s *ChannelGroupService) GetChannels(ctx context.Context, groupID uint) ([]model.Channel, error) {
	group, err := s.GetByID(ctx, groupID)
	if err != nil {
		return nil, err
	}

	switch group.MixMode {
	case "TAG_BASED":
		return s.resolveByTagFilter(ctx, group)
	default:
		return s.resolveByChannelIDs(ctx, group)
	}
}

// resolveByChannelIDs 根据 group.ChannelIDs 解析成员渠道
func (s *ChannelGroupService) resolveByChannelIDs(ctx context.Context, group *model.ChannelGroup) ([]model.Channel, error) {
	if group.ChannelIDs == nil {
		return nil, nil
	}

	var ids []uint
	if err := json.Unmarshal(group.ChannelIDs, &ids); err != nil {
		return nil, fmt.Errorf("failed to parse channel_ids: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	var channels []model.Channel
	err := s.db.WithContext(ctx).
		Where("id IN ? AND status = ?", ids, "active").
		Preload("Tags").
		Order("priority DESC, weight DESC").
		Find(&channels).Error
	if err != nil {
		return nil, fmt.Errorf("failed to resolve channels: %w", err)
	}

	return channels, nil
}

// TagFilterConfig 标签过滤配置
type TagFilterConfig struct {
	TagIDs   []uint `json:"tag_ids"`
	MatchAll bool   `json:"match_all"`
}

// resolveByTagFilter 根据标签过滤条件动态解析匹配的渠道
func (s *ChannelGroupService) resolveByTagFilter(ctx context.Context, group *model.ChannelGroup) ([]model.Channel, error) {
	if group.TagFilter == nil {
		return nil, nil
	}

	var filter TagFilterConfig
	if err := json.Unmarshal(group.TagFilter, &filter); err != nil {
		return nil, fmt.Errorf("failed to parse tag_filter: %w", err)
	}
	if len(filter.TagIDs) == 0 {
		return nil, nil
	}

	query := s.db.WithContext(ctx).
		Model(&model.Channel{}).
		Joins("JOIN channel_tags_relation ctr ON ctr.channel_id = channels.id").
		Where("ctr.channel_tag_id IN ?", filter.TagIDs).
		Where("channels.status = ?", "active")

	if filter.MatchAll {
		query = query.
			Group("channels.id").
			Having("COUNT(DISTINCT ctr.channel_tag_id) = ?", len(filter.TagIDs))
	} else {
		query = query.Group("channels.id")
	}

	var channels []model.Channel
	if err := query.Preload("Tags").Order("priority DESC, weight DESC").Find(&channels).Error; err != nil {
		return nil, fmt.Errorf("failed to resolve channels by tag filter: %w", err)
	}

	return channels, nil
}
