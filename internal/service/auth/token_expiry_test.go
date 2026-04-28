package auth

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/model"
)

func TestGenerateTokenPair_AccessTokenExpiresInThirtyDays(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	svc := NewAuthService(db, nil, config.JWTConfig{Secret: "test-secret"})

	pair, err := svc.generateTokenPair(&model.User{BaseModel: model.BaseModel{ID: 1}, TenantID: 1})
	if err != nil {
		t.Fatalf("generateTokenPair: %v", err)
	}
	if pair.ExpiresIn != int64((30 * 24 * time.Hour).Seconds()) {
		t.Fatalf("expected 30 day access token, got %d seconds", pair.ExpiresIn)
	}
}
