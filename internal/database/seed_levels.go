package database

import (
	"fmt"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// RunSeedLevels 初始化会员等级、代理等级和汇率种子数据
// 幂等设计：仅在对应表为空时写入，避免重复插入
func RunSeedLevels(db *gorm.DB) {
	seedMemberLevels(db)
	seedAgentLevels(db)
	seedExchangeRates(db)
}

// seedMemberLevels 写入或更新 V0-V4 五个会员等级种子数据
// 使用 Upsert 语义：按 LevelCode 查找，不存在则创建，已存在则更新名称
func seedMemberLevels(db *gorm.DB) {
	// V0-V4 会员等级配置，消费门槛递增，折扣率递减，权益递增
	// 金额单位：积分(credits)，1 RMB = 10,000 credits
	levels := []model.MemberLevel{
		{
			LevelCode:          "V0",
			LevelName:          "体验会员",
			Rank:               0,
			MinTotalConsume:    0,
			MinTotalConsumeRMB: 0,
			ModelDiscount:      1.00,
			MonthlyGift:        0,
			MonthlyGiftRMB:     0,
			MaxTokensPerReq:    4096,
			DailyLimit:         100000, // 10元 = 100000积分
			DailyLimitRMB:      10,
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
			MonthlyGift:        50000, // 5元 = 50000积分
			MonthlyGiftRMB:     5,
			MaxTokensPerReq:    8192,
			DailyLimit:         500000, // 50元 = 500000积分
			DailyLimitRMB:      50,
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
			MonthlyGift:        200000, // 20元 = 200000积分
			MonthlyGiftRMB:     20,
			MaxTokensPerReq:    16384,
			DailyLimit:         2000000, // 200元 = 2000000积分
			DailyLimitRMB:      200,
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
			MonthlyGift:        500000, // 50元 = 500000积分
			MonthlyGiftRMB:     50,
			MaxTokensPerReq:    32768,
			DailyLimit:         5000000, // 500元 = 5000000积分
			DailyLimitRMB:      500,
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
			MonthlyGift:        1000000, // 100元 = 1000000积分
			MonthlyGiftRMB:     100,
			MaxTokensPerReq:    65536,
			DailyLimit:         0, // 0 表示不限
			DailyLimitRMB:      0,
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
					"monthly_gift":          lvl.MonthlyGift,
					"monthly_gift_rmb":      lvl.MonthlyGiftRMB,
					"max_tokens_per_req":    lvl.MaxTokensPerReq,
					"daily_limit":           lvl.DailyLimit,
					"daily_limit_rmb":       lvl.DailyLimitRMB,
					"degrade_months":        lvl.DegradeMonths,
					"is_active":             lvl.IsActive,
				}
				if err := tx.Model(&existing).Updates(updates).Error; err != nil {
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

// seedAgentLevels 写入或更新 A0-A4 五个代理等级种子数据
// 业务规则：代理商只享受直推会员消费产生的佣金，无多级佣金
// 使用 Upsert 语义：按 LevelCode 查找，不存在则创建，已存在则更新名称
func seedAgentLevels(db *gorm.DB) {
	// A0-A4 代理等级配置，销售门槛和佣金比例逐级递增
	// 金额单位：积分(credits)，1 RMB = 10,000 credits
	// 注意：L2Commission 和 L3Commission 已停用，全部设为 0
	levels := []model.AgentLevel{
		{
			LevelCode:          "A0",
			LevelName:          "合伙人",
			Rank:               0,
			MinMonthlySales:    0,
			MinMonthlySalesRMB: 0,
			MinDirectSubs:      0,
			DirectCommission:   0.05,
			L2Commission:       0, // 已停用
			L3Commission:       0, // 已停用
			Benefits:           `{}`,
			DegradeMonths:      2,
			IsActive:           true,
		},
		{
			LevelCode:          "A1",
			LevelName:          "金牌合伙人",
			Rank:               1,
			MinMonthlySales:    50000000, // 5000元 = 50000000积分
			MinMonthlySalesRMB: 5000,
			MinDirectSubs:      5,
			DirectCommission:   0.08,
			L2Commission:       0, // 已停用
			L3Commission:       0, // 已停用
			Benefits:           `{"推广素材": true}`,
			DegradeMonths:      2,
			IsActive:           true,
		},
		{
			LevelCode:          "A2",
			LevelName:          "钻石合伙人",
			Rank:               2,
			MinMonthlySales:    200000000, // 20000元 = 200000000积分
			MinMonthlySalesRMB: 20000,
			MinDirectSubs:      20,
			DirectCommission:   0.12,
			L2Commission:       0, // 已停用
			L3Commission:       0, // 已停用
			Benefits:           `{"推广素材": true, "数据看板": true, "API折扣": true}`,
			DegradeMonths:      2,
			IsActive:           true,
		},
		{
			LevelCode:          "A3",
			LevelName:          "至尊合伙人",
			Rank:               3,
			MinMonthlySales:    1000000000, // 100000元 = 1000000000积分
			MinMonthlySalesRMB: 100000,
			MinDirectSubs:      50,
			DirectCommission:   0.15,
			L2Commission:       0, // 已停用
			L3Commission:       0, // 已停用
			Benefits:           `{"独立子站": true, "API折扣": true, "专属客服": true, "优先结算": true, "推广素材": true, "数据看板": true}`,
			DegradeMonths:      2,
			IsActive:           true,
		},
		{
			LevelCode:          "A4",
			LevelName:          "创世合伙人",
			Rank:               4,
			MinMonthlySales:    5000000000, // 500000元 = 5000000000积分
			MinMonthlySalesRMB: 500000,
			MinDirectSubs:      100,
			DirectCommission:   0.18,
			L2Commission:       0, // 已停用
			L3Commission:       0, // 已停用
			Benefits:           `{"独立子站": true, "API折扣": true, "专属客服": true, "优先结算": true, "推广素材": true, "数据看板": true, "定制品牌": true, "专属通道": true}`,
			DegradeMonths:      2,
			IsActive:           true,
		},
	}

	// Upsert 写入：按 LevelCode 查找，不存在则创建，已存在则全量更新
	if err := db.Transaction(func(tx *gorm.DB) error {
		for _, lvl := range levels {
			var existing model.AgentLevel
			result := tx.Where("level_code = ?", lvl.LevelCode).First(&existing)
			if result.Error == nil {
				// 已存在：全量更新所有字段
				updates := map[string]interface{}{
					"level_name":            lvl.LevelName,
					"level_rank":            lvl.Rank,
					"min_monthly_sales":     lvl.MinMonthlySales,
					"min_monthly_sales_rmb": lvl.MinMonthlySalesRMB,
					"min_direct_subs":       lvl.MinDirectSubs,
					"direct_commission":     lvl.DirectCommission,
					"l2_commission":         lvl.L2Commission,
					"l3_commission":         lvl.L3Commission,
					"benefits":              lvl.Benefits,
					"degrade_months":        lvl.DegradeMonths,
					"is_active":             lvl.IsActive,
				}
				if err := tx.Model(&existing).Updates(updates).Error; err != nil {
					return fmt.Errorf("更新代理等级 %s 失败: %w", lvl.LevelCode, err)
				}
				fmt.Printf("[seed] agent_level %s updated\n", lvl.LevelCode)
			} else {
				// 不存在：创建新记录
				if err := tx.Create(&lvl).Error; err != nil {
					return fmt.Errorf("创建代理等级 %s 失败: %w", lvl.LevelCode, err)
				}
			}
		}
		return nil
	}); err != nil {
		fmt.Printf("[seed] agent_levels seed failed: %v\n", err)
		return
	}
	fmt.Printf("[seed] agent_levels seeded/updated: %d levels\n", len(levels))
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
