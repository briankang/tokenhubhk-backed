package authlog_test

import (
	"context"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/authlog"
)

func newRecorderTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.UserAuthLog{}); err != nil {
		t.Fatalf("migrate auth logs: %v", err)
	}
	return db
}

func TestRecorderList_SearchesLoginMetadata(t *testing.T) {
	db := newRecorderTestDB(t)
	now := time.Now()
	rows := []model.UserAuthLog{
		{UserID: 583, Email: "alice@example.com", EventType: model.AuthEventRegister, IP: "172.18.0.1", UserAgent: "Mozilla/5.0 Chrome", RequestID: "req-alpha-583", Country: "CN", City: "Shanghai", CreatedAt: now},
		{UserID: 584, Email: "bob@example.com", EventType: model.AuthEventLoginFailed, IP: "10.0.0.8", UserAgent: "Go-http-client/1.1", RequestID: "req-beta-584", FailReason: model.AuthFailReasonWrongPassword, CreatedAt: now},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed auth logs: %v", err)
	}

	recorder := authlog.NewRecorder(db)
	ctx := context.Background()

	t.Run("keyword searches request id city and ua", func(t *testing.T) {
		list, total, err := recorder.List(ctx, &model.UserAuthLogQuery{Keyword: "Chrome", Page: 1, PageSize: 20})
		if err != nil {
			t.Fatalf("list by keyword: %v", err)
		}
		if total != 1 || len(list) != 1 || list[0].UserID != 583 {
			t.Fatalf("expected user 583 by user agent keyword, total=%d list=%v", total, list)
		}

		list, total, err = recorder.List(ctx, &model.UserAuthLogQuery{Keyword: "Shanghai", Page: 1, PageSize: 20})
		if err != nil {
			t.Fatalf("list by city keyword: %v", err)
		}
		if total != 1 || len(list) != 1 || list[0].UserID != 583 {
			t.Fatalf("expected user 583 by city keyword, total=%d list=%v", total, list)
		}

		list, total, err = recorder.List(ctx, &model.UserAuthLogQuery{Keyword: "584", Page: 1, PageSize: 20})
		if err != nil {
			t.Fatalf("list by numeric keyword: %v", err)
		}
		if total != 1 || len(list) != 1 || list[0].UserID != 584 {
			t.Fatalf("expected user 584 by numeric keyword, total=%d list=%v", total, list)
		}
	})

	t.Run("specific filters are fuzzy", func(t *testing.T) {
		list, total, err := recorder.List(ctx, &model.UserAuthLogQuery{Email: "bob@", RequestID: "beta", UserAgent: "Go-http", FailReason: "wrong", Page: 1, PageSize: 20})
		if err != nil {
			t.Fatalf("list by fuzzy filters: %v", err)
		}
		if total != 1 || len(list) != 1 || list[0].UserID != 584 {
			t.Fatalf("expected user 584 by fuzzy filters, total=%d list=%v", total, list)
		}
	})
}
