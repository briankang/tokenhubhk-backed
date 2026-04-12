package referral

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

const (
	referralCodeLength = 8
	referralCodeChars  = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
)

// ReferralService 邀请链接与配置管理服务
type ReferralService struct {
	db *gorm.DB
}

// NewReferralService 创建邀请服务实例
func NewReferralService(db *gorm.DB) *ReferralService {
	return &ReferralService{db: db}
}

// GenerateCode 生成 8 位随机邀请码
func GenerateCode() (string, error) {
	b := make([]byte, referralCodeLength)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(referralCodeChars))))
		if err != nil {
			return "", fmt.Errorf("generate referral code: %w", err)
		}
		b[i] = referralCodeChars[n.Int64()]
	}
	return string(b), nil
}

// GetOrCreateLink 获取或创建用户的邀请链接，已存在则直接返回
func (s *ReferralService) GetOrCreateLink(ctx context.Context, userID, tenantID uint) (*model.ReferralLink, error) {
	var link model.ReferralLink
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&link).Error
	if err == nil {
		return &link, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("query referral link: %w", err)
	}

	// Generate unique code
	var code string
	for i := 0; i < 10; i++ {
		c, err := GenerateCode()
		if err != nil {
			return nil, err
		}
		var count int64
		s.db.WithContext(ctx).Model(&model.ReferralLink{}).Where("code = ?", c).Count(&count)
		if count == 0 {
			code = c
			break
		}
	}
	if code == "" {
		return nil, fmt.Errorf("failed to generate unique referral code after 10 attempts")
	}

	link = model.ReferralLink{
		UserID:   userID,
		TenantID: tenantID,
		Code:     code,
	}
	if err := s.db.WithContext(ctx).Create(&link).Error; err != nil {
		return nil, fmt.Errorf("create referral link: %w", err)
	}

	// Also update user's ReferralCode field
	s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).Update("referral_code", code)

	return &link, nil
}

// FindByCode 根据邀请码查找邀请链接
func (s *ReferralService) FindByCode(ctx context.Context, code string) (*model.ReferralLink, error) {
	var link model.ReferralLink
	if err := s.db.WithContext(ctx).Where("code = ?", code).First(&link).Error; err != nil {
		return nil, err
	}
	return &link, nil
}

// IncrementClickCount 增加邀请链接的点击计数
func (s *ReferralService) IncrementClickCount(ctx context.Context, code string) error {
	return s.db.WithContext(ctx).Model(&model.ReferralLink{}).
		Where("code = ?", code).
		UpdateColumn("click_count", gorm.Expr("click_count + 1")).Error
}

// IncrementRegisterCount 增加邀请链接的注册计数
func (s *ReferralService) IncrementRegisterCount(ctx context.Context, linkID uint) error {
	return s.db.WithContext(ctx).Model(&model.ReferralLink{}).
		Where("id = ?", linkID).
		UpdateColumn("register_count", gorm.Expr("register_count + 1")).Error
}

// ReferralStats 用户邀请统计汇总数据
type ReferralStats struct {
	ClickCount       int     `json:"clickCount"`
	RegisterCount    int     `json:"registerCount"`
	TotalCommission  float64 `json:"totalCommission"`
	PendingAmount    float64 `json:"pendingAmount"`
	SettledAmount    float64 `json:"settledAmount"`
	WithdrawnAmount  float64 `json:"withdrawnAmount"`
}

// GetStats 获取用户的邀请统计数据
func (s *ReferralService) GetStats(ctx context.Context, userID uint) (*ReferralStats, error) {
	var link model.ReferralLink
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&link).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &ReferralStats{}, nil
		}
		return nil, err
	}

	stats := &ReferralStats{
		ClickCount:    link.ClickCount,
		RegisterCount: link.RegisterCount,
	}

	// Aggregate commission amounts
	type amountResult struct {
		Status string
		Total  float64
	}
	var results []amountResult
	s.db.WithContext(ctx).Model(&model.CommissionRecord{}).
		Select("status, COALESCE(SUM(commission_amount), 0) as total").
		Where("user_id = ?", userID).
		Group("status").
		Scan(&results)

	for _, r := range results {
		switch r.Status {
		case "PENDING":
			stats.PendingAmount = r.Total
		case "SETTLED":
			stats.SettledAmount = r.Total
		case "WITHDRAWN":
			stats.WithdrawnAmount = r.Total
		}
		stats.TotalCommission += r.Total
	}

	return stats, nil
}

// GetUserCommissions 分页查询用户的佣金记录
func (s *ReferralService) GetUserCommissions(ctx context.Context, userID uint, page, pageSize int) ([]model.CommissionRecord, int64, error) {
	var total int64
	s.db.WithContext(ctx).Model(&model.CommissionRecord{}).Where("user_id = ?", userID).Count(&total)

	var records []model.CommissionRecord
	err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&records).Error

	return records, total, err
}

// GetAllCommissions 分页查询所有佣金记录（管理员视图）
func (s *ReferralService) GetAllCommissions(ctx context.Context, page, pageSize int) ([]model.CommissionRecord, int64, error) {
	var total int64
	s.db.WithContext(ctx).Model(&model.CommissionRecord{}).Count(&total)

	var records []model.CommissionRecord
	err := s.db.WithContext(ctx).
		Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&records).Error

	return records, total, err
}

// GetConfig 获取活跃的邀请配置，不存在则创建默认配置
func (s *ReferralService) GetConfig(ctx context.Context) (*model.ReferralConfig, error) {
	var cfg model.ReferralConfig
	err := s.db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error
	if err == nil {
		return &cfg, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// Create default config
	cfg = model.ReferralConfig{
		PersonalCashbackRate: 0.05,
		L1CommissionRate:     0.10,
		L2CommissionRate:     0.05,
		L3CommissionRate:     0.02,
		MinWithdrawAmount:    10,
		IsActive:             true,
	}
	if err := s.db.WithContext(ctx).Create(&cfg).Error; err != nil {
		return nil, err
	}
	return &cfg, nil
}

// UpdateConfig 更新邀请配置
func (s *ReferralService) UpdateConfig(ctx context.Context, cfg *model.ReferralConfig) error {
	return s.db.WithContext(ctx).Save(cfg).Error
}

// CommissionSummary 代理商佣金汇总数据
type CommissionSummary struct {
	PendingAmount   float64 `json:"pendingAmount"`
	SettledAmount   float64 `json:"settledAmount"`
	WithdrawnAmount float64 `json:"withdrawnAmount"`
	TotalAmount     float64 `json:"totalAmount"`
}

// GetCommissionSummary 获取用户的佣金汇总数据
func (s *ReferralService) GetCommissionSummary(ctx context.Context, userID uint) (*CommissionSummary, error) {
	summary := &CommissionSummary{}

	type amountResult struct {
		Status string
		Total  float64
	}
	var results []amountResult
	err := s.db.WithContext(ctx).Model(&model.CommissionRecord{}).
		Select("status, COALESCE(SUM(commission_amount), 0) as total").
		Where("user_id = ?", userID).
		Group("status").
		Scan(&results).Error
	if err != nil {
		return nil, err
	}

	for _, r := range results {
		switch r.Status {
		case "PENDING":
			summary.PendingAmount = r.Total
		case "SETTLED":
			summary.SettledAmount = r.Total
		case "WITHDRAWN":
			summary.WithdrawnAmount = r.Total
		}
		summary.TotalAmount += r.Total
	}

	return summary, nil
}
