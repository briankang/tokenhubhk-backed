package announcement

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestAnnouncementServicePersistsEnglishCopy(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Announcement{}); err != nil {
		t.Fatal(err)
	}

	svc := NewService(db)
	ann, err := svc.Create(context.Background(), CreateRequest{
		Title:      "中文标题",
		TitleEn:    "English title",
		Content:    "中文内容",
		ContentEn:  "English content",
		Type:       "info",
		Priority:   "normal",
		Status:     "active",
		ShowBanner: true,
	}, 7)
	if err != nil {
		t.Fatal(err)
	}

	var stored model.Announcement
	if err := db.First(&stored, ann.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.TitleEn != "English title" {
		t.Fatalf("TitleEn=%q, want English title", stored.TitleEn)
	}
	if stored.ContentEn != "English content" {
		t.Fatalf("ContentEn=%q, want English content", stored.ContentEn)
	}
}
