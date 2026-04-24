package aimodel

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gorlogger "gorm.io/gorm/logger"

	internalmodel "tokenhub-server/internal/model"
)

// setupCapabilityDB 创建内存 SQLite DB 并迁移能力测试所需表
func setupCapabilityDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gorlogger.Default.LogMode(gorlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&internalmodel.AIModel{},
		&internalmodel.ModelCheckLog{},
		&internalmodel.CapabilityTestCase{},
		&internalmodel.CapabilityTestTask{},
		&internalmodel.CapabilityTestResult{},
		&internalmodel.CapabilityTestBaseline{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

// newTestCapabilityTester 构造最简 CapabilityTester，不需要 httpClient
func newTestCapabilityTester(db *gorm.DB) *CapabilityTester {
	mc := &ModelChecker{db: db, logger: zap.NewNop()}
	return &CapabilityTester{db: db, mc: mc, logger: zap.NewNop()}
}

// ---------- U-31: VLM 模型可匹配 chat 类用例 ----------

func TestCaseMatchesModel_VLMCanDoChat(t *testing.T) {
	tester := &CapabilityTester{logger: zap.NewNop()}
	c := internalmodel.CapabilityTestCase{
		ModelType: "chat",
		Category:  "baseline",
	}
	m := internalmodel.AIModel{
		ModelType: "VLM",
		ModelName: "doubao-seed-2-0-pro",
	}
	got, reason := tester.caseMatchesModelV2(c, m, false)
	if !got {
		t.Fatalf("caseMatchesModelV2(chat, VLM) = false (%q), want true", reason)
	}
}

// ---------- U-32: features.supports_thinking=false + SkipKnownDisabled=true → 跳过 ----------

func TestCaseMatchesModel_FeatureDisabled(t *testing.T) {
	tester := &CapabilityTester{logger: zap.NewNop()}
	c := internalmodel.CapabilityTestCase{
		ModelType:  "chat",
		Category:   "thinking",
		Capability: "supports_thinking",
	}
	featBytes, _ := json.Marshal(map[string]interface{}{"supports_thinking": false})
	m := internalmodel.AIModel{
		ModelType: "LLM",
		ModelName: "qwen-plus",
		Features:  internalmodel.JSON(featBytes),
	}
	got, reason := tester.caseMatchesModelV2(c, m, true)
	if got {
		t.Fatalf("caseMatchesModelV2 = true, want false when feature explicitly disabled")
	}
	if reason != "feature_disabled" {
		t.Errorf("skipReason = %q, want feature_disabled", reason)
	}
}

// ---------- U-33: features.supports_web_search=true 绕过 provider_filter ----------

func TestCaseMatchesModel_FeatureEnabled_BypassProviderFilter(t *testing.T) {
	tester := &CapabilityTester{logger: zap.NewNop()}
	c := internalmodel.CapabilityTestCase{
		ModelType:      "chat",
		Category:       "search",
		Capability:     "supports_web_search",
		ProviderFilter: "qwen,hunyuan,glm",
	}
	featBytes, _ := json.Marshal(map[string]interface{}{"supports_web_search": true})
	m := internalmodel.AIModel{
		ModelName: "doubao-pro-32k",
		ModelType: "LLM",
		Features:  internalmodel.JSON(featBytes),
	}
	got, reason := tester.caseMatchesModelV2(c, m, false)
	if !got {
		t.Fatalf("caseMatchesModelV2 = false (%q), want true — feature=true should bypass provider_filter", reason)
	}
}

// ---------- U-34: AutoApplySuggestions 对 2 个 passed 结果写入 enable ----------

func TestAutoApplySuggestions_ApplyEnable(t *testing.T) {
	db := setupCapabilityDB(t)
	tester := newTestCapabilityTester(db)

	// 创建模型（无初始 features）
	m := internalmodel.AIModel{
		ModelName:  "thinking-model-u34",
		ModelType:  "LLM",
		IsActive:   true,
		Status:     "online",
		CategoryID: 1,
		SupplierID: 1,
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("create model: %v", err)
	}

	// 创建任务
	task := internalmodel.CapabilityTestTask{Status: "completed"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	// 两个不同名称的 thinking 用例（满足 Name uniqueIndex + uk_task_model_case）
	case1 := internalmodel.CapabilityTestCase{
		Name:            "thinking_pass_1",
		Category:        "thinking",
		ModelType:       "chat",
		Capability:      "supports_thinking",
		RequestTemplate: `{"messages":[{"role":"user","content":"test"}]}`,
		Assertions:      `[{"type":"status_eq","expected":200}]`,
	}
	case2 := internalmodel.CapabilityTestCase{
		Name:            "thinking_pass_2",
		Category:        "thinking",
		ModelType:       "chat",
		Capability:      "supports_thinking",
		RequestTemplate: `{"messages":[{"role":"user","content":"test2"}]}`,
		Assertions:      `[{"type":"status_eq","expected":200}]`,
	}
	db.Create(&case1)
	db.Create(&case2)

	// 2 条 passed 结果（effectiveTotal=2, failed=0 → enable/high）
	db.Create(&internalmodel.CapabilityTestResult{
		TaskID:    task.ID,
		ModelID:   m.ID,
		CaseID:    case1.ID,
		ModelName: m.ModelName,
		CaseName:  case1.Name,
		Status:    "passed",
	})
	db.Create(&internalmodel.CapabilityTestResult{
		TaskID:    task.ID,
		ModelID:   m.ID,
		CaseID:    case2.ID,
		ModelName: m.ModelName,
		CaseName:  case2.Name,
		Status:    "passed",
	})

	applied, _, err := tester.AutoApplySuggestions(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("AutoApplySuggestions error: %v", err)
	}
	if applied != 1 {
		t.Errorf("applied = %d, want 1", applied)
	}

	// 验证 features.supports_thinking = true 写入 DB
	var updated internalmodel.AIModel
	db.First(&updated, m.ID)
	var feats map[string]interface{}
	if err := json.Unmarshal([]byte(updated.Features), &feats); err != nil {
		t.Fatalf("unmarshal features: %v", err)
	}
	if feats["supports_thinking"] != true {
		t.Errorf("features.supports_thinking = %v, want true", feats["supports_thinking"])
	}
}

// ---------- U-35: AutoApplySuggestions 对 mixed 建议跳过 ----------

func TestAutoApplySuggestions_SkipMixed(t *testing.T) {
	db := setupCapabilityDB(t)
	tester := newTestCapabilityTester(db)

	m := internalmodel.AIModel{
		ModelName:  "thinking-model-u35",
		ModelType:  "LLM",
		IsActive:   true,
		Status:     "online",
		CategoryID: 1,
		SupplierID: 1,
	}
	db.Create(&m)

	task := internalmodel.CapabilityTestTask{Status: "completed"}
	db.Create(&task)

	case1 := internalmodel.CapabilityTestCase{
		Name:            "thinking_mixed_1",
		Category:        "thinking",
		ModelType:       "chat",
		Capability:      "supports_thinking",
		RequestTemplate: `{"messages":[{"role":"user","content":"test"}]}`,
		Assertions:      `[{"type":"status_eq","expected":200}]`,
	}
	case2 := internalmodel.CapabilityTestCase{
		Name:            "thinking_mixed_2",
		Category:        "thinking",
		ModelType:       "chat",
		Capability:      "supports_thinking",
		RequestTemplate: `{"messages":[{"role":"user","content":"test2"}]}`,
		Assertions:      `[{"type":"status_eq","expected":200}]`,
	}
	db.Create(&case1)
	db.Create(&case2)

	// 1 passed + 1 hard failed (assertion_failed) → action=mixed → skipped
	db.Create(&internalmodel.CapabilityTestResult{
		TaskID:    task.ID,
		ModelID:   m.ID,
		CaseID:    case1.ID,
		ModelName: m.ModelName,
		CaseName:  case1.Name,
		Status:    "passed",
	})
	db.Create(&internalmodel.CapabilityTestResult{
		TaskID:        task.ID,
		ModelID:       m.ID,
		CaseID:        case2.ID,
		ModelName:     m.ModelName,
		CaseName:      case2.Name,
		Status:        "failed",
		ErrorCategory: "assertion_failed",
	})

	applied, skipped, err := tester.AutoApplySuggestions(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("AutoApplySuggestions error: %v", err)
	}
	if applied != 0 {
		t.Errorf("applied = %d, want 0 (mixed action should not be applied)", applied)
	}
	if skipped == 0 {
		t.Errorf("skipped = 0, want > 0 (mixed action should count as skipped)")
	}
}

// ---------- U-36: SyncModelStatusFromBaseline 全 hard-fail → offline ----------

func TestSyncModelStatusFromBaseline_AllFail(t *testing.T) {
	db := setupCapabilityDB(t)
	tester := newTestCapabilityTester(db)

	m := internalmodel.AIModel{
		ModelName:  "model-all-fail-u36",
		ModelType:  "LLM",
		IsActive:   true,
		Status:     "online",
		CategoryID: 1,
		SupplierID: 1,
	}
	db.Create(&m)

	task := internalmodel.CapabilityTestTask{Status: "completed"}
	db.Create(&task)

	// baseline 用例，名称必须在 syncModelStatusFromBaseline 的 IN 列表内
	chatBasic := internalmodel.CapabilityTestCase{
		Name:            "chat_basic",
		Category:        "baseline",
		ModelType:       "chat",
		RequestTemplate: `{"messages":[{"role":"user","content":"Hi"}]}`,
		Assertions:      `[{"type":"status_eq","expected":200}]`,
	}
	db.Create(&chatBasic)

	// assertion_failed 属硬失败，计入 hard_fail_count
	db.Create(&internalmodel.CapabilityTestResult{
		TaskID:        task.ID,
		ModelID:       m.ID,
		CaseID:        chatBasic.ID,
		ModelName:     m.ModelName,
		CaseName:      chatBasic.Name,
		Status:        "failed",
		ErrorCategory: "assertion_failed",
	})

	tester.syncModelStatusFromBaseline(context.Background(), task.ID)

	var updated internalmodel.AIModel
	db.First(&updated, m.ID)
	if updated.Status != "offline" {
		t.Errorf("status = %q, want offline (hard fail on chat_basic should set offline)", updated.Status)
	}
}

// ---------- U-37: SyncModelStatusFromBaseline rate_limited → status 不变 ----------

func TestSyncModelStatusFromBaseline_RateLimited(t *testing.T) {
	db := setupCapabilityDB(t)
	tester := newTestCapabilityTester(db)

	m := internalmodel.AIModel{
		ModelName:  "model-ratelimited-u37",
		ModelType:  "LLM",
		IsActive:   true,
		Status:     "online",
		CategoryID: 1,
		SupplierID: 1,
	}
	db.Create(&m)

	task := internalmodel.CapabilityTestTask{Status: "completed"}
	db.Create(&task)

	chatBasic := internalmodel.CapabilityTestCase{
		Name:            "chat_basic",
		Category:        "baseline",
		ModelType:       "chat",
		RequestTemplate: `{"messages":[{"role":"user","content":"Hi"}]}`,
		Assertions:      `[{"type":"status_eq","expected":200}]`,
	}
	db.Create(&chatBasic)

	// rate_limited 被排除在 hard_fail_count 外 → pass_count=0, hard_fail_count=0 → no change
	db.Create(&internalmodel.CapabilityTestResult{
		TaskID:        task.ID,
		ModelID:       m.ID,
		CaseID:        chatBasic.ID,
		ModelName:     m.ModelName,
		CaseName:      chatBasic.Name,
		Status:        "failed",
		ErrorCategory: "rate_limited",
	})

	tester.syncModelStatusFromBaseline(context.Background(), task.ID)

	var updated internalmodel.AIModel
	db.First(&updated, m.ID)
	if updated.Status != "online" {
		t.Errorf("status = %q, want online (rate_limited should not trigger offline)", updated.Status)
	}
}
