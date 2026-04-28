package billing

import (
	"context"
	"sync"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestLoadBillableModelUsesLocalCache(t *testing.T) {
	billableModelCache = sync.Map{}
	t.Cleanup(func() {
		billableModelCache = sync.Map{}
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.AIModel{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Create(&model.AIModel{ModelName: "mock-chat", IsActive: true, Status: "online"}).Error; err != nil {
		t.Fatalf("create model: %v", err)
	}

	svc := NewService(db, nil, nil)
	first, err := svc.loadBillableModel(context.Background(), "mock-chat")
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if first.ModelName != "mock-chat" {
		t.Fatalf("unexpected model: %+v", first)
	}

	if err := db.Where("model_name = ?", "mock-chat").Delete(&model.AIModel{}).Error; err != nil {
		t.Fatalf("delete model: %v", err)
	}
	second, err := svc.loadBillableModel(context.Background(), "mock-chat")
	if err != nil {
		t.Fatalf("second load should use cache: %v", err)
	}
	if second.ModelName != "mock-chat" {
		t.Fatalf("unexpected cached model: %+v", second)
	}
}
