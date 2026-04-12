package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	pkgredis "tokenhub-server/internal/pkg/redis"
)

// BackupStatus 备份规则的当前状态
type BackupStatus struct {
	RuleID         uint      `json:"rule_id"`
	RuleName       string    `json:"rule_name"`
	IsActive       bool      `json:"is_active"`
	IsSwitched     bool      `json:"is_switched"`
	ActiveGroupID  uint      `json:"active_group_id"`
	PrimaryGroupID uint      `json:"primary_group_id"`
	SwitchedAt     time.Time `json:"switched_at,omitempty"`
	Reason         string    `json:"reason,omitempty"`
}

// BackupEvent 备份切换或恢复事件记录
type BackupEvent struct {
	RuleID    uint      `json:"rule_id"`
	EventType string    `json:"event_type"` // "switch" or "recover"
	FromGroup uint      `json:"from_group"`
	ToGroup   uint      `json:"to_group"`
	Reason    string    `json:"reason"`
	Timestamp time.Time `json:"timestamp"`
}

// BackupService 备份规则服务，管理备份规则的增删改查和手动切换/恢复操作
type BackupService struct {
	db    *gorm.DB
	redis *goredis.Client
}

// NewBackupService 创建备份服务实例
func NewBackupService(db *gorm.DB, redis *goredis.Client) *BackupService {
	if db == nil {
		panic("BackupService: db is nil")
	}
	return &BackupService{db: db, redis: redis}
}

// Create 创建新的备份规则，校验主组和备份组的存在性
func (s *BackupService) Create(ctx context.Context, rule *model.BackupRule) error {
	if rule == nil {
		return fmt.Errorf("rule is nil")
	}
	if rule.Name == "" {
		return fmt.Errorf("rule name is required")
	}
	if rule.PrimaryGroupID == 0 {
		return fmt.Errorf("primary_group_id is required")
	}
	if rule.ModelPattern == "" {
		return fmt.Errorf("model_pattern is required")
	}

	// 校验主组是否存在
	var count int64
	s.db.WithContext(ctx).Model(&model.ChannelGroup{}).Where("id = ?", rule.PrimaryGroupID).Count(&count)
	if count == 0 {
		return fmt.Errorf("primary group not found")
	}

	// 校验备份组 ID 是否存在
	if rule.BackupGroupIDs != nil {
		var ids []uint
		if err := json.Unmarshal(rule.BackupGroupIDs, &ids); err == nil {
			for _, id := range ids {
				var c int64
				s.db.WithContext(ctx).Model(&model.ChannelGroup{}).Where("id = ?", id).Count(&c)
				if c == 0 {
					return fmt.Errorf("backup group %d not found", id)
				}
			}
		}
	}

	if err := s.db.WithContext(ctx).Create(rule).Error; err != nil {
		return fmt.Errorf("failed to create backup rule: %w", err)
	}

	logger.L.Info("backup rule created", zap.Uint("id", rule.ID), zap.String("name", rule.Name))
	return nil
}

// Update 根据 ID 更新备份规则
func (s *BackupService) Update(ctx context.Context, id uint, updates map[string]interface{}) error {
	if id == 0 {
		return fmt.Errorf("rule id is required")
	}
	if len(updates) == 0 {
		return fmt.Errorf("no fields to update")
	}

	delete(updates, "id")
	delete(updates, "created_at")

	result := s.db.WithContext(ctx).Model(&model.BackupRule{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to update backup rule: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("backup rule not found")
	}

	logger.L.Info("backup rule updated", zap.Uint("id", id))
	return nil
}

// Delete 软删除备份规则，同时清理 Redis 状态
func (s *BackupService) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("rule id is required")
	}

	// 清理 Redis 状态
	rule, err := s.GetByID(ctx, id)
	if err == nil && rule != nil {
		s.clearSwitchState(ctx, rule.PrimaryGroupID)
	}

	result := s.db.WithContext(ctx).Delete(&model.BackupRule{}, id)
	if result.Error != nil {
		return fmt.Errorf("failed to delete backup rule: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("backup rule not found")
	}

	logger.L.Info("backup rule deleted", zap.Uint("id", id))
	return nil
}

