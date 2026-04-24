package guard

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// Service 注册风控服务
// 负责:
//  1. RegistrationGuard 配置 CRUD (带 5 分钟 Redis 缓存)
//  2. OTP 签发/校验 (EmailOTPToken)
//  3. 注册事件审计 (RegistrationEvent)
//  4. 一次性邮箱域名黑名单查询
//  5. 风控决策入口:EvaluateRegistration 返回 Decision + BlockedReason
type Service struct {
	db    *gorm.DB
	redis *redis.Client
}

// NewService 创建风控服务
func NewService(db *gorm.DB, redisClient *redis.Client) *Service {
	return &Service{db: db, redis: redisClient}
}

// cacheKey Redis 缓存键
const (
	cacheKeyGuard     = "config:guard"
	cacheGuardTTL     = 5 * time.Minute
	cacheKeyDispoList = "guard:disposable_domains"
	cacheDispoTTL     = 5 * time.Minute
)

// ---------- 配置 CRUD ----------

// GetConfig 读取当前生效的 RegistrationGuard(有缓存,无则从 DB 回填)
// DB 查不到时返回默认安全配置(所有防御启用)
func (s *Service) GetConfig(ctx context.Context) *model.RegistrationGuard {
	// 先查缓存
	if s.redis != nil {
		if raw, err := s.redis.Get(ctx, cacheKeyGuard).Result(); err == nil && raw != "" {
			var c model.RegistrationGuard
			if err := json.Unmarshal([]byte(raw), &c); err == nil {
				return &c
			}
		}
	}

	var cfg model.RegistrationGuard
	err := s.db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error
	if err != nil {
		// 返回默认安全值
		return &model.RegistrationGuard{
			EmailOTPEnabled:        true,
			EmailOTPLength:         6,
			EmailOTPTTLSeconds:     300,
			IPRegLimitPerHour:      5,
			IPRegLimitPerDay:       20,
			EmailDomainDailyMax:    50,
			FingerprintEnabled:     true,
			FingerprintDailyMax:    2,
			MinFormDwellSeconds:    3,
			IPReputationEnabled:    true,
			BlockTor:               true,
			DisposableEmailBlocked: true,
			FreeUserRPM:            5,
			FreeUserTPM:            10000,
			FreeUserConcurrency:    2,
			IsActive:               true,
		}
	}
	// 回填缓存
	if s.redis != nil {
		if b, err := json.Marshal(&cfg); err == nil {
			s.redis.Set(ctx, cacheKeyGuard, string(b), cacheGuardTTL)
		}
	}
	return &cfg
}

// UpdateConfig 更新或创建 RegistrationGuard;写库后主动删缓存
func (s *Service) UpdateConfig(ctx context.Context, cfg *model.RegistrationGuard) error {
	var existing model.RegistrationGuard
	err := s.db.WithContext(ctx).Where("is_active = ?", true).First(&existing).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			cfg.IsActive = true
			if err := s.db.WithContext(ctx).Create(cfg).Error; err != nil {
				return err
			}
			s.invalidateCache(ctx)
			return nil
		}
		return err
	}

	// 仅覆盖用户可配置的字段,保持 IsActive/BaseModel 不被改掉
	existing.EmailOTPEnabled = cfg.EmailOTPEnabled
	existing.EmailOTPLength = cfg.EmailOTPLength
	existing.EmailOTPTTLSeconds = cfg.EmailOTPTTLSeconds
	existing.IPRegLimitPerHour = cfg.IPRegLimitPerHour
	existing.IPRegLimitPerDay = cfg.IPRegLimitPerDay
	existing.EmailDomainDailyMax = cfg.EmailDomainDailyMax
	existing.FingerprintEnabled = cfg.FingerprintEnabled
	existing.FingerprintDailyMax = cfg.FingerprintDailyMax
	existing.MinFormDwellSeconds = cfg.MinFormDwellSeconds
	existing.IPReputationEnabled = cfg.IPReputationEnabled
	existing.BlockVPN = cfg.BlockVPN
	existing.BlockTor = cfg.BlockTor
	existing.DisposableEmailBlocked = cfg.DisposableEmailBlocked
	existing.FreeUserRPM = cfg.FreeUserRPM
	existing.FreeUserTPM = cfg.FreeUserTPM
	existing.FreeUserConcurrency = cfg.FreeUserConcurrency

	if err := s.db.WithContext(ctx).Save(&existing).Error; err != nil {
		return err
	}
	s.invalidateCache(ctx)
	return nil
}

// invalidateCache 配置变更后主动删缓存
func (s *Service) invalidateCache(ctx context.Context) {
	if s.redis == nil {
		return
	}
	s.redis.Del(ctx, cacheKeyGuard, cacheKeyDispoList)
}

