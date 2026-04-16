package model

import (
	"testing"
	"time"
)

// TestReferralAttribution_TableName 验证表名正确
func TestReferralAttribution_TableName(t *testing.T) {
	if got := (ReferralAttribution{}).TableName(); got != "referral_attributions" {
		t.Errorf("TableName = %s, want referral_attributions", got)
	}
}

// TestReferralAttribution_Fields 验证核心字段可赋值
func TestReferralAttribution_Fields(t *testing.T) {
	now := time.Now()
	unlockedAt := now.Add(time.Hour)
	a := ReferralAttribution{
		UserID:        100,
		InviterID:     200,
		ReferralCode:  "ABC123",
		AttributedAt:  now,
		ExpiresAt:     now.AddDate(0, 0, 90),
		UnlockedAt:    &unlockedAt,
		IsValid:       true,
		InvalidReason: "",
	}
	if a.UserID != 100 || a.InviterID != 200 {
		t.Error("UserID/InviterID assignment failed")
	}
	if a.UnlockedAt == nil || !a.UnlockedAt.Equal(unlockedAt) {
		t.Error("UnlockedAt pointer field failed")
	}
}

// TestUserCommissionOverride_TableName 验证表名
func TestUserCommissionOverride_TableName(t *testing.T) {
	if got := (UserCommissionOverride{}).TableName(); got != "user_commission_overrides" {
		t.Errorf("TableName = %s, want user_commission_overrides", got)
	}
}

// TestUserCommissionOverride_Fields 验证字段
func TestUserCommissionOverride_Fields(t *testing.T) {
	now := time.Now()
	endAt := now.AddDate(1, 0, 0)
	active := true
	o := UserCommissionOverride{
		UserID:         123,
		IsActive:       &active,
		CommissionRate: 0.25,
		EffectiveFrom:  now,
		EffectiveTo:    &endAt,
		Note:           "KOL 合作",
		CreatedBy:      1,
	}
	if o.CommissionRate != 0.25 {
		t.Errorf("CommissionRate = %f, want 0.25", o.CommissionRate)
	}
	if o.EffectiveTo == nil {
		t.Error("EffectiveTo should accept pointer value")
	}
}

// TestRegistrationGuard_TableName 验证表名
func TestRegistrationGuard_TableName(t *testing.T) {
	if got := (RegistrationGuard{}).TableName(); got != "registration_guards" {
		t.Errorf("TableName = %s, want registration_guards", got)
	}
}

// TestRegistrationGuard_AllFieldsSettable 验证所有 7 层防御字段可读写
func TestRegistrationGuard_AllFieldsSettable(t *testing.T) {
	g := RegistrationGuard{
		CaptchaEnabled:         true,
		CaptchaProvider:        "turnstile",
		CaptchaSiteKey:         "key",
		CaptchaSecretEnc:       "enc",
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
		BlockVPN:               false,
		BlockTor:               true,
		DisposableEmailBlocked: true,
		IsActive:               true,
	}
	if g.EmailOTPLength != 6 || g.IPRegLimitPerHour != 5 {
		t.Error("OTP or IP limit field failed")
	}
	if !g.BlockTor || g.BlockVPN {
		t.Error("VPN/Tor flags failed")
	}
}

// TestRegistrationEvent_TableName 验证表名
func TestRegistrationEvent_TableName(t *testing.T) {
	if got := (RegistrationEvent{}).TableName(); got != "registration_events" {
		t.Errorf("TableName = %s, want registration_events", got)
	}
}

// TestRegistrationEvent_DecisionValues 验证 Decision 枚举值使用合法
func TestRegistrationEvent_DecisionValues(t *testing.T) {
	decisions := []string{"PASS", "BLOCKED", "SHADOW"}
	for _, d := range decisions {
		e := RegistrationEvent{Decision: d}
		if e.Decision != d {
			t.Errorf("decision %s failed", d)
		}
	}
}

// TestEmailOTPToken_TableName 验证表名
func TestEmailOTPToken_TableName(t *testing.T) {
	if got := (EmailOTPToken{}).TableName(); got != "email_otp_tokens" {
		t.Errorf("TableName = %s, want email_otp_tokens", got)
	}
}

