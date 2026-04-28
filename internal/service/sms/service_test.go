package sms

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

type mockSender struct {
	calls []SendRequest
}

func (m *mockSender) Send(_ context.Context, req SendRequest) (*SendResult, error) {
	m.calls = append(m.calls, req)
	return &SendResult{Success: true, RequestID: "mock-request", Code: "OK", Message: "OK"}, nil
}

func TestNormalizeCNPhone(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain mainland number", in: "13800138000", want: "+8613800138000"},
		{name: "plus country code", in: "+86 138-0013-8000", want: "+8613800138000"},
		{name: "double zero country code", in: "008613800138000", want: "+8613800138000"},
		{name: "country code without plus", in: "8613800138000", want: "+8613800138000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeCNPhone(tt.in)
			if err != nil {
				t.Fatalf("NormalizeCNPhone returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeCNPhone() = %q, want %q", got, tt.want)
			}
		})
	}

	invalid := []string{"12800138000", "+85291234567", "1380013800", "test"}
	for _, in := range invalid {
		t.Run("reject "+in, func(t *testing.T) {
			if _, err := NormalizeCNPhone(in); err == nil {
				t.Fatalf("NormalizeCNPhone(%q) returned nil error", in)
			}
		})
	}
}

func TestValidateUsername(t *testing.T) {
	valid := []string{"token_user", "User2026", "abc_123"}
	for _, username := range valid {
		if err := ValidateUsername(username); err != nil {
			t.Fatalf("ValidateUsername(%q) returned error: %v", username, err)
		}
	}

	invalid := []string{"abc", "_token", "token_", "123456", "has-dash", "中文账号"}
	for _, username := range invalid {
		if err := ValidateUsername(username); err == nil {
			t.Fatalf("ValidateUsername(%q) returned nil error", username)
		}
	}
}

func TestBuildEncryptedSceneID(t *testing.T) {
	ekey := base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012"))
	got, err := buildEncryptedSceneID("k7nl3rju", ekey, 3600, time.Unix(1777377600, 0))
	if err != nil {
		t.Fatalf("buildEncryptedSceneID returned error: %v", err)
	}
	if got == "" {
		t.Fatal("encrypted scene id is empty")
	}
	if got == "k7nl3rju" {
		t.Fatal("encrypted scene id should not equal raw scene id")
	}

	raw, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("encrypted scene id is not base64: %v", err)
	}
	if len(raw) <= aes.BlockSize || len(raw)%aes.BlockSize != 0 {
		t.Fatalf("encrypted scene id payload length = %d, want iv plus block-aligned ciphertext", len(raw))
	}
	key, _ := base64.StdEncoding.DecodeString(ekey)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	iv, encrypted := raw[:aes.BlockSize], raw[aes.BlockSize:]
	plain := make([]byte, len(encrypted))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, encrypted)
	padding := int(plain[len(plain)-1])
	if padding <= 0 || padding > aes.BlockSize {
		t.Fatalf("invalid pkcs7 padding: %d", padding)
	}
	plain = plain[:len(plain)-padding]
	if string(plain) != "k7nl3rju&1777377600&3600" {
		t.Fatalf("decrypted scene payload = %q", plain)
	}
}

func TestParseCaptchaVerifyResponseUsesVerifyResult(t *testing.T) {
	failed := parseCaptchaVerifyResponse(map[string]interface{}{
		"RequestId":    "req-1",
		"Success":      true,
		"VerifyResult": false,
		"VerifyCode":   "F020",
		"Message":      "ok",
	})
	if failed.Success {
		t.Fatal("OpenAPI Success=true must not be treated as captcha verification success")
	}
	if failed.Code != "F020" || failed.RequestID != "req-1" {
		t.Fatalf("failed captcha parse = %+v", failed)
	}

	passed := parseCaptchaVerifyResponse(map[string]interface{}{
		"RequestId": "req-2",
		"Result": map[string]interface{}{
			"VerifyResult": "true",
			"VerifyCode":   "T001",
		},
	})
	if !passed.Success || passed.Code != "T001" || passed.RequestID != "req-2" {
		t.Fatalf("passed captcha parse = %+v", passed)
	}
}

