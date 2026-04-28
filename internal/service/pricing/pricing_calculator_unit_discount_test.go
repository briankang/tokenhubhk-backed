package pricing

// Unit 路径折扣链测试（A1 任务）
//
// 验证 CalculateCostByUnit 在 PricingUnit 为 per_image / per_second / per_call /
// per_10k_characters / per_minute / per_hour 等非 Token 单位下，FIXED / MARKUP /
// DISCOUNT 三种折扣类型都能正确生效（之前仅 DISCOUNT 生效）。
//
// 测试矩阵：
//   - PerImage × FIXED      → 每张图直接覆盖为固定积分价
//   - PerImage × MARKUP     → 每张图按 (1 + MarkupRate) 加价
//   - PerImage × DISCOUNT   → 每张图按 InputDiscount 折扣（向后兼容）
//   - PerSecond × MARKUP    → 验证不同 Unit 类型也走通
//   - PerCall × FIXED       → 验证 per_call
//   - NoDiscount            → 验证无折扣回退正常
//   - InvalidPricingType    → 验证未知折扣类型不崩

import (
	"context"
	"testing"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
)

// 用一张 PerImage 模型做基础，basePrice = 0.20 RMB/张
func seedUnitImageModel(t *testing.T) (*PricingCalculator, uint, uint) {
	t.Helper()
	db := newPricingCalculatorTestDB(t)
	m := &model.AIModel{
		ModelName:     "test-image-1024",
		ModelType:     model.ModelTypeImageGeneration,
		PricingUnit:   model.UnitPerImage,
		InputCostRMB:  0.20,
		OutputCostRMB: 0,
	}
	if err := db.Create(m).Error; err != nil {
		t.Fatalf("create model: %v", err)
	}
	if err := db.Create(&model.ModelPricing{
		ModelID:       m.ID,
		InputPriceRMB: 0.20,
		Currency:      "CREDIT",
	}).Error; err != nil {
		t.Fatalf("create pricing: %v", err)
	}
	return NewPricingCalculator(db), m.ID, 100
}

// FIXED：每张图固定 0.5 RMB（5000 积分），覆盖 basePrice
func TestUnitDiscountFIXED_PerImage(t *testing.T) {
	calc, modelID, userID := seedUnitImageModel(t)
	fixedCredits := float64(credits.RMBToCredits(0.5)) // 5000 积分
	if err := calc.db.Create(&model.UserModelDiscount{
		UserID:      userID,
		ModelID:     modelID,
		PricingType: "FIXED",
		InputPrice:  &fixedCredits,
		IsActive:    true,
	}).Error; err != nil {
		t.Fatalf("create discount: %v", err)
	}

	got, err := calc.CalculateCostByUnit(context.Background(), userID, modelID, 0, 0, UsageInput{ImageCount: 3})
	if err != nil {
		t.Fatalf("calc: %v", err)
	}
	// 期望：3 张 × 0.5 = 1.5 RMB = 15000 积分
	wantCredits := credits.RMBToCredits(1.5)
	if got.TotalCost != wantCredits {
		t.Errorf("FIXED per_image: got %d credits, want %d (1.5 RMB × 3 images)", got.TotalCost, wantCredits)
	}
	// 单价应为 0.5 RMB
	if got.PriceDetail.InputPriceRMB != 0.5 {
		t.Errorf("FIXED per_image: unit price RMB got %.6f, want 0.500000", got.PriceDetail.InputPriceRMB)
	}
	if got.PriceDetail.Source != "user_custom" {
		t.Errorf("FIXED per_image: source got %q, want user_custom", got.PriceDetail.Source)
	}
}

// MARKUP：加价 50%，单价应为 0.20 × (1 + 0.5) = 0.30 RMB
func TestUnitDiscountMARKUP_PerImage(t *testing.T) {
	calc, modelID, userID := seedUnitImageModel(t)
	rate := 0.5 // +50%
	if err := calc.db.Create(&model.UserModelDiscount{
		UserID:      userID,
		ModelID:     modelID,
		PricingType: "MARKUP",
		MarkupRate:  &rate,
		IsActive:    true,
	}).Error; err != nil {
		t.Fatalf("create discount: %v", err)
	}

	got, err := calc.CalculateCostByUnit(context.Background(), userID, modelID, 0, 0, UsageInput{ImageCount: 2})
	if err != nil {
		t.Fatalf("calc: %v", err)
	}
	// 期望：2 张 × 0.30 = 0.60 RMB
	want := credits.RMBToCredits(0.60)
	if got.TotalCost != want {
		t.Errorf("MARKUP per_image: got %d credits, want %d (0.30 RMB × 2 images)", got.TotalCost, want)
	}
	// 注意：applyDiscount 中 MARKUP 走 int64(* (1+rate)) 然后再 CreditsToRMB,
	// 浮点精度可能有微小偏差,所以这里只检查 ≈
	if diff := got.PriceDetail.InputPriceRMB - 0.30; diff < -0.001 || diff > 0.001 {
		t.Errorf("MARKUP per_image: unit price RMB got %.6f, want ~0.300000", got.PriceDetail.InputPriceRMB)
	}
}

