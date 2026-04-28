package sms

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

const (
	ProviderAliyun   = "aliyun"
	PurposeLogin     = "LOGIN"
	SMSStatusSent    = "sent"
	SMSStatusFailed  = "failed"
	SMSStatusBlocked = "blocked"
	maskedSecret     = "***"
)

type Service struct {
	db       *gorm.DB
	redis    *goredis.Client
	sender   Sender
	verifier CaptchaVerifier
}

func NewService(db *gorm.DB, redisClient *goredis.Client) *Service {
	client := NewAliyunClient()
	return &Service{db: db, redis: redisClient, sender: client, verifier: client}
}

func (s *Service) WithSender(sender Sender) *Service {
	if sender != nil {
		s.sender = sender
	}
	return s
}

func (s *Service) WithCaptchaVerifier(verifier CaptchaVerifier) *Service {
	if verifier != nil {
		s.verifier = verifier
	}
	return s
}

type SMSProviderDTO struct {
	ID                 uint   `json:"id,omitempty"`
	Provider           string `json:"provider"`
	AccessKeyID        string `json:"access_key_id"`
	HasAccessKeySecret bool   `json:"has_access_key_secret"`
	RegionID           string `json:"region_id"`
	Endpoint           string `json:"endpoint"`
	SignName           string `json:"sign_name"`
	TemplateCode       string `json:"template_code"`
	TemplateParamName  string `json:"template_param_name"`
	IsActive           bool   `json:"is_active"`
}

type SMSProviderUpsert struct {
	AccessKeyID       string `json:"access_key_id"`
	AccessKeySecret   string `json:"access_key_secret"`
	RegionID          string `json:"region_id"`
	Endpoint          string `json:"endpoint"`
	SignName          string `json:"sign_name"`
	TemplateCode      string `json:"template_code"`
	TemplateParamName string `json:"template_param_name"`
	IsActive          *bool  `json:"is_active"`
}

type CaptchaProviderDTO struct {
	ID                 uint   `json:"id,omitempty"`
	Provider           string `json:"provider"`
	AccessKeyID        string `json:"access_key_id"`
	HasAccessKeySecret bool   `json:"has_access_key_secret"`
	HasEKey            bool   `json:"has_ekey"`
	RegionID           string `json:"region_id"`
	Endpoint           string `json:"endpoint"`
	SceneID            string `json:"scene_id"`
	Prefix             string `json:"prefix"`
	EncryptMode        bool   `json:"encrypt_mode"`
	EncryptedSceneTTL  int    `json:"encrypted_scene_ttl_seconds"`
	IsActive           bool   `json:"is_active"`
}

type CaptchaProviderUpsert struct {
	AccessKeyID       string `json:"access_key_id"`
	AccessKeySecret   string `json:"access_key_secret"`
	EKey              string `json:"ekey"`
	RegionID          string `json:"region_id"`
	Endpoint          string `json:"endpoint"`
	SceneID           string `json:"scene_id"`
	Prefix            string `json:"prefix"`
	EncryptMode       *bool  `json:"encrypt_mode"`
	EncryptedSceneTTL int    `json:"encrypted_scene_ttl_seconds"`
	IsActive          *bool  `json:"is_active"`
}

type PrecheckRequest struct {
	Phone       string
	Fingerprint string
	IP          string
	Purpose     string
}

type PrecheckResult struct {
	Allowed     bool   `json:"allowed"`
	NeedCaptcha bool   `json:"need_captcha"`
	RetryAfter  int    `json:"retry_after"`
	LimitType   string `json:"limit_type,omitempty"`
	Phone       string `json:"phone,omitempty"`
}

type SendCodeRequest struct {
	Phone              string
	Fingerprint        string
	IP                 string
	Purpose            string
	CaptchaVerifyParam string
}

type SendCodeResult struct {
	Sent      bool   `json:"sent"`
	ExpiresIn int    `json:"expires_in"`
	Cooldown  int    `json:"cooldown"`
	Phone     string `json:"phone"`
}

type RateLimitError struct {
	LimitType  string
	RetryAfter int
	Message    string
}

func (e *RateLimitError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "sms rate limited"
}

