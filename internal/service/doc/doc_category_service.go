package doc

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// DocCategoryService 文档分类服务，提供分类的增删改查操作
type DocCategoryService struct {
	db *gorm.DB
}

// NewDocCategoryService 创建文档分类服务实例
func NewDocCategoryService(db *gorm.DB) *DocCategoryService {
	if db == nil {
		panic("DocCategoryService: db is nil")
	}
	return &DocCategoryService{db: db}
}

// Create 创建新的文档分类，校验名称和 slug
func (s *DocCategoryService) Create(ctx context.Context, cat *model.DocCategory) error {
	if cat == nil {
		return fmt.Errorf("category is nil")
	}
	if cat.Name == "" {
		return fmt.Errorf("category name is required")
	}
	if cat.Slug == "" {
		return fmt.Errorf("category slug is required")
	}

	if err := s.db.WithContext(ctx).Create(cat).Error; err != nil {
		return fmt.Errorf("failed to create doc category: %w", err)
	}

	logger.L.Info("doc category created", zap.Uint("id", cat.ID), zap.String("name", cat.Name))
	return nil
}

// Update 根据 ID 更新文档分类信息
func (s *DocCategoryService) Update(ctx context.Context, id uint, updates map[string]interface{}) error {
	if id == 0 {
		return fmt.Errorf("category id is required")
	}
	if len(updates) == 0 {
		return fmt.Errorf("no fields to update")
	}

	delete(updates, "id")
	delete(updates, "created_at")

	result := s.db.WithContext(ctx).Model(&model.DocCategory{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to update doc category: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("doc category not found")
	}

	logger.L.Info("doc category updated", zap.Uint("id", id))
	return nil
}

// Delete 软删除文档分类，同时解除其下文档的分类关联
func (s *DocCategoryService) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("category id is required")
	}

	// 解除该分类下旧文档的关联
	s.db.WithContext(ctx).Model(&model.Doc{}).Where("category_id = ?", id).Update("category_id", nil)
	// 解除该分类下新文档文章的关联
	s.db.WithContext(ctx).Model(&model.DocArticle{}).Where("category_id = ?", id).Update("category_id", 0)

	result := s.db.WithContext(ctx).Delete(&model.DocCategory{}, id)
	if result.Error != nil {
		return fmt.Errorf("failed to delete doc category: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("doc category not found")
	}

	logger.L.Info("doc category deleted", zap.Uint("id", id))
	return nil
}

// List 查询所有文档分类（平铺），按排序顺序返回
func (s *DocCategoryService) List(ctx context.Context) ([]model.DocCategory, error) {
	var cats []model.DocCategory
	err := s.db.WithContext(ctx).
		Order("sort_order ASC, id ASC").
		Find(&cats).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list doc categories: %w", err)
	}
	return cats, nil
}

// GetCategoryTree 获取分类树，一级分类嵌套子分类，每个分类包含已发布的文档文章列表
// 返回结构：一级分类 → Children(子分类) → Articles(文档列表)
func (s *DocCategoryService) GetCategoryTree(ctx context.Context) ([]model.DocCategory, error) {
	return s.GetCategoryTreeByLocale(ctx, "zh")
}

func (s *DocCategoryService) GetCategoryTreeByLocale(ctx context.Context, locale string) ([]model.DocCategory, error) {
	locale = NormalizeLocale(locale)
	var allCats []model.DocCategory
	// 查询所有分类，按排序字段排列
	err := s.db.WithContext(ctx).
		Order("sort_order ASC, id ASC").
		Find(&allCats).Error
	if err != nil {
		return nil, fmt.Errorf("查询分类失败: %w", err)
	}

	// 查询每个分类下已发布的文档数量和文档列表
	allArticles, err := s.publishedArticlesByLocale(ctx, locale)
	if err != nil {
		return nil, fmt.Errorf("查询文档列表失败: %w", err)
	}

	// 构建分类 ID → 文档列表映射
	articlesByCat := make(map[uint][]model.DocArticle)
	for _, a := range allArticles {
		articlesByCat[a.CategoryID] = append(articlesByCat[a.CategoryID], a)
	}

	// 构建分类 ID → 子分类映射
	childrenMap := make(map[uint][]model.DocCategory)
	var roots []model.DocCategory
	for i := range allCats {
		cat := &allCats[i]
		cat.Articles = articlesByCat[cat.ID]
		if cat.ParentID != nil && *cat.ParentID > 0 {
			childrenMap[*cat.ParentID] = append(childrenMap[*cat.ParentID], *cat)
		} else {
			roots = append(roots, *cat)
		}
	}

	// 将子分类挂载到父分类下
	for i := range roots {
		roots[i].Children = childrenMap[roots[i].ID]
		// 子分类也需要挂载文档
		for j := range roots[i].Children {
			roots[i].Children[j].Articles = articlesByCat[roots[i].Children[j].ID]
		}
	}

	if locale != "zh" {
		localizeDocCategories(roots, locale)
	}

	return prunePublicDocCategories(roots), nil
}

