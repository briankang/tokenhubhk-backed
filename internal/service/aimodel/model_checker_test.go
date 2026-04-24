package aimodel

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gorlogger "gorm.io/gorm/logger"

	internalmodel "tokenhub-server/internal/model"
	"tokenhub-server/internal/service/modeldiscovery"
)

func TestInferModelTypeByName(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected string
	}{
		{name: "embedding vision", model: "doubao-embedding-vision-250615", expected: "Embedding"},
		{name: "translation", model: "doubao-seed-translation-250915", expected: "Translation"},
		{name: "seedream image", model: "doubao-seedream-5-0-lite", expected: "ImageGeneration"},
		{name: "tts", model: "doubao-tts-2-0", expected: "TextToSpeech"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferModelTypeByName(tt.model)
			if got != tt.expected {
				t.Fatalf("inferModelTypeByName(%q) = %q, want %q", tt.model, got, tt.expected)
			}
		})
	}
}

func TestCategorizeCheckError(t *testing.T) {
	tests := []struct {
		name           string
		result         ModelCheckResult
		wantCategory   string
		wantSuggestion string
	}{
		{
			name: "product not activated",
			result: ModelCheckResult{Error: `{"error":{"message":"The product is not activated, please confirm that you have activated products and try again after activation."}}`, StatusCode: 400},
			wantCategory: "product_not_activated",
			wantSuggestion: "供应商产品未激活",
		},
		{
			name: "api mismatch",
			result: ModelCheckResult{Error: `{"error":{"code":"InvalidParameter","message":"the requested model doubao-embedding-vision-250615 does not support this api"}}`, StatusCode: 400},
			wantCategory: "api_mismatch",
			wantSuggestion: "不支持当前 API 端点",
		},
		{
			name: "model not found",
			result: ModelCheckResult{Error: `{"error":{"code":"InvalidEndpointOrModel.NotFound","message":"The model or endpoint doubao-1-5-ui-tars-250428 does not exist"}}`, StatusCode: 404},
			wantCategory: "model_not_found",
			wantSuggestion: "供应商 API 返回模型不存在",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCategory, gotSuggestion := categorizeCheckError(tt.result)
			if gotCategory != tt.wantCategory {
				t.Fatalf("categorizeCheckError() category = %q, want %q", gotCategory, tt.wantCategory)
			}
			if gotSuggestion == "" || !contains(gotSuggestion, tt.wantSuggestion) {
				t.Fatalf("categorizeCheckError() suggestion = %q, want substring %q", gotSuggestion, tt.wantSuggestion)
			}
		})
	}
}

func TestClassifyAgainstUpstream_VolcengineShutdownModel(t *testing.T) {
	m := internalmodel.AIModel{
		BaseModel:  internalmodel.BaseModel{ID: 1},
		ModelName:  "doubao-1-5-ui-tars-250428",
		ModelType:  "VLM",
		SupplierID: 7,
	}

	snapshots := map[uint]*upstreamSnapshot{
		7: {
			SupplierID: 7,
			Names: map[string]bool{
				"doubao-1-5-ui-tars-250428": true,
			},
			ShutdownNames: map[string]bool{
				"doubao-1-5-ui-tars-250428": true,
			},
			Available: true,
			ReturnedModelTypes: map[string]bool{
				"LLM": true, "VLM": true, "Embedding": true, "ImageGeneration": true, "VideoGeneration": true,
			},
		},
	}

	got := classifyAgainstUpstream(m, snapshots)
	if got != UpstreamDeprecated {
		t.Fatalf("classifyAgainstUpstream() = %q, want %q", got, UpstreamDeprecated)
	}
}

func TestClassifyAgainstUpstream_VolcengineActiveModel(t *testing.T) {
	m := internalmodel.AIModel{
		BaseModel:  internalmodel.BaseModel{ID: 1},
		ModelName:  "doubao-1-5-pro-32k-250115",
		ModelType:  "LLM",
		SupplierID: 7,
	}

	snapshots := map[uint]*upstreamSnapshot{
		7: {
			SupplierID: 7,
			Names: map[string]bool{
				"doubao-1-5-pro-32k-250115": true,
			},
			ShutdownNames:      map[string]bool{},
			Available:          true,
			ReturnedModelTypes: map[string]bool{"LLM": true},
		},
	}

	got := classifyAgainstUpstream(m, snapshots)
	if got != UpstreamActive {
		t.Fatalf("classifyAgainstUpstream() = %q, want %q", got, UpstreamActive)
	}
}