// TestEmailOTPToken_UsedAtNullable 验证 UsedAt 是可空指针
func TestEmailOTPToken_UsedAtNullable(t *testing.T) {
	tok := EmailOTPToken{
		Email:       "test@example.com",
		TokenHash:   "hash",
		Purpose:     "REGISTER",
		ExpiresAt:   time.Now().Add(5 * time.Minute),
		UsedAt:      nil,
		Attempts:    0,
		MaxAttempts: 5,
	}
	if tok.UsedAt != nil {
		t.Error("UsedAt should start nil")
	}
	now := time.Now()
	tok.UsedAt = &now
	if tok.UsedAt == nil {
		t.Error("UsedAt should accept pointer")
	}
}

// TestConfigAuditLog_TableName 验证表名
func TestConfigAuditLog_TableName(t *testing.T) {
	if got := (ConfigAuditLog{}).TableName(); got != "config_audit_logs" {
		t.Errorf("TableName = %s, want config_audit_logs", got)
	}
}

// TestConfigAuditLog_ActionTypes 验证 Action 字段支持的枚举值
func TestConfigAuditLog_ActionTypes(t *testing.T) {
	actions := []string{"CREATE", "UPDATE", "DELETE", "TOGGLE"}
	for _, a := range actions {
		log := ConfigAuditLog{Action: a}
		if log.Action != a {
			t.Errorf("action %s failed", a)
		}
	}
}

// TestDisposableEmailDomain_TableName 验证表名
func TestDisposableEmailDomain_TableName(t *testing.T) {
	if got := (DisposableEmailDomain{}).TableName(); got != "disposable_email_domains" {
		t.Errorf("TableName = %s, want disposable_email_domains", got)
	}
}

// TestReferralConfig_NewFields 验证 v3.1 新字段存在
func TestReferralConfig_NewFields(t *testing.T) {
	c := ReferralConfig{
		CommissionRate:       0.10,
		AttributionDays:      90,
		LifetimeCapCredits:   30000000,
		MinPaidCreditsUnlock: 100000,
		MinWithdrawAmount:    1000000,
		SettleDays:           7,
		IsActive:             true,
	}
	if c.CommissionRate != 0.10 {
		t.Errorf("CommissionRate = %f, want 0.10", c.CommissionRate)
	}
	if c.AttributionDays != 90 {
		t.Errorf("AttributionDays = %d, want 90", c.AttributionDays)
	}
	if c.LifetimeCapCredits != 30000000 {
		t.Errorf("LifetimeCapCredits = %d, want 30000000", c.LifetimeCapCredits)
	}
}

// TestReferralConfig_DeprecatedFieldsStillExist 验证弃用字段仍保留兼容
func TestReferralConfig_DeprecatedFieldsStillExist(t *testing.T) {
	c := ReferralConfig{
		PersonalCashbackRate: 0.05,
		L1CommissionRate:     0,
		L2CommissionRate:     0,
		L3CommissionRate:     0,
	}
	if c.PersonalCashbackRate != 0.05 {
		t.Error("PersonalCashbackRate compat field missing")
	}
}

// TestQuotaConfig_NewFields 验证 v3.1 新字段存在
func TestQuotaConfig_NewFields(t *testing.T) {
	q := QuotaConfig{
		DefaultFreeQuota:     3000,
		InviteeBonus:         5000,
		InviteeUnlockCredits: 10000,
		InviterBonus:         10000,
		InviterUnlockPaidRMB: 100000,
		InviterMonthlyCap:    10,
	}
	if q.InviteeBonus != 5000 {
		t.Errorf("InviteeBonus = %d, want 5000", q.InviteeBonus)
	}
	if q.InviterMonthlyCap != 10 {
		t.Errorf("InviterMonthlyCap = %d, want 10", q.InviterMonthlyCap)
	}
}

// TestCommissionRecord_NewFields 验证 v3.1 新字段存在
func TestCommissionRecord_NewFields(t *testing.T) {
	now := time.Now()
	attrID := uint(1)
	overID := uint(2)
	r := CommissionRecord{
		AttributionID: &attrID,
		OverrideID:    &overID,
		EffectiveRate: 0.25,
		Credited:      false,
		SettleAt:      &now,
		RelatedID:     "order_xxx",
	}
	if r.AttributionID == nil || *r.AttributionID != 1 {
		t.Error("AttributionID pointer failed")
	}
	if r.OverrideID == nil || *r.OverrideID != 2 {
		t.Error("OverrideID pointer failed")
	}
	if r.EffectiveRate != 0.25 {
		t.Errorf("EffectiveRate = %f, want 0.25", r.EffectiveRate)
	}
}
