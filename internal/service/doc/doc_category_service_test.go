package doc

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestGetCategoryTreeHidesEmptyPlaceholderCategories(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:doc_category_tree?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.DocCategory{}, &model.DocArticle{}); err != nil {
		t.Fatalf("migrate docs: %v", err)
	}

	placeholder := model.DocCategory{Name: "Category cat_1777000000000", Slug: "cat_1777000000000", SortOrder: 1}
	emptyNormal := model.DocCategory{Name: "Empty Normal", Slug: "empty-normal", SortOrder: 2}
	apiRef := model.DocCategory{Name: "API 参考", Slug: "api-reference", SortOrder: 3}
	if err := db.Create(&[]model.DocCategory{placeholder, emptyNormal, apiRef}).Error; err != nil {
		t.Fatalf("seed categories: %v", err)
	}
	if err := db.Where("slug = ?", "api-reference").First(&apiRef).Error; err != nil {
		t.Fatalf("reload api category: %v", err)
	}
	if err := db.Create(&model.DocArticle{
		CategoryID:  apiRef.ID,
		Title:       "Chat API",
		Slug:        "chat-api",
		Content:     "ok",
		IsPublished: true,
	}).Error; err != nil {
		t.Fatalf("seed article: %v", err)
	}

	tree, err := NewDocCategoryService(db).GetCategoryTree(context.Background())
	if err != nil {
		t.Fatalf("get tree: %v", err)
	}
	if len(tree) != 1 {
		t.Fatalf("expected only category with published article, got %#v", tree)
	}
	if tree[0].Slug != "api-reference" {
		t.Fatalf("unexpected category returned: %#v", tree[0])
	}
}

func TestGetCategoryTreeByLocaleFallsBackToChinese(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:doc_category_locale?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.DocCategory{}, &model.DocArticle{}); err != nil {
		t.Fatalf("migrate docs: %v", err)
	}
	cat := model.DocCategory{Name: "API", Slug: "api-reference", SortOrder: 1}
	if err := db.Create(&cat).Error; err != nil {
		t.Fatalf("seed category: %v", err)
	}
	if err := db.Create(&model.DocArticle{
		CategoryID:  cat.ID,
		Title:       "中文文档",
		Slug:        "zh-doc",
		Content:     "ok",
		Locale:      "zh",
		IsPublished: true,
	}).Error; err != nil {
		t.Fatalf("seed article: %v", err)
	}

	tree, err := NewDocCategoryService(db).GetCategoryTreeByLocale(context.Background(), "en-US")
	if err != nil {
		t.Fatalf("get tree: %v", err)
	}
	if len(tree) != 1 || len(tree[0].Articles) != 1 {
		t.Fatalf("expected zh fallback article, got %#v", tree)
	}
	if tree[0].Articles[0].Locale != "zh" {
		t.Fatalf("expected zh fallback locale, got %q", tree[0].Articles[0].Locale)
	}
}

func TestGetCategoryTreeByLocaleLocalizesCategoryLabels(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:doc_category_label_locale?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.DocCategory{}, &model.DocArticle{}); err != nil {
		t.Fatalf("migrate docs: %v", err)
	}
	cat := model.DocCategory{Name: "快速开始", Slug: "getting-started", SortOrder: 1}
	if err := db.Create(&cat).Error; err != nil {
		t.Fatalf("seed category: %v", err)
	}
	if err := db.Create(&model.DocArticle{
		CategoryID:  cat.ID,
		Title:       "Quick Start",
		Slug:        "quick-start",
		Content:     "ok",
		Locale:      "en",
		IsPublished: true,
	}).Error; err != nil {
		t.Fatalf("seed article: %v", err)
	}

	tree, err := NewDocCategoryService(db).GetCategoryTreeByLocale(context.Background(), "en-US")
	if err != nil {
		t.Fatalf("get tree: %v", err)
	}
	if len(tree) != 1 || tree[0].Name != "Getting Started" {
		t.Fatalf("expected localized category label, got %#v", tree)
	}
}
