package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/referral"
	smsauth "tokenhub-server/internal/service/sms"
)

type PhoneRegisterRequest struct {
	Phone        string `json:"phone" binding:"required"`
	Code         string `json:"code" binding:"required,len=6"`
	Username     string `json:"username" binding:"required"`
	Password     string `json:"password" binding:"required"`
	InviteCode   string `json:"invite_code,omitempty"`
	ReferralCode string `json:"referral_code,omitempty"`
}

type PhoneLoginRequest struct {
	Phone string `json:"phone" binding:"required"`
	Code  string `json:"code" binding:"required,len=6"`
}

// PhoneLogin 使用中国大陆手机号短信验证码登录已存在账号。
func (s *AuthService) PhoneLogin(ctx context.Context, req *PhoneLoginRequest) (*model.User, *TokenPair, error) {
	if req == nil {
		return nil, nil, fmt.Errorf("phone login request is nil")
	}
	phone, err := smsauth.NormalizeCNPhone(req.Phone)
	if err != nil {
		return nil, nil, err
	}
	var user model.User
	if err := s.db.WithContext(ctx).Where("phone_e164 = ?", phone).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, fmt.Errorf("phone not registered")
		}
		return nil, nil, err
	}
	if _, err := s.smsSvc.VerifyCode(ctx, req.Phone, smsauth.PurposeLogin, req.Code); err != nil {
		return nil, nil, err
	}
	if !user.IsActive {
		return nil, nil, fmt.Errorf("account is deactivated")
	}
	pair, err := s.issueTokenPair(ctx, &user)
	if err != nil {
		return nil, nil, err
	}
	return &user, pair, nil
}

// PhoneRegister 创建手机号新账号，并沿用统一 users.id / 角色 / 余额 / 邀请归因流程。
func (s *AuthService) PhoneRegister(ctx context.Context, req *PhoneRegisterRequest) (*model.User, *TokenPair, error) {
	if req == nil {
		return nil, nil, fmt.Errorf("phone register request is nil")
	}
	phone, err := s.smsSvc.VerifyCode(ctx, req.Phone, smsauth.PurposeLogin, req.Code)
	if err != nil {
		return nil, nil, err
	}
	username := strings.ToLower(strings.TrimSpace(req.Username))
	if err := smsauth.ValidateUsername(username); err != nil {
		return nil, nil, err
	}
	if !isClientPasswordHash(req.Password) {
		return nil, nil, fmt.Errorf("invalid password")
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.User{}).Where("username = ?", username).Count(&count).Error; err != nil {
		return nil, nil, err
	}
	if count > 0 {
		return nil, nil, fmt.Errorf("username already exists")
	}
	if err := s.db.WithContext(ctx).Model(&model.User{}).Where("phone_e164 = ?", phone).Count(&count).Error; err != nil {
		return nil, nil, err
	}
	if count > 0 {
		return nil, nil, fmt.Errorf("phone already registered")
	}
	refCode := req.ReferralCode
	if refCode == "" {
		refCode = req.InviteCode
	}
	if refCode != "" && !s.isValidInviteOrReferralCode(ctx, refCode) {
		return nil, nil, fmt.Errorf("invalid invite code")
	}
	tenantID, err := s.resolveTenantForInvite(ctx, req.InviteCode)
	if err != nil {
		return nil, nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	user := &model.User{
		TenantID:         tenantID,
		Email:            smsauth.InternalPhoneEmail(phone),
		Username:         &username,
		PhoneCountryCode: "CN",
		PhoneE164:        &phone,
		PhoneVerifiedAt:  &now,
		RegisterSource:   "phone",
		PasswordHash:     string(hash),
		Name:             username,
		IsActive:         true,
		Language:         "zh",
		CountryCode:      "CN",
	}
	if err := s.db.WithContext(ctx).Create(user).Error; err != nil {
		return nil, nil, err
	}
	if err := s.assignDefaultUserRole(ctx, user.ID); err != nil {
		return nil, nil, err
	}
	if s.balanceSvc != nil {
		_ = s.balanceSvc.InitBalance(ctx, user.ID, user.TenantID)
	}
	if refCode != "" {
		_ = referral.ProcessReferralOnRegister(s.db, ctx, user, refCode)
	}
	if s.referralSvc != nil {
		_, _ = s.referralSvc.GetOrCreateLink(ctx, user.ID, user.TenantID)
	}
	pair, err := s.issueTokenPair(ctx, user)
	if err != nil {
		return nil, nil, err
	}
	return user, pair, nil
}

func (s *AuthService) resolveTenantForInvite(ctx context.Context, inviteCode string) (uint, error) {
	if inviteCode != "" {
		var tenant model.Tenant
		if err := s.db.WithContext(ctx).Where("domain = ? AND is_active = ?", inviteCode, true).First(&tenant).Error; err == nil {
			return tenant.ID, nil
		}
	}
	var defaultTenant model.Tenant
	if err := s.db.WithContext(ctx).Where("parent_id IS NULL AND level = 1").First(&defaultTenant).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, fmt.Errorf("no default tenant available")
		}
		return 0, err
	}
	return defaultTenant.ID, nil
}

func (s *AuthService) issueTokenPair(ctx context.Context, user *model.User) (*TokenPair, error) {
	pair, err := s.generateTokenPair(user)
	if err != nil {
		return nil, fmt.Errorf("failed to generate tokens: %w", err)
	}
	now := time.Now()
	_ = s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", user.ID).Update("last_login_at", now).Error
	if s.redis != nil {
		key := fmt.Sprintf("%s%d", redisTokenKeyPrefix, user.ID)
		_ = s.redis.Set(ctx, key, pair.AccessToken, accessTokenExpiry).Err()
	}
	return pair, nil
}
