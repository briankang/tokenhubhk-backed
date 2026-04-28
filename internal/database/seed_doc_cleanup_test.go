package database

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestRunCleanPlaceholderDocCategoriesDeletesOnlyEmptyGeneratedRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:doc_cleanup?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.DocCategory{}, &model.DocArticle{}, &model.Doc{}); err != nil {
		t.Fatalf("migrate docs: %v", err)
	}

	emptyPlaceholder := model.DocCategory{Name: "Category cat_1777000000000", Slug: "cat_1777000000000"}
	usedPlaceholder := model.DocCategory{Name: "Category cat_1777000000001", Slug: "cat_1777000000001"}
	normal := model.DocCategory{Name: "API 参考", Slug: "api-reference"}
	if err := db.Create(&emptyPlaceholder).Error; err != nil {
		t.Fatalf("create empty placeholder: %v", err)
	}
	if err := db.Create(&usedPlaceholder).Error; err != nil {
		t.Fatalf("create used placeholder: %v", err)
	}
	if err := db.Create(&normal).Error; err != nil {
		t.Fatalf("create normal: %v", err)
	}
	if err := db.Create(&model.DocArticle{
		CategoryID:  usedPlaceholder.ID,
		Title:       "keep me",
		Slug:        "keep-me",
		IsPublished: true,
	}).Error; err != nil {
		t.Fatalf("create article: %v", err)
	}

	RunCleanPlaceholderDocCategories(db)

	var count int64
	db.Model(&model.DocCategory{}).Where("slug = ?", emptyPlaceholder.Slug).Count(&count)
	if count != 0 {
		t.Fatalf("empty placeholder should be deleted, count=%d", count)
	}
	db.Model(&model.DocCategory{}).Where("slug = ?", usedPlaceholder.Slug).Count(&count)
	if count != 1 {
		t.Fatalf("used placeholder should be preserved, count=%d", count)
	}
	db.Model(&model.DocCategory{}).Where("slug = ?", normal.Slug).Count(&count)
	if count != 1 {
		t.Fatalf("normal category should be preserved, count=%d", count)
	}
}
