package model

// RegistrationGuard 注册风控配置
// 所有字段均可在管理后台热更新,Redis 缓存 5 分钟,变更即刻失效
// 7 层防御:CAPTCHA + OTP + IP 限流 + 邮箱域名限流 + 设备指纹 + 停留时长 + IP 情报 + 一次性邮箱黑名单
type RegistrationGuard struct {
	BaseModel
	// --- CAPTCHA 已移除 ---

	// --- 邮箱 OTP ---
	EmailOTPEnabled    bool `gorm:"default:true" json:"email_otp_enabled"`    // 是否启用邮箱 OTP
	EmailOTPLength     int  `gorm:"default:6" json:"email_otp_length"`        // OTP 位数 4~8
	EmailOTPTTLSeconds int  `gorm:"default:300" json:"email_otp_ttl_seconds"` // 过期秒数 60~1800

	// --- 限流 ---
	IPRegLimitPerHour    int `gorm:"default:5" json:"ip_reg_limit_per_hour"`    // 单 IP 每小时注册上限,0=关闭
	IPRegLimitPerDay     int `gorm:"default:20" json:"ip_reg_limit_per_day"`    // 单 IP 每天注册上限
	EmailDomainDailyMax  int `gorm:"default:50" json:"email_domain_daily_max"`  // 单邮箱域名每天注册上限

	// --- 设备指纹 ---
	FingerprintEnabled  bool `gorm:"default:true" json:"fingerprint_enabled"`  // 是否启用设备指纹
	FingerprintDailyMax int  `gorm:"default:2" json:"fingerprint_daily_max"`   // 单指纹每天注册上限

	// --- 行为 ---
	MinFormDwellSeconds int `gorm:"default:3" json:"min_form_dwell_seconds"`  // 表单最低停留秒数(<该值记 shadow,注册成功但无奖励)

	// --- IP 情报 ---
	IPReputationEnabled    bool `gorm:"default:true" json:"ip_reputation_enabled"`   // 是否启用 IP 情报
	BlockVPN               bool `gorm:"default:false" json:"block_vpn"`              // 是否拦截 VPN
	BlockTor               bool `gorm:"default:true" json:"block_tor"`               // 是否拦截 Tor 出口

	// --- 一次性邮箱 ---
	DisposableEmailBlocked bool `gorm:"default:true" json:"disposable_email_blocked"` // 是否屏蔽一次性邮箱

	// --- 全局反滥用限速 (v5.1) ---
	FreeUserRPM         int `gorm:"default:5" json:"free_user_rpm"`         // 免费用户每分钟请求数 (RPM)
	FreeUserTPM         int `gorm:"default:10000" json:"free_user_tpm"`     // 免费用户每分钟 Token 数 (TPM)
	FreeUserConcurrency int `gorm:"default:1" json:"free_user_concurrency"` // 免费用户并发限制

	// --- 总开关 ---
	IsActive bool `gorm:"default:true" json:"is_active"`
}

// TableName 指定表名
func (RegistrationGuard) TableName() string {
	return "registration_guards"
}