func (s *Service) GetRiskConfig(ctx context.Context) *model.SMSRiskConfig {
	var cfg model.SMSRiskConfig
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error; err == nil {
		normalizeRiskConfig(&cfg)
		return &cfg
	}
	cfg = defaultRiskConfig()
	return &cfg
}

func (s *Service) UpdateRiskConfig(ctx context.Context, cfg *model.SMSRiskConfig) error {
	normalizeRiskConfig(cfg)
	var existing model.SMSRiskConfig
	err := s.db.WithContext(ctx).Where("is_active = ?", true).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		cfg.IsActive = true
		return s.db.WithContext(ctx).Create(cfg).Error
	}
	if err != nil {
		return err
	}
	existing.CodeTTLSeconds = cfg.CodeTTLSeconds
	existing.SendCooldownSeconds = cfg.SendCooldownSeconds
	existing.PhoneHourlyLimit = cfg.PhoneHourlyLimit
	existing.PhoneDailyLimit = cfg.PhoneDailyLimit
	existing.IPHourlyLimit = cfg.IPHourlyLimit
	existing.IPDailyLimit = cfg.IPDailyLimit
	existing.FingerprintDailyLimit = cfg.FingerprintDailyLimit
	existing.MaxVerifyAttempts = cfg.MaxVerifyAttempts
	existing.FreezeMinutes = cfg.FreezeMinutes
	existing.RequireCaptchaOnRisk = cfg.RequireCaptchaOnRisk
	existing.RequireCaptchaAlways = cfg.RequireCaptchaAlways
	existing.BlockVirtualPrefixes = cfg.BlockVirtualPrefixes
	return s.db.WithContext(ctx).Save(&existing).Error
}

func defaultRiskConfig() model.SMSRiskConfig {
	return model.SMSRiskConfig{
		IsActive: true, CodeTTLSeconds: 300, SendCooldownSeconds: 60,
		PhoneHourlyLimit: 5, PhoneDailyLimit: 10, IPHourlyLimit: 20, IPDailyLimit: 100,
		FingerprintDailyLimit: 10, MaxVerifyAttempts: 5, FreezeMinutes: 15,
		RequireCaptchaOnRisk: true, BlockVirtualPrefixes: true,
	}
}

func normalizeRiskConfig(cfg *model.SMSRiskConfig) {
	if cfg.CodeTTLSeconds < 60 || cfg.CodeTTLSeconds > 1800 {
		cfg.CodeTTLSeconds = 300
	}
	if cfg.SendCooldownSeconds < 30 || cfg.SendCooldownSeconds > 600 {
		cfg.SendCooldownSeconds = 60
	}
	if cfg.PhoneHourlyLimit < 1 {
		cfg.PhoneHourlyLimit = 5
	}
	if cfg.PhoneDailyLimit < cfg.PhoneHourlyLimit {
		cfg.PhoneDailyLimit = cfg.PhoneHourlyLimit * 2
	}
	if cfg.IPHourlyLimit < 1 {
		cfg.IPHourlyLimit = 20
	}
	if cfg.IPDailyLimit < cfg.IPHourlyLimit {
		cfg.IPDailyLimit = cfg.IPHourlyLimit * 5
	}
	if cfg.FingerprintDailyLimit < 1 {
		cfg.FingerprintDailyLimit = 10
	}
	if cfg.MaxVerifyAttempts < 1 || cfg.MaxVerifyAttempts > 20 {
		cfg.MaxVerifyAttempts = 5
	}
	if cfg.FreezeMinutes < 1 || cfg.FreezeMinutes > 1440 {
		cfg.FreezeMinutes = 15
	}
	cfg.IsActive = true
}

func (s *Service) GetSMSProvider(ctx context.Context) (*SMSProviderDTO, error) {
	cfg := s.getSMSProviderConfig(ctx)
	dto := toSMSProviderDTO(cfg)
	return &dto, nil
}