func prunePublicDocCategories(cats []model.DocCategory) []model.DocCategory {
	if len(cats) == 0 {
		return cats
	}
	out := make([]model.DocCategory, 0, len(cats))
	for _, cat := range cats {
		if isPlaceholderDocCategory(cat) {
			continue
		}
		cat.Children = prunePublicDocCategories(cat.Children)
		if len(cat.Articles) == 0 && len(cat.Children) == 0 {
			continue
		}
		out = append(out, cat)
	}
	return out
}

func isPlaceholderDocCategory(cat model.DocCategory) bool {
	slug := strings.TrimSpace(strings.ToLower(cat.Slug))
	name := strings.TrimSpace(strings.ToLower(cat.Name))
	return strings.HasPrefix(slug, "cat_") || strings.HasPrefix(name, "category cat_")
}

type localizedDocCategory struct {
	Name        string
	Description string
}

var localizedDocCategoryText = map[string]map[string]localizedDocCategory{
	"en": {
		"getting-started":    {Name: "Getting Started", Description: "Register, create an API key, and make your first request"},
		"account-billing":    {Name: "Account and Billing", Description: "Account security, API keys, top-ups, balance, and bills"},
		"models-pricing":     {Name: "Models and Pricing", Description: "Browse models, choose the right model, and understand billing"},
		"playground":         {Name: "Playground", Description: "Debug models in the browser and reuse request payloads"},
		"api-usage":          {Name: "API Usage", Description: "Use OpenAI-compatible APIs through TokenHub"},
		"client-integration": {Name: "Client Integrations", Description: "Configure TokenHub in common chat and developer clients"},
		"help":               {Name: "Help", Description: "Troubleshoot authentication, balance, model, and response issues"},
	},
}

func localizeDocCategories(cats []model.DocCategory, locale string) {
	translations := localizedDocCategoryText[NormalizeLocale(locale)]
	if len(translations) == 0 {
		return
	}
	for i := range cats {
		if text, ok := translations[cats[i].Slug]; ok {
			cats[i].Name = text.Name
			cats[i].Description = text.Description
		}
		localizeDocCategories(cats[i].Children, locale)
	}
}

func (s *DocCategoryService) publishedArticlesByLocale(ctx context.Context, locale string) ([]model.DocArticle, error) {
	var articles []model.DocArticle
	err := s.db.WithContext(ctx).
		Where("is_published = ? AND locale = ?", true, locale).
		Order("sort_order ASC, id ASC").
		Find(&articles).Error
	if err != nil {
		return nil, err
	}
	if len(articles) > 0 || locale == "zh" {
		return articles, nil
	}
	err = s.db.WithContext(ctx).
		Where("is_published = ? AND locale = ?", true, "zh").
		Order("sort_order ASC, id ASC").
		Find(&articles).Error
	return articles, err
}

// GetArticlesByCategorySlug 根据分类 slug 查询该分类下所有已发布文档
func (s *DocCategoryService) GetArticlesByCategorySlug(ctx context.Context, slug string) ([]model.DocArticle, error) {
	return s.GetArticlesByCategorySlugAndLocale(ctx, slug, "zh")
}

func (s *DocCategoryService) GetArticlesByCategorySlugAndLocale(ctx context.Context, slug, locale string) ([]model.DocArticle, error) {
	if slug == "" {
		return nil, fmt.Errorf("category slug is required")
	}
	locale = NormalizeLocale(locale)

	// 先查找分类
	var cat model.DocCategory
	if err := s.db.WithContext(ctx).Where("slug = ?", slug).First(&cat).Error; err != nil {
		return nil, fmt.Errorf("分类不存在: %w", err)
	}

	// 收集该分类及其子分类的 ID
	catIDs := []uint{cat.ID}
	var children []model.DocCategory
	if err := s.db.WithContext(ctx).Where("parent_id = ?", cat.ID).Find(&children).Error; err == nil {
		for _, child := range children {
			catIDs = append(catIDs, child.ID)
		}
	}

	// 查询这些分类下的已发布文档
	var articles []model.DocArticle
	err := s.db.WithContext(ctx).
		Where("category_id IN ? AND is_published = ? AND locale = ?", catIDs, true, locale).
		Order("sort_order ASC, id ASC").
		Find(&articles).Error
	if err != nil {
		return nil, fmt.Errorf("查询文档失败: %w", err)
	}
	if len(articles) == 0 && locale != "zh" {
		err = s.db.WithContext(ctx).
			Where("category_id IN ? AND is_published = ? AND locale = ?", catIDs, true, "zh").
			Order("sort_order ASC, id ASC").
			Find(&articles).Error
		if err != nil {
			return nil, fmt.Errorf("查询文档失败: %w", err)
		}
	}

	return articles, nil
}

// GetWithDocs 查询分类并预加载其下的文档列表（旧接口兼容）
func (s *DocCategoryService) GetWithDocs(ctx context.Context, id uint) (*model.DocCategory, error) {
	if id == 0 {
		return nil, fmt.Errorf("category id is required")
	}

	var cat model.DocCategory
	err := s.db.WithContext(ctx).
		Preload("Docs", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order ASC, created_at DESC")
		}).
		First(&cat, id).Error
	if err != nil {
		return nil, fmt.Errorf("doc category not found: %w", err)
	}
	return &cat, nil
}