// GetByID 根据 ID 查询备份规则，预加载主组信息
func (s *BackupService) GetByID(ctx context.Context, id uint) (*model.BackupRule, error) {
	if id == 0 {
		return nil, fmt.Errorf("rule id is required")
	}

	var rule model.BackupRule
	if err := s.db.WithContext(ctx).Preload("PrimaryGroup").First(&rule, id).Error; err != nil {
		return nil, fmt.Errorf("failed to get backup rule: %w", err)
	}
	return &rule, nil
}

// List 分页查询备份规则列表
func (s *BackupService) List(ctx context.Context, page, pageSize int) ([]model.BackupRule, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var total int64
	if err := s.db.WithContext(ctx).Model(&model.BackupRule{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count backup rules: %w", err)
	}

	var rules []model.BackupRule
	err := s.db.WithContext(ctx).
		Preload("PrimaryGroup").
		Order("id DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&rules).Error
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list backup rules: %w", err)
	}

	return rules, total, nil
}

// GetStatus 获取备份规则的当前状态（是否已切换、切换时间、原因等）
func (s *BackupService) GetStatus(ctx context.Context, ruleID uint) (*BackupStatus, error) {
	rule, err := s.GetByID(ctx, ruleID)
	if err != nil {
		return nil, err
	}

	status := &BackupStatus{
		RuleID:         rule.ID,
		RuleName:       rule.Name,
		IsActive:       rule.IsActive,
		PrimaryGroupID: rule.PrimaryGroupID,
		ActiveGroupID:  rule.PrimaryGroupID,
	}

	// 检查是否已切换
	switchKey := fmt.Sprintf("backup:active:%d", rule.PrimaryGroupID)
	val, err := pkgredis.Get(ctx, switchKey)
	if err == nil && val != "" {
		var backupGroupID uint
		if json.Unmarshal([]byte(val), &backupGroupID) == nil && backupGroupID != 0 {
			status.IsSwitched = true
			status.ActiveGroupID = backupGroupID
		}
	}

	// 获取切换时间戳
	tsKey := fmt.Sprintf("backup:switch_at:%d", rule.PrimaryGroupID)
	tsVal, err := pkgredis.Get(ctx, tsKey)
	if err == nil && tsVal != "" {
		if t, err := time.Parse(time.RFC3339, tsVal); err == nil {
			status.SwitchedAt = t
		}
	}

	// 获取切换原因
	reasonKey := fmt.Sprintf("backup:reason:%d", rule.PrimaryGroupID)
	reason, _ := pkgredis.Get(ctx, reasonKey)
	status.Reason = reason

	return status, nil
}

// ManualSwitch 手动将主组切换到第一个备份组
func (s *BackupService) ManualSwitch(ctx context.Context, ruleID uint) error {
	rule, err := s.GetByID(ctx, ruleID)
	if err != nil {
		return err
	}

	backupIDs, err := s.parseBackupGroupIDs(rule)
	if err != nil || len(backupIDs) == 0 {
		return fmt.Errorf("no backup groups configured")
	}

	lock, err := pkgredis.Lock(ctx, fmt.Sprintf("backup:switch:%d", ruleID), 10*time.Second)
	if err != nil {
		return fmt.Errorf("failed to acquire switch lock: %w", err)
	}
	defer lock.Unlock(ctx)

	targetGroupID := backupIDs[0]

	// 在 Redis 中设置活跃备份组
	s.setSwitchState(ctx, rule.PrimaryGroupID, targetGroupID, "manual switch")

	// 记录事件
	s.recordEvent(ctx, ruleID, "switch", rule.PrimaryGroupID, targetGroupID, "manual switch")

	logger.L.Warn("backup rule manually switched",
		zap.Uint("rule_id", ruleID),
		zap.Uint("from_group", rule.PrimaryGroupID),
		zap.Uint("to_group", targetGroupID),
	)

	return nil
}

// ManualRecover 手动恢复到主组
func (s *BackupService) ManualRecover(ctx context.Context, ruleID uint) error {
	rule, err := s.GetByID(ctx, ruleID)
	if err != nil {
		return err
	}

	lock, err := pkgredis.Lock(ctx, fmt.Sprintf("backup:switch:%d", ruleID), 10*time.Second)
	if err != nil {
		return fmt.Errorf("failed to acquire switch lock: %w", err)
	}
	defer lock.Unlock(ctx)

	// 获取当前备份组用于事件记录
	var currentBackup uint
	switchKey := fmt.Sprintf("backup:active:%d", rule.PrimaryGroupID)
	val, _ := pkgredis.Get(ctx, switchKey)
	if val != "" {
		_ = json.Unmarshal([]byte(val), &currentBackup)
	}

	// 清除切换状态
	s.clearSwitchState(ctx, rule.PrimaryGroupID)

	// 记录事件
	s.recordEvent(ctx, ruleID, "recover", currentBackup, rule.PrimaryGroupID, "manual recover")

	logger.L.Info("backup rule manually recovered",
		zap.Uint("rule_id", ruleID),
		zap.Uint("primary_group", rule.PrimaryGroupID),
	)

	return nil
}

// GetEvents 从 Redis 分页获取备份事件列表
func (s *BackupService) GetEvents(ctx context.Context, ruleID uint, page, pageSize int) ([]BackupEvent, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	key := fmt.Sprintf("backup:events:%d", ruleID)

	// 获取总数
	total, err := s.redis.LLen(ctx, key).Result()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get event count: %w", err)
	}

	start := int64((page - 1) * pageSize)
	end := start + int64(pageSize) - 1

	vals, err := s.redis.LRange(ctx, key, start, end).Result()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get events: %w", err)
	}

	var events []BackupEvent
	for _, v := range vals {
		var evt BackupEvent
		if json.Unmarshal([]byte(v), &evt) == nil {
			events = append(events, evt)
		}
	}

	return events, total, nil
}