// ---------- 一次性邮箱黑名单 ----------

// IsDisposableDomain 检查域名是否在一次性邮箱黑名单(有 Redis 缓存)
func (s *Service) IsDisposableDomain(ctx context.Context, domain string) bool {
	if domain == "" {
		return false
	}
	dom := strings.ToLower(strings.TrimSpace(domain))

	// 尝试从缓存加载全量 set
	if s.redis != nil {
		if n, err := s.redis.SIsMember(ctx, cacheKeyDispoList, dom).Result(); err == nil {
			// 集合存在则直接判定;否则下沉到 DB
			if n {
				return true
			}
			// 若集合已在缓存则 false;否则需要回源
			if exists, _ := s.redis.Exists(ctx, cacheKeyDispoList).Result(); exists > 0 {
				return false
			}
		}
	}

	// 回源并回填
	var rows []model.DisposableEmailDomain
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).Find(&rows).Error; err != nil {
		return false
	}
	hit := false
	if s.redis != nil && len(rows) > 0 {
		items := make([]interface{}, 0, len(rows))
		for _, r := range rows {
			d := strings.ToLower(r.Domain)
			items = append(items, d)
			if d == dom {
				hit = true
			}
		}
		s.redis.SAdd(ctx, cacheKeyDispoList, items...)
		s.redis.Expire(ctx, cacheKeyDispoList, cacheDispoTTL)
	} else {
		for _, r := range rows {
			if strings.ToLower(r.Domain) == dom {
				hit = true
				break
			}
		}
	}
	return hit
}

// ListDisposable 分页查询一次性邮箱域名
func (s *Service) ListDisposable(ctx context.Context, page, pageSize int) ([]model.DisposableEmailDomain, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	var total int64
	s.db.WithContext(ctx).Model(&model.DisposableEmailDomain{}).Where("is_active = ?", true).Count(&total)
	var list []model.DisposableEmailDomain
	err := s.db.WithContext(ctx).
		Where("is_active = ?", true).
		Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&list).Error
	return list, total, err
}

