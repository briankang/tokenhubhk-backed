package payment

import (
	"context"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
)

// ExchangeService 汇率服务，管理汇率查询和费用计算
type ExchangeService struct {
	db    *gorm.DB
	redis *goredis.Client
}

// NewExchangeService 创建汇率服务实例
func NewExchangeService(db *gorm.DB, redis *goredis.Client) *ExchangeService {
	return &ExchangeService{db: db, redis: redis}
}

// ExchangeResult 汇率计算结果
type ExchangeResult struct {
	OriginalAmount float64 // 原始金额（外币）
	OriginalCurrency string // 原始币种
	ExchangeRate    float64 // 汇率
	FeeRate         float64 // 手续费比例
	FeeAmount       float64 // 手续费金额（RMB）
	RMBAmount       float64 // 换汇后人民币净额
	CreditAmount    int64   // 兑换积分数量
}

// GetExchangeRate 获取指定币种到人民币的汇率
// 如果没有配置，返回默认值（1:1，无手续费）
func (s *ExchangeService) GetExchangeRate(ctx context.Context, fromCurrency string) (*model.ExchangeRate, error) {
	if fromCurrency == "" || fromCurrency == "CNY" {
		// 人民币无需换汇
		return &model.ExchangeRate{
			FromCurrency: "CNY",
			ToCurrency:   "CNY",
			Rate:         1.0,
			FeeRate:      0,
			IsActive:     true,
		}, nil
	}

	var rate model.ExchangeRate
	err := s.db.WithContext(ctx).
		Where("from_currency = ? AND is_active = ?", fromCurrency, true).
		First(&rate).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			// 未配置汇率，返回默认值（1:1）
			return &model.ExchangeRate{
				FromCurrency: fromCurrency,
				ToCurrency:   "CNY",
				Rate:         1.0,
				FeeRate:      0,
				IsActive:     true,
			}, nil
		}
		return nil, fmt.Errorf("query exchange rate: %w", err)
	}
	return &rate, nil
}

// CalculateExchange 计算外币换算后的积分数量
// 流程：外币金额 × 汇率 = RMB → 扣手续费 → RMB × 10000 = credits
func (s *ExchangeService) CalculateExchange(ctx context.Context, amount float64, currency string) (*ExchangeResult, error) {
	rate, err := s.GetExchangeRate(ctx, currency)
	if err != nil {
		return nil, err
	}

	// 计算换汇后人民币金额
	rmbAmount := amount * rate.Rate
	
	// 计算手续费
	feeAmount := rmbAmount * rate.FeeRate
	
	// 扣除手续费后的净额
	rmbNet := rmbAmount - feeAmount
	if rmbNet < 0 {
		rmbNet = 0
	}
	
	// 转换为积分
	creditAmount := credits.RMBToCredits(rmbNet)

	return &ExchangeResult{
		OriginalAmount:   amount,
		OriginalCurrency: currency,
		ExchangeRate:     rate.Rate,
		FeeRate:          rate.FeeRate,
		FeeAmount:        feeAmount,
		RMBAmount:        rmbNet,
		CreditAmount:     creditAmount,
	}, nil
}

// ListExchangeRates 获取所有汇率配置
func (s *ExchangeService) ListExchangeRates(ctx context.Context) ([]model.ExchangeRate, error) {
	var rates []model.ExchangeRate
	err := s.db.WithContext(ctx).
		Where("is_active = ?", true).
		Order("from_currency ASC").
		Find(&rates).Error
	if err != nil {
		return nil, fmt.Errorf("list exchange rates: %w", err)
	}
	return rates, nil
}

// UpdateExchangeRate 更新或创建汇率配置
func (s *ExchangeService) UpdateExchangeRate(ctx context.Context, rate *model.ExchangeRate) error {
	var existing model.ExchangeRate
	err := s.db.WithContext(ctx).
		Where("from_currency = ? AND to_currency = ?", rate.FromCurrency, rate.ToCurrency).
		First(&existing).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return s.db.WithContext(ctx).Create(rate).Error
		}
		return err
	}

	existing.Rate = rate.Rate
	existing.FeeRate = rate.FeeRate
	existing.IsActive = rate.IsActive
	return s.db.WithContext(ctx).Save(&existing).Error
}

// GetExchangeRateByID 根据ID获取汇率配置
func (s *ExchangeService) GetExchangeRateByID(ctx context.Context, id uint) (*model.ExchangeRate, error) {
	var rate model.ExchangeRate
	err := s.db.WithContext(ctx).First(&rate, id).Error
	if err != nil {
		return nil, err
	}
	return &rate, nil
}

// UpdateExchangeRateByID 根据ID更新汇率配置
func (s *ExchangeService) UpdateExchangeRateByID(ctx context.Context, id uint, rate *model.ExchangeRate) error {
	var existing model.ExchangeRate
	err := s.db.WithContext(ctx).First(&existing, id).Error
	if err != nil {
		return err
	}

	existing.Rate = rate.Rate
	existing.FeeRate = rate.FeeRate
	existing.IsActive = rate.IsActive
	return s.db.WithContext(ctx).Save(&existing).Error
}