func (s *Service) UpsertSMSProvider(ctx context.Context, req SMSProviderUpsert) (*SMSProviderDTO, error) {
	cfg := s.getSMSProviderConfig(ctx)
	if req.AccessKeyID != "" {
		cfg.AccessKeyID = strings.TrimSpace(req.AccessKeyID)
	}
	if req.AccessKeySecret != "" && req.AccessKeySecret != maskedSecret {
		enc, err := encryptSecret(req.AccessKeySecret)
		if err != nil {
			return nil, err
		}
		cfg.AccessKeySecretEncrypted = enc
	}
	if req.RegionID != "" {
		cfg.RegionID = strings.TrimSpace(req.RegionID)
	}
	if req.Endpoint != "" {
		cfg.Endpoint = strings.TrimSpace(req.Endpoint)
	}
	if req.SignName != "" {
		cfg.SignName = strings.TrimSpace(req.SignName)
	}
	if req.TemplateCode != "" {
		cfg.TemplateCode = strings.TrimSpace(req.TemplateCode)
	}
	if req.TemplateParamName != "" {
		cfg.TemplateParamName = strings.TrimSpace(req.TemplateParamName)
	}
	if req.IsActive != nil {
		cfg.IsActive = *req.IsActive
	}
	if cfg.Provider == "" {
		cfg.Provider = ProviderAliyun
	}
	if cfg.TemplateCode == "" {
		cfg.TemplateCode = "SMS_505710272"
	}
	if cfg.TemplateParamName == "" {
		cfg.TemplateParamName = "code"
	}
	if cfg.RegionID == "" {
		cfg.RegionID = "cn-hangzhou"
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "dysmsapi.aliyuncs.com"
	}
	if cfg.ID == 0 {
		if err := s.db.WithContext(ctx).Create(&cfg).Error; err != nil {
			return nil, err
		}
	} else if err := s.db.WithContext(ctx).Save(&cfg).Error; err != nil {
		return nil, err
	}
	dto := toSMSProviderDTO(cfg)
	return &dto, nil
}

func (s *Service) GetCaptchaProvider(ctx context.Context) (*CaptchaProviderDTO, error) {
	cfg := s.getCaptchaProviderConfig(ctx)
	dto := toCaptchaProviderDTO(cfg)
	return &dto, nil
}

func (s *Service) UpsertCaptchaProvider(ctx context.Context, req CaptchaProviderUpsert) (*CaptchaProviderDTO, error) {
	cfg := s.getCaptchaProviderConfig(ctx)
	if req.AccessKeyID != "" {
		cfg.AccessKeyID = strings.TrimSpace(req.AccessKeyID)
	}
	if req.AccessKeySecret != "" && req.AccessKeySecret != maskedSecret {
		enc, err := encryptSecret(req.AccessKeySecret)
		if err != nil {
			return nil, err
		}
		cfg.AccessKeySecretEncrypted = enc
	}
	if req.EKey != "" && req.EKey != maskedSecret {
		enc, err := encryptSecret(req.EKey)
		if err != nil {
			return nil, err
		}
		cfg.EKeyEncrypted = enc
	}
	if req.RegionID != "" {
		cfg.RegionID = strings.TrimSpace(req.RegionID)
	}
	if req.Endpoint != "" {
		cfg.Endpoint = strings.TrimSpace(req.Endpoint)
	}
	if req.SceneID != "" {
		cfg.SceneID = strings.TrimSpace(req.SceneID)
	}
	if req.Prefix != "" {
		cfg.Prefix = strings.TrimSpace(req.Prefix)
	}
	if req.IsActive != nil {
		cfg.IsActive = *req.IsActive
	}
	if req.EncryptMode != nil {
		cfg.EncryptMode = *req.EncryptMode
	}
	if req.EncryptedSceneTTL > 0 {
		cfg.EncryptedSceneTTLSeconds = req.EncryptedSceneTTL
	}
	if cfg.Provider == "" {
		cfg.Provider = ProviderAliyun
	}
	if cfg.RegionID == "" {
		cfg.RegionID = "cn-shanghai"
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "captcha.cn-shanghai.aliyuncs.com"
	}
	if cfg.EncryptedSceneTTLSeconds <= 0 {
		cfg.EncryptedSceneTTLSeconds = 3600
	}
	if cfg.ID == 0 {
		if err := s.db.WithContext(ctx).Create(&cfg).Error; err != nil {
			return nil, err
		}
	} else if err := s.db.WithContext(ctx).Save(&cfg).Error; err != nil {
		return nil, err
	}
	dto := toCaptchaProviderDTO(cfg)
	return &dto, nil
}

