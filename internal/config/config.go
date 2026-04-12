package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// AppConfig 应用程序全局配置结构体
type AppConfig struct {
	Server    ServerConfig    `mapstructure:"server"`
	Database  DatabaseConfig  `mapstructure:"database"`
	Redis     RedisConfig     `mapstructure:"redis"`
	JWT       JWTConfig       `mapstructure:"jwt"`
	Log       LogConfig       `mapstructure:"log"`
	RateLimit RateLimitConfig `mapstructure:"ratelimit"`
	I18n      I18nConfig      `mapstructure:"i18n"`
	Payment   PaymentConfig   `mapstructure:"payment"`
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
	AppID         string `mapstructure:"app_id"`
	MchID         string `mapstructure:"mch_id"`
	APIKey        string `mapstructure:"api_key"`
	CertSerialNo  string `mapstructure:"cert_serial_no"`
	PrivateKeyPath string `mapstructure:"private_key_path"`
	NotifyURL     string `mapstructure:"notify_url"`
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
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		d.User, d.Password, d.Host, d.Port, d.DBName)
}

// ConnMaxLifetimeDuration 将ConnMaxLifetime转换为time.Duration类型
func (d *DatabaseConfig) ConnMaxLifetimeDuration() time.Duration {
	return time.Duration(d.ConnMaxLifetime) * time.Second
}

// RedisConfig Redis连接配置
type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
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
}

// bindEnvVars 为Docker部署显式绑定环境变量
func bindEnvVars() {
	_ = viper.BindEnv("server.port", "SERVER_PORT")
	_ = viper.BindEnv("server.mode", "SERVER_MODE")
	_ = viper.BindEnv("database.host", "DATABASE_HOST")
	_ = viper.BindEnv("database.port", "DATABASE_PORT")
	_ = viper.BindEnv("database.user", "DATABASE_USER")
	_ = viper.BindEnv("database.password", "DATABASE_PASSWORD")
	_ = viper.BindEnv("database.dbname", "DATABASE_DBNAME")
	_ = viper.BindEnv("redis.addr", "REDIS_ADDR")
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
}
