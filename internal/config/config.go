package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// ServiceRole 服务角色常量
const (
	RoleGateway  = "gateway"  // API 网关 / 模型中转
	RoleBackend  = "backend"  // 用户 + 管理后台
	RoleWorker   = "worker"   // 后台任务 + 重操作执行器
	RoleMonolith = ""         // 单体模式（全功能，向后兼容）
)

// AppConfig 应用程序全局配置结构体
type AppConfig struct {
	Service   ServiceConfig   `mapstructure:"service"`
	Server    ServerConfig    `mapstructure:"server"`
	Database  DatabaseConfig  `mapstructure:"database"`
	Redis     RedisConfig     `mapstructure:"redis"`
	JWT       JWTConfig       `mapstructure:"jwt"`
	Log       LogConfig       `mapstructure:"log"`
	RateLimit RateLimitConfig `mapstructure:"ratelimit"`
	I18n      I18nConfig      `mapstructure:"i18n"`
	Payment   PaymentConfig   `mapstructure:"payment"`
	ExchangeRate ExchangeRateConfig `mapstructure:"exchange_rate"`
	Geo       GeoConfig       `mapstructure:"geo"`
	Support   SupportConfig   `mapstructure:"support"`
}

// GeoConfig IP 地理位置服务配置
// 主源：阿里云市场 cmapi021970（https://c2ba.api.huachen.cn/ip）
// 兜底：6 个免费 API 并发竞速（ip-api / ipinfo / ip.sb / ipwho.is / country.is / geojs.io）
type GeoConfig struct {
	AliyunURL             string `mapstructure:"aliyun_url"`                 // https://c2ba.api.huachen.cn/ip
	AppCode               string `mapstructure:"appcode"`                    // 阿里云市场 APPCODE
	CacheTTL              int    `mapstructure:"cache_ttl"`                  // 秒，默认 31536000 (1年)
	AliyunTimeoutMs       int    `mapstructure:"aliyun_timeout_ms"`          // 毫秒，阿里云主源超时，默认 2500
	FallbackTimeoutMs     int    `mapstructure:"fallback_timeout_ms"`        // 毫秒，免费源并发竞速总超时，默认 3000
	SingleProviderTimeout int    `mapstructure:"single_provider_timeout_ms"` // 毫秒，单个免费源超时，默认 2500
}

// SupportConfig AI 客服系统配置
type SupportConfig struct {
	Enabled               bool   `mapstructure:"enabled"`                 // 总开关，默认 true
	InternalBaseURL       string `mapstructure:"internal_base_url"`       // 内部调用 /v1/chat/completions 的基础 URL（默认 http://localhost:8080）
	InternalAPIKey        string `mapstructure:"internal_api_key"`        // 系统级 API Key（调自家 embedding / chat）
	EmbeddingModel        string `mapstructure:"embedding_model"`         // 默认 text-embedding-v3
	BudgetMonthlyCredits  int64  `mapstructure:"budget_monthly_credits"`  // 月预算（积分，默认 5000000 = ¥500）
	BudgetEconomyPct      int    `mapstructure:"budget_economy_pct"`      // 降级阈值 %（默认 30）
	BudgetEmergencyPct    int    `mapstructure:"budget_emergency_pct"`    // 熔断阈值 %（默认 5）
	RagTopK               int    `mapstructure:"rag_top_k"`               // 默认 5
	RagScoreThreshold     float32 `mapstructure:"rag_score_threshold"`    // 默认 0.5
	RagMultiSource        bool   `mapstructure:"rag_multi_source"`        // 默认 true
	MaxHistoryTurns       int    `mapstructure:"max_history_turns"`       // 默认 8
	MaxMsgLen             int    `mapstructure:"max_msg_len"`             // 默认 2000
	MemoryTopK            int    `mapstructure:"memory_top_k"`            // 默认 10
	TicketSLAHours        int    `mapstructure:"ticket_sla_hours"`        // 默认 24
	ProviderDocsTopK      int    `mapstructure:"provider_docs_top_k"`     // 默认 3
	RateLimitPerHour      int    `mapstructure:"rate_limit_per_hour"`     // 用户级 chat 限流（默认 60）
	TicketRateLimitPerDay int    `mapstructure:"ticket_rate_limit_per_day"` // 默认 20
}

