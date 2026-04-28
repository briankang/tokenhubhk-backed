package auth

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/balance"
	"tokenhub-server/internal/service/referral"
)

const (
	OAuthProviderGoogle = "google"
	OAuthProviderGitHub = "github"

	oauthStatePrefix = "oauth:state:"
	oauthStateTTL    = 10 * time.Minute
	maskedSecret     = "***"
)

const defaultOAuthEncryptKey = "tokenhub-oauth-secret-key-32bytes!"

// OAuthProviderDTO 是后台和公开接口可返回的脱敏 provider 配置。
type OAuthProviderDTO struct {
	ID                  uint   `json:"id,omitempty"`
	Provider            string `json:"provider"`
	DisplayName         string `json:"display_name"`
	ClientID            string `json:"client_id,omitempty"`
	HasClientSecret     bool   `json:"has_client_secret,omitempty"`
	RedirectURL         string `json:"redirect_url,omitempty"`
	FrontendRedirectURL string `json:"frontend_redirect_url,omitempty"`
	AuthURL             string `json:"auth_url,omitempty"`
	TokenURL            string `json:"token_url,omitempty"`
	UserInfoURL         string `json:"userinfo_url,omitempty"`
	EmailURL            string `json:"email_url,omitempty"`
	Scopes              string `json:"scopes,omitempty"`
	IsActive            bool   `json:"is_active"`
	AllowSignup         bool   `json:"allow_signup,omitempty"`
	AutoLinkEmail       bool   `json:"auto_link_email,omitempty"`
	SortOrder           int    `json:"sort_order,omitempty"`
}

// OAuthProviderUpsertRequest 是后台写入 provider 配置的请求。
type OAuthProviderUpsertRequest struct {
	DisplayName         string `json:"display_name"`
	ClientID            string `json:"client_id"`
	ClientSecret        string `json:"client_secret"`
	RedirectURL         string `json:"redirect_url"`
	FrontendRedirectURL string `json:"frontend_redirect_url"`
	AuthURL             string `json:"auth_url"`
	TokenURL            string `json:"token_url"`
	UserInfoURL         string `json:"userinfo_url"`
	EmailURL            string `json:"email_url"`
	Scopes              string `json:"scopes"`
	IsActive            *bool  `json:"is_active"`
	AllowSignup         *bool  `json:"allow_signup"`
	AutoLinkEmail       *bool  `json:"auto_link_email"`
	SortOrder           *int   `json:"sort_order"`
}

type oauthState struct {
	Provider     string `json:"provider"`
	Redirect     string `json:"redirect"`
	InviteCode   string `json:"invite_code,omitempty"`
	ReferralCode string `json:"referral_code,omitempty"`
}

type oauthProfile struct {
	ProviderUserID string
	Email          string
	EmailVerified  bool
	Name           string
	AvatarURL      string
}

// OAuthService 负责 OAuth provider 配置、授权地址生成、回调交换和本地账号绑定。
type OAuthService struct {
	db         *gorm.DB
	redis      *goredis.Client
	jwt        config.JWTConfig
	httpClient *http.Client
	encryptKey []byte
	balanceSvc *balance.BalanceService

	mu           sync.Mutex
	memoryStates map[string]oauthState
}

// NewOAuthService 创建 OAuth 服务实例。
func NewOAuthService(db *gorm.DB, redisClient *goredis.Client, jwtCfg config.JWTConfig) *OAuthService {
	key := os.Getenv("OAUTH_ENCRYPT_KEY")
	if key == "" {
		key = defaultOAuthEncryptKey
	}
	keyBytes := []byte(key)
	if len(keyBytes) < 32 {
		padded := make([]byte, 32)
		copy(padded, keyBytes)
		keyBytes = padded
	} else if len(keyBytes) > 32 {
		keyBytes = keyBytes[:32]
	}
	return &OAuthService{
		db:           db,
		redis:        redisClient,
		jwt:          jwtCfg,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		encryptKey:   keyBytes,
		balanceSvc:   balance.NewBalanceService(db, redisClient),
		memoryStates: map[string]oauthState{},
	}
}

