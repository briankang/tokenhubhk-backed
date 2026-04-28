package pricing

import (
	"encoding/json"
	"testing"

	"tokenhub-server/internal/model"
)

// TestValidatePriceTiersMonotonic_Empty 空 / null tiers 不警告
func TestValidatePriceTiersMonotonic_Empty(t *testing.T) {
	if got := ValidatePriceTiersMonotonic(nil, "test"); got != nil {
		t.Errorf("nil tiers: expected no warnings, got %v", got)
	}
	if got := ValidatePriceTiersMonotonic(model.JSON("null"), "test"); got != nil {
		t.Errorf("null tiers: expected no warnings, got %v", got)
	}
	if got := ValidatePriceTiersMonotonic(model.JSON(""), "test"); got != nil {
		t.Errorf("empty tiers: expected no warnings, got %v", got)
	}
}

// TestValidatePriceTiersMonotonic_SingleTier 单 tier 不警告
func TestValidatePriceTiersMonotonic_SingleTier(t *testing.T) {
	tiers := model.PriceTiersData{
		Tiers: []model.PriceTier{{Name: "[0,∞)", InputMin: 0, InputPrice: 1.5}},
	}
	raw, _ := json.Marshal(tiers)
	if got := ValidatePriceTiersMonotonic(raw, "test"); got != nil {
		t.Errorf("single tier: expected no warnings, got %v", got)
	}
}

// TestValidatePriceTiersMonotonic_StrictlyIncreasing 单调递增不警告
func TestValidatePriceTiersMonotonic_StrictlyIncreasing(t *testing.T) {
	tiers := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "[0,32k)", InputMin: 0, InputPrice: 1.2},
			{Name: "[32k,128k)", InputMin: 32000, InputPrice: 1.5},
			{Name: "[128k,∞)", InputMin: 128000, InputPrice: 1.8},
		},
	}
	raw, _ := json.Marshal(tiers)
	if got := ValidatePriceTiersMonotonic(raw, "test"); got != nil {
		t.Errorf("strictly increasing: expected no warnings, got %v", got)
	}
}

// TestValidatePriceTiersMonotonic_Decreasing 反向单调（消费多反而便宜）→ warning
func TestValidatePriceTiersMonotonic_Decreasing(t *testing.T) {
	tiers := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "[0,32k)", InputMin: 0, InputPrice: 1.2},
			{Name: "[32k,128k)", InputMin: 32000, InputPrice: 0.9}, // 反向
		},
	}
	raw, _ := json.Marshal(tiers)
	got := ValidatePriceTiersMonotonic(raw, "test")
	if len(got) != 1 {
		t.Errorf("decreasing tier: expected 1 warning, got %d (%v)", len(got), got)
	}
}

// TestValidatePriceTiersMonotonic_SellingPriceTakesPrecedence 阶梯 SellingPrice 优先于 InputPrice
func TestValidatePriceTiersMonotonic_SellingPriceTakesPrecedence(t *testing.T) {
	low, high := 0.5, 2.0
	tiers := model.PriceTiersData{
		Tiers: []model.PriceTier{
			// 基础 InputPrice 单调递增，但 SellingInputPrice 反向 → 应触发 warning
			{Name: "[0,32k)", InputMin: 0, InputPrice: 1.0, SellingInputPrice: &high},
			{Name: "[32k,∞)", InputMin: 32000, InputPrice: 1.5, SellingInputPrice: &low},
		},
	}
	raw, _ := json.Marshal(tiers)
	got := ValidatePriceTiersMonotonic(raw, "test")
	if len(got) != 1 {
		t.Errorf("decreasing selling price: expected 1 warning, got %d (%v)", len(got), got)
	}
}

// TestValidatePriceTiersMonotonic_ZeroPriceSkipped 零价不参与比较（避免误报新增 tier）
func TestValidatePriceTiersMonotonic_ZeroPriceSkipped(t *testing.T) {
	tiers := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "[0,32k)", InputMin: 0, InputPrice: 1.5},
			{Name: "[32k,∞)", InputMin: 32000, InputPrice: 0}, // 零价跳过
		},
	}
	raw, _ := json.Marshal(tiers)
	if got := ValidatePriceTiersMonotonic(raw, "test"); got != nil {
		t.Errorf("zero price tier: expected no warnings, got %v", got)
	}
}

// TestValidatePriceTiersMonotonic_UnsortedInput 输入顺序乱，按 InputMin 重排后比较
func TestValidatePriceTiersMonotonic_UnsortedInput(t *testing.T) {
	tiers := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "[32k,128k)", InputMin: 32000, InputPrice: 1.5},
			{Name: "[0,32k)", InputMin: 0, InputPrice: 1.2}, // 顺序乱
			{Name: "[128k,∞)", InputMin: 128000, InputPrice: 1.8},
		},
	}
	raw, _ := json.Marshal(tiers)
	if got := ValidatePriceTiersMonotonic(raw, "test"); got != nil {
		t.Errorf("unsorted but monotonic: expected no warnings, got %v", got)
	}
}