// WithSupportDefaults 把零值字段填充为业务默认值
func (c *SupportConfig) WithDefaults() SupportConfig {
	out := *c
	if out.EmbeddingModel == "" {
		out.EmbeddingModel = "text-embedding-v3"
	}
	if out.InternalBaseURL == "" {
		out.InternalBaseURL = "http://localhost:8080"
	}
	if out.BudgetMonthlyCredits <= 0 {
		out.BudgetMonthlyCredits = 5000000
	}
	if out.BudgetEconomyPct <= 0 {
		out.BudgetEconomyPct = 30
	}
	if out.BudgetEmergencyPct <= 0 {
		out.BudgetEmergencyPct = 5
	}
	if out.RagTopK <= 0 {
		out.RagTopK = 5
	}
	if out.RagScoreThreshold <= 0 {
		out.RagScoreThreshold = 0.5
	}
	if out.MaxHistoryTurns <= 0 {
		out.MaxHistoryTurns = 8
	}
	if out.MaxMsgLen <= 0 {
		out.MaxMsgLen = 2000
	}
	if out.MemoryTopK <= 0 {
		out.MemoryTopK = 10
	}
	if out.TicketSLAHours <= 0 {
		out.TicketSLAHours = 24
	}
	if out.ProviderDocsTopK <= 0 {
		out.ProviderDocsTopK = 3
	}
	if out.RateLimitPerHour <= 0 {
		out.RateLimitPerHour = 60
	}
	if out.TicketRateLimitPerDay <= 0 {
		out.TicketRateLimitPerDay = 20
	}
	return out
}

// ExchangeRateConfig 汇率服务配置
type ExchangeRateConfig struct {
	PrimaryURL     string  `mapstructure:"primary_url"`      // 阿里云市场 cmapi00064402
	BackupURL      string  `mapstructure:"backup_url"`       // 阿里云市场 cmapi00063890
	PublicURL      string  `mapstructure:"public_url"`       // 第三级兜底公开免费接口 (open.er-api.com)
	AppCode        string  `mapstructure:"appcode"`
	AppKey         string  `mapstructure:"appkey"`
	AppSecret      string  `mapstructure:"appsecret"`
	CacheTTL       int     `mapstructure:"cache_ttl"`        // seconds，默认 86400
	DefaultRate    float64 `mapstructure:"default_rate"`     // 默认 USD→CNY 7.2
	RequestTimeout int     `mapstructure:"request_timeout"`  // seconds，默认 10
}

// ServiceConfig 服务角色配置
type ServiceConfig struct {
	Role          string `mapstructure:"role"`           // gateway / backend / worker / ""(monolith)
	RunMigrations bool   `mapstructure:"run_migrations"` // 是否执行数据库迁移（仅 backend 为 true）
	TaskSignKey   string `mapstructure:"task_sign_key"`  // Redis Stream 任务签名密钥
}

// IsGateway 判断是否为 API 网关角色
func (s *ServiceConfig) IsGateway() bool { return s.Role == RoleGateway }

// IsBackend 判断是否为后台服务角色
func (s *ServiceConfig) IsBackend() bool { return s.Role == RoleBackend }

// IsWorker 判断是否为后台 Worker 角色
func (s *ServiceConfig) IsWorker() bool { return s.Role == RoleWorker }

// IsMonolith 判断是否为单体模式（默认）
func (s *ServiceConfig) IsMonolith() bool { return s.Role == RoleMonolith }

// ShouldRunMigrations 判断是否应执行数据库迁移
func (s *ServiceConfig) ShouldRunMigrations() bool {
	return s.RunMigrations || s.IsMonolith()
}

// ShouldRunScheduler 判断是否应启动定时任务调度器
func (s *ServiceConfig) ShouldRunScheduler() bool {
	return s.IsWorker() || s.IsMonolith()
}

// PaymentConfig 支付网关配置集合
type PaymentConfig struct {
	Wechat WechatPayConfig `mapstructure:"wechat"`
	Alipay AlipayConfig    `mapstructure:"alipay"`
	Stripe StripeConfig    `mapstructure:"stripe"`
	PayPal PayPalConfig    `mapstructure:"paypal"`
}

// WechatPayConfig 微信支付配置
type WechatPayConfig struct {
	AppID          string `mapstructure:"app_id"`
	MchID          string `mapstructure:"mch_id"`
	APIKey         string `mapstructure:"api_key"`      // V2 API 密钥（兼容老接入）
	APIV3Key       string `mapstructure:"api_v3_key"`   // V3 API 密钥（32 字节，用于 AEAD_AES_256_GCM 回调解密）
	CertSerialNo   string `mapstructure:"cert_serial_no"`
	PrivateKeyPath string `mapstructure:"private_key_path"`
	NotifyURL      string `mapstructure:"notify_url"`
}

