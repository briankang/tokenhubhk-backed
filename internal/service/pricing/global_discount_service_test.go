package pricing

import (
	"context"
	"encoding/json"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
)

func newDiscountTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.AIModel{}, &model.ModelPricing{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func seedDiscountModel(t *testing.T, db *gorm.DB, opts func(*model.AIModel)) *model.AIModel {
	t.Helper()
	m := model.AIModel{
		ModelName:     "global-discount-test",
		DisplayName:   "Global Discount Test",
		IsActive:      true,
		Status:        "online",
		ModelType:     model.ModelTypeLLM,
		PricingUnit:   model.UnitPerMillionTokens,
		InputCostRMB:  2.0,
		OutputCostRMB: 10.0,
	}
	if opts != nil {
		opts(&m)
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	return &m
}

// TestApply_BaseScopeOnly 应用全局折扣到基础价。
func TestApply_BaseScopeOnly(t *testing.T) {
	db := newDiscountTestDB(t)
	m := seedDiscountModel(t, db, nil)

	svc := NewGlobalDiscountService(db)
	res, err := svc.Apply(context.Background(), ApplyRequest{
		ModelID: m.ID,
		Rate:    0.85,
		Scopes:  []GlobalDiscountScope{ScopeBase},
	})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	// 验证返回的 changed 字段
	if got, want := res.Changed["input"], 1.7; got != want {
		t.Errorf("changed[input] = %v, want %v", got, want)
	}
	if got, want := res.Changed["output"], 8.5; got != want {
		t.Errorf("changed[output] = %v, want %v", got, want)
	}

	// 验证 DB 中 ModelPricing 已写入
	var mp model.ModelPricing
	if err := db.Where("model_id = ?", m.ID).First(&mp).Error; err != nil {
		t.Fatalf("load pricing: %v", err)
	}
	if mp.InputPriceRMB != 1.7 {
		t.Errorf("InputPriceRMB = %v, want 1.7", mp.InputPriceRMB)
	}
	if mp.OutputPriceRMB != 8.5 {
		t.Errorf("OutputPriceRMB = %v, want 8.5", mp.OutputPriceRMB)
	}
	if mp.GlobalDiscountRate != 0.85 {
		t.Errorf("GlobalDiscountRate = %v, want 0.85", mp.GlobalDiscountRate)
	}
	if !mp.GlobalDiscountAnchored {
		t.Error("GlobalDiscountAnchored should be true after Apply")
	}
}

// TestApply_PreserveLockOverrides 单档解锁的字段不参与全局折扣。
func TestApply_PreserveLockOverrides(t *testing.T) {
	db := newDiscountTestDB(t)
	m := seedDiscountModel(t, db, nil)
	svc := NewGlobalDiscountService(db)

	// 第一次先应用,创建 ModelPricing
	if _, err := svc.Apply(context.Background(), ApplyRequest{
		ModelID: m.ID,
		Rate:    0.50,
		Scopes:  []GlobalDiscountScope{ScopeBase},
	}); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// 解锁 output 档,保持当前值 5.0
	if err := svc.SetLockOverride(context.Background(), m.ID, "output", 5.0); err != nil {
		t.Fatalf("SetLockOverride: %v", err)
	}

	// 再应用 0.85 折扣,output 档应该不变
	res, err := svc.Apply(context.Background(), ApplyRequest{
		ModelID:           m.ID,
		Rate:              0.85,
		Scopes:            []GlobalDiscountScope{ScopeBase},
		PreserveOverrides: true,
	})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if _, ok := res.Changed["output"]; ok {
		t.Errorf("output should be skipped, got changed=%v", res.Changed["output"])
	}
	if got, want := res.Changed["input"], 1.7; got != want {
		t.Errorf("input changed = %v, want 1.7", got)
	}
	hasOutputSkip := false
	for _, k := range res.SkippedLocks {
		if k == "output" {
			hasOutputSkip = true
			break
		}
	}
	if !hasOutputSkip {
		t.Errorf("SkippedLocks should contain output, got %v", res.SkippedLocks)
	}
}

// TestApply_TiersScope 阶梯价应用 selling_*_price。
func TestApply_TiersScope(t *testing.T) {
	db := newDiscountTestDB(t)
	tiers := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "tier1", InputMin: 0, InputMax: int64Ptr(32000), InputPrice: 1.20, OutputPrice: 3.00},
			{Name: "tier2", InputMin: 32000, InputPrice: 1.80, OutputPrice: 4.50},
		},
		Currency: "CNY",
	}
	tierJSON, _ := json.Marshal(tiers)
	m := seedDiscountModel(t, db, func(m *model.AIModel) {
		m.PriceTiers = tierJSON
	})

	svc := NewGlobalDiscountService(db)
	res, err := svc.Apply(context.Background(), ApplyRequest{
		ModelID: m.ID,
		Rate:    0.50,
		Scopes:  []GlobalDiscountScope{ScopeTiers},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.TiersUpdated != 2 {
		t.Errorf("TiersUpdated = %d, want 2", res.TiersUpdated)
	}
	if got, want := res.Changed["tier_0_input"], 0.6; got != want {
		t.Errorf("tier_0_input = %v, want 0.6", got)
	}
	if got, want := res.Changed["tier_1_output"], 2.25; got != want {
		t.Errorf("tier_1_output = %v, want 2.25", got)
	}

	// 验证 ModelPricing.PriceTiers JSON 中的 SellingPrice 已写入
	var mp model.ModelPricing
	if err := db.Where("model_id = ?", m.ID).First(&mp).Error; err != nil {
		t.Fatalf("load pricing: %v", err)
	}
	var savedTiers model.PriceTiersData
	if err := json.Unmarshal(mp.PriceTiers, &savedTiers); err != nil {
		t.Fatalf("unmarshal saved tiers: %v", err)
	}
	if len(savedTiers.Tiers) != 2 {
		t.Fatalf("saved tiers len = %d, want 2", len(savedTiers.Tiers))
	}
	if savedTiers.Tiers[0].SellingInputPrice == nil || *savedTiers.Tiers[0].SellingInputPrice != 0.6 {
		t.Errorf("tier 0 SellingInputPrice = %v, want 0.6", savedTiers.Tiers[0].SellingInputPrice)
	}
}

// TestPreview_DoesNotPersist Preview 不写库。
func TestPreview_DoesNotPersist(t *testing.T) {
	db := newDiscountTestDB(t)
	m := seedDiscountModel(t, db, nil)
	svc := NewGlobalDiscountService(db)

	res, err := svc.Preview(context.Background(), ApplyRequest{
		ModelID: m.ID,
		Rate:    0.85,
		Scopes:  []GlobalDiscountScope{ScopeBase},
	})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if got, want := res.Changed["input"], 1.7; got != want {
		t.Errorf("preview input = %v, want 1.7", got)
	}

	// DB 不应有 ModelPricing 记录
	var count int64
	if err := db.Model(&model.ModelPricing{}).Where("model_id = ?", m.ID).Count(&count).Error; err != nil {
		t.Fatalf("count pricing: %v", err)
	}
	if count != 0 {
		t.Errorf("ModelPricing count after Preview = %d, want 0", count)
	}
}

// TestApply_InvalidRate 不合法折扣率被拒绝。
func TestApply_InvalidRate(t *testing.T) {
	db := newDiscountTestDB(t)
	m := seedDiscountModel(t, db, nil)
	svc := NewGlobalDiscountService(db)

	cases := []float64{-1, 0, 11}
	for _, rate := range cases {
		_, err := svc.Apply(context.Background(), ApplyRequest{ModelID: m.ID, Rate: rate})
		if err == nil {
			t.Errorf("rate=%v should be rejected", rate)
		}
	}
}

// TestApply_ExchangeRateLocked 应用后锁定汇率字段被填充。
func TestApply_ExchangeRateLocked(t *testing.T) {
	db := newDiscountTestDB(t)
	m := seedDiscountModel(t, db, nil)
	svc := NewGlobalDiscountService(db)

	_, err := svc.Apply(context.Background(), ApplyRequest{
		ModelID:            m.ID,
		Rate:               0.85,
		Scopes:             []GlobalDiscountScope{ScopeBase},
		ExchangeRate:       7.20,
		ExchangeRateSource: "aliyun_market",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var mp model.ModelPricing
	if err := db.Where("model_id = ?", m.ID).First(&mp).Error; err != nil {
		t.Fatalf("load pricing: %v", err)
	}
	if mp.PricedAtExchangeRate != 7.20 {
		t.Errorf("PricedAtExchangeRate = %v, want 7.20", mp.PricedAtExchangeRate)
	}
	if mp.PricedAtRateSource != "aliyun_market" {
		t.Errorf("PricedAtRateSource = %v, want aliyun_market", mp.PricedAtRateSource)
	}
	if mp.PricedAtAt == nil {
		t.Error("PricedAtAt should not be nil")
	}
}

// TestSetClearLockOverride 单档解锁/恢复。
func TestSetClearLockOverride(t *testing.T) {
	db := newDiscountTestDB(t)
	m := seedDiscountModel(t, db, nil)
	svc := NewGlobalDiscountService(db)
	if _, err := svc.Apply(context.Background(), ApplyRequest{ModelID: m.ID, Rate: 0.85, Scopes: []GlobalDiscountScope{ScopeBase}}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// 锁定 cache_input
	if err := svc.SetLockOverride(context.Background(), m.ID, "cache_input", 0.10); err != nil {
		t.Fatalf("SetLockOverride: %v", err)
	}
	var mp model.ModelPricing
	_ = db.Where("model_id = ?", m.ID).First(&mp).Error
	var locks map[string]float64
	_ = json.Unmarshal(mp.PriceLockOverrides, &locks)
	if v, ok := locks["cache_input"]; !ok || v != 0.10 {
		t.Errorf("cache_input lock = %v, want 0.10", v)
	}

	// 清除锁定
	if err := svc.ClearLockOverride(context.Background(), m.ID, "cache_input"); err != nil {
		t.Fatalf("ClearLockOverride: %v", err)
	}
	_ = db.Where("model_id = ?", m.ID).First(&mp).Error
	var locks2 map[string]float64
	_ = json.Unmarshal(mp.PriceLockOverrides, &locks2)
	if _, ok := locks2["cache_input"]; ok {
		t.Errorf("cache_input lock should be cleared")
	}
}

func int64Ptr(v int64) *int64 { return &v }
