package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func setupApiCallLogHandlerTest(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{},
		&model.Supplier{},
		&model.ModelCategory{},
		&model.AIModel{},
		&model.ModelPricing{},
		&model.ApiCallLog{},
		&model.ReferralAttribution{},
		&model.ReferralConfig{},
		&model.CommissionRule{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	seedCostAnalysisFixtures(t, db)

	r := gin.New()
	rg := r.Group("/admin")
	NewApiCallLogHandler(db, nil).Register(rg)
	return r, db
}

func seedCostAnalysisFixtures(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC)

	user := model.User{TenantID: 1, Email: "cost-user@example.com", Name: "Cost User", PasswordHash: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	supplier := model.Supplier{Name: "Fixture Supplier", Code: "fixture", AccessType: "api", IsActive: true}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatalf("seed supplier: %v", err)
	}
	category := model.ModelCategory{Name: "LLM"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("seed category: %v", err)
	}
	aiModel := model.AIModel{
		CategoryID:          category.ID,
		SupplierID:          supplier.ID,
		ModelName:           "fixture-model",
		DisplayName:         "Fixture Model",
		IsActive:            true,
		Status:              "online",
		InputCostRMB:        0.8,
		OutputCostRMB:       2.0,
		InputPricePerToken:  8000,
		OutputPricePerToken: 20000,
		PricingUnit:         model.UnitPerMillionTokens,
	}
	if err := db.Create(&aiModel).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	pricing := model.ModelPricing{
		ModelID:             aiModel.ID,
		InputPricePerToken:  6800,
		OutputPricePerToken: 17000,
		InputPriceRMB:       0.68,
		OutputPriceRMB:      1.70,
		Currency:            "CREDIT",
	}
	if err := db.Create(&pricing).Error; err != nil {
		t.Fatalf("seed pricing: %v", err)
	}

	snapshot := model.JSON(`{"schema_version":1,"total_cost_credits":111,"input_cost_credits":70,"output_cost_credits":41,"total_cost_rmb":0.0111,"platform_cost_credits":30,"billing_status":"settled"}`)
	logs := []model.ApiCallLog{
		{
			CreatedAt:           now,
			RequestID:           "snap-1",
			UserID:              user.ID,
			TenantID:            user.TenantID,
			Endpoint:            "/v1/chat/completions",
			RequestModel:        "fixture-model",
			Status:              "success",
			StatusCode:          200,
			PromptTokens:        21,
			CompletionTokens:    1,
			TotalTokens:         22,
			CostCredits:         123,
			CostRMB:             0.0123,
			ActualCostCredits:   120,
			PlatformCostRMB:     0.003,
			BillingStatus:       "settled",
			BillingSnapshot:     snapshot,
			MatchedPriceTierIdx: -1,
			MatchedPriceTier:    "",
		},
		{
			CreatedAt:             now.Add(time.Hour),
			RequestID:             "failed-1",
			UserID:                user.ID,
			TenantID:              user.TenantID,
			Endpoint:              "/v1/chat/completions",
			RequestModel:          "fixture-model",
			Status:                "error",
			StatusCode:            402,
			CostCredits:           80,
			CostRMB:               0.008,
			ActualCostCredits:     0,
			UnderCollectedCredits: 80,
			PlatformCostRMB:       0.002,
			BillingStatus:         "deduct_failed",
			MatchedPriceTierIdx:   -1,
		},
	}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatalf("seed logs: %v", err)
	}
}

func doApiCallLogRequest(t *testing.T, r *gin.Engine, path string) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%s", path, w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp["data"].(map[string]interface{})
}

func TestApiCallLogSummaryBillingStatusTotals(t *testing.T) {
	r, _ := setupApiCallLogHandlerTest(t)

	data := doApiCallLogRequest(t, r, "/admin/api-call-logs/summary")
	if got := int64(data["total_requests"].(float64)); got != 2 {
		t.Fatalf("total_requests=%d, want 2", got)
	}
	if got := int64(data["total_cost_credits"].(float64)); got != 203 {
		t.Fatalf("total_cost_credits=%d, want 203", got)
	}
	if got := int64(data["actual_revenue_credits"].(float64)); got != 120 {
		t.Fatalf("actual_revenue_credits=%d, want 120", got)
	}
	if got := int64(data["under_collected_credits"].(float64)); got != 80 {
		t.Fatalf("under_collected_credits=%d, want 80", got)
	}
	if got := int64(data["deduct_failed_requests"].(float64)); got != 1 {
		t.Fatalf("deduct_failed_requests=%d, want 1", got)
	}
}

func TestCostBreakdownPrefersBillingSnapshot(t *testing.T) {
	r, _ := setupApiCallLogHandlerTest(t)

	data := doApiCallLogRequest(t, r, "/admin/api-call-logs/snap-1/cost-breakdown")
	if data["snapshot_found"] != true {
		t.Fatalf("snapshot_found=%v, want true", data["snapshot_found"])
	}
	if got := int64(data["recomputed_total_cost"].(float64)); got != 111 {
		t.Fatalf("recomputed_total_cost=%d, want snapshot value 111", got)
	}
	if got := int64(data["platform_cost_credits"].(float64)); got != 30 {
		t.Fatalf("platform_cost_credits=%d, want snapshot value 30", got)
	}
	if got := int64(data["actual_revenue_credits"].(float64)); got != 120 {
		t.Fatalf("actual_revenue_credits=%d, want 120", got)
	}
}