// AlipayConfig 支付宝配置
type AlipayConfig struct {
	AppID           string `mapstructure:"app_id"`
	PrivateKey      string `mapstructure:"private_key"`
	AlipayPublicKey string `mapstructure:"alipay_public_key"`
	NotifyURL       string `mapstructure:"notify_url"`
}

// StripeConfig Stripe支付配置
type StripeConfig struct {
	SecretKey     string `mapstructure:"secret_key"`
	WebhookSecret string `mapstructure:"webhook_secret"`
}

// PayPalConfig PayPal支付配置
type PayPalConfig struct {
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	WebhookID    string `mapstructure:"webhook_id"` // v3.2: Webhook 签名验证必填（PayPal 控制台 Webhooks 配置获取）
	Sandbox      bool   `mapstructure:"sandbox"`
}

// ServerConfig HTTP服务器配置
type ServerConfig struct {
	Port int    `mapstructure:"port"`
	Mode string `mapstructure:"mode"`
}

// DatabaseConfig MySQL数据库连接配置
type DatabaseConfig struct {
	Host            string `mapstructure:"host"`
	Port            int    `mapstructure:"port"`
	User            string `mapstructure:"user"`
	Password        string `mapstructure:"password"`
	DBName          string `mapstructure:"dbname"`
	MaxOpenConns    int    `mapstructure:"max_open_conns"`
	MaxIdleConns    int    `mapstructure:"max_idle_conns"`
	ConnMaxLifetime int    `mapstructure:"conn_max_lifetime"` // seconds
}

// DSN 返回MySQL数据源名称连接字符串
func (d *DatabaseConfig) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=True&loc=Local",
		d.User, d.Password, d.Host, d.Port, d.DBName)
}

// ConnMaxLifetimeDuration 将ConnMaxLifetime转换为time.Duration类型
func (d *DatabaseConfig) ConnMaxLifetimeDuration() time.Duration {
	return time.Duration(d.ConnMaxLifetime) * time.Second
}

// RedisConfig Redis连接配置
// Username: 可选，阿里云 Tair/Redis ACL 模式需要；开源 Redis 6+ 也支持 ACL
// 留空时走传统 AUTH password 模式（兼容社区版 Redis 5/6 + 阿里云 Tair 默认账号）
type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

// JWTConfig holds JWT settings.
type JWTConfig struct {
	Secret string `mapstructure:"secret"`
	Expire int    `mapstructure:"expire"` // hours
}

// ExpireDuration 将Expire转换为time.Duration类型
func (j *JWTConfig) ExpireDuration() time.Duration {
	return time.Duration(j.Expire) * time.Hour
}

// LogConfig 日志配置
type LogConfig struct {
	Level      string `mapstructure:"level"`
	Dir        string `mapstructure:"dir"`
	MaxSize    int    `mapstructure:"max_size"`    // MB
	MaxAge     int    `mapstructure:"max_age"`     // days
	MaxBackups int    `mapstructure:"max_backups"`
}

// RateLimitConfig 限流配置
type RateLimitConfig struct {
	Enabled       bool `mapstructure:"enabled"`
	MaxRequests   int  `mapstructure:"max_requests"`
	WindowSeconds int  `mapstructure:"window_seconds"`
}

// I18nConfig holds internationalization settings.
type I18nConfig struct {
	DefaultLang string `mapstructure:"default_lang"`
	LocalesDir  string `mapstructure:"locales_dir"`
}

// Global 全局应用配置实例
var Global AppConfig

// Load 从配置文件和环境变量加载配置
func Load(cfgFile string) error {
	if cfgFile == "" {
		cfgFile = "configs/config.yaml"
	}

	viper.SetConfigFile(cfgFile)
	viper.SetConfigType("yaml")

	// 环境变量支持：例如 DATABASE_HOST 覆盖 database.host
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// 为Docker部署显式绑定环境变量
	bindEnvVars()

	// 读取配置文件
	if err := viper.ReadInConfig(); err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	if err := viper.Unmarshal(&Global); err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// 应用默认值
	applyDefaults()

	return nil
}

