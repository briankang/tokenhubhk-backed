package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/pricing"
)

func setupBillingQuoteHandlerTest(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.AIModel{},
		&model.ModelPricing{},
		&model.UserModelDiscount{},
		&model.AgentPricing{},
		&model.AgentLevelDiscount{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	seedQuotePreviewFixtures(t, db)

	r := gin.New()
	rg := r.Group("/admin")
	pricingCalc := pricing.NewPricingCalculator(db)
	h := NewBillingQuoteHandler(db, pricingCalc)
	rg.POST("/billing/quote-preview", h.QuotePreview)
	return r, db
}

func seedQuotePreviewFixtures(t *testing.T, db *gorm.DB) {
	t.Helper()
	m := model.AIModel{
		ModelName:     "preview-model",
		DisplayName:   "Preview Model",
		IsActive:      true,
		Status:        "online",
		ModelType:     model.ModelTypeLLM,
		PricingUnit:   model.UnitPerMillionTokens,
		InputCostRMB:  1.0,
		OutputCostRMB: 2.0,
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	mp := model.ModelPricing{
		ModelID:             m.ID,
		InputPricePerToken:  10000,
		OutputPricePerToken: 20000,
		InputPriceRMB:       1.0,
		OutputPriceRMB:      2.0,
		Currency:            "CREDIT",
	}
	if err := db.Create(&mp).Error; err != nil {
		t.Fatalf("seed pricing: %v", err)
	}
}

func doQuotePreviewRequest(t *testing.T, r *gin.Engine, body map[string]interface{}) (int, map[string]interface{}) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/billing/quote-preview", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var resp map[string]interface{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w.Code, resp
}

// TestBillingQuotePreviewHappyPath token 模型试算正常返回 BillingQuote。
func TestBillingQuotePreviewHappyPath(t *testing.T) {
	r, db := setupBillingQuoteHandlerTest(t)
	var m model.AIModel
	if err := db.First(&m).Error; err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	code, resp := doQuotePreviewRequest(t, r, map[string]interface{}{
		"model_id": m.ID,
		"scenario": "preview",
		"usage": map[string]interface{}{
			"input_tokens":  1000,
			"output_tokens": 500,
		},
	})
	if code != http.StatusOK {
		t.Fatalf("status=%d, body=%v", code, resp)
	}
	data, _ := resp["data"].(map[string]interface{})
	quote, _ := data["quote"].(map[string]interface{})
	if quote == nil {
		t.Fatalf("quote missing in response: %v", resp)
	}

	// 关键断言: total_credits 与 hand-computed 真值一致。
	// 1000 input @ 10/M = 10 credits; 500 output @ 20/M = 10 credits; total = 20.
	if got, want := quoteIntField(quote, "total_credits"), int64(20); got != want {
		t.Fatalf("total_credits=%d, want %d (quote=%v)", got, want, quote)
	}
	if hash, _ := quote["quote_hash"].(string); hash == "" {
		t.Fatal("quote_hash should not be empty")
	}
	if scenario, _ := quote["scenario"].(string); scenario != "preview" {
		t.Fatalf("scenario=%q, want preview", scenario)
	}
}

// TestBillingQuotePreviewMissingModel 模型不存在返 404。
func TestBillingQuotePreviewMissingModel(t *testing.T) {
	r, _ := setupBillingQuoteHandlerTest(t)

	code, _ := doQuotePreviewRequest(t, r, map[string]interface{}{
		"model_id": uint(9999),
		"scenario": "preview",
	})
	if code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", code)
	}
}

// TestBillingQuotePreviewMissingModelID 缺 model_id 返 400。
func TestBillingQuotePreviewMissingModelID(t *testing.T) {
	r, _ := setupBillingQuoteHandlerTest(t)

	code, _ := doQuotePreviewRequest(t, r, map[string]interface{}{
		"scenario": "preview",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", code)
	}
}

// TestBillingQuotePreviewHashStability 同输入跑两次返回相同 quote_hash。
func TestBillingQuotePreviewHashStability(t *testing.T) {
	r, db := setupBillingQuoteHandlerTest(t)
	var m model.AIModel
	if err := db.First(&m).Error; err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	body := map[string]interface{}{
		"model_id": m.ID,
		"scenario": "preview",
		"usage": map[string]interface{}{
			"input_tokens":  2000,
			"output_tokens": 1000,
		},
	}
	_, resp1 := doQuotePreviewRequest(t, r, body)
	_, resp2 := doQuotePreviewRequest(t, r, body)
	hash1, _ := resp1["data"].(map[string]interface{})["quote"].(map[string]interface{})["quote_hash"].(string)
	hash2, _ := resp2["data"].(map[string]interface{})["quote"].(map[string]interface{})["quote_hash"].(string)
	if hash1 == "" || hash2 == "" {
		t.Fatalf("quote_hash should not be empty")
	}
	if hash1 != hash2 {
		t.Fatalf("quote_hash unstable: %q vs %q", hash1, hash2)
	}
}

// quoteIntField 从 map[string]interface{} 中读 int64,兼容 float64/json.Number/int。
func quoteIntField(m map[string]interface{}, key string) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}
