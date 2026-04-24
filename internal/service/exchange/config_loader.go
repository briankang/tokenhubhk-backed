package exchange

import (
	"strconv"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// ============================================================
// 汇率服务配置 DB 加载器（v3.2.3）
//
// 从 system_configs 表读取 exchange_rate.* 配置项，覆盖 fallback 中的默认值。
// 任何 DB 读取失败或 key 缺失都回退到 fallback，保证服务不因配置缺失停机。
// ============================================================

// 配置键常量（与 seed_exchange_rate.go 的 exchangeRateSeedData 对齐）
const (
	CfgKeyPrimaryURL         = "exchange_rate.primary_url"
	CfgKeyBackupURL          = "exchange_rate.backup_url"
	CfgKeyPublicURL          = "exchange_rate.public_url"
	CfgKeyAppCode            = "exchange_rate.appcode"
	CfgKeyAppKey             = "exchange_rate.appkey"
	CfgKeyAppSecretEncrypted = "exchange_rate.appsecret_encrypted"
	CfgKeyCacheTTL           = "exchange_rate.cache_ttl"
	CfgKeyDefaultRate        = "exchange_rate.default_rate"
	CfgKeyRequestTimeout     = "exchange_rate.request_timeout"
)

// DecryptFn AppSecret 解密函数签名（由调用方提供，通常是 paymentsvc.PaymentConfigService.DecryptCiphertext）
type DecryptFn func(encoded string) (string, error)

// LoadConfigFromDB 从 system_configs 表构造 Config
//
// 逻辑：
//  1. 对每个 key：DB 有记录 → 覆盖 fallback 的对应字段
//  2. DB 无记录或 key 不存在 → 保留 fallback 值（来自 config.Global）
//  3. 敏感字段 exchange_rate.appsecret_encrypted 使用 decryptFn 解密
//  4. 所有错误都降级处理，不 panic、不 return error
//
// 参数：
//
//	db         — DB 连接
//	fallback   — 默认配置（通常从 config.Global.ExchangeRate 填充）
//	decryptFn  — 可选；为 nil 时跳过 AppSecret 解密
//
// 返回：已合并 DB 值的 Config
func LoadConfigFromDB(db *gorm.DB, fallback Config, decryptFn DecryptFn) Config {
	// URL 与 AppCode/AppKey —— 字符串字段
	strKeys := map[string]*string{
		CfgKeyPrimaryURL: &fallback.PrimaryURL,
		CfgKeyBackupURL:  &fallback.BackupURL,
		CfgKeyPublicURL:  &fallback.PublicURL,
		CfgKeyAppCode:    &fallback.AppCode,
		CfgKeyAppKey:     &fallback.AppKey,
	}
	for key, ptr := range strKeys {
		if val, ok := getSystemConfig(db, key); ok && val != "" {
			*ptr = val
		}
	}

	// AppSecret：密文 → 解密
	if encrypted, ok := getSystemConfig(db, CfgKeyAppSecretEncrypted); ok && encrypted != "" {
		if decryptFn != nil {
			plain, err := decryptFn(encrypted)
			if err != nil {
				log().Warn("exchange_rate appsecret decrypt failed, use fallback",
					zap.Error(err))
			} else {
				fallback.AppSecret = plain
			}
		}
	}

	// 数值字段：CacheTTL / RequestTimeout（秒 → Duration）
	if ttlSec := getIntSystemConfig(db, CfgKeyCacheTTL); ttlSec > 0 {
		fallback.CacheTTL = time.Duration(ttlSec) * time.Second
	}
	if timeoutSec := getIntSystemConfig(db, CfgKeyRequestTimeout); timeoutSec > 0 {
		fallback.RequestTimeout = time.Duration(timeoutSec) * time.Second
	}

	// 浮点字段：DefaultRate
	if r := getFloatSystemConfig(db, CfgKeyDefaultRate); r > 0 {
		fallback.DefaultRate = r
	}

	return fallback
}

// getSystemConfig 读取单条 SystemConfig 值
// 返回 (value, found)；DB 错误、key 不存在均返回 ("", false)
func getSystemConfig(db *gorm.DB, key string) (string, bool) {
	if db == nil {
		return "", false
	}
	var c model.SystemConfig
	if err := db.Where("`key` = ?", key).First(&c).Error; err != nil {
		return "", false
	}
	return c.Value, true
}

// getIntSystemConfig 读取并解析为 int（解析失败返回 0）
func getIntSystemConfig(db *gorm.DB, key string) int {
	val, ok := getSystemConfig(db, key)
	if !ok || val == "" {
		return 0
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		log().Debug("exchange_rate int config parse failed",
			zap.String("key", key), zap.String("value", val))
		return 0
	}
	return n
}

// getFloatSystemConfig 读取并解析为 float64（解析失败返回 0）
func getFloatSystemConfig(db *gorm.DB, key string) float64 {
	val, ok := getSystemConfig(db, key)
	if !ok || val == "" {
		return 0
	}
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		log().Debug("exchange_rate float config parse failed",
			zap.String("key", key), zap.String("value", val))
		return 0
	}
	return f
}

func log() *zap.Logger {
	if logger.L == nil {
		return zap.NewNop()
	}
	return logger.L
}
