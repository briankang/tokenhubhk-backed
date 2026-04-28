package database

import (
	"strings"
	"testing"

	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

func TestRunSeedDocsRegeneratesUserFacingDocs(t *testing.T) {
	logger.L = zap.NewNop()

	db, err := gorm.Open(sqlite.Open("file:user_docs_seed?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.DocCategory{}, &model.DocArticle{}); err != nil {
		t.Fatalf("migrate docs: %v", err)
	}

	oldCat := model.DocCategory{Name: "Coding Plan", Slug: "coding-plan", Icon: "code", SortOrder: 40}
	if err := db.Create(&oldCat).Error; err != nil {
		t.Fatalf("create old category: %v", err)
	}
	if err := db.Create(&model.DocArticle{
		CategoryID:  oldCat.ID,
		Title:       "代理商分销指南",
		Slug:        "agent-distribution",
		Content:     "代理商和 Coding Plan 的旧文档",
		IsPublished: true,
	}).Error; err != nil {
		t.Fatalf("create old article: %v", err)
	}

	RunSeedDocs(db)
	RunSeedDocs(db)

	var categories []model.DocCategory
	if err := db.Order("sort_order ASC").Find(&categories).Error; err != nil {
		t.Fatalf("list categories: %v", err)
	}
	if len(categories) != len(userDocCategories) {
		t.Fatalf("expected %d categories, got %d", len(userDocCategories), len(categories))
	}

	for _, cat := range categories {
		if strings.Contains(strings.ToLower(cat.Slug), "coding") || strings.Contains(cat.Name, "代理") {
			t.Fatalf("unsupported category still visible: %#v", cat)
		}
	}

	var articles []model.DocArticle
	if err := db.Where("is_published = ?", true).Find(&articles).Error; err != nil {
		t.Fatalf("list articles: %v", err)
	}
	if len(articles) != len(userDocArticlesAll) {
		t.Fatalf("expected %d published articles, got %d", len(userDocArticlesAll), len(articles))
	}

	for _, article := range articles {
		content := article.Title + article.Summary + article.Content
		if strings.Contains(content, "Coding Plan") || strings.Contains(content, "代理商") || strings.Contains(content, "分销") ||
			strings.Contains(content, "供应商") || strings.Contains(content, "上游") || strings.Contains(content, "联系管理员") ||
			strings.Contains(content, "provider_extra") {
			t.Fatalf("unsupported content found in article %s", article.Slug)
		}
	}

	var oldPublished int64
	if err := db.Model(&model.DocArticle{}).
		Where("slug = ? AND is_published = ?", "agent-distribution", true).
		Count(&oldPublished).Error; err != nil {
		t.Fatalf("count old article: %v", err)
	}
	if oldPublished != 0 {
		t.Fatalf("old agent article should be unpublished, count=%d", oldPublished)
	}

	var quickStart model.DocArticle
	if err := db.Where("slug = ? AND locale = ?", "quick-start", "zh").First(&quickStart).Error; err != nil {
		t.Fatalf("find quick start: %v", err)
	}
	for _, needle := range []string{"注册账号", "创建 API Key", "Playground", "/v1/chat/completions"} {
		if !strings.Contains(quickStart.Content, needle) {
			t.Fatalf("quick start missing %q", needle)
		}
	}

	var chooseModels model.DocArticle
	if err := db.Where("slug = ? AND locale = ?", "choose-models", "zh").First(&chooseModels).Error; err != nil {
		t.Fatalf("find choose models: %v", err)
	}
	for _, needle := range []string{"[模型市场](/models)", "复制页面展示的模型 ID", "[模型 API 文档](/docs/api-models)"} {
		if !strings.Contains(chooseModels.Content, needle) {
			t.Fatalf("choose models missing %q", needle)
		}
	}

	var chooseModelsEN model.DocArticle
	if err := db.Where("slug = ? AND locale = ?", "choose-models", "en").First(&chooseModelsEN).Error; err != nil {
		t.Fatalf("find english choose models: %v", err)
	}
	for _, needle := range []string{"[model market](/models)", "Copy the model ID", "[Model API Docs](/docs/api-models)"} {
		if !strings.Contains(chooseModelsEN.Content, needle) {
			t.Fatalf("english choose models missing %q", needle)
		}
	}

	var modelsAPIEN model.DocArticle
	if err := db.Where("slug = ? AND locale = ?", "models-api", "en").First(&modelsAPIEN).Error; err != nil {
		t.Fatalf("find english models api: %v", err)
	}
	for _, needle := range []string{"List Models", "/docs/api-models", "dedicated API document"} {
		if !strings.Contains(modelsAPIEN.Content, needle) {
			t.Fatalf("english models api missing %q", needle)
		}
	}
}
