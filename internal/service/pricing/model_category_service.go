package pricing

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// ModelCategoryService 模型分类服务，提供模型分类的CRUD操作
type ModelCategoryService struct {
	db *gorm.DB
}

// NewModelCategoryService 创建模型分类服务实例，db不允许为nil
func NewModelCategoryService(db *gorm.DB) *ModelCategoryService {
	if db == nil {
		panic("ModelCategoryService: db must not be nil")
	}
	return &ModelCategoryService{db: db}
}

// Create 新建模型分类，校验名称、编码和供应商ID必填
func (s *ModelCategoryService) Create(ctx context.Context, category *model.ModelCategory) error {
	if category == nil {
		return fmt.Errorf("category must not be nil")
	}
	if category.Name == "" {
		return fmt.Errorf("name is required")
	}
	if category.Code == "" {
		return fmt.Errorf("code is required")
	}
	if category.SupplierID == 0 {
		return fmt.Errorf("supplier_id is required")
	}
	if err := s.db.WithContext(ctx).Create(category).Error; err != nil {
		return fmt.Errorf("create model category: %w", err)
	}
	return nil
}

// GetByID 根据ID查询模型分类，不存在返回nil
func (s *ModelCategoryService) GetByID(ctx context.Context, id uint) (*model.ModelCategory, error) {
	if id == 0 {
		return nil, fmt.Errorf("id is required")
	}
	var category model.ModelCategory
	if err := s.db.WithContext(ctx).Preload("Supplier").First(&category, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("query model category: %w", err)
	}
	return &category, nil
}

// List 分页获取模型分类列表，按排序权重和ID升序
func (s *ModelCategoryService) List(ctx context.Context, page, pageSize int) ([]model.ModelCategory, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var total int64
	if err := s.db.WithContext(ctx).Model(&model.ModelCategory{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count model categories: %w", err)
	}

	var list []model.ModelCategory
	offset := (page - 1) * pageSize
	if err := s.db.WithContext(ctx).
		Preload("Supplier").
		Order("sort_order ASC, id ASC").
		Offset(offset).Limit(pageSize).
		Find(&list).Error; err != nil {
		return nil, 0, fmt.Errorf("list model categories: %w", err)
	}
	return list, total, nil
}

// ListBySupplier 获取指定供应商下的所有模型分类
func (s *ModelCategoryService) ListBySupplier(ctx context.Context, supplierID uint) ([]model.ModelCategory, error) {
	if supplierID == 0 {
		return nil, fmt.Errorf("supplier_id is required")
	}
	var list []model.ModelCategory
	if err := s.db.WithContext(ctx).
		Preload("Supplier").
		Where("supplier_id = ?", supplierID).
		Order("sort_order ASC, id ASC").
		Find(&list).Error; err != nil {
		return nil, fmt.Errorf("list categories by supplier: %w", err)
	}
	return list, nil
}

// Update 根据ID更新模型分类信息
func (s *ModelCategoryService) Update(ctx context.Context, id uint, category *model.ModelCategory) error {
	if id == 0 {
		return fmt.Errorf("id is required")
	}
	if category == nil {
		return fmt.Errorf("category must not be nil")
	}
	var existing model.ModelCategory
	if err := s.db.WithContext(ctx).First(&existing, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("model category %d not found", id)
		}
		return fmt.Errorf("query model category: %w", err)
	}
	if err := s.db.WithContext(ctx).Model(&model.ModelCategory{}).Where("id = ?", existing.ID).Updates(map[string]interface{}{
		"supplier_id": category.SupplierID,
		"name":        category.Name,
		"code":        category.Code,
		"description": category.Description,
		"sort_order":  category.SortOrder,
	}).Error; err != nil {
		return fmt.Errorf("update model category: %w", err)
	}
	return nil
}

// Delete 根据ID软删除模型分类记录
func (s *ModelCategoryService) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("id is required")
	}
	var existing model.ModelCategory
	if err := s.db.WithContext(ctx).First(&existing, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("model category %d not found", id)
		}
		return fmt.Errorf("query model category: %w", err)
	}
	if err := s.db.WithContext(ctx).Delete(&existing).Error; err != nil {
		return fmt.Errorf("delete model category: %w", err)
	}
	return nil
}
