package database

import (
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	paymentsvc "tokenhub-server/internal/service/payment"
	"tokenhub-server/internal/pkg/logger"
)

// ============================================================
// 汇率 API 配置种子数据（v3.2.3）
//
// 目的：把两个阿里云云市场 API（cmapi00064402 + cmapi00063890）
// 以及 L3 公开兜底接口 (open.er-api.com) 所需的全部参数持久化到
// system_configs 表，让 ExchangeRateService 可以在运行时动态读取。
//
// 特性：
//   - 幂等：每个 key 已存在则跳过（不覆盖管理员修改过的值）
//   - AppSecret 使用 AES-256-GCM 加密存储（复用 PAYMENT_ENCRYPT_KEY）
//   - 可重入：多次调用仅首次写入缺失项
// ============================================================

// exchangeRateSeed 单条种子定义
type exchangeRateSeed struct {
	Key       string // system_configs.key
	Value     string // 明文值（若 IsSecret=true 将被加密后写入）
	IsSecret  bool   // 敏感字段标记
	Desc      string // 仅用于日志/文档
}

// exchangeRateSeedData 种子数据定义
// 对应 v3.2.2 阿里云接口调通所得参数
var exchangeRateSeedData = []exchangeRateSeed{
	{
		Key:   "exchange_rate.primary_url",
		Value: "https://jmhlcx.market.alicloudapi.com/exchange-rate/convert",
		Desc:  "主接口 cmapi00064402 (聚美智数 汇率转换)",
	},
	{
		Key:   "exchange_rate.backup_url",
		Value: "https://smkjzgyhss.market.alicloudapi.com/exchange_rate/realtime",
		Desc:  "备用接口 cmapi00063890 (数脉API 中国银行实时汇率)",
	},
	{
		Key:   "exchange_rate.public_url",
		Value: "https://open.er-api.com/v6/latest/USD",
		Desc:  "L3 兜底公开接口 (open.er-api.com，无需 AppCode)",
	},
	{
		Key:   "exchange_rate.appcode",
		Value: "dcbcacff20e7413ab50231113b364655",
		Desc:  "阿里云云市场 AppCode（两个 cmapi 共用，主要鉴权凭证）",
	},
	{
		Key:   "exchange_rate.appkey",
		Value: "205000469",
		Desc:  "阿里云云市场 AppKey（保留字段；APPCODE 方式下无需签名）",
	},
	{
		Key:      "exchange_rate.appsecret_encrypted",
		Value:    "JTqBS9nayLmd67ya9coZpEAC3oaoIVKb",
		IsSecret: true,
		Desc:     "阿里云云市场 AppSecret（加密存储，仅解密后使用）",
	},
	{
		Key:   "exchange_rate.cache_ttl",
		Value: "86400",
		Desc:  "Redis 缓存 TTL（秒），默认 24h",
	},
	{
		Key:   "exchange_rate.default_rate",
		Value: "7.2",
		Desc:  "全部接口失败时的兜底汇率（USD→CNY）",
	},
	{
		Key:   "exchange_rate.request_timeout",
		Value: "10",
		Desc:  "HTTP 请求超时（秒）",
	},
}

// RunSeedExchangeRateConfig 写入汇率 API 配置种子到 system_configs 表
//
// 幂等策略：按 key 查询，存在则跳过；只插入缺失的记录
// 调用时机：在 RunSeed 之后（PaymentConfigService 的加密能力依赖已初始化的 DB + env）
//
// 参数：
//   db — 数据库连接
func RunSeedExchangeRateConfig(db *gorm.DB) {
	// 用 PaymentConfigService 的 AES-256-GCM 加密能力（复用 PAYMENT_ENCRYPT_KEY）
	pcs := paymentsvc.NewPaymentConfigService(db)

	inserted := 0
	skipped := 0
	for _, s := range exchangeRateSeedData {
		// 幂等检查：存在则跳过
		var existing model.SystemConfig
		if err := db.Where("`key` = ?", s.Key).First(&existing).Error; err == nil {
			skipped++
			continue
		}

		// 加密敏感字段
		value := s.Value
		if s.IsSecret && value != "" {
			encrypted, err := pcs.EncryptPlaintext(value)
			if err != nil {
				logger.L.Warn("seed exchange_rate: encrypt failed, skip",
					zap.String("key", s.Key), zap.Error(err))
				continue
			}
			value = encrypted
		}

		// 写入
		if err := db.Create(&model.SystemConfig{
			Key:   s.Key,
			Value: value,
		}).Error; err != nil {
			logger.L.Warn("seed exchange_rate: create failed",
				zap.String("key", s.Key), zap.Error(err))
			continue
		}
		inserted++
	}

	if inserted > 0 {
		logger.L.Info("exchange_rate config seeds written",
			zap.Int("inserted", inserted),
			zap.Int("skipped", skipped),
			zap.Int("total", len(exchangeRateSeedData)))
	} else {
		logger.L.Debug("exchange_rate config seeds: all keys already exist",
			zap.Int("total", len(exchangeRateSeedData)))
	}
}