func (s *Service) TestSMSProvider(ctx context.Context, phoneRaw, code string) (*SendResult, error) {
	phone, err := NormalizeCNPhone(phoneRaw)
	if err != nil {
		return nil, err
	}
	if code == "" {
		code = "123456"
	}
	cfg := s.getSMSProviderConfig(ctx)
	if !cfg.IsActive {
		return nil, fmt.Errorf("sms provider is not enabled")
	}
	secret, err := decryptSecret(cfg.AccessKeySecretEncrypted)
	if err != nil {
		return nil, err
	}
	return s.sender.Send(ctx, SendRequest{
		PhoneE164: phone, Code: code, AccessKeyID: cfg.AccessKeyID, AccessKeySecret: secret,
		RegionID: cfg.RegionID, Endpoint: cfg.Endpoint, SignName: cfg.SignName,
		TemplateCode: cfg.TemplateCode, TemplateParamName: cfg.TemplateParamName,
	})
}

func (s *Service) TestCaptchaProvider(ctx context.Context, captchaVerifyParam string) (*CaptchaVerifyResult, error) {
	cfg := s.getCaptchaProviderConfig(ctx)
	if !cfg.IsActive {
		return nil, fmt.Errorf("captcha provider is not enabled")
	}
	secret, err := decryptSecret(cfg.AccessKeySecretEncrypted)
	if err != nil {
		return nil, err
	}
	return s.verifier.Verify(ctx, CaptchaVerifyRequest{
		AccessKeyID: cfg.AccessKeyID, AccessKeySecret: secret, RegionID: cfg.RegionID,
		Endpoint: cfg.Endpoint, SceneID: cfg.SceneID, CaptchaVerifyParam: captchaVerifyParam,
	})
}

func (s *Service) PublicPhoneConfig(ctx context.Context) map[string]interface{} {
	smsCfg := s.getSMSProviderConfig(ctx)
	captchaCfg := s.getCaptchaProviderConfig(ctx)
	encryptedSceneID := ""
	if captchaCfg.IsActive && captchaCfg.EncryptMode {
		if ekey, err := decryptSecret(captchaCfg.EKeyEncrypted); err == nil {
			encryptedSceneID, _ = buildEncryptedSceneID(captchaCfg.SceneID, ekey, captchaCfg.EncryptedSceneTTLSeconds, time.Now())
		}
	}
	return map[string]interface{}{
		"enabled":      smsCfg.IsActive,
		"country_code": "CN",
		"dial_code":    "+86",
		"captcha": map[string]interface{}{
			"enabled":            captchaCfg.IsActive,
			"region":             captchaCfg.RegionID,
			"prefix":             captchaCfg.Prefix,
			"scene_id":           captchaCfg.SceneID,
			"encrypt_mode":       captchaCfg.EncryptMode,
			"encrypted_scene_id": encryptedSceneID,
		},
	}
}

func (s *Service) Precheck(ctx context.Context, req PrecheckRequest) (*PrecheckResult, error) {
	phone, err := NormalizeCNPhone(req.Phone)
	if err != nil {
		return nil, err
	}
	cfg := s.GetRiskConfig(ctx)
	if blocked, reason := s.isBlockedPhone(ctx, phone, cfg); blocked {
		s.logSMS(ctx, phone, req.Purpose, SMSStatusBlocked, reason, "", "", req.IP, req.Fingerprint)
		return &PrecheckResult{Allowed: false, NeedCaptcha: false, LimitType: reason, Phone: MaskPhone(phone)}, nil
	}
	if err := s.checkReadOnlyLimits(ctx, phone, req.IP, req.Fingerprint, cfg); err != nil {
		if rl, ok := err.(*RateLimitError); ok {
			return &PrecheckResult{Allowed: false, RetryAfter: rl.RetryAfter, LimitType: rl.LimitType, Phone: MaskPhone(phone)}, nil
		}
		return nil, err
	}
	needCaptcha := cfg.RequireCaptchaAlways
	if !needCaptcha && cfg.RequireCaptchaOnRisk {
		needCaptcha = s.isCaptchaRisk(ctx, phone, req.IP, req.Fingerprint, cfg)
	}
	return &PrecheckResult{Allowed: true, NeedCaptcha: needCaptcha, Phone: MaskPhone(phone)}, nil
}