func TestCostBreakdownFormulaUsesSnapshotTokens(t *testing.T) {
	r, db := setupApiCallLogHandlerTest(t)

	var user model.User
	if err := db.First(&user).Error; err != nil {
		t.Fatalf("find user: %v", err)
	}
	var supplier model.Supplier
	if err := db.First(&supplier).Error; err != nil {
		t.Fatalf("find supplier: %v", err)
	}
	var category model.ModelCategory
	if err := db.First(&category).Error; err != nil {
		t.Fatalf("find category: %v", err)
	}
	videoModel := model.AIModel{
		CategoryID:          category.ID,
		SupplierID:          supplier.ID,
		ModelName:           "fixture-video",
		DisplayName:         "Fixture Video",
		IsActive:            true,
		Status:              "online",
		InputCostRMB:        20,
		OutputCostRMB:       20,
		InputPricePerToken:  200000,
		OutputPricePerToken: 200000,
		PricingUnit:         model.UnitPerMillionTokens,
		ModelType:           model.ModelTypeVideoGeneration,
	}
	if err := db.Create(&videoModel).Error; err != nil {
		t.Fatalf("seed video model: %v", err)
	}
	if err := db.Create(&model.ModelPricing{
		ModelID:             videoModel.ID,
		InputPricePerToken:  126000,
		OutputPricePerToken: 126000,
		InputPriceRMB:       12.6,
		OutputPriceRMB:      12.6,
		Currency:            "CREDIT",
	}).Error; err != nil {
		t.Fatalf("seed video pricing: %v", err)
	}
	snapshot := model.JSON(`{"schema_version":1,"input_tokens":0,"output_tokens":108000,"total_cost_credits":13608,"input_cost_credits":0,"output_cost_credits":13608,"total_cost_rmb":1.3608,"platform_cost_credits":13608,"billing_status":"settled"}`)
	if err := db.Create(&model.ApiCallLog{
		CreatedAt:           time.Now(),
		RequestID:           "video-snap-1",
		UserID:              user.ID,
		TenantID:            user.TenantID,
		Endpoint:            "/v1/videos/generations",
		RequestModel:        "fixture-video",
		ActualModel:         "fixture-video",
		Status:              "success",
		StatusCode:          200,
		CostCredits:         13608,
		CostRMB:             1.3608,
		ActualCostCredits:   13608,
		BillingStatus:       "settled",
		BillingSnapshot:     snapshot,
		MatchedPriceTierIdx: -1,
	}).Error; err != nil {
		t.Fatalf("seed video log: %v", err)
	}

	data := doApiCallLogRequest(t, r, "/admin/api-call-logs/video-snap-1/cost-breakdown")
	formula := data["formula"].(string)
	if !strings.Contains(formula, "108000（输出tokens）") {
		t.Fatalf("formula did not use snapshot output tokens: %s", formula)
	}
	if got := int64(data["recomputed_total_cost"].(float64)); got != 13608 {
		t.Fatalf("recomputed_total_cost=%d, want snapshot total 13608", got)
	}
}

func TestReconciliationReportAndUnderCollectedFilter(t *testing.T) {
	r, _ := setupApiCallLogHandlerTest(t)

	data := doApiCallLogRequest(t, r, "/admin/api-call-logs/reconciliation?under_collected_only=true")
	totals := data["totals"].(map[string]interface{})
	if got := int64(totals["requests"].(float64)); got != 1 {
		t.Fatalf("filtered requests=%d, want 1", got)
	}
	if got := int64(totals["under_collected_credits"].(float64)); got != 80 {
		t.Fatalf("filtered under_collected_credits=%d, want 80", got)
	}
	if got := int64(totals["deduct_failed_requests"].(float64)); got != 1 {
		t.Fatalf("filtered deduct_failed_requests=%d, want 1", got)
	}
	rows := data["by_model"].([]interface{})
	if len(rows) != 1 {
		t.Fatalf("by_model rows=%d, want 1", len(rows))
	}
	if got := int64(rows[0].(map[string]interface{})["requests"].(float64)); got != 1 {
		t.Fatalf("by_model requests=%d, want 1", got)
	}
	alerts := data["alerts"].([]interface{})
	if len(alerts) != 4 {
		t.Fatalf("alerts=%d, want 4", len(alerts))
	}
}

func TestReconciliationCSVExport(t *testing.T) {
	r, _ := setupApiCallLogHandlerTest(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/api-call-logs/reconciliation/export?under_collected_only=true", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "section,dimension,requests") {
		t.Fatalf("missing csv header: %s", body)
	}
	if !strings.Contains(body, "by_model,fixture-model,1") {
		t.Fatalf("missing model export row: %s", body)
	}
}
