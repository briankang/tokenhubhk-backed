package doc

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// DocService 文档服务，提供文档页面的增删改查操作
type DocService struct {
	db *gorm.DB
}

// NewDocService 创建文档服务实例
func NewDocService(db *gorm.DB) *DocService {
	if db == nil {
		panic("DocService: db is nil")
	}
	return &DocService{db: db}
}

// Create 创建新的文档记录，校验标题和 slug
func (s *DocService) Create(ctx context.Context, doc *model.Doc) error {
	if doc == nil {
		return fmt.Errorf("doc is nil")
	}
	if doc.Title == "" {
		return fmt.Errorf("doc title is required")
	}
	if doc.Slug == "" {
		return fmt.Errorf("doc slug is required")
	}

	if err := s.db.WithContext(ctx).Create(doc).Error; err != nil {
		return fmt.Errorf("failed to create doc: %w", err)
	}

	logger.L.Info("doc created", zap.Uint("id", doc.ID), zap.String("slug", doc.Slug))
	return nil
}

// Update 根据 ID 更新文档信息
func (s *DocService) Update(ctx context.Context, id uint, updates map[string]interface{}) error {
	if id == 0 {
		return fmt.Errorf("doc id is required")
	}
	if len(updates) == 0 {
		return fmt.Errorf("no fields to update")
	}

	delete(updates, "id")
	delete(updates, "created_at")

	result := s.db.WithContext(ctx).Model(&model.Doc{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to update doc: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("doc not found")
	}

	logger.L.Info("doc updated", zap.Uint("id", id))
	return nil
}

// Delete 根据 ID 软删除文档
func (s *DocService) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("doc id is required")
	}

	result := s.db.WithContext(ctx).Delete(&model.Doc{}, id)
	if result.Error != nil {
		return fmt.Errorf("failed to delete doc: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("doc not found")
	}

	logger.L.Info("doc deleted", zap.Uint("id", id))
	return nil
}

// GetByID 根据 ID 查询文档，预加载分类信息
func (s *DocService) GetByID(ctx context.Context, id uint) (*model.Doc, error) {
	if id == 0 {
		return nil, fmt.Errorf("doc id is required")
	}

	var doc model.Doc
	err := s.db.WithContext(ctx).Preload("Category").First(&doc, id).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get doc: %w", err)
	}
	return &doc, nil
}

// GetBySlug 根据 slug 查询已发布的文档
func (s *DocService) GetBySlug(ctx context.Context, slug string) (*model.Doc, error) {
	if slug == "" {
		return nil, fmt.Errorf("slug is required")
	}

	var doc model.Doc
	err := s.db.WithContext(ctx).Preload("Category").
		Where("slug = ? AND is_published = ?", slug, true).
		First(&doc).Error
	if err != nil {
		return nil, fmt.Errorf("doc not found: %w", err)
	}
	return &doc, nil
}

// List 分页查询文档列表，可按分类和发布状态过滤
func (s *DocService) List(ctx context.Context, categoryID *uint, published *bool, page, pageSize int) ([]model.Doc, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	query := s.db.WithContext(ctx).Model(&model.Doc{})

	if categoryID != nil {
		query = query.Where("category_id = ?", *categoryID)
	}
	if published != nil {
		query = query.Where("is_published = ?", *published)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count docs: %w", err)
	}

	var docs []model.Doc
	err := query.
		Preload("Category").
		Order("sort_order ASC, created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&docs).Error
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list docs: %w", err)
	}

	return docs, total, nil
}

// Search 关键词搜索文档标题和内容（LIKE 模糊匹配）
func (s *DocService) Search(ctx context.Context, keyword string, page, pageSize int) ([]model.Doc, int64, error) {
	if keyword == "" {
		return nil, 0, fmt.Errorf("keyword is required")
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	like := fmt.Sprintf("%%%s%%", keyword)
	query := s.db.WithContext(ctx).Model(&model.Doc{}).
		Where("is_published = ?", true).
		Where("title LIKE ? OR content LIKE ?", like, like)

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count search results: %w", err)
	}

	var docs []model.Doc
	err := query.
		Preload("Category").
		Order("sort_order ASC, created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&docs).Error
	if err != nil {
		return nil, 0, fmt.Errorf("failed to search docs: %w", err)
	}

	return docs, total, nil
}

// Publish 发布文档，将 is_published 设为 true
func (s *DocService) Publish(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("doc id is required")
	}

	result := s.db.WithContext(ctx).Model(&model.Doc{}).Where("id = ?", id).Update("is_published", true)
	if result.Error != nil {
		return fmt.Errorf("failed to publish doc: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("doc not found")
	}

	logger.L.Info("doc published", zap.Uint("id", id))
	return nil
}

// Unpublish 取消发布文档，将 is_published 设为 false
func (s *DocService) Unpublish(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("doc id is required")
	}

	result := s.db.WithContext(ctx).Model(&model.Doc{}).Where("id = ?", id).Update("is_published", false)
	if result.Error != nil {
		return fmt.Errorf("failed to unpublish doc: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("doc not found")
	}

	logger.L.Info("doc unpublished", zap.Uint("id", id))
	return nil
}
