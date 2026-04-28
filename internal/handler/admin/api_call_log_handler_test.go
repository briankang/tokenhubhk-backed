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
		&model.UserModelDiscount{},
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

func TestCostBreakdownReturnsCalculatorDisplayCapabilities(t *testing.T) {
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
	imageModel := model.AIModel{
		CategoryID:         category.ID,
		SupplierID:         supplier.ID,
		ModelName:          "fixture-image",
		DisplayName:        "Fixture Image",
		IsActive:           true,
		Status:             "online",
		InputCostRMB:       0.2,
		OutputCostRMB:      0,
		InputPricePerToken: 2000,
		PricingUnit:        model.UnitPerImage,
		ModelType:          model.ModelTypeImageGeneration,
	}
	if err := db.Create(&imageModel).Error; err != nil {
		t.Fatalf("seed image model: %v", err)
	}
	if err := db.Create(&model.ModelPricing{
		ModelID:            imageModel.ID,
		InputPricePerToken: 5000,
		InputPriceRMB:      0.5,
		Currency:           "CREDIT",
	}).Error; err != nil {
		t.Fatalf("seed image pricing: %v", err)
	}
	snapshot := model.JSON(`{"schema_version":1,"calculator_type":"image_unit","calculator_fields":["image_count","quality"],"image_count":2,"total_cost_credits":10000,"platform_cost_credits":4000}`)
	if err := db.Create(&model.ApiCallLog{
		CreatedAt:           time.Now(),
		RequestID:           "image-cap-1",
		UserID:              user.ID,
		TenantID:            user.TenantID,
		Endpoint:            "/v1/images/generations",
		RequestModel:        "fixture-image",
		ActualModel:         "fixture-image",
		Status:              "success",
		StatusCode:          200,
		CostCredits:         10000,
		ActualCostCredits:   10000,
		BillingStatus:       "settled",
		BillingSnapshot:     snapshot,
		MatchedPriceTierIdx: -1,
	}).Error; err != nil {
		t.Fatalf("seed image log: %v", err)
	}

	data := doApiCallLogRequest(t, r, "/admin/api-call-logs/image-cap-1/cost-breakdown")
	if got := data["calculator_type"]; got != "image_unit" {
		t.Fatalf("calculator_type=%v, want image_unit", got)
	}
	caps := data["display_capabilities"].(map[string]interface{})
	if caps["supports_tier_pricing"] != false {
		t.Fatalf("supports_tier_pricing=%v, want false", caps["supports_tier_pricing"])
	}
	if caps["shows_token_usage"] != false {
		t.Fatalf("shows_token_usage=%v, want false", caps["shows_token_usage"])
	}
	if caps["shows_image_usage"] != true {
		t.Fatalf("shows_image_usage=%v, want true", caps["shows_image_usage"])
	}
}