// AddDisposable 新增一次性邮箱域名;幂等(按 domain 唯一)
func (s *Service) AddDisposable(ctx context.Context, domain, note, source string) (*model.DisposableEmailDomain, error) {
	d := strings.ToLower(strings.TrimSpace(domain))
	if d == "" {
		return nil, errors.New("domain required")
	}
	var row model.DisposableEmailDomain
	err := s.db.WithContext(ctx).Where("domain = ?", d).First(&row).Error
	if err == nil {
		// 已存在,确保激活并更新 note
		updates := map[string]interface{}{"is_active": true}
		if note != "" {
			updates["note"] = note
		}
		s.db.WithContext(ctx).Model(&model.DisposableEmailDomain{}).Where("id = ?", row.ID).Updates(updates)
		s.invalidateCache(ctx)
		return &row, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if source == "" {
		source = "MANUAL"
	}
	row = model.DisposableEmailDomain{
		Domain:   d,
		Source:   source,
		IsActive: true,
		Note:     note,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	s.invalidateCache(ctx)
	return &row, nil
}

// RemoveDisposable 删除(软删除:is_active=false)一次性邮箱域名
func (s *Service) RemoveDisposable(ctx context.Context, id uint) error {
	res := s.db.WithContext(ctx).Model(&model.DisposableEmailDomain{}).
		Where("id = ?", id).
		Update("is_active", false)
	if res.Error != nil {
		return res.Error
	}
	s.invalidateCache(ctx)
	return nil
}

// ---------- OTP ----------

// GenerateOTP 为目标邮箱生成 OTP,写库(hash),返回明文 6 位码供邮件发送
// 幂等保护:同一邮箱 60 秒内重复请求返回已有 token,不重新生成
func (s *Service) GenerateOTP(ctx context.Context, email, purpose, clientIP string) (string, *model.EmailOTPToken, error) {
	cfg := s.GetConfig(ctx)
	length := cfg.EmailOTPLength
	if length < 4 || length > 8 {
		length = 6
	}
	ttl := cfg.EmailOTPTTLSeconds
	if ttl < 60 || ttl > 1800 {
		ttl = 300
	}

	// 60 秒防刷:如已有未过期且未使用的 token,直接返回
	var existing model.EmailOTPToken
	err := s.db.WithContext(ctx).
		Where("email = ? AND purpose = ? AND used_at IS NULL AND expires_at > ? AND created_at > ?",
			email, purpose, time.Now(), time.Now().Add(-60*time.Second)).
		Order("created_at DESC").
		First(&existing).Error
	if err == nil {
		return "", &existing, errors.New("OTP_RATE_LIMIT: please wait 60 seconds before requesting a new code")
	}

	otp, err := randomNumericOTP(length)
	if err != nil {
		return "", nil, fmt.Errorf("generate otp: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(otp), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, fmt.Errorf("hash otp: %w", err)
	}

	token := model.EmailOTPToken{
		Email:       email,
		TokenHash:   string(hash),
		Purpose:     purpose,
		ExpiresAt:   time.Now().Add(time.Duration(ttl) * time.Second),
		Attempts:    0,
		MaxAttempts: 5,
		IP:          clientIP,
	}
	if err := s.db.WithContext(ctx).Create(&token).Error; err != nil {
		return "", nil, fmt.Errorf("save otp: %w", err)
	}
	return otp, &token, nil
}

// VerifyOTP 校验邮箱 OTP,成功后标记 used_at
// 返回: (ok, reason)
//   - ok=true: 校验通过,该 token 已 used 不可再用
//   - ok=false 原因: NO_TOKEN / EXPIRED / EXCEEDED_ATTEMPTS / MISMATCH / USED
func (s *Service) VerifyOTP(ctx context.Context, email, purpose, code string) (bool, string) {
	var token model.EmailOTPToken
	err := s.db.WithContext(ctx).
		Where("email = ? AND purpose = ? AND used_at IS NULL", email, purpose).
		Order("created_at DESC").
		First(&token).Error
	if err != nil {
		return false, "NO_TOKEN"
	}
	if time.Now().After(token.ExpiresAt) {
		return false, "EXPIRED"
	}
	if token.Attempts >= token.MaxAttempts {
		return false, "EXCEEDED_ATTEMPTS"
	}
	// 先增尝试次数
	s.db.WithContext(ctx).Model(&model.EmailOTPToken{}).Where("id = ?", token.ID).UpdateColumn("attempts", gorm.Expr("attempts + 1"))

	if err := bcrypt.CompareHashAndPassword([]byte(token.TokenHash), []byte(code)); err != nil {
		return false, "MISMATCH"
	}
	now := time.Now()
	if err := s.db.WithContext(ctx).Model(&model.EmailOTPToken{}).Where("id = ?", token.ID).Update("used_at", &now).Error; err != nil {
		return false, "UPDATE_FAIL"
	}
	return true, ""
}

// CleanupExpiredOTP 清理过期 24 小时以上的 OTP 记录(cron 用)
func (s *Service) CleanupExpiredOTP(ctx context.Context) (int64, error) {
	threshold := time.Now().Add(-24 * time.Hour)
	res := s.db.WithContext(ctx).
		Where("expires_at < ?", threshold).
		Delete(&model.EmailOTPToken{})
	return res.RowsAffected, res.Error
}

// ---------- 注册事件审计 ----------

// RegistrationContext 注册请求上下文
type RegistrationContext struct {
	Email        string
	IP           string
	UserAgent    string
	Fingerprint  string
	Country      string
	ASN          string
	IPType       string
	DwellSeconds int
	ReferralCode string
	HoneypotHit  bool
}

// LogRegistrationEvent 记录一次注册尝试事件
func (s *Service) LogRegistrationEvent(ctx context.Context, reqCtx RegistrationContext, userID uint, decision, blockedReason string) error {
	e := model.RegistrationEvent{
		Email:         reqCtx.Email,
		UserID:        userID,
		IP:            reqCtx.IP,
		UserAgent:     trunc(reqCtx.UserAgent, 500),
		Fingerprint:   reqCtx.Fingerprint,
		Country:       reqCtx.Country,
		ASN:           reqCtx.ASN,
		IPType:        reqCtx.IPType,
		DwellSeconds:  reqCtx.DwellSeconds,
		ReferralCode:  reqCtx.ReferralCode,
		Decision:      decision,
		BlockedReason: blockedReason,
		HoneypotHit:   reqCtx.HoneypotHit,
		EventTime:     time.Now(),
	}
	return s.db.WithContext(ctx).Create(&e).Error
}

// ---------- 风控决策入口 ----------

// Decision 注册风控决策结果
type Decision struct {
	Allow         bool   // true=放行, false=拦截
	Shadow        bool   // true=允许注册但不发奖励(如停留过短)
	BlockedReason string // 拦截原因 或 shadow 原因
}

// EvaluateRegistration 综合 7 层防御规则判断是否允许注册
// 入参:注册上下文
// 出参:Decision
func (s *Service) EvaluateRegistration(ctx context.Context, reqCtx RegistrationContext) Decision {
	cfg := s.GetConfig(ctx)
	if cfg == nil || !cfg.IsActive {
		return Decision{Allow: true}
	}

	// 1) Honeypot
	if reqCtx.HoneypotHit {
		return Decision{Allow: false, BlockedReason: "HONEYPOT_HIT"}
	}

	// 3) 一次性邮箱
	if cfg.DisposableEmailBlocked && reqCtx.Email != "" {
		parts := strings.Split(reqCtx.Email, "@")
		if len(parts) == 2 && s.IsDisposableDomain(ctx, parts[1]) {
			return Decision{Allow: false, BlockedReason: "DISPOSABLE_EMAIL"}
		}
	}

	// 4) IP 情报
	if cfg.IPReputationEnabled {
		if cfg.BlockTor && strings.EqualFold(reqCtx.IPType, "tor") {
			return Decision{Allow: false, BlockedReason: "BLOCKED_TOR"}
		}
		if cfg.BlockVPN && strings.EqualFold(reqCtx.IPType, "vpn") {
			return Decision{Allow: false, BlockedReason: "BLOCKED_VPN"}
		}
	}

	// 5) IP 限流
	if cfg.IPRegLimitPerHour > 0 && reqCtx.IP != "" {
		cnt, _ := s.countRecentIPRegs(ctx, reqCtx.IP, time.Hour)
		if cnt >= int64(cfg.IPRegLimitPerHour) {
			return Decision{Allow: false, BlockedReason: "IP_LIMIT_HOUR"}
		}
	}
	if cfg.IPRegLimitPerDay > 0 && reqCtx.IP != "" {
		cnt, _ := s.countRecentIPRegs(ctx, reqCtx.IP, 24*time.Hour)
		if cnt >= int64(cfg.IPRegLimitPerDay) {
			return Decision{Allow: false, BlockedReason: "IP_LIMIT_DAY"}
		}
	}

	// 6) 邮箱域名每日上限
	if cfg.EmailDomainDailyMax > 0 && reqCtx.Email != "" {
		parts := strings.Split(reqCtx.Email, "@")
		if len(parts) == 2 {
			cnt, _ := s.countRecentDomainRegs(ctx, parts[1], 24*time.Hour)
			if cnt >= int64(cfg.EmailDomainDailyMax) {
				return Decision{Allow: false, BlockedReason: "DOMAIN_LIMIT_DAY"}
			}
		}
	}

	// 7) 设备指纹
	if cfg.FingerprintEnabled && cfg.FingerprintDailyMax > 0 && reqCtx.Fingerprint != "" {
		cnt, _ := s.countRecentFingerprintRegs(ctx, reqCtx.Fingerprint, 24*time.Hour)
		if cnt >= int64(cfg.FingerprintDailyMax) {
			return Decision{Allow: false, BlockedReason: "FINGERPRINT_LIMIT"}
		}
	}

	// 8) 停留时长(低于门槛 shadow 放行,不发奖励)
	if cfg.MinFormDwellSeconds > 0 && reqCtx.DwellSeconds < cfg.MinFormDwellSeconds {
		return Decision{Allow: true, Shadow: true, BlockedReason: "DWELL_TOO_SHORT"}
	}

	return Decision{Allow: true}
}

// ---------- 频次统计辅助 ----------

func (s *Service) countRecentIPRegs(ctx context.Context, ip string, window time.Duration) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.RegistrationEvent{}).
		Where("ip = ? AND decision IN ? AND event_time > ?",
			ip, []string{"PASS", "SHADOW"}, time.Now().Add(-window)).
		Count(&n).Error
	return n, err
}

func (s *Service) countRecentDomainRegs(ctx context.Context, domain string, window time.Duration) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.RegistrationEvent{}).
		Where("email LIKE ? AND decision IN ? AND event_time > ?",
			"%@"+domain, []string{"PASS", "SHADOW"}, time.Now().Add(-window)).
		Count(&n).Error
	return n, err
}

func (s *Service) countRecentFingerprintRegs(ctx context.Context, fp string, window time.Duration) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.RegistrationEvent{}).
		Where("fingerprint = ? AND decision IN ? AND event_time > ?",
			fp, []string{"PASS", "SHADOW"}, time.Now().Add(-window)).
		Count(&n).Error
	return n, err
}

// ---------- 工具函数 ----------

// randomNumericOTP 生成 length 位纯数字 OTP(crypto/rand)
func randomNumericOTP(length int) (string, error) {
	const digits = "0123456789"
	out := make([]byte, length)
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		if err != nil {
			return "", err
		}
		out[i] = digits[n.Int64()]
	}
	return string(out), nil
}

func trunc(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
