package doc

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// DocArticleService 文档文章服务，提供文章的增删改查和搜索
type DocArticleService struct {
	db *gorm.DB
}

// NewDocArticleService 创建文档文章服务实例
func NewDocArticleService(db *gorm.DB) *DocArticleService {
	if db == nil {
		panic("DocArticleService: db is nil")
	}
	return &DocArticleService{db: db}
}

// Create 创建新的文档文章，校验标题和 slug
func (s *DocArticleService) Create(ctx context.Context, article *model.DocArticle) error {
	if article == nil {
		return fmt.Errorf("article is nil")
	}
	if article.Title == "" {
		return fmt.Errorf("article title is required")
	}
	if article.Slug == "" {
		return fmt.Errorf("article slug is required")
	}

	if err := s.db.WithContext(ctx).Create(article).Error; err != nil {
		return fmt.Errorf("创建文档失败: %w", err)
	}

	logger.L.Info("文档文章已创建", zap.Uint("id", article.ID), zap.String("slug", article.Slug))
	return nil
}

// Update 根据 ID 更新文档文章
func (s *DocArticleService) Update(ctx context.Context, id uint, updates map[string]interface{}) error {
	if id == 0 {
		return fmt.Errorf("article id is required")
	}
	if len(updates) == 0 {
		return fmt.Errorf("no fields to update")
	}

	// 安全过滤不可更新字段
	delete(updates, "id")
	delete(updates, "created_at")

	result := s.db.WithContext(ctx).Model(&model.DocArticle{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("更新文档失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("文档不存在")
	}

	logger.L.Info("文档文章已更新", zap.Uint("id", id))
	return nil
}

// GetBySlug 根据 slug 查询已发布的文档文章，预加载分类信息
func (s *DocArticleService) GetBySlug(ctx context.Context, slug string) (*model.DocArticle, error) {
	if slug == "" {
		return nil, fmt.Errorf("slug is required")
	}

	var article model.DocArticle
	err := s.db.WithContext(ctx).
		Preload("Category").
		Where("slug = ? AND is_published = ?", slug, true).
		First(&article).Error
	if err != nil {
		return nil, fmt.Errorf("文档不存在: %w", err)
	}
	return &article, nil
}

// Search 关键词搜索文档标题和内容（MySQL LIKE 模糊匹配）
// 搜索范围：标题、内容、摘要、标签
func (s *DocArticleService) Search(ctx context.Context, keyword string, page, pageSize int) ([]model.DocArticle, int64, error) {
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
	query := s.db.WithContext(ctx).Model(&model.DocArticle{}).
		Where("is_published = ?", true).
		Where("title LIKE ? OR content LIKE ? OR summary LIKE ? OR tags LIKE ?", like, like, like, like)

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("搜索计数失败: %w", err)
	}

	var articles []model.DocArticle
	err := query.
		Preload("Category").
		Order("sort_order ASC, id ASC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&articles).Error
	if err != nil {
		return nil, 0, fmt.Errorf("搜索文档失败: %w", err)
	}

	return articles, total, nil
}

// List 分页查询文档文章列表
func (s *DocArticleService) List(ctx context.Context, categoryID *uint, published *bool, page, pageSize int) ([]model.DocArticle, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	query := s.db.WithContext(ctx).Model(&model.DocArticle{})
	if categoryID != nil {
		query = query.Where("category_id = ?", *categoryID)
	}
	if published != nil {
		query = query.Where("is_published = ?", *published)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("查询文档计数失败: %w", err)
	}

	var articles []model.DocArticle
	err := query.
		Preload("Category").
		Order("sort_order ASC, id ASC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&articles).Error
	if err != nil {
		return nil, 0, fmt.Errorf("查询文档列表失败: %w", err)
	}

	return articles, total, nil
}
