package database

import (
	"errors"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

type oauthProviderSeed struct {
	Provider            string
	DisplayName         string
	RedirectURL         string
	FrontendRedirectURL string
	AuthURL             string
	TokenURL            string
	UserInfoURL         string
	EmailURL            string
	Scopes              string
	SortOrder           int
}

var oauthProviderSeeds = []oauthProviderSeed{
	{
		Provider:            "google",
		DisplayName:         "Google",
		RedirectURL:         "https://www.tokenhubhk.com/api/v1/auth/oauth/google/callback",
		FrontendRedirectURL: "https://www.tokenhubhk.com/oauth/callback",
		AuthURL:             "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:            "https://oauth2.googleapis.com/token",
		UserInfoURL:         "https://openidconnect.googleapis.com/v1/userinfo",
		Scopes:              "openid email profile",
		SortOrder:           10,
	},
	{
		Provider:            "github",
		DisplayName:         "GitHub",
		RedirectURL:         "https://www.tokenhubhk.com/api/v1/auth/oauth/github/callback",
		FrontendRedirectURL: "https://www.tokenhubhk.com/oauth/callback",
		AuthURL:             "https://github.com/login/oauth/authorize",
		TokenURL:            "https://github.com/login/oauth/access_token",
		UserInfoURL:         "https://api.github.com/user",
		EmailURL:            "https://api.github.com/user/emails",
		Scopes:              "read:user user:email",
		SortOrder:           20,
	},
}

// RunSeedOAuthProviders inserts disabled Google/GitHub login defaults for the admin config page.
func RunSeedOAuthProviders(db *gorm.DB) {
	if db == nil {
		return
	}

	inserted := 0
	skipped := 0
	updated := 0
	for _, seed := range oauthProviderSeeds {
		var existing model.OAuthProviderConfig
		err := db.Where("provider = ?", seed.Provider).First(&existing).Error
		if err == nil {
			if existing.ClientID == "" && existing.ClientSecretEncrypted == "" {
				if err := db.Model(&existing).Updates(map[string]interface{}{
					"display_name":          seed.DisplayName,
					"redirect_url":          seed.RedirectURL,
					"frontend_redirect_url": seed.FrontendRedirectURL,
					"auth_url":              seed.AuthURL,
					"token_url":             seed.TokenURL,
					"user_info_url":         seed.UserInfoURL,
					"email_url":             seed.EmailURL,
					"scopes":                seed.Scopes,
					"allow_signup":          true,
					"auto_link_email":       true,
					"sort_order":            seed.SortOrder,
				}).Error; err != nil {
					if logger.L != nil {
						logger.L.Warn("oauth provider seed update failed",
							zap.String("provider", seed.Provider),
							zap.Error(err),
						)
					}
				} else {
					updated++
				}
			}
			skipped++
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			if logger.L != nil {
				logger.L.Warn("oauth provider seed lookup failed",
					zap.String("provider", seed.Provider),
					zap.Error(err),
				)
			}
			continue
		}
		if err := db.Create(&model.OAuthProviderConfig{
			Provider:              seed.Provider,
			DisplayName:           seed.DisplayName,
			ClientID:              "",
			ClientSecretEncrypted: "",
			RedirectURL:           seed.RedirectURL,
			FrontendRedirectURL:   seed.FrontendRedirectURL,
			AuthURL:               seed.AuthURL,
			TokenURL:              seed.TokenURL,
			UserInfoURL:           seed.UserInfoURL,
			EmailURL:              seed.EmailURL,
			Scopes:                seed.Scopes,
			IsActive:              false,
			AllowSignup:           true,
			AutoLinkEmail:         true,
			SortOrder:             seed.SortOrder,
		}).Error; err != nil {
			if logger.L != nil {
				logger.L.Warn("oauth provider seed create failed",
					zap.String("provider", seed.Provider),
					zap.Error(err),
				)
			}
			continue
		}
		inserted++
	}

	if logger.L != nil {
		logger.L.Info("oauth provider config seeds done",
			zap.Int("inserted", inserted),
			zap.Int("updated", updated),
			zap.Int("skipped", skipped),
			zap.Int("total", len(oauthProviderSeeds)),
		)
	}
}
