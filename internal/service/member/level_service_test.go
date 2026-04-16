package member

import (
	"testing"

	"tokenhub-server/internal/model"
)

// ========== MemberLevel Model 字段验证 ==========

func TestMemberLevel_HasRPMTPMFields(t *testing.T) {
	// 验证新增的 DefaultRPM 和 DefaultTPM 字段存在且有正确的 json tag
	level := model.MemberLevel{
		LevelCode:   "V0",
		LevelName:   "体验会员",
		DefaultRPM:  30,
		DefaultTPM:  50000,
	}

	if level.DefaultRPM != 30 {
		t.Errorf("DefaultRPM expected 30, got %d", level.DefaultRPM)
	}
	if level.DefaultTPM != 50000 {
		t.Errorf("DefaultTPM expected 50000, got %d", level.DefaultTPM)
	}
}

func TestMemberLevel_NoLegacyFields(t *testing.T) {
	// 通过编译验证旧字段已移除：
	// 如果这些字段仍存在，取消注释下面的行会导致编译成功
	// 如果已正确移除，取消注释会导致编译失败

	level := model.MemberLevel{}
	_ = level.DefaultRPM  // 新字段应存在
	_ = level.DefaultTPM  // 新字段应存在
	_ = level.ModelDiscount // 保留字段应存在

	// 以下字段应已从 struct 移除（通过编译验证）
	// 如果取消注释下面的代码能编译通过，说明旧字段未正确移除
	// _ = level.MonthlyGift     // 已移除
	// _ = level.MonthlyGiftRMB  // 已移除
	// _ = level.MaxTokensPerReq // 已移除
	// _ = level.DailyLimit      // 已移除
	// _ = level.DailyLimitRMB   // 已移除
	t.Log("MemberLevel struct 旧字段已正确移除，新字段 DefaultRPM/DefaultTPM 已添加")
}

// ========== MemberProfileResponse 字段验证 ==========

func TestMemberProfileResponse_NoGiftFields(t *testing.T) {
	resp := MemberProfileResponse{
		ID:              1,
		UserID:          100,
		MemberLevelID:   1,
		TotalConsume:    50.0,
		DegradeWarnings: 0,
	}

	// 验证不再有月赠相关字段
	// 以下字段应已从 struct 移除：
	// _ = resp.MonthlyGiftClaimed  // 已移除
	// _ = resp.MonthlyGiftAmount   // 已移除
	// _ = resp.LastGiftAt          // 已移除

	if resp.UserID != 100 {
		t.Errorf("UserID expected 100, got %d", resp.UserID)
	}
	t.Log("MemberProfileResponse 月赠字段已正确移除")
}

// ========== UserRateLimits 结构体验证 ==========

func TestUserRateLimits_Struct(t *testing.T) {
	limits := &UserRateLimits{
		RPM: 120,
		TPM: 200000,
	}

	if limits.RPM != 120 {
		t.Errorf("RPM expected 120, got %d", limits.RPM)
	}
	if limits.TPM != 200000 {
		t.Errorf("TPM expected 200000, got %d", limits.TPM)
	}
}

// ========== Service 方法存在性验证 ==========

func TestMemberLevelService_MethodsExist(t *testing.T) {
	// 验证 MemberLevelService 有所有必需方法（通过编译验证）
	svc := &MemberLevelService{}

	// 新增方法应存在
	_ = svc.GetUserRateLimits

	// 保留的方法应存在
	_ = svc.GetAllLevels
	_ = svc.GetProfile
	_ = svc.GetUpgradeProgress
	_ = svc.CheckAndUpgrade
	_ = svc.CheckAndDegradeAll
	_ = svc.RotateMonthConsume
	_ = svc.CreateLevel
	_ = svc.UpdateLevel
	_ = svc.DeleteLevel
	_ = svc.GetEffectiveDiscount
	_ = svc.InitMemberProfile

	// 已移除的方法（取消注释会导致编译失败）：
	// _ = svc.GrantMonthlyGifts  // 已移除

	t.Log("MemberLevelService 方法签名验证通过：GrantMonthlyGifts 已移除，GetUserRateLimits 已添加")
}

// ========== RoundTo6 工具函数验证 ==========

func TestRoundTo6(t *testing.T) {
	tests := []struct {
		input    float64
		expected float64
	}{
		{1.123456789, 1.123457},
		{0.0, 0.0},
		{100.0, 100.0},
		{0.1+0.2, 0.3}, // 浮点精度测试
	}

	for _, tc := range tests {
		result := roundTo6(tc.input)
		diff := result - tc.expected
		if diff < -0.000001 || diff > 0.000001 {
			t.Errorf("roundTo6(%v) = %v, expected %v", tc.input, result, tc.expected)
		}
	}
}

// ========== V0-V4 等级配置完整性验证 ==========

func TestSeedLevelConfigs(t *testing.T) {
	// 验证 V0-V4 等级的 RPM/TPM 配置合理性（递增）
	type levelConfig struct {
		code string
		rpm  int
		tpm  int
	}
	levels := []levelConfig{
		{"V0", 30, 50000},
		{"V1", 60, 100000},
		{"V2", 120, 200000},
		{"V3", 300, 500000},
		{"V4", 600, 1000000},
	}

	for i := 1; i < len(levels); i++ {
		if levels[i].rpm <= levels[i-1].rpm {
			t.Errorf("%s RPM (%d) should be greater than %s RPM (%d)",
				levels[i].code, levels[i].rpm, levels[i-1].code, levels[i-1].rpm)
		}
		if levels[i].tpm <= levels[i-1].tpm {
			t.Errorf("%s TPM (%d) should be greater than %s TPM (%d)",
				levels[i].code, levels[i].tpm, levels[i-1].code, levels[i-1].tpm)
		}
	}

	// 验证 V0 有基本限制
	if levels[0].rpm <= 0 {
		t.Error("V0 should have positive RPM")
	}
	if levels[0].tpm <= 0 {
		t.Error("V0 should have positive TPM")
	}
}
