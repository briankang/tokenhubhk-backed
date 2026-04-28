package database

import (
	"testing"

	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

func TestRunSeedCustomParamDocsUnpublishesLegacyPage(t *testing.T) {
	logger.L = zap.NewNop()

	db, err := gorm.Open(sqlite.Open("file:custom_param_docs?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.DocCategory{}, &model.DocArticle{}); err != nil {
		t.Fatalf("migrate docs: %v", err)
	}

	oldCat := model.DocCategory{Name: "API Reference", Slug: "api-reference", Icon: "terminal", SortOrder: 60}
	if err := db.Create(&oldCat).Error; err != nil {
		t.Fatalf("seed category: %v", err)
	}
	if err := db.Create(&model.DocArticle{
		CategoryID:  oldCat.ID,
		Title:       "Legacy passthrough",
		Slug:        customParamsPassthroughSlug,
		Content:     "legacy custom params page",
		IsPublished: true,
	}).Error; err != nil {
		t.Fatalf("seed legacy article: %v", err)
	}

	RunSeedCustomParamDocs(db)
	RunSeedCustomParamDocs(db)

	var legacy model.DocArticle
	if err := db.Where("slug = ?", customParamsPassthroughSlug).First(&legacy).Error; err != nil {
		t.Fatalf("find legacy article: %v", err)
	}
	if legacy.IsPublished {
		t.Fatal("legacy custom params passthrough page should be unpublished")
	}

	var count int64
	if err := db.Model(&model.DocArticle{}).
		Where("slug = ? AND is_published = ?", customParamsPassthroughSlug, true).
		Count(&count).Error; err != nil {
		t.Fatalf("count legacy article: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no published legacy article, got %d", count)
	}
}