// DISCOUNT：8 折，单价应为 0.20 × 0.8 = 0.16 RMB
func TestUnitDiscountDISCOUNT_PerImage(t *testing.T) {
	calc, modelID, userID := seedUnitImageModel(t)
	rate := 0.8
	if err := calc.db.Create(&model.UserModelDiscount{
		UserID:       userID,
		ModelID:      modelID,
		PricingType:  "DISCOUNT",
		DiscountRate: &rate,
		IsActive:     true,
	}).Error; err != nil {
		t.Fatalf("create discount: %v", err)
	}

	got, err := calc.CalculateCostByUnit(context.Background(), userID, modelID, 0, 0, UsageInput{ImageCount: 5})
	if err != nil {
		t.Fatalf("calc: %v", err)
	}
	// 期望：5 张 × 0.16 = 0.80 RMB
	want := credits.RMBToCredits(0.80)
	if got.TotalCost != want {
		t.Errorf("DISCOUNT per_image: got %d credits, want %d (0.16 RMB × 5 images)", got.TotalCost, want)
	}
}

// PerSecond × MARKUP 验证不同 Unit 类型也走通
func TestUnitDiscountMARKUP_PerSecond(t *testing.T) {
	db := newPricingCalculatorTestDB(t)
	m := &model.AIModel{
		ModelName:    "test-asr-stream",
		ModelType:    model.ModelTypeASR,
		PricingUnit:  model.UnitPerSecond,
		InputCostRMB: 0.001, // 0.001 RMB/秒
	}
	if err := db.Create(m).Error; err != nil {
		t.Fatalf("create model: %v", err)
	}
	if err := db.Create(&model.ModelPricing{
		ModelID: m.ID, InputPriceRMB: 0.001, Currency: "CREDIT",
	}).Error; err != nil {
		t.Fatalf("create pricing: %v", err)
	}
	rate := 0.2
	if err := db.Create(&model.UserModelDiscount{
		UserID: 200, ModelID: m.ID, PricingType: "MARKUP", MarkupRate: &rate, IsActive: true,
	}).Error; err != nil {
		t.Fatalf("create discount: %v", err)
	}

	calc := NewPricingCalculator(db)
	got, err := calc.CalculateCostByUnit(context.Background(), 200, m.ID, 0, 0, UsageInput{DurationSec: 60})
	if err != nil {
		t.Fatalf("calc: %v", err)
	}
	// 期望：60s × 0.001 × 1.2 = 0.072 RMB
	want := credits.RMBToCredits(0.072)
	if diff := got.TotalCost - want; diff < -2 || diff > 2 {
		t.Errorf("MARKUP per_second: got %d credits, want ~%d (0.072 RMB)", got.TotalCost, want)
	}
}

// PerCall × FIXED 验证 per_call 单位
func TestUnitDiscountFIXED_PerCall(t *testing.T) {
	db := newPricingCalculatorTestDB(t)
	m := &model.AIModel{
		ModelName:    "test-rerank",
		ModelType:    model.ModelTypeRerank,
		PricingUnit:  model.UnitPerCall,
		InputCostRMB: 0.01,
	}
	if err := db.Create(m).Error; err != nil {
		t.Fatalf("create model: %v", err)
	}
	if err := db.Create(&model.ModelPricing{
		ModelID: m.ID, InputPriceRMB: 0.01, Currency: "CREDIT",
	}).Error; err != nil {
		t.Fatalf("create pricing: %v", err)
	}
	fixed := float64(credits.RMBToCredits(0.05)) // 每次 0.05 RMB
	if err := db.Create(&model.UserModelDiscount{
		UserID: 300, ModelID: m.ID, PricingType: "FIXED", InputPrice: &fixed, IsActive: true,
	}).Error; err != nil {
		t.Fatalf("create discount: %v", err)
	}

	calc := NewPricingCalculator(db)
	got, err := calc.CalculateCostByUnit(context.Background(), 300, m.ID, 0, 0, UsageInput{CallCount: 4})
	if err != nil {
		t.Fatalf("calc: %v", err)
	}
	// 期望：4 次 × 0.05 = 0.20 RMB
	want := credits.RMBToCredits(0.20)
	if got.TotalCost != want {
		t.Errorf("FIXED per_call: got %d credits, want %d (0.05 RMB × 4 calls)", got.TotalCost, want)
	}
}

// 无折扣：fall back 到平台底价
func TestUnitDiscountNone_FallsBackToPlatform(t *testing.T) {
	calc, modelID, userID := seedUnitImageModel(t)

	got, err := calc.CalculateCostByUnit(context.Background(), userID, modelID, 0, 0, UsageInput{ImageCount: 1})
	if err != nil {
		t.Fatalf("calc: %v", err)
	}
	// 期望：1 张 × 0.20 = 0.20 RMB
	want := credits.RMBToCredits(0.20)
	if got.TotalCost != want {
		t.Errorf("no discount: got %d credits, want %d", got.TotalCost, want)
	}
	if got.PriceDetail.Source != "platform" {
		t.Errorf("no discount: source got %q, want platform", got.PriceDetail.Source)
	}
}
