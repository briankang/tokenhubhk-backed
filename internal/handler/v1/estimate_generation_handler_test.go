package v1

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

func newEstimateGenerationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.AIModel{}, &model.ModelPricing{}, &model.ApiCallLog{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

func seedEstimateModel(t *testing.T, db *gorm.DB) model.AIModel {
	t.Helper()
	aiModel := model.AIModel{
		ModelName:      "qwen-plus",
		DisplayName:    "Qwen Plus",
		IsActive:       true,
		Status:         "online",
		ModelType:      model.ModelTypeLLM,
		PricingUnit:    model.UnitPerMillionTokens,
		MaxTokens:      4096,
		MaxInputTokens: 128000,
	}
	if err := db.Create(&aiModel).Error; err != nil {
		t.Fatalf("create model: %v", err)
	}
	if err := db.Create(&model.ModelPricing{
		ModelID:             aiModel.ID,
		InputPricePerToken:  10000,
		OutputPricePerToken: 20000,
		InputPriceRMB:       1,
		OutputPriceRMB:      2,
		Currency:            "CREDIT",
	}).Error; err != nil {
		t.Fatalf("create pricing: %v", err)
	}
	return aiModel
}

func TestEstimateCostAcceptsOpenAIRequestBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newEstimateGenerationTestDB(t)
	seedEstimateModel(t, db)

	router := gin.New()
	NewEstimateHandler(db, pricing.NewPricingCalculator(db)).Register(router.Group("/v1"))

	body := map[string]interface{}{
		"endpoint": "/v1/chat/completions",
		"request": map[string]interface{}{
			"model": "qwen-plus",
			"messages": []map[string]interface{}{
				{"role": "system", "content": "You are concise."},
				{"role": "user", "content": "Summarize TokenHub cost estimation."},
			},
			"tools": []map[string]interface{}{
				{
					"type": "function",
					"function": map[string]interface{}{
						"name":       "lookup_price",
						"parameters": map[string]interface{}{"type": "object"},
					},
				},
			},
			"max_tokens": 128,
		},
	}
	w := httptest.NewRecorder()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "/v1/estimate/cost", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Code int `json:"code"`
		Data struct {
			Model            string   `json:"model"`
			InputTokens      int      `json:"input_tokens"`
			OutputTokens     int      `json:"output_tokens"`
			EstimatedCredits int64    `json:"estimated_credits"`
			EstimateType     string   `json:"estimate_type"`
			Confidence       string   `json:"confidence"`
			Assumptions      []string `json:"assumptions"`
			UsageEstimate    struct {
				Source          string `json:"source"`
				TokensEstimated bool   `json:"tokens_estimated"`
			} `json:"usage_estimate"`
			EstimateRange struct {
				MaxCredits int64 `json:"max_credits"`
			} `json:"estimate_range"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Code != 0 || resp.Data.Model != "qwen-plus" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.Data.InputTokens <= 0 || resp.Data.OutputTokens != 128 {
		t.Fatalf("usage estimate not populated: %+v", resp.Data)
	}
	if resp.Data.EstimateType == "" || resp.Data.Confidence == "" || len(resp.Data.Assumptions) == 0 {
		t.Fatalf("missing estimate metadata: %+v", resp.Data)
	}
	if resp.Data.UsageEstimate.Source != "request_body" || !resp.Data.UsageEstimate.TokensEstimated {
		t.Fatalf("bad usage source: %+v", resp.Data.UsageEstimate)
	}
	if resp.Data.EstimateRange.MaxCredits != resp.Data.EstimatedCredits {
		t.Fatalf("max range should match primary estimate: %+v", resp.Data.EstimateRange)
	}
}

func TestGenerationHandlerReturnsOwnedRequestMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newEstimateGenerationTestDB(t)
	if err := db.Create(&model.ApiCallLog{
		RequestID:            "req-test-123",
		UserID:               42,
		TenantID:             7,
		Endpoint:             "/v1/chat/completions",
		RequestModel:         "qwen-plus",
		ActualModel:          "qwen-plus",
		IsStream:             true,
		PromptTokens:         100,
		CompletionTokens:     50,
		TotalTokens:          150,
		CacheReadTokens:      20,
		CostRMB:              0.02,
		ActualCostCredits:    200,
		EstimatedCostCredits: 260,
		PlatformCostRMB:      0.01,
		SupplierName:         "aliyun",
		ChannelName:          "aliyun-main",
		StatusCode:           200,
		Status:               "success",
		TotalLatencyMs:       1200,
		FirstTokenMs:         240,
		BillingStatus:        "settled",
		UsageSource:          "provider",
	}).Error; err != nil {
		t.Fatalf("create log: %v", err)
	}

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("userId", uint(42))
		c.Next()
	})
	NewGenerationHandler(db).Register(router.Group("/v1"))

	w := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, "/v1/generation?id=req-test-123", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			ID                   string  `json:"id"`
			Model                string  `json:"model"`
			ProviderName         string  `json:"provider_name"`
			TotalCost            float64 `json:"total_cost"`
			TokensPrompt         int     `json:"tokens_prompt"`
			NativeTokensCached   int     `json:"native_tokens_cached"`
			EstimatedCostCredits int64   `json:"estimated_cost_credits"`
			ActualCostCredits    int64   `json:"actual_cost_credits"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.ID != "req-test-123" || resp.Data.Model != "qwen-plus" || resp.Data.ProviderName != "aliyun" {
		t.Fatalf("unexpected generation metadata: %+v", resp.Data)
	}
	if resp.Data.TotalCost != 0.02 || resp.Data.TokensPrompt != 100 || resp.Data.NativeTokensCached != 20 {
		t.Fatalf("unexpected usage/cost metadata: %+v", resp.Data)
	}
	if resp.Data.EstimatedCostCredits != 260 || resp.Data.ActualCostCredits != 200 {
		t.Fatalf("missing TokenHub billing metadata: %+v", resp.Data)
	}
}
