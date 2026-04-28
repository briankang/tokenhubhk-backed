package model

import "time"

// SMSProviderConfig 保存阿里云短信服务配置，AccessKeySecret 使用 AES-256-GCM 加密。
type SMSProviderConfig struct {
	BaseModel
	Provider                 string `gorm:"type:varchar(32);uniqueIndex;default:'aliyun'" json:"provider"`
	AccessKeyID              string `gorm:"type:varchar(255)" json:"access_key_id"`
	AccessKeySecretEncrypted string `gorm:"type:text" json:"-"`
	RegionID                 string `gorm:"type:varchar(64);default:'cn-hangzhou'" json:"region_id"`
	Endpoint                 string `gorm:"type:varchar(255);default:'dysmsapi.aliyuncs.com'" json:"endpoint"`
	SignName                 string `gorm:"type:varchar(128)" json:"sign_name"`
	TemplateCode             string `gorm:"type:varchar(64);default:'SMS_505710272'" json:"template_code"`
	TemplateParamName        string `gorm:"type:varchar(32);default:'code'" json:"template_param_name"`
	IsActive                 bool   `gorm:"default:false" json:"is_active"`
}

func (SMSProviderConfig) TableName() string { return "sms_provider_configs" }

// CaptchaProviderConfig 保存阿里云验证码 2.0 配置。
type CaptchaProviderConfig struct {
	BaseModel
	Provider                 string `gorm:"type:varchar(32);uniqueIndex;default:'aliyun'" json:"provider"`
	AccessKeyID              string `gorm:"type:varchar(255)" json:"access_key_id"`
	AccessKeySecretEncrypted string `gorm:"type:text" json:"-"`
	EKeyEncrypted            string `gorm:"type:text" json:"-"`
	RegionID                 string `gorm:"type:varchar(64);default:'cn-hangzhou'" json:"region_id"`
	Endpoint                 string `gorm:"type:varchar(255);default:'captcha.cn-shanghai.aliyuncs.com'" json:"endpoint"`
	SceneID                  string `gorm:"type:varchar(128)" json:"scene_id"`
	Prefix                   string `gorm:"type:varchar(128)" json:"prefix"`
	EncryptMode              bool   `gorm:"default:false" json:"encrypt_mode"`
	EncryptedSceneTTLSeconds int    `gorm:"default:3600" json:"encrypted_scene_ttl_seconds"`
	IsActive                 bool   `gorm:"default:false" json:"is_active"`
}

func (CaptchaProviderConfig) TableName() string { return "captcha_provider_configs" }

// SMSRiskConfig 保存短信发送风控策略。
type SMSRiskConfig struct {
	BaseModel
	IsActive              bool `gorm:"default:true" json:"is_active"`
	CodeTTLSeconds        int  `gorm:"default:300" json:"code_ttl_seconds"`
	SendCooldownSeconds   int  `gorm:"default:60" json:"send_cooldown_seconds"`
	PhoneHourlyLimit      int  `gorm:"default:5" json:"phone_hourly_limit"`
	PhoneDailyLimit       int  `gorm:"default:10" json:"phone_daily_limit"`
	IPHourlyLimit         int  `gorm:"default:20" json:"ip_hourly_limit"`
	IPDailyLimit          int  `gorm:"default:100" json:"ip_daily_limit"`
	FingerprintDailyLimit int  `gorm:"default:10" json:"fingerprint_daily_limit"`
	MaxVerifyAttempts     int  `gorm:"default:5" json:"max_verify_attempts"`
	FreezeMinutes         int  `gorm:"default:15" json:"freeze_minutes"`
	RequireCaptchaOnRisk  bool `gorm:"default:true" json:"require_captcha_on_risk"`
	RequireCaptchaAlways  bool `gorm:"default:false" json:"require_captcha_always"`
	BlockVirtualPrefixes  bool `gorm:"default:true" json:"block_virtual_prefixes"`
}

func (SMSRiskConfig) TableName() string { return "sms_risk_configs" }

// PhoneOTPToken 保存手机号 OTP 验证码的 hash。
type PhoneOTPToken struct {
	BaseModel
	PhoneE164   string     `gorm:"type:varchar(20);index;not null" json:"phone_e164"`
	TokenHash   string     `gorm:"type:varchar(120);not null" json:"-"`
	Purpose     string     `gorm:"type:varchar(30);default:'LOGIN';index" json:"purpose"`
	ExpiresAt   time.Time  `gorm:"index;not null" json:"expires_at"`
	UsedAt      *time.Time `json:"used_at,omitempty"`
	Attempts    int        `gorm:"default:0" json:"attempts"`
	MaxAttempts int        `gorm:"default:5" json:"max_attempts"`
	IP          string     `gorm:"type:varchar(45);index" json:"ip"`
	Fingerprint string     `gorm:"type:varchar(100);index" json:"fingerprint,omitempty"`
}

func (PhoneOTPToken) TableName() string { return "phone_otp_tokens" }

// SMSSendLog 记录短信发送结果，用于成本审计和无 Redis 时的降级限流。
type SMSSendLog struct {
	BaseModel
	PhoneE164         string `gorm:"type:varchar(20);index" json:"phone_e164"`
	MaskedPhone       string `gorm:"type:varchar(32)" json:"masked_phone"`
	Purpose           string `gorm:"type:varchar(30);index" json:"purpose"`
	Provider          string `gorm:"type:varchar(32);default:'aliyun'" json:"provider"`
	ProviderRequestID string `gorm:"type:varchar(128)" json:"provider_request_id,omitempty"`
	Status            string `gorm:"type:varchar(20);index" json:"status"` // sent/failed/blocked
	LimitType         string `gorm:"type:varchar(64);index" json:"limit_type,omitempty"`
	ErrorCode         string `gorm:"type:varchar(64)" json:"error_code,omitempty"`
	ErrorMessage      string `gorm:"type:text" json:"error_message,omitempty"`
	IP                string `gorm:"type:varchar(45);index" json:"ip"`
	Fingerprint       string `gorm:"type:varchar(100);index" json:"fingerprint,omitempty"`
}

func (SMSSendLog) TableName() string { return "sms_send_logs" }

// PhoneRiskRule 保存接码平台、虚拟号段等手机号风控规则。
type PhoneRiskRule struct {
	BaseModel
	RuleType string `gorm:"type:varchar(20);index;not null" json:"rule_type"` // prefix/exact
	Pattern  string `gorm:"type:varchar(64);index;not null" json:"pattern"`
	Reason   string `gorm:"type:varchar(255)" json:"reason,omitempty"`
	IsActive bool   `gorm:"default:true" json:"is_active"`
}

func (PhoneRiskRule) TableName() string { return "phone_risk_rules" }