func (s *Service) SendCode(ctx context.Context, req SendCodeRequest) (*SendCodeResult, error) {
	phone, err := NormalizeCNPhone(req.Phone)
	if err != nil {
		return nil, err
	}
	purpose := strings.ToUpper(strings.TrimSpace(req.Purpose))
	if purpose == "" {
		purpose = PurposeLogin
	}
	cfg := s.GetRiskConfig(ctx)
	if blocked, reason := s.isBlockedPhone(ctx, phone, cfg); blocked {
		s.logSMS(ctx, phone, purpose, SMSStatusBlocked, reason, "", "", req.IP, req.Fingerprint)
		return nil, &RateLimitError{LimitType: reason, Message: "phone number is blocked"}
	}
	if err := s.consumeRateLimits(ctx, phone, req.IP, req.Fingerprint, cfg); err != nil {
		s.logSMS(ctx, phone, purpose, SMSStatusBlocked, rateLimitType(err), "", "", req.IP, req.Fingerprint)
		return nil, err
	}
	needCaptcha := cfg.RequireCaptchaAlways || (cfg.RequireCaptchaOnRisk && s.isCaptchaRisk(ctx, phone, req.IP, req.Fingerprint, cfg))
	if needCaptcha {
		if err := s.verifyCaptcha(ctx, req.CaptchaVerifyParam); err != nil {
			return nil, err
		}
	}
	code, token, err := s.generatePhoneOTP(ctx, phone, purpose, req.IP, req.Fingerprint, cfg)
	if err != nil {
		return nil, err
	}

	smsCfg := s.getSMSProviderConfig(ctx)
	if !smsCfg.IsActive {
		_ = s.db.WithContext(ctx).Delete(token).Error
		return nil, fmt.Errorf("sms provider is not enabled")
	}
	secret, err := decryptSecret(smsCfg.AccessKeySecretEncrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt sms secret: %w", err)
	}
	result, err := s.sender.Send(ctx, SendRequest{
		PhoneE164: phone, Code: code, AccessKeyID: smsCfg.AccessKeyID, AccessKeySecret: secret,
		RegionID: smsCfg.RegionID, Endpoint: smsCfg.Endpoint, SignName: smsCfg.SignName,
		TemplateCode: smsCfg.TemplateCode, TemplateParamName: smsCfg.TemplateParamName,
	})
	if err != nil || result == nil || !result.Success {
		_ = s.db.WithContext(ctx).Delete(token).Error
		msg := ""
		codeText := ""
		reqID := ""
		if err != nil {
			msg = err.Error()
		}
		if result != nil {
			msg = result.Message
			codeText = result.Code
			reqID = result.RequestID
		}
		s.logSMS(ctx, phone, purpose, SMSStatusFailed, "", codeText, msg, req.IP, req.Fingerprint, reqID)
		if msg == "" {
			msg = "sms send failed"
		}
		return nil, errors.New(msg)
	}
	s.logSMS(ctx, phone, purpose, SMSStatusSent, "", result.Code, result.Message, req.IP, req.Fingerprint, result.RequestID)
	return &SendCodeResult{Sent: true, ExpiresIn: cfg.CodeTTLSeconds, Cooldown: cfg.SendCooldownSeconds, Phone: MaskPhone(phone)}, nil
}

func (s *Service) VerifyCode(ctx context.Context, phoneRaw, purpose, code string) (string, error) {
	phone, err := NormalizeCNPhone(phoneRaw)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(code) == "" {
		return "", fmt.Errorf("verification code required")
	}
	purpose = strings.ToUpper(strings.TrimSpace(purpose))
	if purpose == "" {
		purpose = PurposeLogin
	}
	var token model.PhoneOTPToken
	err = s.db.WithContext(ctx).
		Where("phone_e164 = ? AND purpose = ? AND used_at IS NULL", phone, purpose).
		Order("created_at DESC").
		First(&token).Error
	if err != nil {
		return "", fmt.Errorf("verification code not found or expired")
	}
	if time.Now().After(token.ExpiresAt) {
		return "", fmt.Errorf("verification code expired")
	}
	if token.Attempts >= token.MaxAttempts {
		return "", fmt.Errorf("verification attempts exceeded")
	}
	_ = s.db.WithContext(ctx).Model(&model.PhoneOTPToken{}).Where("id = ?", token.ID).UpdateColumn("attempts", gorm.Expr("attempts + 1")).Error
	if err := bcrypt.CompareHashAndPassword([]byte(token.TokenHash), []byte(code)); err != nil {
		return "", fmt.Errorf("invalid verification code")
	}
	now := time.Now()
	if err := s.db.WithContext(ctx).Model(&model.PhoneOTPToken{}).Where("id = ?", token.ID).Update("used_at", &now).Error; err != nil {
		return "", err
	}
	return phone, nil
}

