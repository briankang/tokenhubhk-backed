package modelcategory

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// ModelCategoryService 模型分类服务，处理模型分类的增删改查操作
type ModelCategoryService struct {
	db *gorm.DB
}

// NewModelCategoryService 创建模型分类服务实例
func NewModelCategoryService(db *gorm.DB) *ModelCategoryService {
	if db == nil {
		panic("model category service: db is nil")
	}
	return &ModelCategoryService{db: db}
}

// Create 创建新的模型分类，校验名称、编码和供应商 ID
func (s *ModelCategoryService) Create(ctx context.Context, cat *model.ModelCategory) error {
	if cat == nil {
		return fmt.Errorf("model category is nil")
	}
	if cat.Name == "" {
		return fmt.Errorf("category name is required")
	}
	if cat.Code == "" {
		return fmt.Errorf("category code is required")
	}
	if cat.SupplierID == 0 {
		return fmt.Errorf("supplier id is required")
	}
	return s.db.WithContext(ctx).Create(cat).Error
}

// GetByID 根据 ID 查询模型分类，预加载供应商信息
func (s *ModelCategoryService) GetByID(ctx context.Context, id uint) (*model.ModelCategory, error) {
	if id == 0 {
		return nil, fmt.Errorf("category id is required")
	}
	var cat model.ModelCategory
	if err := s.db.WithContext(ctx).Preload("Supplier").First(&cat, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("category not found")
		}
		return nil, fmt.Errorf("failed to get category: %w", err)
	}
	return &cat, nil
}

// List 分页查询模型分类列表
func (s *ModelCategoryService) List(ctx context.Context, page, pageSize int) ([]model.ModelCategory, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	query := s.db.WithContext(ctx).Model(&model.ModelCategory{})
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count categories: %w", err)
	}
	var cats []model.ModelCategory
	offset := (page - 1) * pageSize
	if err := query.Preload("Supplier").Offset(offset).Limit(pageSize).Order("sort_order ASC, id DESC").Find(&cats).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list categories: %w", err)
	}
	return cats, total, nil
}

// Update 根据 ID 更新模型分类信息
func (s *ModelCategoryService) Update(ctx context.Context, id uint, updates map[string]interface{}) error {
	if id == 0 {
		return fmt.Errorf("category id is required")
	}
	delete(updates, "id")
	res := s.db.WithContext(ctx).Model(&model.ModelCategory{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("failed to update category: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("category not found")
	}
	return nil
}

// Delete 根据 ID 软删除模型分类
func (s *ModelCategoryService) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("category id is required")
	}
	res := s.db.WithContext(ctx).Delete(&model.ModelCategory{}, id)
	if res.Error != nil {
		return fmt.Errorf("failed to delete category: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("category not found")
	}
	return nil
}