func TestClassifyAgainstUpstream_MissingLLMModel(t *testing.T) {
	m := internalmodel.AIModel{
		BaseModel:  internalmodel.BaseModel{ID: 1},
		ModelName:  "deepseek-v3-1-250821",
		ModelType:  "LLM",
		SupplierID: 7,
	}

	snapshots := map[uint]*upstreamSnapshot{
		7: {
			SupplierID:     7,
			Names:          map[string]bool{},
			ShutdownNames:  map[string]bool{},
			Available:      true,
			ReturnedModelTypes: map[string]bool{"LLM": true},
		},
	}

	got := classifyAgainstUpstream(m, snapshots)
	if got != UpstreamDeprecated {
		t.Fatalf("classifyAgainstUpstream() = %q, want %q", got, UpstreamDeprecated)
	}
}

func contains(s, substr string) bool {
	return len(substr) == 0 || (len(s) >= len(substr) && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ---------- U-26~U-30: ModelChecker 单元测试 ----------

// setupCheckerDB 创建内存 SQLite DB 并迁移 ModelCheckLog 表
func setupCheckerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gorlogger.Default.LogMode(gorlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&internalmodel.ModelCheckLog{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

// newTestChecker 用 zap.NewNop() 构造 ModelChecker，避免 nil logger panic
func newTestChecker(db *gorm.DB) *ModelChecker {
	return &ModelChecker{
		db:         db,
		logger:     zap.NewNop(),
		discovery:  modeldiscovery.NewDiscoveryService(db),
		httpClient: newProbeHTTPClient(),
	}
}

// ---------- U-26: CheckByIDs 空列表 ----------

func TestCheckByIDs_EmptyList(t *testing.T) {
	db := setupCheckerDB(t)
	mc := newTestChecker(db)

	results, err := mc.CheckByIDs(context.Background(), []uint{}, nil)
	if err != nil {
		t.Fatalf("CheckByIDs empty list returned error: %v", err)
	}
	if results != nil {
		t.Errorf("CheckByIDs empty list: expected nil results, got %v", results)
	}
}

// ---------- U-27: BuildDetailedSummary 聚合 20 条结果 ----------

func TestBatchCheck_ConcurrencyLimit(t *testing.T) {
	// 构造 20 条结果：15 可用 + 5 失败(auth_error)
	results := make([]ModelCheckResult, 0, 20)
	for i := 0; i < 15; i++ {
		results = append(results, ModelCheckResult{
			ModelID:   uint(i + 1),
			ModelName: fmt.Sprintf("avail-model-%d", i+1),
			Available: true,
		})
	}
	for i := 0; i < 5; i++ {
		results = append(results, ModelCheckResult{
			ModelID:    uint(16 + i),
			ModelName:  fmt.Sprintf("fail-model-%d", i+1),
			Available:  false,
			StatusCode: 401,
			Error:      "unauthorized access",
		})
	}

	summary := BuildDetailedSummary(results)

	if summary.Total != 20 {
		t.Errorf("Total = %d, want 20", summary.Total)
	}
	if summary.Available != 15 {
		t.Errorf("Available = %d, want 15", summary.Available)
	}
	if summary.Failed != 5 {
		t.Errorf("Failed = %d, want 5", summary.Failed)
	}
	if len(summary.Groups) == 0 {
		t.Fatal("expected at least one error group")
	}
	found := false
	for _, g := range summary.Groups {
		if g.Category == "auth_error" && g.Count == 5 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected auth_error group with count=5, got: %+v", summary.Groups)
	}
}

// ---------- U-28: 429 视为可用 ----------

func TestBatchCheck_429AsAvailable(t *testing.T) {
	// categorizeCheckError 对 429 返回 "rate_limited"
	r429 := ModelCheckResult{StatusCode: 429, Available: true}
	cat, _ := categorizeCheckError(r429)
	if cat != "rate_limited" {
		t.Errorf("categorizeCheckError(429) = %q, want rate_limited", cat)
	}

	// BuildDetailedSummary 应将 Available=true 的 429 结果计入可用桶
	results := []ModelCheckResult{
		{ModelID: 1, ModelName: "qwq-plus", Available: true, StatusCode: 429},
	}
	summary := BuildDetailedSummary(results)
	if summary.Available != 1 {
		t.Errorf("Available = %d, want 1 (429 should be available)", summary.Available)
	}
	if summary.Failed != 0 {
		t.Errorf("Failed = %d, want 0 (429 should not count as failed)", summary.Failed)
	}
}

// ---------- U-29: writeCheckLog 写入检测日志 ----------

func TestWriteBaselineToCheckLogs(t *testing.T) {
	db := setupCheckerDB(t)
	mc := newTestChecker(db)

	r := &ModelCheckResult{
		ModelID:    42,
		ModelName:  "baseline-test-model",
		Available:  true,
		LatencyMs:  120,
		StatusCode: 200,
	}
	mc.writeCheckLog(context.Background(), r, time.Now())

	var logs []internalmodel.ModelCheckLog
	db.Where("model_id = ?", uint(42)).Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("expected 1 log after writeCheckLog, got %d", len(logs))
	}
	if !logs[0].Available {
		t.Errorf("log.Available = false, want true")
	}
	if logs[0].StatusCode != 200 {
		t.Errorf("log.StatusCode = %d, want 200", logs[0].StatusCode)
	}
	if logs[0].LatencyMs != 120 {
		t.Errorf("log.LatencyMs = %d, want 120", logs[0].LatencyMs)
	}
}

func TestWriteBaselineToCheckLogs_Failed(t *testing.T) {
	db := setupCheckerDB(t)
	mc := newTestChecker(db)

	r := &ModelCheckResult{
		ModelID:       43,
		ModelName:     "baseline-fail-model",
		Available:     false,
		StatusCode:    401,
		Error:         "unauthorized",
		ErrorCategory: "auth_error",
	}
	mc.writeCheckLog(context.Background(), r, time.Now())

	var logs []internalmodel.ModelCheckLog
	db.Where("model_id = ?", uint(43)).Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Available {
		t.Errorf("log.Available = true, want false")
	}
	if logs[0].ErrorCategory != "auth_error" {
		t.Errorf("log.ErrorCategory = %q, want auth_error", logs[0].ErrorCategory)
	}
}

// ---------- U-30: countRecentFailures 连续失败计数 ----------

func TestCountRecentFailures(t *testing.T) {
	db := setupCheckerDB(t)
	mc := newTestChecker(db)

	var modelID uint = 99
	now := time.Now()

	// 写入 3 条连续失败日志
	for i := 0; i < 3; i++ {
		db.Create(&internalmodel.ModelCheckLog{
			ModelID:   modelID,
			ModelName: "count-fail-model",
			Available: false,
			CheckedAt: now.Add(time.Duration(i) * time.Minute),
		})
	}

	count := mc.countRecentFailures(context.Background(), modelID)
	// 函数返回 1(本次) + len(历史失败行)；3 条失败 → 1+3=4 ≥ FailureThreshold(3)
	if count < FailureThreshold {
		t.Errorf("countRecentFailures = %d, want >= %d (3 historical failures)", count, FailureThreshold)
	}
}

func TestCountRecentFailures_RecentSuccess(t *testing.T) {
	db := setupCheckerDB(t)
	mc := newTestChecker(db)

	var modelID uint = 100
	now := time.Now()

	// 2 条旧失败 + 1 条最新成功
	for i := 0; i < 2; i++ {
		db.Create(&internalmodel.ModelCheckLog{
			ModelID:   modelID,
			ModelName: "recover-model",
			Available: false,
			CheckedAt: now.Add(time.Duration(i) * time.Minute),
		})
	}
	db.Create(&internalmodel.ModelCheckLog{
		ModelID:   modelID,
		ModelName: "recover-model",
		Available: true,
		CheckedAt: now.Add(10 * time.Minute), // 最新
	})

	count := mc.countRecentFailures(context.Background(), modelID)
	// DESC 排序后第一行是成功 → break → count=1 < FailureThreshold(3)
	if count >= FailureThreshold {
		t.Errorf("countRecentFailures = %d, want < %d (most recent log is success)", count, FailureThreshold)
	}
}

func TestCountRecentFailures_EmptyLogs(t *testing.T) {
	db := setupCheckerDB(t)
	mc := newTestChecker(db)

	count := mc.countRecentFailures(context.Background(), 999)
	// 无日志 → count=1（仅本次计数）< FailureThreshold(3)
	if count >= FailureThreshold {
		t.Errorf("countRecentFailures (no logs) = %d, want < %d", count, FailureThreshold)
	}
}
