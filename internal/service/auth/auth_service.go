package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/balance"
	"tokenhub-server/internal/service/referral"
)

const (
	bcryptCost          = 12
	accessTokenExpiry   = 24 * time.Hour
	refreshTokenExpiry  = 30 * 24 * time.Hour  // 刷新令牌有效期 30 天
	redisTokenKeyPrefix = "token:user:"
)

// RegisterRequest 用户注册请求参数
type RegisterRequest struct {
	Email        string `json:"email" binding:"required,email"`
	Password     string `json:"password" binding:"required,min=8"`
	Name         string `json:"name" binding:"required"`
	InviteCode   string `json:"invite_code,omitempty"`
	ReferralCode string `json:"referral_code,omitempty"` // 邀请码（用户推荐）
}

// LoginRequest 用户登录请求参数
type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// TokenPair 令牌对，包含访问令牌和刷新令牌
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// Claims JWT 令牌的声明信息
// v4.0: Role 字段已移除，权限由 user_roles 表承载
type Claims struct {
	UserID   uint `json:"user_id"`
	TenantID uint `json:"tenant_id"`
	jwt.RegisteredClaims
}

// AuthService 认证服务，处理用户注册、登录、令牌刷新和注销等逻辑
// 包含余额服务和邀请服务的依赖注入
type AuthService struct {
	db         *gorm.DB
	redis      *goredis.Client
	jwt        config.JWTConfig
	balanceSvc *balance.BalanceService
	referralSvc *referral.ReferralService
}

// NewAuthService 创建认证服务实例，db 不能为 nil 否则 panic
// 同时初始化余额服务和邀请服务
func NewAuthService(db *gorm.DB, redis *goredis.Client, jwtCfg config.JWTConfig) *AuthService {
	if db == nil {
		panic("auth service: db is nil")
	}
	return &AuthService{
		db:          db,
		redis:       redis,
		jwt:         jwtCfg,
		balanceSvc:  balance.NewBalanceService(db, redis),
		referralSvc: referral.NewReferralService(db),
	}
}

// Register 注册新用户
// 1. 校验邮箱唯一性
// 2. 使用 bcrypt(cost=12) 哈希密码
// 3. 若有邀请码则关联对应租户
// 4. 创建用户记录并初始化余额
func (s *AuthService) Register(ctx context.Context, req *RegisterRequest) (*model.User, error) {
	if req == nil {
		return nil, fmt.Errorf("register request is nil")
	}

	// 检查邮箱是否已注册
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.User{}).Where("email = ?", req.Email).Count(&count).Error; err != nil {
		return nil, fmt.Errorf("failed to check email uniqueness: %w", err)
	}
	if count > 0 {
		return nil, fmt.Errorf("email already registered")
	}

	// 哈希密码
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	// 根据邀请码解析租户
	var tenantID uint
	if req.InviteCode != "" {
		var tenant model.Tenant
		if err := s.db.WithContext(ctx).Where("domain = ? AND is_active = ?", req.InviteCode, true).First(&tenant).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("invalid invite code")
			}
			return nil, fmt.Errorf("failed to resolve invite code: %w", err)
		}
		tenantID = tenant.ID
	} else {
		// 默认租户：查找顶级默认租户
		var defaultTenant model.Tenant
		err := s.db.WithContext(ctx).Where("parent_id IS NULL AND level = 1").First(&defaultTenant).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("no default tenant available")
			}
			return nil, fmt.Errorf("failed to find default tenant: %w", err)
		}
		tenantID = defaultTenant.ID
	}

	user := &model.User{
		TenantID:     tenantID,
		Email:        req.Email,
		PasswordHash: string(hash),
		Name:         req.Name,
		IsActive:     true,
		Language:     "en",
	}

	if err := s.db.WithContext(ctx).Create(user).Error; err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	// v4.0: 为新用户分配 USER 角色（RBAC 系统）
	if err := s.assignDefaultUserRole(ctx, user.ID); err != nil {
		return nil, fmt.Errorf("failed to assign default role: %w", err)
	}

	// 初始化用户余额（默认免费额度）
	if s.balanceSvc != nil {
		if err := s.balanceSvc.InitBalance(ctx, user.ID, user.TenantID); err != nil {
			// 仅记录日志，不影响注册流程
			_ = err
		}
	}

	// 处理推荐码 — v3.1: 调用 ProcessReferralOnRegister 统一建立 ReferralAttribution + User.ReferredBy
	refCode := req.ReferralCode
	if refCode == "" {
		refCode = req.InviteCode // fallback: also check invite_code field
	}
	if refCode != "" {
		// 优先走 v3.1 归因流程(ReferralLink 路径)
		if err := referral.ProcessReferralOnRegister(s.db, ctx, user, refCode); err != nil {
			// ReferralLink 查找失败时,尝试通过 User.ReferralCode 字段匹配(兼容旧数据)
			var referrer model.User
			if err2 := s.db.WithContext(ctx).Where("referral_code = ? AND id != ?", refCode, user.ID).First(&referrer).Error; err2 == nil {
				if user.ReferredBy == nil {
					s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", user.ID).Update("referred_by", referrer.ID)
				}
				// 为兼容路径也建立归因快照
				_ = s.createAttributionFallback(ctx, user.ID, referrer.ID, refCode)
			}
		}
	}

	// 注册成功后立即为用户生成邀请码，确保用户首次访问邀请页面时链接已就绪
	if s.referralSvc != nil {
		if _, err := s.referralSvc.GetOrCreateLink(ctx, user.ID, user.TenantID); err != nil {
			// 仅记录日志，不影响注册流程
			_ = err
		}
	}

	return user, nil
}

