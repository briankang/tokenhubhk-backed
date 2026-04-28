package database

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestRunSeedOAuthProviders_InsertsDisabledDefaults(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:oauth_defaults?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.OAuthProviderConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	RunSeedOAuthProviders(db)
	RunSeedOAuthProviders(db)

	var rows []model.OAuthProviderConfig
	if err := db.Order("sort_order ASC").Find(&rows).Error; err != nil {
		t.Fatalf("list oauth providers: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 default providers, got %d", len(rows))
	}
	if rows[0].Provider != "google" || rows[1].Provider != "github" {
		t.Fatalf("unexpected provider order: %#v", rows)
	}
	for _, row := range rows {
		if row.IsActive {
			t.Fatalf("default provider %s must be disabled before admin config", row.Provider)
		}
		if row.ClientID != "" || row.ClientSecretEncrypted != "" {
			t.Fatalf("default provider %s must not include mock credentials", row.Provider)
		}
		if row.RedirectURL == "" || row.FrontendRedirectURL == "" || row.AuthURL == "" || row.TokenURL == "" || row.UserInfoURL == "" || row.Scopes == "" {
			t.Fatalf("default provider %s missing endpoint defaults: %#v", row.Provider, row)
		}
		if got, want := row.FrontendRedirectURL, "https://www.tokenhubhk.com/oauth/callback"; got != want {
			t.Fatalf("unexpected frontend redirect for %s: got %q want %q", row.Provider, got, want)
		}
		wantBackend := "https://www.tokenhubhk.com/api/v1/auth/oauth/" + row.Provider + "/callback"
		if row.RedirectURL != wantBackend {
			t.Fatalf("unexpected backend redirect for %s: got %q want %q", row.Provider, row.RedirectURL, wantBackend)
		}
	}
}

func TestRunSeedOAuthProviders_UpgradesUnconfiguredRelativeDefaults(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:oauth_upgrade?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.OAuthProviderConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Create(&model.OAuthProviderConfig{
		Provider:              "google",
		DisplayName:           "Google",
		RedirectURL:           "/api/v1/auth/oauth/google/callback",
		FrontendRedirectURL:   "/oauth/callback",
		AuthURL:               "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:              "https://oauth2.googleapis.com/token",
		UserInfoURL:           "https://openidconnect.googleapis.com/v1/userinfo",
		Scopes:                "openid email profile",
		IsActive:              false,
		AllowSignup:           true,
		AutoLinkEmail:         true,
		SortOrder:             10,
		ClientID:              "",
		ClientSecretEncrypted: "",
	}).Error; err != nil {
		t.Fatalf("seed relative row: %v", err)
	}

	RunSeedOAuthProviders(db)

	var google model.OAuthProviderConfig
	if err := db.Where("provider = ?", "google").First(&google).Error; err != nil {
		t.Fatalf("find google: %v", err)
	}
	if got, want := google.RedirectURL, "https://www.tokenhubhk.com/api/v1/auth/oauth/google/callback"; got != want {
		t.Fatalf("unexpected upgraded backend redirect: got %q want %q", got, want)
	}
	if got, want := google.FrontendRedirectURL, "https://www.tokenhubhk.com/oauth/callback"; got != want {
		t.Fatalf("unexpected upgraded frontend redirect: got %q want %q", got, want)
	}
}

func TestRunSeedOAuthProviders_DoesNotOverwriteConfiguredCredentials(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:oauth_preserve?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.OAuthProviderConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Create(&model.OAuthProviderConfig{
		Provider:              "github",
		DisplayName:           "GitHub Custom",
		ClientID:              "github-client",
		ClientSecretEncrypted: "encrypted-secret",
		RedirectURL:           "https://custom.example.com/github/callback",
		FrontendRedirectURL:   "https://custom.example.com/oauth/callback",
		AuthURL:               "https://github.com/login/oauth/authorize",
		TokenURL:              "https://github.com/login/oauth/access_token",
		UserInfoURL:           "https://api.github.com/user",
		EmailURL:              "https://api.github.com/user/emails",
		Scopes:                "read:user user:email",
		IsActive:              true,
		AllowSignup:           true,
		AutoLinkEmail:         true,
		SortOrder:             20,
	}).Error; err != nil {
		t.Fatalf("seed configured row: %v", err)
	}

	RunSeedOAuthProviders(db)

	var github model.OAuthProviderConfig
	if err := db.Where("provider = ?", "github").First(&github).Error; err != nil {
		t.Fatalf("find github: %v", err)
	}
	if got, want := github.RedirectURL, "https://custom.example.com/github/callback"; got != want {
		t.Fatalf("configured backend redirect was overwritten: got %q want %q", got, want)
	}
	if got, want := github.ClientID, "github-client"; got != want {
		t.Fatalf("configured client id was overwritten: got %q want %q", got, want)
	}
}
