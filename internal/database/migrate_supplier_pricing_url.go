package database

import (
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/logger"
)

// defaultPricingURLByCode 4 家主流供应商的官方定价页 URL（2026-04 抓取）
// 当存量供应商的 pricing_url 为空时，按 code 匹配回填
// 不覆盖管理员已自定义的值
var defaultPricingURLByCode = map[string]string{
	// 阿里云百炼
	"alibaba":           "https://help.aliyun.com/zh/model-studio/model-pricing",
	"aliyun":            "https://help.aliyun.com/zh/model-studio/model-pricing",
	"aliyun_dashscope":  "https://help.aliyun.com/zh/model-studio/model-pricing",
	"dashscope":         "https://help.aliyun.com/zh/model-studio/model-pricing",
	// 火山引擎
	"volcengine": "https://www.volcengine.com/docs/82379/1544106",
	"volc":       "https://www.volcengine.com/docs/82379/1544106",
	// 百度千帆
	"qianfan":       "https://cloud.baidu.com/doc/qianfan-docs/s/Jm8r1826a",
	"baidu_qianfan": "https://cloud.baidu.com/doc/qianfan-docs/s/Jm8r1826a",
	// 腾讯混元
	"hunyuan":         "https://cloud.tencent.com/document/product/1729/97731",
	"tencent_hunyuan": "https://cloud.tencent.com/document/product/1729/97731",
}

// RunSupplierPricingURLMigration 为存量供应商回填官网定价 URL
//
// 迁移策略（幂等）：
//  1. 查询所有 pricing_url 为空/NULL 的供应商
//  2. 按 code 匹配 defaultPricingURLByCode 表
//  3. 命中则 UPDATE，不命中则跳过（日志记录）
//  4. 不覆盖已配置的 pricing_url（保留管理员自定义）
//
// 此迁移在 AutoMigrate 之后运行，确保 pricing_url 字段已存在。
func RunSupplierPricingURLMigration(db *gorm.DB) {
	start := time.Now()

	type row struct {
		ID         uint
		Code       string
		PricingURL string
	}

	var rows []row
	if err := db.Table("suppliers").
		Select("id, code, pricing_url").
		Where("pricing_url IS NULL OR pricing_url = ''").
		Find(&rows).Error; err != nil {
		logger.L.Warn("supplier pricing_url migration: query failed", zap.Error(err))
		return
	}

	if len(rows) == 0 {
		logger.L.Info("supplier pricing_url migration: all suppliers already have pricing_url",
			zap.Duration("duration", time.Since(start)))
		return
	}

	updated := 0
	skipped := 0
	for _, r := range rows {
		url, ok := defaultPricingURLByCode[r.Code]
		if !ok {
			skipped++
			continue
		}
		if err := db.Table("suppliers").
			Where("id = ?", r.ID).
			Update("pricing_url", url).Error; err != nil {
			logger.L.Warn("supplier pricing_url migration: update failed",
				zap.Uint("supplier_id", r.ID),
				zap.String("code", r.Code),
				zap.Error(err))
			continue
		}
		updated++
	}

	logger.L.Info("supplier pricing_url migration: complete",
		zap.Int("checked", len(rows)),
		zap.Int("updated", updated),
		zap.Int("skipped_unknown_code", skipped),
		zap.Duration("duration", time.Since(start)))
}