// createAttributionFallback 兼容路径:用户通过 User.ReferralCode 字段(非 ReferralLink)匹配到推荐人时
// 手动建立 ReferralAttribution 快照,保证 v3.1 归因数据完整性
func (s *AuthService) createAttributionFallback(ctx context.Context, userID, inviterID uint, refCode string) error {
	if userID == 0 || inviterID == 0 || userID == inviterID {
		return nil
	}
	// 已存在则跳过
	var existing model.ReferralAttribution
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&existing).Error; err == nil {
		return nil
	}
	// 读取 AttributionDays 配置(兜底 90 天)
	attributionDays := 90
	var cfg model.ReferralConfig
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error; err == nil {
		if cfg.AttributionDays > 0 {
			attributionDays = cfg.AttributionDays
		}
	}
	now := time.Now()
	attr := model.ReferralAttribution{
		UserID:       userID,
		InviterID:    inviterID,
		ReferralCode: refCode,
		AttributedAt: now,
		ExpiresAt:    now.AddDate(0, 0, attributionDays),
		UnlockedAt:   nil,
		IsValid:      true,
	}
	return s.db.WithContext(ctx).Create(&attr).Error
}

// Login 用户登录认证，成功返回令牌对
// 1. 根据邮箱查找用户
// 2. bcrypt 比对密码
// 3. 生成 JWT 令牌（含 userID/tenantID/role）
// 4. 更新最后登录时间
// 5. 将令牌存入 Redis 供注销/刷新使用
func (s *AuthService) Login(ctx context.Context, req *LoginRequest) (*TokenPair, error) {
	if req == nil {
		return nil, fmt.Errorf("login request is nil")
	}

	var user model.User
	if err := s.db.WithContext(ctx).Where("email = ?", req.Email).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("invalid credentials")
		}
		return nil, fmt.Errorf("failed to find user: %w", err)
	}

	if !user.IsActive {
		return nil, fmt.Errorf("account is deactivated")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return nil, fmt.Errorf("invalid credentials")
	}

	// 生成令牌对
	tokenPair, err := s.generateTokenPair(&user)
	if err != nil {
		return nil, fmt.Errorf("failed to generate tokens: %w", err)
	}

	// 更新最后登录时间
	now := time.Now()
	s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", user.ID).Update("last_login_at", now)

	// 将令牌存入 Redis
	if s.redis != nil {
		key := fmt.Sprintf("%s%d", redisTokenKeyPrefix, user.ID)
		_ = s.redis.Set(ctx, key, tokenPair.AccessToken, accessTokenExpiry).Err()
	}

	return tokenPair, nil
}

// RefreshToken 使用有效的刷新令牌生成新的令牌对
func (s *AuthService) RefreshToken(ctx context.Context, refreshToken string) (*TokenPair, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}

	claims, err := s.parseToken(refreshToken)
	if err != nil {
		return nil, fmt.Errorf("invalid refresh token: %w", err)
	}

	var user model.User
	if err := s.db.WithContext(ctx).First(&user, claims.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("failed to find user: %w", err)
	}

	if !user.IsActive {
		return nil, fmt.Errorf("account is deactivated")
	}

	tokenPair, err := s.generateTokenPair(&user)
	if err != nil {
		return nil, fmt.Errorf("failed to generate tokens: %w", err)
	}

	// 更新 Redis 中的令牌
	if s.redis != nil {
		key := fmt.Sprintf("%s%d", redisTokenKeyPrefix, user.ID)
		_ = s.redis.Set(ctx, key, tokenPair.AccessToken, accessTokenExpiry).Err()
	}

	return tokenPair, nil
}

// Logout 用户注销，从 Redis 中删除令牌使其失效
func (s *AuthService) Logout(ctx context.Context, userID uint) error {
	if userID == 0 {
		return fmt.Errorf("user ID is required")
	}
	if s.redis != nil {
		key := fmt.Sprintf("%s%d", redisTokenKeyPrefix, userID)
		return s.redis.Del(ctx, key).Err()
	}
	return nil
}

// generateTokenPair 为指定用户生成访问令牌和刷新令牌
func (s *AuthService) generateTokenPair(user *model.User) (*TokenPair, error) {
	if user == nil {
		return nil, fmt.Errorf("user is nil")
	}

	now := time.Now()
	secret := []byte(s.jwt.Secret)

	// 生成访问令牌
	accessClaims := &Claims{
		UserID:   user.ID,
		TenantID: user.TenantID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(accessTokenExpiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			Subject:   fmt.Sprintf("%d", user.ID),
		},
	}
	accessToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString(secret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token: %w", err)
	}

	// 生成刷新令牌（更长有效期）
	refreshID := make([]byte, 16)
	if _, err := rand.Read(refreshID); err != nil {
		return nil, fmt.Errorf("failed to generate refresh token id: %w", err)
	}
	refreshClaims := &Claims{
		UserID:   user.ID,
		TenantID: user.TenantID,
		Role:     user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(refreshTokenExpiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        hex.EncodeToString(refreshID),
			Subject:   fmt.Sprintf("%d", user.ID),
		},
	}
	refreshTokenStr, err := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString(secret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshTokenStr,
		ExpiresIn:    int64(accessTokenExpiry.Seconds()),
	}, nil
}

// parseToken 解析并验证 JWT 令牌，返回声明信息
func (s *AuthService) parseToken(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(s.jwt.Secret), nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, jwt.ErrSignatureInvalid
	}
	return claims, nil
}