func oauthDefaults(provider string) (model.OAuthProviderConfig, bool) {
	switch strings.ToLower(provider) {
	case OAuthProviderGoogle:
		return model.OAuthProviderConfig{
			Provider:      OAuthProviderGoogle,
			DisplayName:   "Google",
			AuthURL:       "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:      "https://oauth2.googleapis.com/token",
			UserInfoURL:   "https://openidconnect.googleapis.com/v1/userinfo",
			Scopes:        "openid email profile",
			AllowSignup:   true,
			AutoLinkEmail: true,
			SortOrder:     10,
		}, true
	case OAuthProviderGitHub:
		return model.OAuthProviderConfig{
			Provider:      OAuthProviderGitHub,
			DisplayName:   "GitHub",
			AuthURL:       "https://github.com/login/oauth/authorize",
			TokenURL:      "https://github.com/login/oauth/access_token",
			UserInfoURL:   "https://api.github.com/user",
			EmailURL:      "https://api.github.com/user/emails",
			Scopes:        "read:user user:email",
			AllowSignup:   true,
			AutoLinkEmail: true,
			SortOrder:     20,
		}, true
	default:
		return model.OAuthProviderConfig{}, false
	}
}

// ListProviderConfigs 返回后台脱敏配置；未创建的内置 provider 也会返回占位行。
func (s *OAuthService) ListProviderConfigs(ctx context.Context) ([]OAuthProviderDTO, error) {
	var rows []model.OAuthProviderConfig
	if err := s.db.WithContext(ctx).Order("sort_order ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	byProvider := map[string]model.OAuthProviderConfig{}
	for _, row := range rows {
		byProvider[row.Provider] = row
	}

	out := make([]OAuthProviderDTO, 0, 2)
	for _, provider := range []string{OAuthProviderGoogle, OAuthProviderGitHub} {
		cfg, ok := byProvider[provider]
		if !ok {
			cfg, _ = oauthDefaults(provider)
		}
		out = append(out, toOAuthProviderDTO(cfg, true))
	}
	return out, nil
}

// ListPublicProviders 返回前台可见的已启用 provider。
func (s *OAuthService) ListPublicProviders(ctx context.Context) ([]OAuthProviderDTO, error) {
	var rows []model.OAuthProviderConfig
	if err := s.db.WithContext(ctx).
		Where("is_active = ? AND client_id <> ''", true).
		Order("sort_order ASC, id ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]OAuthProviderDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, OAuthProviderDTO{
			Provider:    row.Provider,
			DisplayName: row.DisplayName,
			IsActive:    row.IsActive,
		})
	}
	return out, nil
}

