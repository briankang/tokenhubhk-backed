package referral

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

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

// EffectiveRuleDTO 用户当前生效的返佣规则(合并全局 + 个人 override)
type EffectiveRuleDTO struct {
	CommissionRate       float64    `json:"commissionRate"`
	AttributionDays      int        `json:"attributionDays"`
	LifetimeCapCredits   int64      `json:"lifetimeCapCredits"`
	MinPaidCreditsUnlock int64      `json:"minPaidCreditsUnlock"`
	MinWithdrawAmount    int64      `json:"minWithdrawAmount"`
	SettleDays           int        `json:"settleDays"`
	IsOverride           bool       `json:"isOverride"`
	OverrideNote         string     `json:"overrideNote,omitempty"`
	EffectiveFrom        *time.Time `json:"effectiveFrom,omitempty"`
	EffectiveTo          *time.Time `json:"effectiveTo,omitempty"`
}

// GetMyEffectiveRule 获取用户的有效返佣规则
// 读取全局 ReferralConfig 并叠加该用户的活跃 UserCommissionOverride
// 个人 override 可覆盖 CommissionRate / AttributionDays / LifetimeCapCredits / MinPaidCreditsUnlock
// 其余参数(MinWithdrawAmount / SettleDays)始终来自全局
func (s *ReferralService) GetMyEffectiveRule(ctx context.Context, userID uint) (*EffectiveRuleDTO, error) {
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return nil, err
	}

	dto := &EffectiveRuleDTO{
		CommissionRate:       cfg.CommissionRate,
		AttributionDays:      cfg.AttributionDays,
		LifetimeCapCredits:   cfg.LifetimeCapCredits,
		MinPaidCreditsUnlock: cfg.MinPaidCreditsUnlock,
		MinWithdrawAmount:    cfg.MinWithdrawAmount,
		SettleDays:           cfg.SettleDays,
		IsOverride:           false,
	}
	// 兼容老字段:cfg.CommissionRate 为 0 时尝试 PersonalCashbackRate
	if dto.CommissionRate <= 0 && cfg.PersonalCashbackRate > 0 {
		dto.CommissionRate = cfg.PersonalCashbackRate
	}
	if dto.AttributionDays <= 0 {
		dto.AttributionDays = 90
	}

	// 查询活跃 override
	now := time.Now()
	var ov model.UserCommissionOverride
	err = s.db.WithContext(ctx).
		Where("user_id = ? AND is_active = ? AND effective_from <= ?", userID, true, now).
		Where("effective_to IS NULL OR effective_to > ?", now).
		First(&ov).Error
	if err == nil {
		if ov.CommissionRate > 0 {
			dto.CommissionRate = ov.CommissionRate
		}
		if ov.AttributionDays != nil && *ov.AttributionDays > 0 {
			dto.AttributionDays = *ov.AttributionDays
		}
		// 终身上限 / 解锁门槛:override 非 NULL 即覆盖(允许 0 表示"无上限"/"立即解锁")
		if ov.LifetimeCapCredits != nil {
			dto.LifetimeCapCredits = *ov.LifetimeCapCredits
		}
		if ov.MinPaidCreditsUnlock != nil {
			dto.MinPaidCreditsUnlock = *ov.MinPaidCreditsUnlock
		}
		dto.IsOverride = true
		dto.OverrideNote = ov.Note
		effFrom := ov.EffectiveFrom
		dto.EffectiveFrom = &effFrom
		dto.EffectiveTo = ov.EffectiveTo
	}

	return dto, nil
}

