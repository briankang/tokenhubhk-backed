package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// AuditService 审计日志服务
type AuditService struct {
	db *gorm.DB
}

// NewAuditService 创建审计日志服务实例
func NewAuditService(db *gorm.DB) *AuditService {
	return &AuditService{db: db}
}

// Create 创建审计日志记录
func (s *AuditService) Create(ctx context.Context, log *model.AuditLog) error {
	return s.db.WithContext(ctx).Create(log).Error
}

// CreateWithValues 创建审计日志（带旧值和新值）
func (s *AuditService) CreateWithValues(ctx context.Context, action string, resourceID, operatorID uint, oldValue, newValue interface{}, ip, remark string) error {
	oldJSON, _ := json.Marshal(oldValue)
	newJSON, _ := json.Marshal(newValue)

	log := &model.AuditLog{
		Action:     action,
		ResourceID: resourceID,
		OperatorID: operatorID,
		OldValue:   string(oldJSON),
		NewValue:   string(newJSON),
		IP:         ip,
		Remark:     remark,
	}

	return s.db.WithContext(ctx).Create(log).Error
}

// List 分页查询审计日志
func (s *AuditService) List(ctx context.Context, query *model.AuditLogQuery) ([]model.AuditLog, int64, error) {
	db := s.db.WithContext(ctx).Model(&model.AuditLog{})

	// 应用过滤条件
	if query.Action != "" {
		db = db.Where("action = ?", query.Action)
	}
	if query.OperatorID > 0 {
		db = db.Where("operator_id = ?", query.OperatorID)
	}
	if !query.StartDate.IsZero() {
		db = db.Where("created_at >= ?", query.StartDate)
	}
	if !query.EndDate.IsZero() {
		db = db.Where("created_at <= ?", query.EndDate)
	}

	// 统计总数
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count audit logs: %w", err)
	}

	// 分页查询
	page := query.Page
	if page < 1 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	var logs []model.AuditLog
	if err := db.Order("created_at DESC").Offset(offset).Limit(pageSize).Find(&logs).Error; err != nil {
		return nil, 0, fmt.Errorf("list audit logs: %w", err)
	}

	return logs, total, nil
}

// GetByID 根据ID获取审计日志
func (s *AuditService) GetByID(ctx context.Context, id uint) (*model.AuditLog, error) {
	var log model.AuditLog
	if err := s.db.WithContext(ctx).First(&log, id).Error; err != nil {
		return nil, err
	}
	return &log, nil
}

// GetByResource 获取指定资源的审计日志
func (s *AuditService) GetByResource(ctx context.Context, resource string, resourceID uint) ([]model.AuditLog, error) {
	var logs []model.AuditLog
	if err := s.db.WithContext(ctx).
		Where("resource = ? AND resource_id = ?", resource, resourceID).
		Order("created_at DESC").
		Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

// GetByUser 获取指定用户的审计日志
func (s *AuditService) GetByUser(ctx context.Context, userID uint, page, pageSize int) ([]model.AuditLog, int64, error) {
	var total int64
	db := s.db.WithContext(ctx).Model(&model.AuditLog{}).Where("user_id = ?", userID)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	var logs []model.AuditLog
	if err := db.Order("created_at DESC").Offset(offset).Limit(pageSize).Find(&logs).Error; err != nil {
		return nil, 0, err
	}

	return logs, total, nil
}

// GetRecent 获取最近的审计日志
func (s *AuditService) GetRecent(ctx context.Context, limit int) ([]model.AuditLog, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}

	var logs []model.AuditLog
	if err := s.db.WithContext(ctx).
		Order("created_at DESC").
		Limit(limit).
		Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

// DeleteOld 删除指定日期之前的审计日志（用于数据清理）
func (s *AuditService) DeleteOld(ctx context.Context, before time.Time) (int64, error) {
	result := s.db.WithContext(ctx).Where("created_at < ?", before).Delete(&model.AuditLog{})
	return result.RowsAffected, result.Error
}