// UpsertProviderConfig 创建或更新 provider 配置。
// requestOrigin（如 "https://www.tokenhubhk.com"）用于在请求和数据库中均没有 redirect_url
// 时按 Host 自动派生，可空：调用方拿不到 origin 时传 ""，行为退化为原始严格校验。
func (s *OAuthService) UpsertProviderConfig(ctx context.Context, provider string, req OAuthProviderUpsertRequest, requestOrigin string) (*OAuthProviderDTO, error) {
	defaults, ok := oauthDefaults(provider)
	if !ok {
		return nil, fmt.Errorf("unsupported oauth provider: %s", provider)
	}

	var existing model.OAuthProviderConfig
	err := s.db.WithContext(ctx).Where("provider = ?", defaults.Provider).First(&existing).Error
	isNew := errors.Is(err, gorm.ErrRecordNotFound)
	if err != nil && !isNew {
		return nil, err
	}
	if isNew {
		existing = defaults
	}

	if req.DisplayName != "" {
		existing.DisplayName = strings.TrimSpace(req.DisplayName)
	}
	if req.ClientID != "" {
		existing.ClientID = strings.TrimSpace(req.ClientID)
	}
	if req.RedirectURL != "" {
		existing.RedirectURL = strings.TrimSpace(req.RedirectURL)
	}
	if req.FrontendRedirectURL != "" {
		existing.FrontendRedirectURL = strings.TrimSpace(req.FrontendRedirectURL)
	}
	if req.AuthURL != "" {
		existing.AuthURL = strings.TrimSpace(req.AuthURL)
	}
	if req.TokenURL != "" {
		existing.TokenURL = strings.TrimSpace(req.TokenURL)
	}
	if req.UserInfoURL != "" {
		existing.UserInfoURL = strings.TrimSpace(req.UserInfoURL)
	}
	if req.EmailURL != "" {
		existing.EmailURL = strings.TrimSpace(req.EmailURL)
	}
	if req.Scopes != "" {
		existing.Scopes = strings.TrimSpace(req.Scopes)
	}
	if req.ClientSecret != "" && req.ClientSecret != maskedSecret {
		enc, err := s.encrypt(req.ClientSecret)
		if err != nil {
			return nil, err
		}
		existing.ClientSecretEncrypted = enc
	}
	if req.IsActive != nil {
		existing.IsActive = *req.IsActive
	}
	if req.AllowSignup != nil {
		existing.AllowSignup = *req.AllowSignup
	}
	if req.AutoLinkEmail != nil {
		existing.AutoLinkEmail = *req.AutoLinkEmail
	}
	if req.SortOrder != nil {
		existing.SortOrder = *req.SortOrder
	}

	// 兜底：客户端没填且 DB 也没存时，按当前请求的 scheme+host 自动派生 callback URL。
	// 覆盖 curl / postman / SSR / 任何前端因 race 丢字段的场景。
	if existing.RedirectURL == "" && requestOrigin != "" {
		existing.RedirectURL = strings.TrimRight(requestOrigin, "/") +
			"/api/v1/auth/oauth/" + defaults.Provider + "/callback"
	}
	if existing.FrontendRedirectURL == "" && requestOrigin != "" {
		existing.FrontendRedirectURL = strings.TrimRight(requestOrigin, "/") + "/oauth/callback"
	}

	if existing.ClientID == "" {
		return nil, fmt.Errorf("client_id is required")
	}
	if existing.RedirectURL == "" {
		return nil, fmt.Errorf("redirect_url is required")
	}
	if isNew && existing.ClientSecretEncrypted == "" {
		return nil, fmt.Errorf("client_secret is required")
	}

	if isNew {
		if err := s.db.WithContext(ctx).Create(&existing).Error; err != nil {
			return nil, err
		}
	} else if err := s.db.WithContext(ctx).Save(&existing).Error; err != nil {
		return nil, err
	}
	dto := toOAuthProviderDTO(existing, true)
	return &dto, nil
}

// BuildAuthURL 生成第三方授权地址，并保存 CSRF state。
func (s *OAuthService) BuildAuthURL(ctx context.Context, provider, redirectPath, inviteCode, referralCode string) (string, error) {
	cfg, _, err := s.getActiveConfig(ctx, provider)
	if err != nil {
		return "", err
	}
	state, err := randomHex(24)
	if err != nil {
		return "", err
	}
	if redirectPath == "" || !strings.HasPrefix(redirectPath, "/") || strings.HasPrefix(redirectPath, "//") {
		redirectPath = "/dashboard"
	}
	st := oauthState{
		Provider:     cfg.Provider,
		Redirect:     redirectPath,
		InviteCode:   inviteCode,
		ReferralCode: referralCode,
	}
	if err := s.saveState(ctx, state, st); err != nil {
		return "", err
	}

	values := url.Values{}
	values.Set("client_id", cfg.ClientID)
	values.Set("redirect_uri", cfg.RedirectURL)
	values.Set("response_type", "code")
	values.Set("scope", cfg.Scopes)
	values.Set("state", state)
	if cfg.Provider == OAuthProviderGoogle {
		values.Set("access_type", "offline")
		values.Set("prompt", "select_account")
	}
	return cfg.AuthURL + "?" + values.Encode(), nil
}

// HandleCallback 完成 code 交换、资料读取、本地账号绑定并签发 Token。
func (s *OAuthService) HandleCallback(ctx context.Context, provider, code, state string) (*TokenPair, string, string, error) {
	if code == "" || state == "" {
		return nil, "", "", fmt.Errorf("code and state are required")
	}
	st, err := s.consumeState(ctx, state)
	if err != nil {
		return nil, "", "", err
	}
	if st.Provider != strings.ToLower(provider) {
		return nil, "", "", fmt.Errorf("oauth state provider mismatch")
	}
	cfg, secret, err := s.getActiveConfig(ctx, provider)
	if err != nil {
		return nil, "", "", err
	}
	token, err := s.exchangeCode(ctx, cfg, secret, code)
	if err != nil {
		return nil, "", "", err
	}
	profile, err := s.fetchProfile(ctx, cfg, token)
	if err != nil {
		return nil, "", "", err
	}
	user, err := s.loginOrCreateUser(ctx, cfg, profile, st)
	if err != nil {
		return nil, "", "", err
	}

	authSvc := NewAuthService(s.db, s.redis, s.jwt)
	pair, err := authSvc.generateTokenPair(user)
	if err != nil {
		return nil, "", "", err
	}
	now := time.Now()
	_ = s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", user.ID).Update("last_login_at", now).Error
	if s.redis != nil {
		key := fmt.Sprintf("%s%d", redisTokenKeyPrefix, user.ID)
		_ = s.redis.Set(ctx, key, pair.AccessToken, accessTokenExpiry).Err()
	}
	return pair, st.Redirect, cfg.FrontendRedirectURL, nil
}