func (s *Service) generatePhoneOTP(ctx context.Context, phone, purpose, ip, fp string, cfg *model.SMSRiskConfig) (string, *model.PhoneOTPToken, error) {
	otp, err := randomNumericOTP(6)
	if err != nil {
		return "", nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(otp), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, err
	}
	token := model.PhoneOTPToken{
		PhoneE164: phone, TokenHash: string(hash), Purpose: purpose,
		ExpiresAt:   time.Now().Add(time.Duration(cfg.CodeTTLSeconds) * time.Second),
		MaxAttempts: cfg.MaxVerifyAttempts, IP: ip, Fingerprint: fp,
	}
	if err := s.db.WithContext(ctx).Create(&token).Error; err != nil {
		return "", nil, err
	}
	return otp, &token, nil
}

func randomNumericOTP(length int) (string, error) {
	const digits = "0123456789"
	out := make([]byte, length)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		if err != nil {
			return "", err
		}
		out[i] = digits[n.Int64()]
	}
	return string(out), nil
}

func (s *Service) verifyCaptcha(ctx context.Context, param string) error {
	captchaCfg := s.getCaptchaProviderConfig(ctx)
	if !captchaCfg.IsActive {
		return nil
	}
	if strings.TrimSpace(param) == "" {
		return fmt.Errorf("captcha verification is required")
	}
	secret, err := decryptSecret(captchaCfg.AccessKeySecretEncrypted)
	if err != nil {
		return fmt.Errorf("decrypt captcha secret: %w", err)
	}
	result, err := s.verifier.Verify(ctx, CaptchaVerifyRequest{
		AccessKeyID: captchaCfg.AccessKeyID, AccessKeySecret: secret,
		RegionID: captchaCfg.RegionID, Endpoint: captchaCfg.Endpoint,
		SceneID: captchaCfg.SceneID, CaptchaVerifyParam: param,
	})
	if err != nil {
		return err
	}
	if result == nil || !result.Success {
		return fmt.Errorf("captcha verification failed")
	}
	return nil
}

func (s *Service) getSMSProviderConfig(ctx context.Context) model.SMSProviderConfig {
	var cfg model.SMSProviderConfig
	if err := s.db.WithContext(ctx).Where("provider = ?", ProviderAliyun).First(&cfg).Error; err == nil {
		return cfg
	}
	return model.SMSProviderConfig{Provider: ProviderAliyun, RegionID: "cn-hangzhou", Endpoint: "dysmsapi.aliyuncs.com", TemplateCode: "SMS_505710272", TemplateParamName: "code"}
}

func (s *Service) getCaptchaProviderConfig(ctx context.Context) model.CaptchaProviderConfig {
	var cfg model.CaptchaProviderConfig
	if err := s.db.WithContext(ctx).Where("provider = ?", ProviderAliyun).First(&cfg).Error; err == nil {
		return cfg
	}
	return model.CaptchaProviderConfig{Provider: ProviderAliyun, RegionID: "cn-shanghai", Endpoint: "captcha.cn-shanghai.aliyuncs.com"}
}

func toSMSProviderDTO(c model.SMSProviderConfig) SMSProviderDTO {
	return SMSProviderDTO{ID: c.ID, Provider: valueOr(c.Provider, ProviderAliyun), AccessKeyID: c.AccessKeyID, HasAccessKeySecret: c.AccessKeySecretEncrypted != "", RegionID: c.RegionID, Endpoint: c.Endpoint, SignName: c.SignName, TemplateCode: c.TemplateCode, TemplateParamName: c.TemplateParamName, IsActive: c.IsActive}
}

