package email

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
)

func newEmailConfigTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", name)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.EmailProviderConfig{}); err != nil {
		t.Fatalf("migrate email provider config: %v", err)
	}
	return db
}

func boolPtr(v bool) *bool { return &v }
func intPtr(v int) *int    { return &v }

func TestEmailConfigUpsertValidationAndSecretPreservation(t *testing.T) {
	db := newEmailConfigTestDB(t)
	svc := NewConfigService(db)
	ctx := context.Background()

	if _, err := svc.Upsert(ctx, UpsertRequest{Channel: "alerts", APIKey: "secret"}); err == nil || !strings.Contains(err.Error(), "invalid channel") {
		t.Fatalf("invalid channel error = %v, want invalid channel", err)
	}
	if _, err := svc.Upsert(ctx, UpsertRequest{Channel: model.EmailChannelNotification, APIKey: MaskedSecret}); err == nil || !strings.Contains(err.Error(), "api_key is required") {
		t.Fatalf("new masked secret error = %v, want api_key required", err)
	}

	created, err := svc.Upsert(ctx, UpsertRequest{
		Channel:    model.EmailChannelNotification,
		APIUser:    "notice-user",
		APIKey:     "plain-secret",
		FromEmail:  "notice@example.com",
		FromName:   "Notice",
		Domain:     "example.com",
		IsActive:   boolPtr(true),
		DailyLimit: intPtr(100),
	})
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	if created.APIKeyEncrypted == "" || created.APIKeyEncrypted == "plain-secret" {
		t.Fatalf("api key was not encrypted: %q", created.APIKeyEncrypted)
	}
	_, decrypted, err := svc.GetDecrypted(ctx, model.EmailChannelNotification)
	if err != nil {
		t.Fatalf("GetDecrypted: %v", err)
	}
	if decrypted != "plain-secret" {
		t.Fatalf("decrypted key = %q, want original", decrypted)
	}

	updated, err := svc.Upsert(ctx, UpsertRequest{
		Channel:    model.EmailChannelNotification,
		APIUser:    "notice-user-2",
		APIKey:     MaskedSecret,
		FromEmail:  "notice2@example.com",
		FromName:   "Notice Two",
		IsActive:   boolPtr(false),
		DailyLimit: intPtr(5),
	})
	if err != nil {
		t.Fatalf("update config with masked secret: %v", err)
	}
	if updated.APIKeyEncrypted != created.APIKeyEncrypted {
		t.Fatalf("masked secret should preserve encrypted key, got %q want %q", updated.APIKeyEncrypted, created.APIKeyEncrypted)
	}
	cfg, decryptedAfterUpdate, err := svc.GetDecrypted(ctx, model.EmailChannelNotification)
	if err != nil {
		t.Fatalf("GetDecrypted after update: %v", err)
	}
	if decryptedAfterUpdate != "plain-secret" {
		t.Fatalf("decrypted key after update = %q, want original", decryptedAfterUpdate)
	}
	if cfg.FromName != "Notice Two" || cfg.IsActive || cfg.DailyLimit != 5 {
		t.Fatalf("non-secret fields not updated correctly: %#v", cfg)
	}
}
