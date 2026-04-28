package v1

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func newCompletionsLogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.ChannelLog{}, &model.ApiCallLog{}); err != nil {
		t.Fatalf("migrate logs: %v", err)
	}
	return db
}

func TestInsertChannelLogBatch(t *testing.T) {
	db := newCompletionsLogTestDB(t)
	batch := []*model.ChannelLog{
		{ChannelID: 1, TenantID: 1, UserID: 1, ModelName: "mock-chat", RequestID: "req-channel-1", StatusCode: 200, CreatedAt: time.Now()},
		{ChannelID: 1, TenantID: 1, UserID: 1, ModelName: "mock-chat", RequestID: "req-channel-2", StatusCode: 200, CreatedAt: time.Now()},
	}

	if err := insertChannelLogBatch(db, batch); err != nil {
		t.Fatalf("insertChannelLogBatch: %v", err)
	}

	var count int64
	if err := db.Model(&model.ChannelLog{}).Count(&count).Error; err != nil {
		t.Fatalf("count channel logs: %v", err)
	}
	if count != int64(len(batch)) {
		t.Fatalf("channel log count = %d, want %d", count, len(batch))
	}
}

func TestInsertAPICallLogBatch(t *testing.T) {
	db := newCompletionsLogTestDB(t)
	batch := []*model.ApiCallLog{
		{RequestID: "req-api-1", UserID: 1, TenantID: 1, ApiKeyID: 1, Endpoint: "/v1/chat/completions", RequestModel: "mock-chat", StatusCode: 200, Status: "success"},
		{RequestID: "req-api-2", UserID: 1, TenantID: 1, ApiKeyID: 1, Endpoint: "/v1/chat/completions", RequestModel: "mock-chat", StatusCode: 200, Status: "success"},
	}

	if err := insertAPICallLogBatch(db, batch); err != nil {
		t.Fatalf("insertAPICallLogBatch: %v", err)
	}

	var count int64
	if err := db.Model(&model.ApiCallLog{}).Count(&count).Error; err != nil {
		t.Fatalf("count api call logs: %v", err)
	}
	if count != int64(len(batch)) {
		t.Fatalf("api call log count = %d, want %d", count, len(batch))
	}
}