func toCaptchaProviderDTO(c model.CaptchaProviderConfig) CaptchaProviderDTO {
	return CaptchaProviderDTO{ID: c.ID, Provider: valueOr(c.Provider, ProviderAliyun), AccessKeyID: c.AccessKeyID, HasAccessKeySecret: c.AccessKeySecretEncrypted != "", HasEKey: c.EKeyEncrypted != "", RegionID: c.RegionID, Endpoint: c.Endpoint, SceneID: c.SceneID, Prefix: c.Prefix, EncryptMode: c.EncryptMode, EncryptedSceneTTL: c.EncryptedSceneTTLSeconds, IsActive: c.IsActive}
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func (s *Service) isBlockedPhone(ctx context.Context, phone string, cfg *model.SMSRiskConfig) (bool, string) {
	local := LocalCNPhone(phone)
	if cfg.BlockVirtualPrefixes {
		for _, p := range []string{"162", "165", "167", "170", "171"} {
			if strings.HasPrefix(local, p) {
				return true, "blocked_virtual_prefix"
			}
		}
	}
	var rules []model.PhoneRiskRule
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).Find(&rules).Error; err != nil {
		return false, ""
	}
	for _, r := range rules {
		p := strings.TrimSpace(r.Pattern)
		if p == "" {
			continue
		}
		switch strings.ToLower(r.RuleType) {
		case "exact":
			if p == phone || p == local {
				return true, "blocked_phone"
			}
		case "prefix":
			if strings.HasPrefix(phone, p) || strings.HasPrefix(local, p) {
				return true, "blocked_prefix"
			}
		}
	}
	return false, ""
}

func (s *Service) checkReadOnlyLimits(ctx context.Context, phone, ip, fp string, cfg *model.SMSRiskConfig) error {
	if s.redis != nil {
		if ttl, _ := s.redis.TTL(ctx, "sms:cooldown:phone:"+phone).Result(); ttl > 0 {
			return &RateLimitError{LimitType: "phone_cooldown", RetryAfter: int(ttl.Seconds())}
		}
	}
	return s.checkDBLimits(ctx, phone, ip, fp, cfg)
}

func (s *Service) checkDBLimits(ctx context.Context, phone, ip, fp string, cfg *model.SMSRiskConfig) error {
	now := time.Now()
	if cfg.SendCooldownSeconds > 0 {
		var n int64
		_ = s.db.WithContext(ctx).Model(&model.PhoneOTPToken{}).Where("phone_e164 = ? AND created_at > ?", phone, now.Add(-time.Duration(cfg.SendCooldownSeconds)*time.Second)).Count(&n).Error
		if n > 0 {
			return &RateLimitError{LimitType: "phone_cooldown", RetryAfter: cfg.SendCooldownSeconds}
		}
	}
	check := func(field, value, typ string, limit int, window time.Duration) error {
		if value == "" || limit <= 0 {
			return nil
		}
		var n int64
		_ = s.db.WithContext(ctx).Model(&model.SMSSendLog{}).Where(field+" = ? AND status = ? AND created_at > ?", value, SMSStatusSent, now.Add(-window)).Count(&n).Error
		if n >= int64(limit) {
			return &RateLimitError{LimitType: typ, RetryAfter: int(window.Seconds())}
		}
		return nil
	}
	if err := check("phone_e164", phone, "phone_hourly", cfg.PhoneHourlyLimit, time.Hour); err != nil {
		return err
	}
	if err := check("phone_e164", phone, "phone_daily", cfg.PhoneDailyLimit, 24*time.Hour); err != nil {
		return err
	}
	if err := check("ip", ip, "ip_hourly", cfg.IPHourlyLimit, time.Hour); err != nil {
		return err
	}
	if err := check("ip", ip, "ip_daily", cfg.IPDailyLimit, 24*time.Hour); err != nil {
		return err
	}
	return check("fingerprint", fp, "fingerprint_daily", cfg.FingerprintDailyLimit, 24*time.Hour)
}

