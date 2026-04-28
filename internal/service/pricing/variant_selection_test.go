package pricing

import (
	"testing"

	"tokenhub-server/internal/model"
)

// TestSelectPriceByVariant_Wan27 验证阿里云 wan2.7-t2v 1080P 修复 (F3)
//
// 历史 Bug：CalculateCostByUnit 在 per_second 路径不读 PriceTiers，导致 1080P 用户被
// 按 720P 价（0.6 元/秒）计费，1080P 实际应该 1.0 元/秒，漏扣 40%。
//
// F3 修复后：UsageInput.Variant 触发 selectPriceByVariant 选档，1080P 命中正确价。
func TestSelectPriceByVariant_Wan27(t *testing.T) {
	tiers := []model.PriceTier{
		{Name: "720P", Variant: "720P", InputPrice: 0.6, OutputPrice: 0},
		{Name: "1080P", Variant: "1080P", InputPrice: 1.0, OutputPrice: 0},
	}

	cases := []struct {
		name    string
		variant string
		want    float64
		hit     bool
	}{
		{"720P exact match", "720P", 0.6, true},
		{"1080P exact match", "1080P", 1.0, true},
		{"case insensitive 1080p", "1080p", 1.0, true},
		{"case insensitive 720p", "720p", 0.6, true},
		{"unknown variant", "4K", 0, false},
		{"empty variant", "", 0, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			price, hit := selectPriceByVariant(tiers, c.variant)
			if hit != c.hit {
				t.Errorf("hit=%v, want %v", hit, c.hit)
			}
			if hit && price != c.want {
				t.Errorf("price=%v, want %v", price, c.want)
			}
		})
	}
}

// TestSelectPriceByVariant_FallsThrough_NoTiers 验证空 tiers 不命中
func TestSelectPriceByVariant_FallsThrough_NoTiers(t *testing.T) {
	if _, hit := selectPriceByVariant(nil, "1080P"); hit {
		t.Error("nil tiers 不应命中")
	}
	if _, hit := selectPriceByVariant([]model.PriceTier{}, "1080P"); hit {
		t.Error("空 tiers 不应命中")
	}
}

// TestSelectPriceByVariant_PreferInputPrice 验证 InputPrice 优先（per_second 单位约定）
//
// 注：per_second / per_image / per_call 统一用 InputPrice 表示单价（OutputPrice 通常为 0）
// 仅当 InputPrice=0 时退而求其次用 OutputPrice
func TestSelectPriceByVariant_PreferInputPrice(t *testing.T) {
	tiers := []model.PriceTier{
		{Variant: "A", InputPrice: 0, OutputPrice: 5},
		{Variant: "B", InputPrice: 3, OutputPrice: 7}, // InputPrice 优先
	}

	pa, _ := selectPriceByVariant(tiers, "A")
	if pa != 5 {
		t.Errorf("A: 应回退到 OutputPrice=5, got %v", pa)
	}

	pb, _ := selectPriceByVariant(tiers, "B")
	if pb != 3 {
		t.Errorf("B: 应优先 InputPrice=3, got %v", pb)
	}
}

// TestPickFirstNonZero 验证 helper 行为
func TestPickFirstNonZero(t *testing.T) {
	if v := pickFirstNonZero(0, 0, 5, 7); v != 5 {
		t.Errorf("got %v, want 5", v)
	}
	if v := pickFirstNonZero(); v != 0 {
		t.Errorf("got %v, want 0", v)
	}
	if v := pickFirstNonZero(0, 0); v != 0 {
		t.Errorf("got %v, want 0", v)
	}
	if v := pickFirstNonZero(8.5); v != 8.5 {
		t.Errorf("got %v, want 8.5", v)
	}
}