// InviteeInfo 被邀用户信息（用于邀请人仪表盘展示）
type InviteeInfo struct {
	UserID              uint       `json:"userId"`
	Email               string     `json:"email"`               // 脱敏邮箱
	Name                string     `json:"name"`
	RegisteredAt        time.Time  `json:"registeredAt"`
	IsUnlocked          bool       `json:"isUnlocked"`          // 是否已达消费解锁门槛
	UnlockedAt          *time.Time `json:"unlockedAt"`
	IsValid             bool       `json:"isValid"`             // 归因是否有效（未过期）
	ExpiresAt           time.Time  `json:"expiresAt"`
	TotalConsumptionRMB float64    `json:"totalConsumptionRmb"` // 累计消费金额（人民币）
	TotalCommissionRMB  float64    `json:"totalCommissionRmb"`  // 该用户产生的佣金（人民币）
	InviterBonusGranted bool       `json:"inviterBonusGranted"` // 邀请人奖励是否已发放
	InviteeBonusGranted bool       `json:"inviteeBonusGranted"` // 被邀者奖励是否已发放
}

// maskEmail 对邮箱进行脱敏处理，如 abc***@domain.com
func maskEmail(email string) string {
	for i, ch := range email {
		if ch == '@' {
			prefix := email[:i]
			domain := email[i:]
			show := 3
			if len(prefix) <= 3 {
				show = 1
			}
			return prefix[:show] + "***" + domain
		}
	}
	return email
}

// GetInvitees 分页获取邀请人下的被邀用户列表，附带消费状态
func (s *ReferralService) GetInvitees(ctx context.Context, inviterID uint, page, pageSize int) ([]InviteeInfo, int64, error) {
	var total int64
	s.db.WithContext(ctx).Model(&model.ReferralAttribution{}).
		Where("inviter_id = ?", inviterID).
		Count(&total)

	var attrs []model.ReferralAttribution
	err := s.db.WithContext(ctx).
		Where("inviter_id = ?", inviterID).
		Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&attrs).Error
	if err != nil {
		return nil, 0, err
	}
	if len(attrs) == 0 {
		return []InviteeInfo{}, 0, nil
	}

	// 收集所有被邀用户 ID
	userIDs := make([]uint, len(attrs))
	for i, a := range attrs {
		userIDs[i] = a.UserID
	}

	// 批量查询用户信息
	var users []model.User
	s.db.WithContext(ctx).Select("id, email, name, created_at").
		Where("id IN ?", userIDs).
		Find(&users)
	userMap := make(map[uint]model.User, len(users))
	for _, u := range users {
		userMap[u.ID] = u
	}

	// 批量聚合佣金数据（按 source_user_id 分组）
	type commAgg struct {
		SourceUserID        uint
		TotalConsumptionRMB float64
		TotalCommissionRMB  float64
	}
	var aggRows []commAgg
	s.db.WithContext(ctx).Model(&model.CommissionRecord{}).
		Select("source_user_id, COALESCE(SUM(order_amount_rmb),0) as total_consumption_rmb, COALESCE(SUM(commission_amount_rmb),0) as total_commission_rmb").
		Where("user_id = ? AND source_user_id IN ?", inviterID, userIDs).
		Group("source_user_id").
		Scan(&aggRows)
	aggMap := make(map[uint]commAgg, len(aggRows))
	for _, row := range aggRows {
		aggMap[row.SourceUserID] = row
	}

	result := make([]InviteeInfo, 0, len(attrs))
	for _, a := range attrs {
		u := userMap[a.UserID]
		agg := aggMap[a.UserID]
		result = append(result, InviteeInfo{
			UserID:              a.UserID,
			Email:               maskEmail(u.Email),
			Name:                u.Name,
			RegisteredAt:        a.AttributedAt,
			IsUnlocked:          a.UnlockedAt != nil,
			UnlockedAt:          a.UnlockedAt,
			IsValid:             a.IsValid,
			ExpiresAt:           a.ExpiresAt,
			TotalConsumptionRMB: agg.TotalConsumptionRMB,
			TotalCommissionRMB:  agg.TotalCommissionRMB,
			InviterBonusGranted: a.InviterBonusGranted,
			InviteeBonusGranted: a.InviteeBonusGranted,
		})
	}
	return result, total, nil
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