func TestCostBreakdownUsesSnapshotUserDiscountDetail(t *testing.T) {
	r, db := setupApiCallLogHandlerTest(t)

	var user model.User
	if err := db.First(&user).Error; err != nil {
		t.Fatalf("find user: %v", err)
	}
	snapshot := model.JSON(`{"schema_version":1,"calculator_type":"token_io","user_discount_detail":{"id":77,"user_id":1,"model_id":1,"pricing_type":"FIXED","input_price":0.11,"output_price":0.22,"note":"snapshot discount"},"user_discount_rate":0.5,"total_cost_credits":10,"platform_cost_credits":3}`)
	discountID := uint(77)
	discountRate := 0.5
	if err := db.Create(&model.ApiCallLog{
		CreatedAt:           time.Now(),
		RequestID:           "snap-discount-1",
		UserID:              user.ID,
		TenantID:            user.TenantID,
		Endpoint:            "/v1/chat/completions",
		RequestModel:        "fixture-model",
		Status:              "success",
		StatusCode:          200,
		PromptTokens:        100,
		CompletionTokens:    50,
		CostCredits:         10,
		ActualCostCredits:   10,
		BillingStatus:       "settled",
		BillingSnapshot:     snapshot,
		UserDiscountID:      &discountID,
		UserDiscountRate:    &discountRate,
		MatchedPriceTierIdx: -1,
	}).Error; err != nil {
		t.Fatalf("seed discount log: %v", err)
	}

	data := doApiCallLogRequest(t, r, "/admin/api-call-logs/snap-discount-1/cost-breakdown")
	if data["user_discount_applied"] != true {
		t.Fatalf("user_discount_applied=%v, want true", data["user_discount_applied"])
	}
	detail := data["user_discount_detail"].(map[string]interface{})
	if detail["pricing_type"] != "FIXED" || detail["note"] != "snapshot discount" {
		t.Fatalf("unexpected snapshot discount detail: %#v", detail)
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

// TestCostBreakdownReturnsQuoteFromSnapshot 历史快照含 quote 时,优先返回 quote 字段。
//
// 对应 Spec §3 取数规则 #1:有 billing_snapshot.quote 时,直接渲染 quote。
func TestCostBreakdownReturnsQuoteFromSnapshot(t *testing.T) {
	r, db := setupApiCallLogHandlerTest(t)

	var user model.User
	if err := db.First(&user).Error; err != nil {
		t.Fatalf("find user: %v", err)
	}

	// 构造一份带 quote 的 snapshot,模拟新版扣费链路写入。
	snapshotJSON := `{
		"schema_version": 1,
		"total_cost_credits": 200,
		"input_cost_credits": 80,
		"output_cost_credits": 120,
		"platform_cost_credits": 50,
		"billing_status": "settled",
		"quote": {
			"schema_version": 2,
			"engine_version": "billing_quote_v1",
			"scenario": "charge",
			"quote_id": "quote-snap-1",
			"model_id": 1,
			"model_name": "fixture-model",
			"pricing_unit": "per_million_tokens",
			"total_credits": 200,
			"line_items": [
				{"component": "regular_input", "cost_credits": 80, "unit_price_credits": 8000},
				{"component": "output", "cost_credits": 120, "unit_price_credits": 17000}
			],
			"matched_tier_idx": -1,
			"quote_hash": "abc123"
		},
		"quote_hash": "abc123",
		"quote_schema_version": 2
	}`

	log := model.ApiCallLog{
		CreatedAt:           time.Date(2026, 4, 24, 11, 0, 0, 0, time.UTC),
		RequestID:           "quote-snap-1",
		UserID:              user.ID,
		TenantID:            user.TenantID,
		Endpoint:            "/v1/chat/completions",
		RequestModel:        "fixture-model",
		Status:              "success",
		StatusCode:          200,
		PromptTokens:        10,
		CompletionTokens:    6,
		TotalTokens:         16,
		CostCredits:         200,
		ActualCostCredits:   200,
		BillingStatus:       "settled",
		BillingSnapshot:     model.JSON(snapshotJSON),
		MatchedPriceTierIdx: -1,
	}
	if err := db.Create(&log).Error; err != nil {
		t.Fatalf("seed log: %v", err)
	}

	data := doApiCallLogRequest(t, r, "/admin/api-call-logs/quote-snap-1/cost-breakdown")

	if got := data["quote_source"].(string); got != "billing_snapshot" {
		t.Fatalf("quote_source=%q, want billing_snapshot", got)
	}
	quote, ok := data["quote"].(map[string]interface{})
	if !ok || quote == nil {
		t.Fatalf("quote field missing or wrong type: %v", data["quote"])
	}
	if got := quote["quote_id"].(string); got != "quote-snap-1" {
		t.Fatalf("quote.quote_id=%q, want quote-snap-1", got)
	}
	if got := int64(quote["total_credits"].(float64)); got != 200 {
		t.Fatalf("quote.total_credits=%d, want 200", got)
	}

	consistency, ok := data["quote_consistency"].(map[string]interface{})
	if !ok || consistency == nil {
		t.Fatalf("quote_consistency missing: %v", data["quote_consistency"])
	}
	if got := consistency["snapshot_found"].(bool); !got {
		t.Fatalf("quote_consistency.snapshot_found=false, want true")
	}
	if got := consistency["line_item_match"].(bool); !got {
		t.Fatalf("quote_consistency.line_item_match=false, line items 80+120=200 should match total")
	}
	if got := int64(consistency["amount_delta_credits"].(float64)); got != 0 {
		t.Fatalf("quote_consistency.amount_delta_credits=%d, want 0 (log.cost_credits 200 == quote.total 200)", got)
	}
}

// TestCostBreakdownLegacySnapshotWithoutQuote 旧日志(snapshot 无 quote) → quote_source = "legacy_snapshot"。
func TestCostBreakdownLegacySnapshotWithoutQuote(t *testing.T) {
	r, _ := setupApiCallLogHandlerTest(t)

	// snap-1 已在 fixture 中,snapshot 无 quote 字段
	data := doApiCallLogRequest(t, r, "/admin/api-call-logs/snap-1/cost-breakdown")

	if got := data["quote_source"].(string); got != "legacy_snapshot" {
		t.Fatalf("quote_source=%q, want legacy_snapshot for snap without quote", got)
	}
	if data["quote"] != nil {
		t.Fatalf("quote should be nil for legacy snapshot, got %v", data["quote"])
	}
	consistency := data["quote_consistency"].(map[string]interface{})
	if got := consistency["snapshot_found"].(bool); !got {
		t.Fatalf("snapshot_found should be true (legacy snapshot still exists)")
	}
}

// TestCostBreakdownNoSnapshot 无 snapshot → quote_source = "current_price_recompute_only"。
func TestCostBreakdownNoSnapshot(t *testing.T) {
	r, _ := setupApiCallLogHandlerTest(t)

	// failed-1 已在 fixture 中,无 snapshot
	data := doApiCallLogRequest(t, r, "/admin/api-call-logs/failed-1/cost-breakdown")

	if got := data["quote_source"].(string); got != "current_price_recompute_only" {
		t.Fatalf("quote_source=%q, want current_price_recompute_only", got)
	}
	consistency := data["quote_consistency"].(map[string]interface{})
	if consistency["snapshot_found"].(bool) {
		t.Fatalf("snapshot_found should be false for log without snapshot")
	}
	if got := int64(data["actual_revenue_credits"].(float64)); got != 0 {
		t.Fatalf("actual_revenue_credits=%d, want 0 for deduct_failed", got)
	}
	if got := data["actual_revenue_rmb"].(float64); got != 0 {
		t.Fatalf("actual_revenue_rmb=%f, want 0 for deduct_failed", got)
	}
	if got := data["platform_cost_rmb"].(float64); got != 0.002 {
		t.Fatalf("platform_cost_rmb=%f, want log platform cost 0.002", got)
	}
	if got := data["recomputed_platform_cost_rmb"].(float64); got != 0.002 {
		t.Fatalf("recomputed_platform_cost_rmb=%f, want authoritative platform cost 0.002", got)
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
