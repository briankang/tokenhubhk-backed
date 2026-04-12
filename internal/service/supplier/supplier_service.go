package supplier

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// SupplierService handles supplier CRUD operations.
// 供应商服务，支持按接入点类型过滤
type SupplierService struct {
	db *gorm.DB
}

// NewSupplierService creates a new SupplierService instance.
func NewSupplierService(db *gorm.DB) *SupplierService {
	if db == nil {
		panic("supplier service: db is nil")
	}
	return &SupplierService{db: db}
}

// Create creates a new supplier.
// 创建供应商，检查 code + access_type 联合唯一性
func (s *SupplierService) Create(ctx context.Context, supplier *model.Supplier) error {
	if supplier == nil {
		return fmt.Errorf("supplier is nil")
	}
	if supplier.Name == "" {
		return fmt.Errorf("supplier name is required")
	}
	if supplier.Code == "" {
		return fmt.Errorf("supplier code is required")
	}
	// 设置默认值
	if supplier.AccessType == "" {
		supplier.AccessType = "api"
	}
	if supplier.Discount == 0 {
		supplier.Discount = 1.0
	}
	if supplier.Status == "" {
		supplier.Status = "active"
	}
	return s.db.WithContext(ctx).Create(supplier).Error
}

// GetByID returns a supplier by ID.
// 根据ID获取供应商
func (s *SupplierService) GetByID(ctx context.Context, id uint) (*model.Supplier, error) {
	if id == 0 {
		return nil, fmt.Errorf("supplier id is required")
	}
	var supplier model.Supplier
	if err := s.db.WithContext(ctx).First(&supplier, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("supplier not found")
		}
		return nil, fmt.Errorf("failed to get supplier: %w", err)
	}
	return &supplier, nil
}

// List returns a paginated list of suppliers.
// 支持按 accessType 过滤（api / coding_plan）
func (s *SupplierService) List(ctx context.Context, page, pageSize int, accessType string) ([]model.Supplier, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	query := s.db.WithContext(ctx).Model(&model.Supplier{})
	// 按 access_type 过滤
	if accessType != "" {
		query = query.Where("access_type = ?", accessType)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count suppliers: %w", err)
	}
	var suppliers []model.Supplier
	offset := (page - 1) * pageSize
	if err := query.Offset(offset).Limit(pageSize).Order("sort_order ASC, id DESC").Find(&suppliers).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list suppliers: %w", err)
	}
	return suppliers, total, nil
}

// GetByCodeAndType 根据 code 和 accessType 获取供应商
// 用于精确查找特定接入点类型的供应商记录
func (s *SupplierService) GetByCodeAndType(ctx context.Context, code, accessType string) (*model.Supplier, error) {
	if code == "" {
		return nil, fmt.Errorf("supplier code is required")
	}
	var supplier model.Supplier
	query := s.db.WithContext(ctx).Where("code = ?", code)
	if accessType != "" {
		query = query.Where("access_type = ?", accessType)
	}
	if err := query.First(&supplier).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("supplier not found")
		}
		return nil, fmt.Errorf("failed to get supplier: %w", err)
	}
	return &supplier, nil
}

// Update updates a supplier by ID.
// 更新供应商信息
func (s *SupplierService) Update(ctx context.Context, id uint, updates map[string]interface{}) error {
	if id == 0 {
		return fmt.Errorf("supplier id is required")
	}
	delete(updates, "id")
	res := s.db.WithContext(ctx).Model(&model.Supplier{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("failed to update supplier: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("supplier not found")
	}
	return nil
}

// Delete soft-deletes a supplier by ID.
// 删除供应商（软删除）
func (s *SupplierService) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("supplier id is required")
	}
	res := s.db.WithContext(ctx).Delete(&model.Supplier{}, id)
	if res.Error != nil {
		return fmt.Errorf("failed to delete supplier: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("supplier not found")
	}
	return nil
}
