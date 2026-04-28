package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/model"
	referralsvc "tokenhub-server/internal/service/referral"
)

func newOAuthTestService(t *testing.T) (*OAuthService, *gorm.DB) {
	t.Helper()
	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Tenant{},
		&model.User{},
		&model.UserBalance{},
		&model.BalanceRecord{},
		&model.QuotaConfig{},
		&model.OAuthProviderConfig{},
		&model.OAuthIdentity{},
		&model.ReferralAttribution{},
		&model.ReferralLink{},
		&model.ReferralConfig{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Create(&model.Tenant{Name: "Platform", Domain: "platform", Level: 1, IsActive: true}).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return NewOAuthService(db, nil, config.JWTConfig{Secret: "test-secret"}), db
}

func TestOAuthProviderConfig_UpsertEncryptsAndMasksSecret(t *testing.T) {
	svc, _ := newOAuthTestService(t)
	active := true

	dto, err := svc.UpsertProviderConfig(context.Background(), OAuthProviderGoogle, OAuthProviderUpsertRequest{
		ClientID:     "google-client",
		ClientSecret: "google-secret",
		RedirectURL:  "https://api.example.com/api/v1/auth/oauth/google/callback",
		IsActive:     &active,
	}, "")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if !dto.HasClientSecret {
		t.Fatalf("expected masked secret flag")
	}

	cfg, secret, err := svc.getActiveConfig(context.Background(), OAuthProviderGoogle)
	if err != nil {
		t.Fatalf("get active config: %v", err)
	}
	if secret != "google-secret" {
		t.Fatalf("expected decrypted secret, got %q", secret)
	}
	if cfg.ClientSecretEncrypted == "google-secret" {
		t.Fatalf("secret must not be stored as plaintext")
	}

	_, err = svc.UpsertProviderConfig(context.Background(), OAuthProviderGoogle, OAuthProviderUpsertRequest{
		ClientID:     "google-client-2",
		ClientSecret: maskedSecret,
		RedirectURL:  "https://api.example.com/api/v1/auth/oauth/google/callback",
		IsActive:     &active,
	}, "")
	if err != nil {
		t.Fatalf("upsert masked: %v", err)
	}
	_, secret, err = svc.getActiveConfig(context.Background(), OAuthProviderGoogle)
	if err != nil {
		t.Fatalf("get after masked upsert: %v", err)
	}
	if secret != "google-secret" {
		t.Fatalf("masked secret should preserve old value, got %q", secret)
	}
}

func TestOAuthCallback_CreatesUserAndIdentity(t *testing.T) {
	svc, db := newOAuthTestService(t)
	active := true

	var tokenSeen, userSeen bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenSeen = true
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			if r.Form.Get("code") != "auth-code" {
				t.Fatalf("unexpected code %q", r.Form.Get("code"))
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "upstream-token"})
		case "/userinfo":
			userSeen = true
			if got := r.Header.Get("Authorization"); got != "Bearer upstream-token" {
				t.Fatalf("unexpected auth header %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sub":            "google-user-1",
				"email":          "oauth-user@example.com",
				"email_verified": true,
				"name":           "OAuth User",
				"picture":        "https://example.com/avatar.png",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	_, err := svc.UpsertProviderConfig(context.Background(), OAuthProviderGoogle, OAuthProviderUpsertRequest{
		ClientID:            "google-client",
		ClientSecret:        "google-secret",
		RedirectURL:         "https://api.example.com/api/v1/auth/oauth/google/callback",
		FrontendRedirectURL: "https://app.example.com/oauth/callback",
		AuthURL:             "https://accounts.example.com/auth",
		TokenURL:            upstream.URL + "/token",
		UserInfoURL:         upstream.URL + "/userinfo",
		IsActive:            &active,
	}, "")
	if err != nil {
		t.Fatalf("upsert config: %v", err)
	}

	authURL, err := svc.BuildAuthURL(context.Background(), OAuthProviderGoogle, "/dashboard/settings", "", "")
	if err != nil {
		t.Fatalf("build auth url: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatalf("expected state in auth url")
	}

	pair, redirectPath, frontendURL, err := svc.HandleCallback(context.Background(), OAuthProviderGoogle, "auth-code", state)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatalf("expected token pair")
	}
	if redirectPath != "/dashboard/settings" {
		t.Fatalf("unexpected redirect path %q", redirectPath)
	}
	if frontendURL != "https://app.example.com/oauth/callback" {
		t.Fatalf("unexpected frontend url %q", frontendURL)
	}
	if !tokenSeen || !userSeen {
		t.Fatalf("expected token and userinfo upstream calls")
	}

	var user model.User
	if err := db.Where("email = ?", "oauth-user@example.com").First(&user).Error; err != nil {
		t.Fatalf("find oauth user: %v", err)
	}
	var identity model.OAuthIdentity
	if err := db.Where("provider = ? AND provider_user_id = ?", OAuthProviderGoogle, "google-user-1").First(&identity).Error; err != nil {
		t.Fatalf("find identity: %v", err)
	}
	if identity.UserID != user.ID || !strings.EqualFold(identity.Email, user.Email) {
		t.Fatalf("identity not linked to created user")
	}
}

func TestOAuthCallback_GitHubUsesPrimaryVerifiedEmail(t *testing.T) {
	svc, db := newOAuthTestService(t)
	active := true

	var tokenSeen, userSeen, emailSeen bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenSeen = true
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			if r.Form.Get("code") != "github-code" {
				t.Fatalf("unexpected code %q", r.Form.Get("code"))
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "github-upstream-token"})
		case "/user":
			userSeen = true
			if got := r.Header.Get("Authorization"); got != "Bearer github-upstream-token" {
				t.Fatalf("unexpected auth header %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":         991,
				"login":      "octo-user",
				"name":       "Octo User",
				"email":      "",
				"avatar_url": "https://example.com/octo.png",
			})
		case "/emails":
			emailSeen = true
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{
				{"email": "secondary@example.com", "primary": false, "verified": true},
				{"email": "octo@example.com", "primary": true, "verified": true},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	_, err := svc.UpsertProviderConfig(context.Background(), OAuthProviderGitHub, OAuthProviderUpsertRequest{
		ClientID:            "github-client",
		ClientSecret:        "github-secret",
		RedirectURL:         "https://api.example.com/api/v1/auth/oauth/github/callback",
		FrontendRedirectURL: "https://app.example.com/oauth/callback",
		AuthURL:             "https://github.example.com/login/oauth/authorize",
		TokenURL:            upstream.URL + "/token",
		UserInfoURL:         upstream.URL + "/user",
		EmailURL:            upstream.URL + "/emails",
		IsActive:            &active,
	}, "")
	if err != nil {
		t.Fatalf("upsert config: %v", err)
	}

	authURL, err := svc.BuildAuthURL(context.Background(), OAuthProviderGitHub, "/dashboard", "", "")
	if err != nil {
		t.Fatalf("build auth url: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatalf("expected state in auth url")
	}

	pair, redirectPath, frontendURL, err := svc.HandleCallback(context.Background(), OAuthProviderGitHub, "github-code", state)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatalf("expected token pair")
	}
	if redirectPath != "/dashboard" {
		t.Fatalf("unexpected redirect path %q", redirectPath)
	}
	if frontendURL != "https://app.example.com/oauth/callback" {
		t.Fatalf("unexpected frontend url %q", frontendURL)
	}
	if !tokenSeen || !userSeen || !emailSeen {
		t.Fatalf("expected token, user and email upstream calls")
	}

	var user model.User
	if err := db.Where("email = ?", "octo@example.com").First(&user).Error; err != nil {
		t.Fatalf("find oauth user: %v", err)
	}
	var identity model.OAuthIdentity
	if err := db.Where("provider = ? AND provider_user_id = ?", OAuthProviderGitHub, "991").First(&identity).Error; err != nil {
		t.Fatalf("find identity: %v", err)
	}
	if identity.UserID != user.ID || !strings.EqualFold(identity.Email, user.Email) {
		t.Fatalf("identity not linked to created user")
	}
}

func TestOAuthCallback_InviteCodeReferralCreatesAttribution(t *testing.T) {
	svc, db := newOAuthTestService(t)
	ctx := context.Background()
	active := true

	inviter := model.User{
		TenantID:       1,
		Email:          "oauth-inviter@example.com",
		PasswordHash:   "unused",
		Name:           "OAuth Inviter",
		IsActive:       true,
		CountryCode:    "US",
		RegisterSource: "email",
	}
	if err := db.Create(&inviter).Error; err != nil {
		t.Fatalf("create inviter: %v", err)
	}
	link, err := referralsvc.NewReferralService(db).GetOrCreateLink(ctx, inviter.ID, inviter.TenantID)
	if err != nil {
		t.Fatalf("create referral link: %v", err)
	}
	if err := db.Create(&model.ReferralConfig{
		CommissionRate:    0.10,
		AttributionDays:   45,
		RequireInviteCode: true,
		IsActive:          true,
	}).Error; err != nil {
		t.Fatalf("create referral config: %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "google-referral-token"})
		case "/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sub":            "google-referral-user",
				"email":          "oauth-invitee@example.com",
				"email_verified": true,
				"name":           "OAuth Invitee",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	_, err = svc.UpsertProviderConfig(ctx, OAuthProviderGoogle, OAuthProviderUpsertRequest{
		ClientID:            "google-client",
		ClientSecret:        "google-secret",
		RedirectURL:         "https://api.example.com/api/v1/auth/oauth/google/callback",
		FrontendRedirectURL: "https://app.example.com/oauth/callback",
		AuthURL:             "https://accounts.example.com/auth",
		TokenURL:            upstream.URL + "/token",
		UserInfoURL:         upstream.URL + "/userinfo",
		IsActive:            &active,
	}, "")
	if err != nil {
		t.Fatalf("upsert config: %v", err)
	}

	authURL, err := svc.BuildAuthURL(ctx, OAuthProviderGoogle, "/dashboard/referral", link.Code, "")
	if err != nil {
		t.Fatalf("build auth url: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatalf("expected state in auth url")
	}

	pair, redirectPath, _, err := svc.HandleCallback(ctx, OAuthProviderGoogle, "auth-code", state)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatalf("expected token pair")
	}
	if redirectPath != "/dashboard/referral" {
		t.Fatalf("unexpected redirect path %q", redirectPath)
	}

	var user model.User
	if err := db.Where("email = ?", "oauth-invitee@example.com").First(&user).Error; err != nil {
		t.Fatalf("find invitee: %v", err)
	}
	if user.RegisterSource != OAuthProviderGoogle {
		t.Fatalf("expected register source google, got %q", user.RegisterSource)
	}
	if user.ReferredBy == nil || *user.ReferredBy != inviter.ID {
		t.Fatalf("expected referred_by=%d, got %#v", inviter.ID, user.ReferredBy)
	}

	var attr model.ReferralAttribution
	if err := db.Where("user_id = ?", user.ID).First(&attr).Error; err != nil {
		t.Fatalf("find attribution: %v", err)
	}
	if attr.InviterID != inviter.ID || attr.ReferralCode != link.Code || !attr.IsValid {
		t.Fatalf("unexpected attribution: inviter=%d code=%q valid=%v", attr.InviterID, attr.ReferralCode, attr.IsValid)
	}

	var updatedLink model.ReferralLink
	if err := db.First(&updatedLink, link.ID).Error; err != nil {
		t.Fatalf("reload referral link: %v", err)
	}
	if updatedLink.RegisterCount != 1 {
		t.Fatalf("expected register_count=1, got %d", updatedLink.RegisterCount)
	}
	if user.ReferralCode == "" {
		t.Fatalf("oauth-created user should receive own referral code")
	}
}

func TestOAuthCallback_RequireInviteCodeBlocksSignupWithoutCode(t *testing.T) {
	svc, db := newOAuthTestService(t)
	ctx := context.Background()
	active := true

	if err := db.Create(&model.ReferralConfig{
		CommissionRate:    0.10,
		AttributionDays:   90,
		RequireInviteCode: true,
		IsActive:          true,
	}).Error; err != nil {
		t.Fatalf("create referral config: %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "google-no-invite-token"})
		case "/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sub":            "google-no-invite-user",
				"email":          "oauth-no-invite@example.com",
				"email_verified": true,
				"name":           "No Invite",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	_, err := svc.UpsertProviderConfig(ctx, OAuthProviderGoogle, OAuthProviderUpsertRequest{
		ClientID:            "google-client",
		ClientSecret:        "google-secret",
		RedirectURL:         "https://api.example.com/api/v1/auth/oauth/google/callback",
		FrontendRedirectURL: "https://app.example.com/oauth/callback",
		AuthURL:             "https://accounts.example.com/auth",
		TokenURL:            upstream.URL + "/token",
		UserInfoURL:         upstream.URL + "/userinfo",
		IsActive:            &active,
	}, "")
	if err != nil {
		t.Fatalf("upsert config: %v", err)
	}

	authURL, err := svc.BuildAuthURL(ctx, OAuthProviderGoogle, "/dashboard", "", "")
	if err != nil {
		t.Fatalf("build auth url: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatalf("expected state in auth url")
	}

	_, _, _, err = svc.HandleCallback(ctx, OAuthProviderGoogle, "auth-code", state)
	if err == nil || !strings.Contains(err.Error(), "invite code is required") {
		t.Fatalf("expected invite code required error, got %v", err)
	}

	var count int64
	if err := db.Model(&model.User{}).Where("email = ?", "oauth-no-invite@example.com").Count(&count).Error; err != nil {
		t.Fatalf("count user: %v", err)
	}
	if count != 0 {
		t.Fatalf("oauth signup without invite should not create user, got %d", count)
	}
}
