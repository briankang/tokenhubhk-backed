package model

// OAuthProviderConfig 保存第三方 OAuth 登录配置，client_secret 使用 AES-256-GCM 加密存储。
type OAuthProviderConfig struct {
	BaseModel
	Provider              string `gorm:"type:varchar(32);uniqueIndex;not null" json:"provider"`
	DisplayName           string `gorm:"type:varchar(80);not null" json:"display_name"`
	ClientID              string `gorm:"type:varchar(255);not null" json:"client_id"`
	ClientSecretEncrypted string `gorm:"type:text" json:"-"`
	RedirectURL           string `gorm:"type:varchar(500);not null" json:"redirect_url"`
	FrontendRedirectURL   string `gorm:"type:varchar(500)" json:"frontend_redirect_url"`
	AuthURL               string `gorm:"type:varchar(500);not null" json:"auth_url"`
	TokenURL              string `gorm:"type:varchar(500);not null" json:"token_url"`
	UserInfoURL           string `gorm:"type:varchar(500);not null" json:"userinfo_url"`
	EmailURL              string `gorm:"type:varchar(500)" json:"email_url"`
	Scopes                string `gorm:"type:varchar(500);not null" json:"scopes"`
	IsActive              bool   `gorm:"default:false" json:"is_active"`
	AllowSignup           bool   `gorm:"default:true" json:"allow_signup"`
	AutoLinkEmail         bool   `gorm:"default:true" json:"auto_link_email"`
	SortOrder             int    `gorm:"default:0" json:"sort_order"`
}

func (OAuthProviderConfig) TableName() string { return "oauth_provider_configs" }

// OAuthIdentity 记录第三方账号与本地用户的绑定关系。
type OAuthIdentity struct {
	BaseModel
	UserID         uint   `gorm:"not null;index" json:"user_id"`
	Provider       string `gorm:"type:varchar(32);not null;uniqueIndex:uidx_oauth_identity" json:"provider"`
	ProviderUserID string `gorm:"type:varchar(255);not null;uniqueIndex:uidx_oauth_identity" json:"provider_user_id"`
	Email          string `gorm:"type:varchar(255);index" json:"email"`
	Name           string `gorm:"type:varchar(100)" json:"name"`
	AvatarURL      string `gorm:"type:varchar(500)" json:"avatar_url"`

	User User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (OAuthIdentity) TableName() string { return "oauth_identities" }