func (s *OAuthService) getActiveConfig(ctx context.Context, provider string) (model.OAuthProviderConfig, string, error) {
	var cfg model.OAuthProviderConfig
	if err := s.db.WithContext(ctx).Where("provider = ? AND is_active = ?", strings.ToLower(provider), true).First(&cfg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return cfg, "", fmt.Errorf("oauth provider is not enabled")
		}
		return cfg, "", err
	}
	secret, err := s.decrypt(cfg.ClientSecretEncrypted)
	if err != nil {
		return cfg, "", fmt.Errorf("decrypt oauth secret: %w", err)
	}
	if cfg.ClientID == "" || secret == "" || cfg.RedirectURL == "" {
		return cfg, "", fmt.Errorf("oauth provider is not fully configured")
	}
	return cfg, secret, nil
}

func (s *OAuthService) exchangeCode(ctx context.Context, cfg model.OAuthProviderConfig, secret, code string) (string, error) {
	form := url.Values{}
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", secret)
	form.Set("code", code)
	form.Set("redirect_uri", cfg.RedirectURL)
	form.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("oauth token exchange failed: status=%d", resp.StatusCode)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Error != "" {
		return "", fmt.Errorf("oauth token exchange failed: %s", payload.Error)
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("oauth token exchange returned empty access_token")
	}
	return payload.AccessToken, nil
}

func (s *OAuthService) fetchProfile(ctx context.Context, cfg model.OAuthProviderConfig, accessToken string) (oauthProfile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.UserInfoURL, nil)
	if err != nil {
		return oauthProfile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return oauthProfile{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oauthProfile{}, fmt.Errorf("oauth userinfo failed: status=%d", resp.StatusCode)
	}
	switch cfg.Provider {
	case OAuthProviderGoogle:
		var p struct {
			Sub           string `json:"sub"`
			Email         string `json:"email"`
			EmailVerified bool   `json:"email_verified"`
			Name          string `json:"name"`
			Picture       string `json:"picture"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return oauthProfile{}, err
		}
		return oauthProfile{ProviderUserID: p.Sub, Email: strings.ToLower(p.Email), EmailVerified: p.EmailVerified, Name: p.Name, AvatarURL: p.Picture}, nil
	case OAuthProviderGitHub:
		var p struct {
			ID        int64  `json:"id"`
			Email     string `json:"email"`
			Name      string `json:"name"`
			Login     string `json:"login"`
			AvatarURL string `json:"avatar_url"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return oauthProfile{}, err
		}
		email := strings.ToLower(p.Email)
		verified := email != ""
		if email == "" && cfg.EmailURL != "" {
			if primary, ok := s.fetchGitHubPrimaryEmail(ctx, cfg.EmailURL, accessToken); ok {
				email = strings.ToLower(primary)
				verified = true
			}
		}
		name := p.Name
		if name == "" {
			name = p.Login
		}
		return oauthProfile{ProviderUserID: fmt.Sprintf("%d", p.ID), Email: email, EmailVerified: verified, Name: name, AvatarURL: p.AvatarURL}, nil
	default:
		return oauthProfile{}, fmt.Errorf("unsupported oauth provider: %s", cfg.Provider)
	}
}

func (s *OAuthService) fetchGitHubPrimaryEmail(ctx context.Context, emailURL, accessToken string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, emailURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&emails); err != nil {
		return "", false
	}
	for _, item := range emails {
		if item.Primary && item.Verified && item.Email != "" {
			return item.Email, true
		}
	}
	return "", false
}