// parseBackupGroupIDs 解析备份组 ID 列表
func (s *BackupService) parseBackupGroupIDs(rule *model.BackupRule) ([]uint, error) {
	if rule.BackupGroupIDs == nil {
		return nil, nil
	}
	var ids []uint
	if err := json.Unmarshal(rule.BackupGroupIDs, &ids); err != nil {
		return nil, fmt.Errorf("failed to parse backup_group_ids: %w", err)
	}
	return ids, nil
}

// setSwitchState 在 Redis 中记录活跃的备份组状态
func (s *BackupService) setSwitchState(ctx context.Context, primaryGroupID, backupGroupID uint, reason string) {
	data, _ := json.Marshal(backupGroupID)
	_ = pkgredis.Set(ctx, fmt.Sprintf("backup:active:%d", primaryGroupID), string(data), 0)
	_ = pkgredis.Set(ctx, fmt.Sprintf("backup:switch_at:%d", primaryGroupID), time.Now().Format(time.RFC3339), 0)
	_ = pkgredis.Set(ctx, fmt.Sprintf("backup:reason:%d", primaryGroupID), reason, 0)
}

// clearSwitchState 清除 Redis 中的备份切换状态
func (s *BackupService) clearSwitchState(ctx context.Context, primaryGroupID uint) {
	_ = pkgredis.Del(ctx,
		fmt.Sprintf("backup:active:%d", primaryGroupID),
		fmt.Sprintf("backup:switch_at:%d", primaryGroupID),
		fmt.Sprintf("backup:reason:%d", primaryGroupID),
	)
}

// recordEvent 将备份事件追加到 Redis 列表，保留最近 1000 条
func (s *BackupService) recordEvent(ctx context.Context, ruleID uint, eventType string, fromGroup, toGroup uint, reason string) {
	evt := BackupEvent{
		RuleID:    ruleID,
		EventType: eventType,
		FromGroup: fromGroup,
		ToGroup:   toGroup,
		Reason:    reason,
		Timestamp: time.Now(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return
	}

	key := fmt.Sprintf("backup:events:%d", ruleID)
	s.redis.LPush(ctx, key, string(data))
	// 保留最近 1000 条事件
	s.redis.LTrim(ctx, key, 0, 999)
}