func applyDefaults() {
	if Global.Server.Port == 0 {
		Global.Server.Port = 8080
	}
	if Global.Server.Mode == "" {
		Global.Server.Mode = "debug"
	}
	if Global.Database.MaxOpenConns == 0 {
		Global.Database.MaxOpenConns = 100
	}
	if Global.Database.MaxIdleConns == 0 {
		Global.Database.MaxIdleConns = 10
	}
	if Global.Database.ConnMaxLifetime == 0 {
		Global.Database.ConnMaxLifetime = 3600
	}
	if Global.JWT.Expire == 0 {
		Global.JWT.Expire = 24
	}
	if Global.Log.Level == "" {
		Global.Log.Level = "info"
	}
	if Global.Log.Dir == "" {
		Global.Log.Dir = "./logs"
	}
	if Global.Log.MaxSize == 0 {
		Global.Log.MaxSize = 100
	}
	if Global.Log.MaxAge == 0 {
		Global.Log.MaxAge = 30
	}
	if Global.Log.MaxBackups == 0 {
		Global.Log.MaxBackups = 5
	}
	if Global.I18n.DefaultLang == "" {
		Global.I18n.DefaultLang = "en"
	}
	// v3.2.2: 汇率服务默认 URL
	// 主接口 = 阿里云云市场 cmapi00064402 (jmhlcx)
	// 备用   = 阿里云云市场 cmapi00063890 (smkjzgyhss - 中国银行实时汇率)
	// 公开   = open.er-api.com (L3 兜底，无需 AppCode)
	if Global.ExchangeRate.PrimaryURL == "" {
		Global.ExchangeRate.PrimaryURL = "https://jmhlcx.market.alicloudapi.com/exchange-rate/convert"
	}
	if Global.ExchangeRate.BackupURL == "" {
		Global.ExchangeRate.BackupURL = "https://smkjzgyhss.market.alicloudapi.com/exchange_rate/realtime"
	}
	if Global.ExchangeRate.PublicURL == "" {
		Global.ExchangeRate.PublicURL = "https://open.er-api.com/v6/latest/USD"
	}
	if Global.ExchangeRate.AppCode == "" {
		Global.ExchangeRate.AppCode = "dcbcacff20e7413ab50231113b364655"
	}
	if Global.ExchangeRate.AppCode == "" {
		Global.ExchangeRate.AppCode = "dcbcacff20e7413ab50231113b364655"
	}
	if Global.ExchangeRate.CacheTTL == 0 {
		Global.ExchangeRate.CacheTTL = 86400 // 24h
	}
	if Global.ExchangeRate.DefaultRate == 0 {
		Global.ExchangeRate.DefaultRate = 7.2
	}
	if Global.ExchangeRate.RequestTimeout == 0 {
		Global.ExchangeRate.RequestTimeout = 10
	}

	// Geo 默认值：阿里云市场 cmapi021970 作为主源，免费源作为兜底
	if Global.Geo.AliyunURL == "" {
		Global.Geo.AliyunURL = "https://c2ba.api.huachen.cn/ip"
	}
	if Global.Geo.AppCode == "" {
		Global.Geo.AppCode = "dcbcacff20e7413ab50231113b364655"
	}
	if Global.Geo.CacheTTL == 0 {
		Global.Geo.CacheTTL = 31536000 // 1 年
	}
	if Global.Geo.AliyunTimeoutMs == 0 {
		Global.Geo.AliyunTimeoutMs = 2500
	}
	if Global.Geo.FallbackTimeoutMs == 0 {
		Global.Geo.FallbackTimeoutMs = 3000
	}
	if Global.Geo.SingleProviderTimeout == 0 {
		Global.Geo.SingleProviderTimeout = 2500
	}
}