func (s *OAuthService) loginOrCreateUser(ctx context.Context, cfg model.OAuthProviderConfig, profile oauthProfile, st oauthState) (*model.User, error) {
	if profile.ProviderUserID == "" {
		return nil, fmt.Errorf("oauth profile missing subject")
	}
	if profile.Email == "" || !profile.EmailVerified {
		return nil, fmt.Errorf("oauth email is missing or unverified")
	}

	var identity model.OAuthIdentity
	err := s.db.WithContext(ctx).Where("provider = ? AND provider_user_id = ?", cfg.Provider, profile.ProviderUserID).First(&identity).Error
	if err == nil {
		var user model.User
		if err := s.db.WithContext(ctx).First(&user, identity.UserID).Error; err != nil {
			return nil, err
		}
		if !user.IsActive {
			return nil, fmt.Errorf("account is deactivated")
		}
		s.updateIdentitySnapshot(ctx, identity.ID, profile)
		return &user, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	if cfg.AutoLinkEmail {
		var user model.User
		if err := s.db.WithContext(ctx).Where("email = ?", profile.Email).First(&user).Error; err == nil {
			if !user.IsActive {
				return nil, fmt.Errorf("account is deactivated")
			}
			if err := s.createIdentity(ctx, user.ID, cfg.Provider, profile); err != nil {
				return nil, err
			}
			return &user, nil
		}
	}

	if !cfg.AllowSignup {
		return nil, fmt.Errorf("oauth signup is disabled")
	}

	tenantID, referralCode, err := s.resolveOAuthSignupInvite(ctx, st)
	if err != nil {
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte("oauth:"+cfg.Provider+":"+profile.ProviderUserID), bcryptCost)
	if err != nil {
		return nil, err
	}
	user := model.User{
		TenantID:       tenantID,
		Email:          profile.Email,
		PasswordHash:   string(hash),
		Name:           s.uniqueOAuthName(ctx, profile.Name, profile.Email),
		IsActive:       true,
		Language:       "en",
		CountryCode:    "US",
		RegisterSource: cfg.Provider,
	}
	if err := s.db.WithContext(ctx).Create(&user).Error; err != nil {
		return nil, err
	}
	authSvc := NewAuthService(s.db, s.redis, s.jwt)
	if err := authSvc.assignDefaultUserRole(ctx, user.ID); err != nil {
		return nil, err
	}
	if s.balanceSvc != nil {
		_ = s.balanceSvc.InitBalance(ctx, user.ID, user.TenantID)
	}
	if err := s.createIdentity(ctx, user.ID, cfg.Provider, profile); err != nil {
		return nil, err
	}

	if referralCode != "" {
		_ = referral.ProcessReferralOnRegister(s.db, ctx, &user, referralCode)
	}
	_, _ = referral.NewReferralService(s.db).GetOrCreateLink(ctx, user.ID, user.TenantID)
	return &user, nil
}

func (s *OAuthService) resolveOAuthSignupInvite(ctx context.Context, st oauthState) (uint, string, error) {
	inviteCode := strings.TrimSpace(st.InviteCode)
	referralCode := strings.TrimSpace(st.ReferralCode)
	requiredCode := referralCode
	if requiredCode == "" {
		requiredCode = inviteCode
	}

	if s.oauthInviteRequired(ctx) {
		if requiredCode == "" {
			return 0, "", fmt.Errorf("invite code is required")
		}
		if !s.isValidInviteOrReferralCode(ctx, requiredCode) {
			return 0, "", fmt.Errorf("invalid invite code")
		}
	}

	if inviteCode != "" {
		var tenant model.Tenant
		err := s.db.WithContext(ctx).Where("domain = ? AND is_active = ?", inviteCode, true).First(&tenant).Error
		if err == nil {
			if referralCode == "" {
				referralCode = inviteCode
			}
			return tenant.ID, referralCode, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, "", fmt.Errorf("failed to resolve invite code: %w", err)
		}
		if !s.isValidInviteOrReferralCode(ctx, inviteCode) {
			return 0, "", fmt.Errorf("invalid invite code")
		}
	}

	var defaultTenant model.Tenant
	if err := s.db.WithContext(ctx).Where("parent_id IS NULL AND level = 1").First(&defaultTenant).Error; err != nil {
		return 0, "", fmt.Errorf("no default tenant available")
	}
	if referralCode == "" {
		referralCode = inviteCode
	}
	return defaultTenant.ID, referralCode, nil
}

func (s *OAuthService) oauthInviteRequired(ctx context.Context) bool {
	var cfg model.ReferralConfig
	err := s.db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error
	return err == nil && cfg.RequireInviteCode
}

func (s *OAuthService) isValidInviteOrReferralCode(ctx context.Context, code string) bool {
	if code == "" {
		return false
	}
	var n int64
	s.db.WithContext(ctx).Table("referral_links").Where("code = ?", code).Count(&n)
	if n > 0 {
		return true
	}
	s.db.WithContext(ctx).Model(&model.User{}).Where("referral_code = ?", code).Count(&n)
	if n > 0 {
		return true
	}
	s.db.WithContext(ctx).Model(&model.Tenant{}).Where("domain = ? AND is_active = ?", code, true).Count(&n)
	return n > 0
}

func (s *OAuthService) uniqueOAuthName(ctx context.Context, name, email string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.Split(email, "@")[0]
	}
	if len([]rune(name)) < 2 {
		name = "OAuthUser"
	}
	base := name
	for i := 0; i < 20; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s_%d", base, i+1)
		}
		var count int64
		_ = s.db.WithContext(ctx).Model(&model.User{}).Where("name = ?", candidate).Count(&count).Error
		if count == 0 {
			return candidate
		}
	}
	suffix, _ := randomHex(4)
	return base + "_" + suffix
}