func (s *Service) consumeRateLimits(ctx context.Context, phone, ip, fp string, cfg *model.SMSRiskConfig) error {
	if s.redis == nil {
		return s.checkDBLimits(ctx, phone, ip, fp, cfg)
	}
	if ttl, _ := s.redis.TTL(ctx, "sms:cooldown:phone:"+phone).Result(); ttl > 0 {
		return &RateLimitError{LimitType: "phone_cooldown", RetryAfter: int(ttl.Seconds())}
	}
	if ok, _ := s.redis.SetNX(ctx, "sms:cooldown:phone:"+phone, "1", time.Duration(cfg.SendCooldownSeconds)*time.Second).Result(); !ok {
		return &RateLimitError{LimitType: "phone_cooldown", RetryAfter: cfg.SendCooldownSeconds}
	}
	if err := s.consumeCounter(ctx, "sms:count:phone:hour:"+phone, cfg.PhoneHourlyLimit, time.Hour, "phone_hourly"); err != nil {
		return err
	}
	if err := s.consumeCounter(ctx, "sms:count:phone:day:"+phone, cfg.PhoneDailyLimit, 24*time.Hour, "phone_daily"); err != nil {
		return err
	}
	if ip != "" {
		if err := s.consumeCounter(ctx, "sms:count:ip:hour:"+ip, cfg.IPHourlyLimit, time.Hour, "ip_hourly"); err != nil {
			return err
		}
		if err := s.consumeCounter(ctx, "sms:count:ip:day:"+ip, cfg.IPDailyLimit, 24*time.Hour, "ip_daily"); err != nil {
			return err
		}
	}
	if fp != "" {
		if err := s.consumeCounter(ctx, "sms:count:fp:day:"+fp, cfg.FingerprintDailyLimit, 24*time.Hour, "fingerprint_daily"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) consumeCounter(ctx context.Context, key string, limit int, ttl time.Duration, typ string) error {
	if limit <= 0 {
		return nil
	}
	n, err := s.redis.Incr(ctx, key).Result()
	if err != nil {
		return nil
	}
	if n == 1 {
		_ = s.redis.Expire(ctx, key, ttl).Err()
	}
	if n > int64(limit) {
		return &RateLimitError{LimitType: typ, RetryAfter: int(ttl.Seconds())}
	}
	return nil
}

func (s *Service) isCaptchaRisk(ctx context.Context, phone, ip, fp string, cfg *model.SMSRiskConfig) bool {
	if cfg.RequireCaptchaAlways {
		return true
	}
	if cfg.PhoneHourlyLimit <= 1 {
		return true
	}
	now := time.Now()
	var n int64
	_ = s.db.WithContext(ctx).Model(&model.SMSSendLog{}).Where("phone_e164 = ? AND status = ? AND created_at > ?", phone, SMSStatusSent, now.Add(-time.Hour)).Count(&n).Error
	if n >= int64(cfg.PhoneHourlyLimit-1) {
		return true
	}
	if ip != "" {
		_ = s.db.WithContext(ctx).Model(&model.SMSSendLog{}).Where("ip = ? AND status = ? AND created_at > ?", ip, SMSStatusSent, now.Add(-time.Hour)).Count(&n).Error
		if n >= int64(cfg.IPHourlyLimit/2) {
			return true
		}
	}
	if fp != "" {
		_ = s.db.WithContext(ctx).Model(&model.SMSSendLog{}).Where("fingerprint = ? AND status = ? AND created_at > ?", fp, SMSStatusSent, now.Add(-24*time.Hour)).Count(&n).Error
		if n >= int64(cfg.FingerprintDailyLimit-1) {
			return true
		}
	}
	return false
}

func (s *Service) logSMS(ctx context.Context, phone, purpose, status, limitType, errCode, errMsg, ip, fp string, requestID ...string) {
	reqID := ""
	if len(requestID) > 0 {
		reqID = requestID[0]
	}
	_ = s.db.WithContext(ctx).Create(&model.SMSSendLog{
		PhoneE164: phone, MaskedPhone: MaskPhone(phone), Purpose: purpose,
		Provider: ProviderAliyun, ProviderRequestID: reqID, Status: status,
		LimitType: limitType, ErrorCode: errCode, ErrorMessage: errMsg, IP: ip, Fingerprint: fp,
	}).Error
}

func rateLimitType(err error) string {
	var rl *RateLimitError
	if errors.As(err, &rl) {
		return rl.LimitType
	}
	return ""
}
