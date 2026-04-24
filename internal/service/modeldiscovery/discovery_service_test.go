package modeldiscovery

import (
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	internalmodel "tokenhub-server/internal/model"
)

// setupDiscoveryDB 创建内存 SQLite DB 并迁移测试所需表
func setupDiscoveryDB(t *testing.T) (*DiscoveryService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&internalmodel.Supplier{},
		&internalmodel.ModelCategory{},
		&internalmodel.AIModel{},
		&internalmodel.ModelPricing{},
		&internalmodel.ModelCheckLog{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return NewDiscoveryService(db), db
}

// seedDiscoverySupplier 创建测试用供应商
func seedDiscoverySupplier(t *testing.T, db *gorm.DB, code string) uint {
	t.Helper()
	sup := internalmodel.Supplier{
		Name:       "Test Supplier " + code,
		Code:       code,
		IsActive:   true,
		Status:     "active",
		AccessType: "api",
	}
	if err := db.Create(&sup).Error; err != nil {
		t.Fatalf("create supplier: %v", err)
	}
	return sup.ID
}

// seedDiscoveryCategory 创建测试用分类
func seedDiscoveryCategory(t *testing.T, db *gorm.DB, supplierID uint, code string) uint {
	t.Helper()
	cat := internalmodel.ModelCategory{
		SupplierID: supplierID,
		Name:       "Test Category " + code,
		Code:       code,
	}
	if err := db.Create(&cat).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	return cat.ID
}

// seedActiveModel 创建 is_active=true 的测试模型
func seedActiveModel(t *testing.T, db *gorm.DB, supID, catID uint, name string) uint {
	t.Helper()
	m := internalmodel.AIModel{
		SupplierID:  supID,
		CategoryID:  catID,
		ModelName:   name,
		DisplayName: name,
		IsActive:    true,
		Status:      "offline",
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("create model %s: %v", name, err)
	}
	return m.ID
}

// ---------- U-13: InferModelTags GPT-4o ----------

func TestInferModelTags_GPT4(t *testing.T) {
	tags := InferModelTags("gpt-4o", "openai")
	if !strings.Contains(tags, "OpenAI") {
		t.Errorf("expected tags to contain 'OpenAI', got: %q", tags)
	}
}

// ---------- U-14: InferModelTags Qwen ----------

func TestInferModelTags_Qwen(t *testing.T) {
	tags := InferModelTags("qwen-max", "aliyun_dashscope")
	if !strings.Contains(tags, "Qwen") {
		t.Errorf("expected tags to contain 'Qwen', got: %q", tags)
	}
	// 供应商品牌也应注入
	if !strings.Contains(tags, "Alibaba") {
		t.Errorf("expected tags to contain 'Alibaba' (supplier brand), got: %q", tags)
	}
}

func TestInferModelTags_DoubaoVolcengine(t *testing.T) {
	tags := InferModelTags("doubao-pro-32k", "volcengine")
	if !strings.Contains(tags, "Volcengine") {
		t.Errorf("expected tags to contain 'Volcengine', got: %q", tags)
	}
}

func TestInferModelTags_UnknownModel(t *testing.T) {
	// 未知模型名 + 未知供应商 → 空字符串
	tags := InferModelTags("my-custom-model", "")
	// 不会崩溃即可；无法匹配任何规则时返回空
	_ = tags
}

func TestInferModelTags_Dedup(t *testing.T) {
	// gpt- 匹配到 OpenAI，openai supplier 同样是 OpenAI → 去重后只有一个
	tags := InferModelTags("gpt-4o", "openai")
	count := strings.Count(tags, "OpenAI")
	if count != 1 {
		t.Errorf("expected exactly 1 'OpenAI' in tags (dedup), got %d in %q", count, tags)
	}
}

// ---------- U-15: isOldDatedModel 带 MMDD 后缀 ----------

func TestIsOldDatedModel_MMDD(t *testing.T) {
	cases := []struct {
		name   string
		expect bool
	}{
		{"qwen-plus-0428", true},
		{"qwen-max-1201", true},
		{"qwen-plus-0806", true},
		{"gpt-4-0613", true},
	}
	for _, tc := range cases {
		got := isOldDatedModel(tc.name)
		if got != tc.expect {
			t.Errorf("isOldDatedModel(%q) = %v, want %v", tc.name, got, tc.expect)
		}
	}
}

// ---------- U-16: isOldDatedModel 普通模型名 ----------

func TestIsOldDatedModel_Normal(t *testing.T) {
	cases := []struct {
		name   string
		expect bool
	}{
		{"qwen-plus", false},
		{"gpt-4o", false},
		{"doubao-pro-32k", false},
		{"claude-3-5-sonnet", false},
		{"ab", false},   // 太短
		{"", false},     // 空
		{"qwen-max-v2", false},  // 非纯数字后缀
	}
	for _, tc := range cases {
		got := isOldDatedModel(tc.name)
		if got != tc.expect {
			t.Errorf("isOldDatedModel(%q) = %v, want %v", tc.name, got, tc.expect)
		}
	}
}

// ---------- U-17: isModelCheckFailed 无日志 ----------

func TestIsModelCheckFailed_NoLogs(t *testing.T) {
	svc, _ := setupDiscoveryDB(t)
	// 没有任何检测日志 → 不跳过
	got := svc.isModelCheckFailed("gpt-4o")
	if got {
		t.Error("expected false when no check logs exist")
	}
}

// ---------- U-18: isModelCheckFailed 三次失败 ----------

func TestIsModelCheckFailed_ThreeFails(t *testing.T) {
	svc, db := setupDiscoveryDB(t)
	modelName := "ernie-offline"
	now := time.Now()
	// 写入 3 条 Available=false 的日志（最旧 → 最新）
	for i := 0; i < 3; i++ {
		log := internalmodel.ModelCheckLog{
			ModelName:      modelName,
			Available:      false,
			CheckedAt:      now.Add(time.Duration(i) * time.Minute),
			UpstreamStatus: "deprecated_upstream",
		}
		db.Create(&log)
	}
	got := svc.isModelCheckFailed(modelName)
	if !got {
		t.Error("expected true when 3 consecutive failures with deprecated_upstream")
	}
}

func TestIsModelCheckFailed_ThreeFails_NoDeprecated(t *testing.T) {
	// 3 条全失败但无 deprecated_upstream 标记 → len(logs)>=3 仍返回 true
	svc, db := setupDiscoveryDB(t)
	modelName := "model-three-fail-no-deprecated"
	now := time.Now()
	for i := 0; i < 3; i++ {
		log := internalmodel.ModelCheckLog{
			ModelName: modelName,
			Available: false,
			CheckedAt: now.Add(time.Duration(i) * time.Minute),
		}
		db.Create(&log)
	}
	got := svc.isModelCheckFailed(modelName)
	if !got {
		t.Error("expected true when len(logs)>=3 and all failed (even without deprecated)")
	}
}

func TestIsModelCheckFailed_TwoFails(t *testing.T) {
	// 只有 2 条失败且无 deprecated → len < threshold，返回 false
	svc, db := setupDiscoveryDB(t)
	modelName := "model-two-fail"
	now := time.Now()
	for i := 0; i < 2; i++ {
		log := internalmodel.ModelCheckLog{
			ModelName: modelName,
			Available: false,
			CheckedAt: now.Add(time.Duration(i) * time.Minute),
		}
		db.Create(&log)
	}
	got := svc.isModelCheckFailed(modelName)
	if got {
		t.Error("expected false when only 2 failures without deprecated_upstream")
	}
}

// ---------- U-19: isModelCheckFailed 最近一条成功 ----------

func TestIsModelCheckFailed_OneSuccess(t *testing.T) {
	svc, db := setupDiscoveryDB(t)
	modelName := "qwen-plus-recovered"
	now := time.Now()
	// 2 条失败（旧）+ 1 条成功（最新）
	for i := 0; i < 2; i++ {
		log := internalmodel.ModelCheckLog{
			ModelName: modelName,
			Available: false,
			CheckedAt: now.Add(time.Duration(i) * time.Minute),
		}
		db.Create(&log)
	}
	// 最新的一条成功
	db.Create(&internalmodel.ModelCheckLog{
		ModelName: modelName,
		Available: true,
		CheckedAt: now.Add(10 * time.Minute),
	})

	got := svc.isModelCheckFailed(modelName)
	if got {
		t.Error("expected false when the most recent log is Available=true")
	}
}

// ---------- U-20: MatchStreamOnly QwQ ----------

func TestMatchStreamOnly_QwQ(t *testing.T) {
	cases := []struct {
		name   string
		expect bool
	}{
		{"qwq-plus-latest", true},
		{"qwq-32b-preview", true},
		{"QwQ-Plus", true}, // 大小写不敏感
		{"qvq-72b-preview", true},
		{"qwen3-coder-plus", true},
	}
	for _, tc := range cases {
		got := MatchStreamOnly(tc.name)
		if got != tc.expect {
			t.Errorf("MatchStreamOnly(%q) = %v, want %v", tc.name, got, tc.expect)
		}
	}
}

// ---------- U-21: MatchStreamOnly 普通模型 ----------

func TestMatchStreamOnly_Normal(t *testing.T) {
	cases := []struct {
		name   string
		expect bool
	}{
		{"gpt-4o", false},
		{"qwen-plus", false},
		{"doubao-pro-32k", false},
		{"", false},
	}
	for _, tc := range cases {
		got := MatchStreamOnly(tc.name)
		if got != tc.expect {
			t.Errorf("MatchStreamOnly(%q) = %v, want %v", tc.name, got, tc.expect)
		}
	}
}

// ---------- U-22: IsBogusFlagKey stop=true ----------

func TestIsBogusFlagKey_StopTrue(t *testing.T) {
	keys := []string{"stop", "stream", "tools", "tool_choice", "voice", "dimensions",
		"enable_thinking", "thinking", "reasoning", "response_format"}
	for _, k := range keys {
		if !internalmodel.IsBogusFlagKey(k) {
			t.Errorf("expected IsBogusFlagKey(%q) = true, got false", k)
		}
	}
}

// ---------- U-23: IsBogusFlagKey 合法参数 ----------

func TestIsBogusFlagKey_ValidParam(t *testing.T) {
	validKeys := []string{"temperature", "max_tokens", "top_p", "top_k", "presence_penalty",
		"frequency_penalty", "model", "messages", "stream_options"}
	for _, k := range validKeys {
		if internalmodel.IsBogusFlagKey(k) {
			t.Errorf("expected IsBogusFlagKey(%q) = false, got true", k)
		}
	}
}

// ---------- U-24: DisableModelsWithoutSellPrice ----------

func TestDisableModelsWithoutSellPrice(t *testing.T) {
	svc, db := setupDiscoveryDB(t)
	supID := seedDiscoverySupplier(t, db, "test-sup-u24")
	catID := seedDiscoveryCategory(t, db, supID, "cat-u24")

	// 创建 5 个 is_active=true 的模型，无 model_pricings 记录
	modelIDs := make([]uint, 5)
	for i := 0; i < 5; i++ {
		modelIDs[i] = seedActiveModel(t, db, supID, catID,
			"test-model-no-price-"+string(rune('a'+i)))
	}

	svc.disableModelsWithoutSellPrice()

	// 验证全部 5 个模型 is_active=false，tags 含 NeedsSellPrice
	for _, id := range modelIDs {
		var m internalmodel.AIModel
		db.First(&m, id)
		if m.IsActive {
			t.Errorf("model id=%d expected is_active=false after disableModelsWithoutSellPrice", id)
		}
		if !strings.Contains(m.Tags, "NeedsSellPrice") {
			t.Errorf("model id=%d expected tags to contain 'NeedsSellPrice', got: %q", id, m.Tags)
		}
	}
}

func TestDisableModelsWithoutSellPrice_SkipFreeModels(t *testing.T) {
	svc, db := setupDiscoveryDB(t)
	supID := seedDiscoverySupplier(t, db, "test-sup-free")
	catID := seedDiscoveryCategory(t, db, supID, "cat-free")

	// 创建一个带 Free 标签的模型
	m := internalmodel.AIModel{
		SupplierID:  supID,
		CategoryID:  catID,
		ModelName:   "test-free-model",
		DisplayName: "Free Model",
		IsActive:    true,
		Status:      "online",
		Tags:        "Free",
	}
	db.Create(&m)

	svc.disableModelsWithoutSellPrice()

	// Free 模型不应被停用
	var updated internalmodel.AIModel
	db.First(&updated, m.ID)
	if !updated.IsActive {
		t.Error("expected Free model to remain active after disableModelsWithoutSellPrice")
	}
}

// ---------- U-25: 有售价的模型不被停用 + tag 辅助函数 ----------

func TestDisableModelsWithoutSellPrice_SkipPricedModels(t *testing.T) {
	// 验证：已有 model_pricings 的模型不被停用
	svc, db := setupDiscoveryDB(t)
	supID := seedDiscoverySupplier(t, db, "test-sup-priced")
	catID := seedDiscoveryCategory(t, db, supID, "cat-priced")

	modelID := seedActiveModel(t, db, supID, catID, "model-with-price")

	// 创建对应的 model_pricings 记录
	pricing := internalmodel.ModelPricing{
		ModelID:       modelID,
		InputPriceRMB: 1.5,
	}
	db.Create(&pricing)

	svc.disableModelsWithoutSellPrice()

	// 有售价的模型应保持 is_active=true
	var m internalmodel.AIModel
	db.First(&m, modelID)
	if !m.IsActive {
		t.Error("expected priced model to remain active after disableModelsWithoutSellPrice")
	}
}

func TestAddTagToStr(t *testing.T) {
	cases := []struct {
		tags   string
		tag    string
		expect string
	}{
		{"", "NeedsSellPrice", "NeedsSellPrice"},
		{"Existing", "NeedsSellPrice", "Existing,NeedsSellPrice"},
		{"NeedsSellPrice", "NeedsSellPrice", "NeedsSellPrice"}, // 已存在不重复
		{"A,B", "C", "A,B,C"},
	}
	for _, tc := range cases {
		got := addTagToStr(tc.tags, tc.tag)
		if got != tc.expect {
			t.Errorf("addTagToStr(%q, %q) = %q, want %q", tc.tags, tc.tag, got, tc.expect)
		}
	}
}

func TestRemoveTagFromStr(t *testing.T) {
	cases := []struct {
		tags   string
		tag    string
		expect string
	}{
		{"NeedsSellPrice", "NeedsSellPrice", ""},
		{"A,NeedsSellPrice,B", "NeedsSellPrice", "A,B"},
		{"A,B", "NeedsSellPrice", "A,B"}, // 不含该标签不报错
		{"", "NeedsSellPrice", ""},
	}
	for _, tc := range cases {
		got := removeTagFromStr(tc.tags, tc.tag)
		if got != tc.expect {
			t.Errorf("removeTagFromStr(%q, %q) = %q, want %q", tc.tags, tc.tag, got, tc.expect)
		}
	}
}