func (s *OAuthService) createIdentity(ctx context.Context, userID uint, provider string, profile oauthProfile) error {
	return s.db.WithContext(ctx).Create(&model.OAuthIdentity{
		UserID:         userID,
		Provider:       provider,
		ProviderUserID: profile.ProviderUserID,
		Email:          profile.Email,
		Name:           profile.Name,
		AvatarURL:      profile.AvatarURL,
	}).Error
}

func (s *OAuthService) updateIdentitySnapshot(ctx context.Context, id uint, profile oauthProfile) {
	_ = s.db.WithContext(ctx).Model(&model.OAuthIdentity{}).Where("id = ?", id).Updates(map[string]interface{}{
		"email":      profile.Email,
		"name":       profile.Name,
		"avatar_url": profile.AvatarURL,
	}).Error
}

func toOAuthProviderDTO(cfg model.OAuthProviderConfig, includeAdminFields bool) OAuthProviderDTO {
	dto := OAuthProviderDTO{
		Provider:        cfg.Provider,
		DisplayName:     cfg.DisplayName,
		IsActive:        cfg.IsActive,
		AllowSignup:     cfg.AllowSignup,
		AutoLinkEmail:   cfg.AutoLinkEmail,
		HasClientSecret: cfg.ClientSecretEncrypted != "",
	}
	if includeAdminFields {
		dto.ID = cfg.ID
		dto.ClientID = cfg.ClientID
		dto.RedirectURL = cfg.RedirectURL
		dto.FrontendRedirectURL = cfg.FrontendRedirectURL
		dto.AuthURL = cfg.AuthURL
		dto.TokenURL = cfg.TokenURL
		dto.UserInfoURL = cfg.UserInfoURL
		dto.EmailURL = cfg.EmailURL
		dto.Scopes = cfg.Scopes
		dto.SortOrder = cfg.SortOrder
	}
	return dto
}

func (s *OAuthService) saveState(ctx context.Context, state string, value oauthState) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if s.redis != nil {
		return s.redis.Set(ctx, oauthStatePrefix+state, data, oauthStateTTL).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.memoryStates[state] = value
	return nil
}

func (s *OAuthService) consumeState(ctx context.Context, state string) (oauthState, error) {
	if s.redis != nil {
		key := oauthStatePrefix + state
		data, err := s.redis.Get(ctx, key).Bytes()
		if err != nil {
			return oauthState{}, fmt.Errorf("oauth state expired or invalid")
		}
		_ = s.redis.Del(ctx, key).Err()
		var st oauthState
		if err := json.Unmarshal(data, &st); err != nil {
			return oauthState{}, err
		}
		return st, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.memoryStates[state]
	if !ok {
		return oauthState{}, fmt.Errorf("oauth state expired or invalid")
	}
	delete(s.memoryStates, state)
	return st, nil
}

func (s *OAuthService) encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	block, err := aes.NewCipher(s.encryptKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plaintext), nil)), nil
}

func (s *OAuthService) decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.encryptKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	plaintext, err := gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