// bindEnvVars 为Docker部署显式绑定环境变量
func bindEnvVars() {
	// 服务角色
	_ = viper.BindEnv("service.role", "SERVICE_ROLE")
	_ = viper.BindEnv("service.run_migrations", "RUN_MIGRATIONS")
	_ = viper.BindEnv("service.task_sign_key", "TASK_SIGN_KEY")

	_ = viper.BindEnv("server.port", "SERVER_PORT")
	_ = viper.BindEnv("server.mode", "SERVER_MODE")
	_ = viper.BindEnv("database.host", "DATABASE_HOST")
	_ = viper.BindEnv("database.port", "DATABASE_PORT")
	_ = viper.BindEnv("database.user", "DATABASE_USER")
	_ = viper.BindEnv("database.password", "DATABASE_PASSWORD")
	_ = viper.BindEnv("database.dbname", "DATABASE_DBNAME")
	_ = viper.BindEnv("database.max_open_conns", "DATABASE_MAX_OPEN_CONNS")
	_ = viper.BindEnv("database.max_idle_conns", "DATABASE_MAX_IDLE_CONNS")
	_ = viper.BindEnv("redis.addr", "REDIS_ADDR")
	_ = viper.BindEnv("redis.username", "REDIS_USERNAME")
	_ = viper.BindEnv("redis.password", "REDIS_PASSWORD")
	_ = viper.BindEnv("redis.db", "REDIS_DB")
	_ = viper.BindEnv("jwt.secret", "JWT_SECRET")
	_ = viper.BindEnv("jwt.expire", "JWT_EXPIRE")
	_ = viper.BindEnv("log.level", "LOG_LEVEL")
	_ = viper.BindEnv("log.dir", "LOG_DIR")
	_ = viper.BindEnv("i18n.default_lang", "I18N_DEFAULTLANG")
	_ = viper.BindEnv("i18n.locales_dir", "I18N_LOCALESDIR")

	// 支付网关环境变量绑定
	_ = viper.BindEnv("payment.wechat.app_id", "PAYMENT_WECHAT_APP_ID")
	_ = viper.BindEnv("payment.wechat.mch_id", "PAYMENT_WECHAT_MCH_ID")
	_ = viper.BindEnv("payment.wechat.api_key", "PAYMENT_WECHAT_API_KEY")
	_ = viper.BindEnv("payment.wechat.cert_serial_no", "PAYMENT_WECHAT_CERT_SERIAL_NO")
	_ = viper.BindEnv("payment.wechat.private_key_path", "PAYMENT_WECHAT_PRIVATE_KEY_PATH")
	_ = viper.BindEnv("payment.wechat.notify_url", "PAYMENT_WECHAT_NOTIFY_URL")
	_ = viper.BindEnv("payment.alipay.app_id", "PAYMENT_ALIPAY_APP_ID")
	_ = viper.BindEnv("payment.alipay.private_key", "PAYMENT_ALIPAY_PRIVATE_KEY")
	_ = viper.BindEnv("payment.alipay.alipay_public_key", "PAYMENT_ALIPAY_PUBLIC_KEY")
	_ = viper.BindEnv("payment.alipay.notify_url", "PAYMENT_ALIPAY_NOTIFY_URL")
	_ = viper.BindEnv("payment.stripe.secret_key", "PAYMENT_STRIPE_SECRET_KEY")
	_ = viper.BindEnv("payment.stripe.webhook_secret", "PAYMENT_STRIPE_WEBHOOK_SECRET")
	_ = viper.BindEnv("payment.paypal.client_id", "PAYMENT_PAYPAL_CLIENT_ID")
	_ = viper.BindEnv("payment.paypal.client_secret", "PAYMENT_PAYPAL_CLIENT_SECRET")
	_ = viper.BindEnv("payment.paypal.sandbox", "PAYMENT_PAYPAL_SANDBOX")

	// AI 客服环境变量绑定
	_ = viper.BindEnv("support.enabled", "SUPPORT_ENABLED")
	_ = viper.BindEnv("support.internal_base_url", "SUPPORT_INTERNAL_BASE_URL")
	_ = viper.BindEnv("support.internal_api_key", "SUPPORT_INTERNAL_API_KEY")
	_ = viper.BindEnv("support.embedding_model", "SUPPORT_EMBEDDING_MODEL")
	_ = viper.BindEnv("support.budget_monthly_credits", "SUPPORT_BUDGET_MONTHLY_CREDITS")

	// 汇率服务环境变量绑定
	_ = viper.BindEnv("exchange_rate.primary_url", "EXCHANGE_RATE_PRIMARY_URL")
	_ = viper.BindEnv("exchange_rate.backup_url", "EXCHANGE_RATE_BACKUP_URL")
	_ = viper.BindEnv("exchange_rate.public_url", "EXCHANGE_RATE_PUBLIC_URL")
	_ = viper.BindEnv("exchange_rate.appcode", "EXCHANGE_RATE_APPCODE")
	_ = viper.BindEnv("exchange_rate.appkey", "EXCHANGE_RATE_APPKEY")
	_ = viper.BindEnv("exchange_rate.appsecret", "EXCHANGE_RATE_APPSECRET")
	_ = viper.BindEnv("exchange_rate.cache_ttl", "EXCHANGE_RATE_CACHE_TTL")
	_ = viper.BindEnv("exchange_rate.default_rate", "EXCHANGE_RATE_DEFAULT_RATE")
}
