package pricing

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// SupplierService 供应商服务，提供供应商的增删改查操作
type SupplierService struct {
	db *gorm.DB
}

// NewSupplierService 创建供应商服务实例，db 不能为 nil
func NewSupplierService(db *gorm.DB) *SupplierService {
	if db == nil {
		panic("SupplierService: db must not be nil")
	}
	return &SupplierService{db: db}
}

// Create 创建新的供应商，校验名称和编码
func (s *SupplierService) Create(ctx context.Context, supplier *model.Supplier) error {
	if supplier == nil {
		return fmt.Errorf("supplier must not be nil")
	}
	if supplier.Name == "" {
		return fmt.Errorf("name is required")
	}
	if supplier.Code == "" {
		return fmt.Errorf("code is required")
	}
	if err := s.db.WithContext(ctx).Create(supplier).Error; err != nil {
		return fmt.Errorf("create supplier: %w", err)
	}
	return nil
}

// GetByID 根据 ID 查询供应商
func (s *SupplierService) GetByID(ctx context.Context, id uint) (*model.Supplier, error) {
	if id == 0 {
		return nil, fmt.Errorf("id is required")
	}
	var supplier model.Supplier
	if err := s.db.WithContext(ctx).First(&supplier, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("query supplier: %w", err)
	}
	return &supplier, nil
}

// List 分页查询供应商列表
func (s *SupplierService) List(ctx context.Context, page, pageSize int) ([]model.Supplier, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var total int64
	if err := s.db.WithContext(ctx).Model(&model.Supplier{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count suppliers: %w", err)
	}

	var list []model.Supplier
	offset := (page - 1) * pageSize
	if err := s.db.WithContext(ctx).
		Order("sort_order ASC, id ASC").
		Offset(offset).Limit(pageSize).
		Find(&list).Error; err != nil {
		return nil, 0, fmt.Errorf("list suppliers: %w", err)
	}
	return list, total, nil
}

// Update 根据 ID 更新供应商信息
func (s *SupplierService) Update(ctx context.Context, id uint, supplier *model.Supplier) error {
	if id == 0 {
		return fmt.Errorf("id is required")
	}
	if supplier == nil {
		return fmt.Errorf("supplier must not be nil")
	}
	var existing model.Supplier
	if err := s.db.WithContext(ctx).First(&existing, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("supplier %d not found", id)
		}
		return fmt.Errorf("query supplier: %w", err)
	}
	if err := s.db.WithContext(ctx).Model(&model.Supplier{}).Where("id = ?", existing.ID).Updates(map[string]interface{}{
		"name":        supplier.Name,
		"code":        supplier.Code,
		"base_url":    supplier.BaseURL,
		"description": supplier.Description,
		"is_active":   supplier.IsActive,
		"sort_order":  supplier.SortOrder,
	}).Error; err != nil {
		return fmt.Errorf("update supplier: %w", err)
	}
	return nil
}

// Delete 根据 ID 软删除供应商
func (s *SupplierService) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("id is required")
	}
	var existing model.Supplier
	if err := s.db.WithContext(ctx).First(&existing, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("supplier %d not found", id)
		}
		return fmt.Errorf("query supplier: %w", err)
	}
	if err := s.db.WithContext(ctx).Delete(&existing).Error; err != nil {
		return fmt.Errorf("delete supplier: %w", err)
	}
	return nil
}