func TestSendCodeEnforcesCooldownAndStoresFiveMinuteOTP(t *testing.T) {
	ctx := context.Background()
	svc, sender, closeFn := newTestService(t)
	defer closeFn()
	enableSMSProvider(t, ctx, svc)

	result, err := svc.SendCode(ctx, SendCodeRequest{
		Phone:       "13800138000",
		IP:          "203.0.113.1",
		Fingerprint: "fp-1",
		Purpose:     PurposeLogin,
	})
	if err != nil {
		t.Fatalf("SendCode returned error: %v", err)
	}
	if !result.Sent || result.ExpiresIn != 300 || result.Cooldown != 60 {
		t.Fatalf("SendCode result = %+v, want sent with 300s ttl and 60s cooldown", result)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("sender calls = %d, want 1", len(sender.calls))
	}

	var token model.PhoneOTPToken
	if err := svc.db.Where("phone_e164 = ?", "+8613800138000").First(&token).Error; err != nil {
		t.Fatalf("load phone otp token: %v", err)
	}
	ttl := time.Until(token.ExpiresAt)
	if ttl < 295*time.Second || ttl > 305*time.Second {
		t.Fatalf("otp ttl = %s, want about 5 minutes", ttl)
	}

	_, err = svc.SendCode(ctx, SendCodeRequest{Phone: "13800138000", IP: "203.0.113.1", Fingerprint: "fp-1", Purpose: PurposeLogin})
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("second SendCode error = %v, want RateLimitError", err)
	}
	if rl.LimitType != "phone_cooldown" || rl.RetryAfter <= 0 {
		t.Fatalf("rate limit = %+v, want phone_cooldown with retry_after", rl)
	}

	phone, err := svc.VerifyCode(ctx, "13800138000", PurposeLogin, sender.calls[0].Code)
	if err != nil {
		t.Fatalf("VerifyCode returned error: %v", err)
	}
	if phone != "+8613800138000" {
		t.Fatalf("VerifyCode phone = %q, want +8613800138000", phone)
	}
}

func TestPrecheckBlocksVirtualAndCustomPhoneRules(t *testing.T) {
	ctx := context.Background()
	svc, _, closeFn := newTestService(t)
	defer closeFn()

	virtual, err := svc.Precheck(ctx, PrecheckRequest{Phone: "17000138000", IP: "203.0.113.1", Purpose: PurposeLogin})
	if err != nil {
		t.Fatalf("Precheck virtual prefix returned error: %v", err)
	}
	if virtual.Allowed || virtual.LimitType != "blocked_virtual_prefix" {
		t.Fatalf("virtual precheck = %+v, want blocked_virtual_prefix", virtual)
	}

	if err := svc.db.Create(&model.PhoneRiskRule{RuleType: "prefix", Pattern: "166123", Reason: "code platform", IsActive: true}).Error; err != nil {
		t.Fatalf("create phone risk rule: %v", err)
	}
	custom, err := svc.Precheck(ctx, PrecheckRequest{Phone: "16612345678", IP: "203.0.113.1", Purpose: PurposeLogin})
	if err != nil {
		t.Fatalf("Precheck custom rule returned error: %v", err)
	}
	if custom.Allowed || custom.LimitType != "blocked_prefix" {
		t.Fatalf("custom precheck = %+v, want blocked_prefix", custom)
	}
}

func newTestService(t *testing.T) (*Service, *mockSender, func()) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.SMSProviderConfig{},
		&model.CaptchaProviderConfig{},
		&model.SMSRiskConfig{},
		&model.PhoneOTPToken{},
		&model.SMSSendLog{},
		&model.PhoneRiskRule{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	redisClient := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	sender := &mockSender{}
	svc := NewService(db, redisClient).WithSender(sender)
	return svc, sender, func() {
		_ = redisClient.Close()
		mr.Close()
	}
}

func enableSMSProvider(t *testing.T, ctx context.Context, svc *Service) {
	t.Helper()
	active := true
	if _, err := svc.UpsertSMSProvider(ctx, SMSProviderUpsert{
		AccessKeyID:       "test-ak",
		AccessKeySecret:   "test-secret",
		SignName:          "TOKENHUBHK",
		TemplateCode:      "SMS_505710272",
		TemplateParamName: "code",
		IsActive:          &active,
	}); err != nil {
		t.Fatalf("enable sms provider: %v", err)
	}
}
