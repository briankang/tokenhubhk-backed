package database

import (
	"fmt"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// RunSeedLevels 初始化会员等级和汇率种子数据
// v3.1：代理等级已废除，不再写入 agent_levels
func RunSeedLevels(db *gorm.DB) {
	seedMemberLevels(db)
	seedExchangeRates(db)
}

// seedMemberLevels 写入/更新 V0-V4 五个会员等级种子数据
// v4.2 起：每次重启均执行 upsert，确保所有环境保持最新的 RPM/TPM/折扣默认值。
// 更新逻辑：按 level_code 匹配，存在则更新全量字段，不存在则创建。
func seedMemberLevels(db *gorm.DB) {
	// V0-V4 会员等级配置，消费门槛递增，折扣率递减，限流配额递增
	// 金额单位：积分(credits)，1 RMB = 10,000 credits
	levels := []model.MemberLevel{
		{
			LevelCode:          "V0",
			LevelName:          "体验会员",
			Rank:               0,
			MinTotalConsume:    0,
			MinTotalConsumeRMB: 0,
			ModelDiscount:      1.00,
			DefaultRPM:         30,
			DefaultTPM:         200000,
			DegradeMonths:      3,
			IsActive:           true,
		},
		{
			LevelCode:          "V1",
			LevelName:          "标准会员",
			Rank:               1,
			MinTotalConsume:    1000000, // 100元 = 1000000积分
			MinTotalConsumeRMB: 100,
			ModelDiscount:      0.95,
			DefaultRPM:         60,
			DefaultTPM:         500000,
			DegradeMonths:      3,
			IsActive:           true,
		},
		{
			LevelCode:          "V2",
			LevelName:          "专业会员",
			Rank:               2,
			MinTotalConsume:    5000000, // 500元 = 5000000积分
			MinTotalConsumeRMB: 500,
			ModelDiscount:      0.90,
			DefaultRPM:         120,
			DefaultTPM:         1500000,
			DegradeMonths:      3,
			IsActive:           true,
		},
		{
			LevelCode:          "V3",
			LevelName:          "企业会员",
			Rank:               3,
			MinTotalConsume:    20000000, // 2000元 = 20000000积分
			MinTotalConsumeRMB: 2000,
			ModelDiscount:      0.85,
			DefaultRPM:         300,
			DefaultTPM:         5000000,
			DegradeMonths:      3,
			IsActive:           true,
		},
		{
			LevelCode:          "V4",
			LevelName:          "创世会员",
			Rank:               4,
			MinTotalConsume:    100000000, // 10000元 = 100000000积分
			MinTotalConsumeRMB: 10000,
			ModelDiscount:      0.80,
			DefaultRPM:         600,
			DefaultTPM:         10000000,
			DegradeMonths:      3,
			IsActive:           true,
		},
	}

	// Upsert 写入：按 LevelCode 查找，不存在则创建，已存在则全量更新
	if err := db.Transaction(func(tx *gorm.DB) error {
		for _, lvl := range levels {
			var existing model.MemberLevel
			result := tx.Where("level_code = ?", lvl.LevelCode).First(&existing)
			if result.Error == nil {
				// 已存在：全量更新所有字段
				updates := map[string]interface{}{
					"level_name":            lvl.LevelName,
					"level_rank":            lvl.Rank,
					"min_total_consume":     lvl.MinTotalConsume,
					"min_total_consume_rmb": lvl.MinTotalConsumeRMB,
					"model_discount":        lvl.ModelDiscount,
					"default_rpm":           lvl.DefaultRPM,
					"default_tpm":           lvl.DefaultTPM,
					"degrade_months":        lvl.DegradeMonths,
					"is_active":             lvl.IsActive,
				}
				if err := tx.Model(&model.MemberLevel{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
					return fmt.Errorf("更新会员等级 %s 失败: %w", lvl.LevelCode, err)
				}
				fmt.Printf("[seed] member_level %s updated\n", lvl.LevelCode)
			} else {
				// 不存在：创建新记录
				if err := tx.Create(&lvl).Error; err != nil {
					return fmt.Errorf("创建会员等级 %s 失败: %w", lvl.LevelCode, err)
				}
			}
		}
		return nil
	}); err != nil {
		fmt.Printf("[seed] member_levels seed failed: %v\n", err)
		return
	}
	fmt.Printf("[seed] member_levels seeded/updated: %d levels\n", len(levels))
}

// seedExchangeRates 写入汇率配置种子数据
// 支持 USD/EUR/GBP/JPY/HKD → CNY 换算，外币收2%手续费，CNY无手续费
func seedExchangeRates(db *gorm.DB) {
	// 幂等检查：如果已有数据则跳过
	var count int64
	db.Model(&model.ExchangeRate{}).Count(&count)
	if count > 0 {
		fmt.Println("[seed] exchange_rates already seeded, skip")
		return
	}

	// 汇率配置：外币→CNY，含2%手续费（CNY→CNY无手续费）
	rates := []model.ExchangeRate{
		{
			FromCurrency: "USD",
			ToCurrency:   "CNY",
			Rate:         7.25,
			FeeRate:      0.02, // 2%手续费
			IsActive:     true,
		},
		{
			FromCurrency: "EUR",
			ToCurrency:   "CNY",
			Rate:         7.85,
			FeeRate:      0.02, // 2%手续费
			IsActive:     true,
		},
		{
			FromCurrency: "GBP",
			ToCurrency:   "CNY",
			Rate:         9.15,
			FeeRate:      0.02, // 2%手续费
			IsActive:     true,
		},
		{
			FromCurrency: "JPY",
			ToCurrency:   "CNY",
			Rate:         0.048,
			FeeRate:      0.02, // 2%手续费
			IsActive:     true,
		},
		{
			FromCurrency: "HKD",
			ToCurrency:   "CNY",
			Rate:         0.93,
			FeeRate:      0.02, // 2%手续费
			IsActive:     true,
		},
		{
			FromCurrency: "CNY",
			ToCurrency:   "CNY",
			Rate:         1.0,
			FeeRate:      0.00, // 无手续费
			IsActive:     true,
		},
	}

	// 事务写入，保证原子性
	if err := db.Transaction(func(tx *gorm.DB) error {
		for _, rate := range rates {
			if err := tx.Create(&rate).Error; err != nil {
				return fmt.Errorf("创建汇率 %s→%s 失败: %w", rate.FromCurrency, rate.ToCurrency, err)
			}
		}
		return nil
	}); err != nil {
		fmt.Printf("[seed] exchange_rates seed failed: %v\n", err)
		return
	}
	fmt.Printf("[seed] exchange_rates seeded: %d rates\n", len(rates))
}
