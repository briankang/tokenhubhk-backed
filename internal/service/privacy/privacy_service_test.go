package privacy

import (
	"context"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
)

func newTestPrivacyService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.PrivacyRequest{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(db), db
}

func createPrivacyTestUser(t *testing.T, db *gorm.DB, email string) model.User {
	t.Helper()
	user := model.User{
		TenantID:     1,
		Email:        email,
		PasswordHash: "hash",
		Name:         "Privacy User",
		IsActive:     true,
		Language:     "en",
		CountryCode:  "US",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func TestCreatePrivacyRequestResolvesEmailAndDueDate(t *testing.T) {
	svc, db := newTestPrivacyService(t)
	user := createPrivacyTestUser(t, db, "privacy@example.com")

	req, err := svc.Create(context.Background(), CreateInput{
		UserID:   user.ID,
		Type:     model.PrivacyRequestExportData,
		Region:   "us",
		Language: "en-US",
		Reason:   "I want a copy of my data.",
		Scope:    "all",
	})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if req.Email != "privacy@example.com" {
		t.Fatalf("expected resolved email, got %q", req.Email)
	}
	if req.Status != model.PrivacyRequestStatusReceived {
		t.Fatalf("expected received status, got %q", req.Status)
	}
	if req.Region != "US" {
		t.Fatalf("expected uppercase region, got %q", req.Region)
	}
	if req.DueAt == nil || req.DueAt.Before(time.Now().Add(29*24*time.Hour)) {
		t.Fatalf("expected due date around 30 days out, got %#v", req.DueAt)
	}
}

func TestCreatePrivacyRequestRejectsInvalidInput(t *testing.T) {
	svc, _ := newTestPrivacyService(t)

	if _, err := svc.Create(context.Background(), CreateInput{Type: model.PrivacyRequestExportData}); err == nil {
		t.Fatalf("expected missing user error")
	}
	if _, err := svc.Create(context.Background(), CreateInput{UserID: 1, Type: "unknown"}); err == nil || !strings.Contains(err.Error(), "invalid privacy request type") {
		t.Fatalf("expected invalid type error, got %v", err)
	}
}

func TestCreatePrivacyRequestReturnsActiveDuplicate(t *testing.T) {
	svc, db := newTestPrivacyService(t)
	user := createPrivacyTestUser(t, db, "duplicate@example.com")

	first, err := svc.Create(context.Background(), CreateInput{
		UserID: user.ID,
		Type:   model.PrivacyRequestDeleteAccount,
		Reason: "delete me",
	})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := svc.Create(context.Background(), CreateInput{
		UserID: user.ID,
		Type:   model.PrivacyRequestDeleteAccount,
		Reason: "duplicate delete",
	})
	if err != nil {
		t.Fatalf("create duplicate: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected active duplicate to return existing request %d, got %d", first.ID, second.ID)
	}
}

func TestMarketingOptOutCompletesImmediately(t *testing.T) {
	svc, db := newTestPrivacyService(t)
	user := createPrivacyTestUser(t, db, "marketing@example.com")

	req, err := svc.Create(context.Background(), CreateInput{
		UserID: user.ID,
		Type:   model.PrivacyRequestMarketingOptOut,
		Scope:  "marketing",
	})
	if err != nil {
		t.Fatalf("create marketing opt-out: %v", err)
	}
	if req.Status != model.PrivacyRequestStatusCompleted {
		t.Fatalf("expected completed, got %q", req.Status)
	}
	if req.ClosedAt == nil {
		t.Fatalf("expected closed_at to be set")
	}
}

func TestListAdminFiltersAndUpdateTerminalStatus(t *testing.T) {
	svc, db := newTestPrivacyService(t)
	userA := createPrivacyTestUser(t, db, "a@example.com")
	userB := createPrivacyTestUser(t, db, "b@example.com")

	reqA, err := svc.Create(context.Background(), CreateInput{
		UserID: userA.ID,
		Type:   model.PrivacyRequestExportData,
		Region: "eu",
		Reason: "export account data",
	})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	if _, err := svc.Create(context.Background(), CreateInput{
		UserID: userB.ID,
		Type:   model.PrivacyRequestDeleteAPILogs,
		Region: "us",
		Reason: "delete api logs",
	}); err != nil {
		t.Fatalf("create b: %v", err)
	}

	list, total, err := svc.ListAdmin(context.Background(), ListAdminFilter{
		Type:    model.PrivacyRequestExportData,
		Region:  "EU",
		Keyword: "account",
	})
	if err != nil {
		t.Fatalf("list admin: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].ID != reqA.ID {
		t.Fatalf("expected only request %d, total=%d list=%v", reqA.ID, total, list)
	}

	updated, err := svc.Update(context.Background(), reqA.ID, UpdateInput{
		Status:         model.PrivacyRequestStatusCompleted,
		AssignedTo:     99,
		ResolutionNote: "Export package delivered.",
		Verified:       true,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Status != model.PrivacyRequestStatusCompleted || updated.ClosedAt == nil || updated.VerifiedAt == nil {
		t.Fatalf("expected terminal verified request, got status=%q closed=%v verified=%v", updated.Status, updated.ClosedAt, updated.VerifiedAt)
	}
	if updated.AssignedTo != 99 {
		t.Fatalf("expected assigned admin 99, got %d", updated.AssignedTo)
	}
}
