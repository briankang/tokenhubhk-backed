package parammapping

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"tokenhub-server/internal/model"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func uniqueParamName(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

var testDB *gorm.DB
var testSvc *ParamMappingService

func TestMain(m *testing.M) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "root:root123456@tcp(localhost:3306)/tokenhubhk?charset=utf8mb4&parseTime=True&loc=Local"
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		os.Exit(0) // 跳过测试：数据库不可用
	}
	testDB = db
	_ = testDB.AutoMigrate(&model.PlatformParam{}, &model.SupplierParamMapping{})
	testSvc = NewParamMappingService(testDB)
	code := m.Run()
	os.Exit(code)
}

// ─── CRUD 测试 ───

func TestCreateAndGetParam(t *testing.T) {
	ctx := context.Background()
	param := &model.PlatformParam{
		ParamName:   uniqueParamName("test_crud"),
		ParamType:   "bool",
		DisplayName: "测试参数",
		Description: "用于单元测试",
		Category:    "thinking",
		IsActive:    true,
	}

	// 创建
	if err := testSvc.CreateParam(ctx, param); err != nil {
		t.Fatalf("CreateParam failed: %v", err)
	}
	if param.ID == 0 {
		t.Fatal("expected param.ID > 0 after create")
	}
	defer testSvc.DeleteParam(ctx, param.ID) // 清理

	// 查询
	got, err := testSvc.GetParam(ctx, param.ID)
	if err != nil {
		t.Fatalf("GetParam failed: %v", err)
	}
	if got.ParamName != param.ParamName {
		t.Errorf("expected param_name=%s, got=%s", param.ParamName, got.ParamName)
	}
	if got.ParamType != "bool" {
		t.Errorf("expected param_type=bool, got=%s", got.ParamType)
	}
}

func TestUpdateParam(t *testing.T) {
	ctx := context.Background()
	param := &model.PlatformParam{
		ParamName: uniqueParamName("test_update"),
		ParamType: "int",
		Category:  "penalty",
		IsActive:  true,
	}
	if err := testSvc.CreateParam(ctx, param); err != nil {
		t.Fatalf("CreateParam failed: %v", err)
	}
	defer testSvc.DeleteParam(ctx, param.ID)

	// 更新
	err := testSvc.UpdateParam(ctx, param.ID, map[string]interface{}{
		"display_name": "已更新的名称",
		"is_active":    false,
	})
	if err != nil {
		t.Fatalf("UpdateParam failed: %v", err)
	}

	got, _ := testSvc.GetParam(ctx, param.ID)
	if got.DisplayName != "已更新的名称" {
		t.Errorf("expected display_name=已更新的名称, got=%s", got.DisplayName)
	}
	if got.IsActive {
		t.Error("expected is_active=false after update")
	}
}

func TestDeleteParamCascade(t *testing.T) {
	ctx := context.Background()
	param := &model.PlatformParam{
		ParamName: uniqueParamName("test_cascade"),
		ParamType: "bool",
		Category:  "thinking",
		IsActive:  true,
	}
	if err := testSvc.CreateParam(ctx, param); err != nil {
		t.Fatalf("CreateParam failed: %v", err)
	}

	// 添加映射
	mapping := &model.SupplierParamMapping{
		PlatformParamID: param.ID,
		SupplierCode:    "openai",
		VendorParamName: "test_vendor_param",
		TransformType:   "direct",
		Supported:       true,
	}
	if err := testSvc.UpsertMapping(ctx, mapping); err != nil {
		t.Fatalf("UpsertMapping failed: %v", err)
	}

	// 删除参数（应级联删除映射）
	if err := testSvc.DeleteParam(ctx, param.ID); err != nil {
		t.Fatalf("DeleteParam failed: %v", err)
	}

	// 验证映射也被删除
	mappings, _ := testSvc.GetMappingsBySupplier(ctx, "openai")
	for _, m := range mappings {
		if m.PlatformParamID == param.ID {
			t.Error("expected mapping to be cascade deleted with param")
		}
	}
}

// ─── 映射 CRUD 测试 ───

func TestUpsertMapping(t *testing.T) {
	ctx := context.Background()
	param := &model.PlatformParam{
		ParamName: uniqueParamName("test_upsert"),
		ParamType: "bool",
		Category:  "search",
		IsActive:  true,
	}
	if err := testSvc.CreateParam(ctx, param); err != nil {
		t.Fatalf("CreateParam failed: %v", err)
	}
	defer testSvc.DeleteParam(ctx, param.ID)

	// 创建映射
	mapping := &model.SupplierParamMapping{
		PlatformParamID: param.ID,
		SupplierCode:    "aliyun_dashscope",
		VendorParamName: "enable_search",
		TransformType:   "direct",
		Supported:       true,
		Notes:           "初始备注",
	}
	if err := testSvc.UpsertMapping(ctx, mapping); err != nil {
		t.Fatalf("UpsertMapping create failed: %v", err)
	}

	// 更新映射（同一供应商+参数组合）
	mapping2 := &model.SupplierParamMapping{
		PlatformParamID: param.ID,
		SupplierCode:    "aliyun_dashscope",
		VendorParamName: "web_search_enable",
		TransformType:   "rename",
		Supported:       true,
		Notes:           "更新后的备注",
	}
	if err := testSvc.UpsertMapping(ctx, mapping2); err != nil {
		t.Fatalf("UpsertMapping update failed: %v", err)
	}

	// 验证更新
	mappings, _ := testSvc.GetMappingsBySupplier(ctx, "aliyun_dashscope")
	found := false
	for _, m := range mappings {
		if m.PlatformParamID == param.ID {
			found = true
			if m.VendorParamName != "web_search_enable" {
				t.Errorf("expected vendor_param=web_search_enable, got=%s", m.VendorParamName)
			}
			if m.TransformType != "rename" {
				t.Errorf("expected transform_type=rename, got=%s", m.TransformType)
			}
		}
	}
	if !found {
		t.Error("expected upserted mapping to be found")
	}
}

// ─── 参数转换测试 ───

func TestTransformParams_Direct(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	// 手动注入缓存（绕过 DB 查询，纯逻辑测试）
	svc.mu.Lock()
	svc.cache["test_supplier"] = []mappingEntry{
		{PlatformParam: "enable_thinking", VendorParam: "enable_thinking", TransformType: "direct", Supported: true},
		{PlatformParam: "frequency_penalty", VendorParam: "frequency_penalty", TransformType: "direct", Supported: true},
	}
	svc.mu.Unlock()

	result := svc.TransformParams("test_supplier", map[string]interface{}{
		"enable_thinking":   true,
		"frequency_penalty": 0.5,
	})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if v, ok := result["enable_thinking"].(bool); !ok || !v {
		t.Errorf("expected enable_thinking=true, got=%v", result["enable_thinking"])
	}
	if v, ok := result["frequency_penalty"].(float64); !ok || v != 0.5 {
		t.Errorf("expected frequency_penalty=0.5, got=%v", result["frequency_penalty"])
	}
}

func TestTransformParams_Rename(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	svc.mu.Lock()
	svc.cache["moonshot"] = []mappingEntry{
		{PlatformParam: "enable_search", VendorParam: "use_search", TransformType: "rename", Supported: true},
	}
	svc.mu.Unlock()

	result := svc.TransformParams("moonshot", map[string]interface{}{
		"enable_search": true,
	})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// enable_search 应被重命名为 use_search
	if _, ok := result["enable_search"]; ok {
		t.Error("expected enable_search to be renamed, but it still exists")
	}
	if v, ok := result["use_search"].(bool); !ok || !v {
		t.Errorf("expected use_search=true, got=%v", result["use_search"])
	}
}

func TestTransformParams_Nested_WhenTrue(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	svc.mu.Lock()
	svc.cache["anthropic"] = []mappingEntry{
		{
			PlatformParam: "enable_thinking",
			VendorParam:   "thinking",
			TransformType: "nested",
			TransformRule: `{"when_true": {"type": "enabled", "budget_tokens": 10000}, "when_false": {"type": "disabled"}}`,
			Supported:     true,
		},
	}
	svc.mu.Unlock()

	// enable_thinking=true → thinking={type: "enabled", budget_tokens: 10000}
	result := svc.TransformParams("anthropic", map[string]interface{}{
		"enable_thinking": true,
	})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	thinking, ok := result["thinking"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected thinking to be map, got=%T", result["thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Errorf("expected thinking.type=enabled, got=%v", thinking["type"])
	}
	if thinking["budget_tokens"] != float64(10000) {
		t.Errorf("expected thinking.budget_tokens=10000, got=%v", thinking["budget_tokens"])
	}
}

func TestTransformParams_Nested_WhenFalse(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	svc.mu.Lock()
	svc.cache["anthropic"] = []mappingEntry{
		{
			PlatformParam: "enable_thinking",
			VendorParam:   "thinking",
			TransformType: "nested",
			TransformRule: `{"when_true": {"type": "enabled", "budget_tokens": 10000}, "when_false": {"type": "disabled"}}`,
			Supported:     true,
		},
	}
	svc.mu.Unlock()

	// enable_thinking=false → thinking={type: "disabled"}
	result := svc.TransformParams("anthropic", map[string]interface{}{
		"enable_thinking": false,
	})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	thinking, ok := result["thinking"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected thinking to be map, got=%T", result["thinking"])
	}
	if thinking["type"] != "disabled" {
		t.Errorf("expected thinking.type=disabled, got=%v", thinking["type"])
	}
}

func TestTransformParams_Nested_PathField(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	svc.mu.Lock()
	svc.cache["google_gemini"] = []mappingEntry{
		{
			PlatformParam: "frequency_penalty",
			VendorParam:   "generationConfig.frequencyPenalty",
			TransformType: "nested",
			TransformRule: `{"path": "generationConfig", "field": "frequencyPenalty"}`,
			Supported:     true,
		},
		{
			PlatformParam: "seed",
			VendorParam:   "generationConfig.seed",
			TransformType: "nested",
			TransformRule: `{"path": "generationConfig", "field": "seed"}`,
			Supported:     true,
		},
	}
	svc.mu.Unlock()

	result := svc.TransformParams("google_gemini", map[string]interface{}{
		"frequency_penalty": 0.8,
		"seed":              42,
	})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	gc, ok := result["generationConfig"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected generationConfig to be map, got=%T", result["generationConfig"])
	}
	if gc["frequencyPenalty"] != 0.8 {
		t.Errorf("expected frequencyPenalty=0.8, got=%v", gc["frequencyPenalty"])
	}
	if gc["seed"] != 42 {
		t.Errorf("expected seed=42, got=%v", gc["seed"])
	}
}

func TestTransformParams_Unsupported(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	svc.mu.Lock()
	svc.cache["deepseek"] = []mappingEntry{
		{PlatformParam: "enable_thinking", VendorParam: "enable_thinking", TransformType: "none", Supported: false},
		{PlatformParam: "frequency_penalty", VendorParam: "frequency_penalty", TransformType: "direct", Supported: true},
	}
	svc.mu.Unlock()

	result := svc.TransformParams("deepseek", map[string]interface{}{
		"enable_thinking":   true,
		"frequency_penalty": 0.3,
	})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// enable_thinking 不支持，不应传递
	if _, ok := result["enable_thinking"]; ok {
		t.Error("expected enable_thinking to be filtered out (unsupported)")
	}
	// frequency_penalty 支持，应保留
	if v, ok := result["frequency_penalty"].(float64); !ok || v != 0.3 {
		t.Errorf("expected frequency_penalty=0.3, got=%v", result["frequency_penalty"])
	}
}

func TestTransformParams_PassthroughUnmapped(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	svc.mu.Lock()
	svc.cache["openai"] = []mappingEntry{
		{PlatformParam: "seed", VendorParam: "seed", TransformType: "direct", Supported: true},
	}
	svc.mu.Unlock()

	// custom_field 没有映射定义，应直接透传
	result := svc.TransformParams("openai", map[string]interface{}{
		"seed":         123,
		"custom_field": "custom_value",
	})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result["seed"] != 123 {
		t.Errorf("expected seed=123, got=%v", result["seed"])
	}
	if result["custom_field"] != "custom_value" {
		t.Errorf("expected custom_field=custom_value, got=%v", result["custom_field"])
	}
}

func TestTransformParams_EmptyInput(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	result := svc.TransformParams("openai", nil)
	if result != nil {
		t.Errorf("expected nil for nil input, got=%v", result)
	}

	result = svc.TransformParams("openai", map[string]interface{}{})
	if result != nil {
		t.Errorf("expected nil for empty input, got=%v", result)
	}
}

func TestTransformParams_NoMappings(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	// 无映射定义的供应商，直接透传
	result := svc.TransformParams("unknown_supplier", map[string]interface{}{
		"enable_thinking": true,
	})

	// 如果 DB 也没有映射，loadMappings 返回空，应直接透传
	if result == nil {
		t.Fatal("expected non-nil result for unmapped supplier")
	}
	if v, ok := result["enable_thinking"].(bool); !ok || !v {
		t.Errorf("expected enable_thinking=true passthrough, got=%v", result["enable_thinking"])
	}
}

// ─── 联网搜索参数测试 ───

// TestTransformParams_EnableSearch_AliyunDashscope 验证阿里云DashScope的联网搜索参数直接透传
func TestTransformParams_EnableSearch_AliyunDashscope(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	svc.mu.Lock()
	svc.cache["aliyun_dashscope"] = []mappingEntry{
		{PlatformParam: "enable_search", VendorParam: "enable_search", TransformType: "direct", Supported: true},
		{PlatformParam: "enable_thinking", VendorParam: "enable_thinking", TransformType: "direct", Supported: true},
		{PlatformParam: "thinking_budget", VendorParam: "thinking_budget", TransformType: "direct", Supported: true},
	}
	svc.mu.Unlock()

	result := svc.TransformParams("aliyun_dashscope", map[string]interface{}{
		"enable_search": true,
	})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if v, ok := result["enable_search"].(bool); !ok || !v {
		t.Errorf("expected enable_search=true, got=%v", result["enable_search"])
	}
}

// TestTransformParams_EnableSearch_WithThinking 验证阿里云DashScope同时启用联网搜索和深度思考
// qwen3.5-plus 等思考模型可能不支持同时开启这两个参数，但参数映射层面都应透传
func TestTransformParams_EnableSearch_WithThinking(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	svc.mu.Lock()
	svc.cache["aliyun_dashscope"] = []mappingEntry{
		{PlatformParam: "enable_search", VendorParam: "enable_search", TransformType: "direct", Supported: true},
		{PlatformParam: "enable_thinking", VendorParam: "enable_thinking", TransformType: "direct", Supported: true},
		{PlatformParam: "thinking_budget", VendorParam: "thinking_budget", TransformType: "direct", Supported: true},
	}
	svc.mu.Unlock()

	result := svc.TransformParams("aliyun_dashscope", map[string]interface{}{
		"enable_search":   true,
		"enable_thinking": true,
		"thinking_budget": 10000,
	})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if v, ok := result["enable_search"].(bool); !ok || !v {
		t.Errorf("expected enable_search=true, got=%v", result["enable_search"])
	}
	if v, ok := result["enable_thinking"].(bool); !ok || !v {
		t.Errorf("expected enable_thinking=true, got=%v", result["enable_thinking"])
	}
	if v, ok := result["thinking_budget"].(int); !ok || v != 10000 {
		t.Errorf("expected thinking_budget=10000, got=%v", result["thinking_budget"])
	}
}

// TestTransformParams_EnableSearch_Moonshot 验证Moonshot(Kimi)的联网搜索参数重命名为use_search
func TestTransformParams_EnableSearch_Moonshot(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	svc.mu.Lock()
	svc.cache["moonshot"] = []mappingEntry{
		{PlatformParam: "enable_search", VendorParam: "use_search", TransformType: "rename", Supported: true},
	}
	svc.mu.Unlock()

	result := svc.TransformParams("moonshot", map[string]interface{}{
		"enable_search": true,
	})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// enable_search 应被重命名为 use_search（Moonshot/Kimi 特有参数名）
	if _, ok := result["enable_search"]; ok {
		t.Error("enable_search should be renamed to use_search for moonshot, but original key still exists")
	}
	if v, ok := result["use_search"].(bool); !ok || !v {
		t.Errorf("expected use_search=true for moonshot, got=%v", result["use_search"])
	}
}

// TestTransformParams_EnableSearch_Unsupported 验证不支持联网搜索的供应商会过滤掉该参数
func TestTransformParams_EnableSearch_Unsupported(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	svc.mu.Lock()
	// deepseek 不支持 enable_search
	svc.cache["deepseek"] = []mappingEntry{
		{PlatformParam: "enable_search", VendorParam: "enable_search", TransformType: "none", Supported: false},
		{PlatformParam: "frequency_penalty", VendorParam: "frequency_penalty", TransformType: "direct", Supported: true},
	}
	svc.mu.Unlock()

	result := svc.TransformParams("deepseek", map[string]interface{}{
		"enable_search":     true,
		"frequency_penalty": 0.5,
	})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// enable_search 不支持，应被过滤
	if _, ok := result["enable_search"]; ok {
		t.Error("expected enable_search to be filtered out for deepseek (Supported=false)")
	}
	// frequency_penalty 支持，应保留
	if v, ok := result["frequency_penalty"].(float64); !ok || v != 0.5 {
		t.Errorf("expected frequency_penalty=0.5, got=%v", result["frequency_penalty"])
	}
}

// TestTransformParams_EnableSearch_BaiduWenxin 验证百度文心联网搜索参数直接透传
func TestTransformParams_EnableSearch_BaiduWenxin(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	svc.mu.Lock()
	svc.cache["baidu_wenxin"] = []mappingEntry{
		{PlatformParam: "enable_search", VendorParam: "enable_search", TransformType: "direct", Supported: true},
	}
	svc.mu.Unlock()

	result := svc.TransformParams("baidu_wenxin", map[string]interface{}{
		"enable_search": true,
	})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if v, ok := result["enable_search"].(bool); !ok || !v {
		t.Errorf("expected enable_search=true for baidu_wenxin, got=%v", result["enable_search"])
	}
}

// TestTransformParams_EnableSearch_False 验证联网搜索关闭时(false)也能正确透传
func TestTransformParams_EnableSearch_False(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	svc.mu.Lock()
	svc.cache["aliyun_dashscope"] = []mappingEntry{
		{PlatformParam: "enable_search", VendorParam: "enable_search", TransformType: "direct", Supported: true},
	}
	svc.mu.Unlock()

	result := svc.TransformParams("aliyun_dashscope", map[string]interface{}{
		"enable_search": false,
	})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if v, ok := result["enable_search"].(bool); !ok || v {
		t.Errorf("expected enable_search=false, got=%v", result["enable_search"])
	}
}

func TestTransformParams_ValueMapping(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	svc.mu.Lock()
	svc.cache["test_mapping"] = []mappingEntry{
		{
			PlatformParam: "safe_mode",
			VendorParam:   "safety_level",
			TransformType: "mapping",
			TransformRule: `{"true": "strict", "false": "none"}`,
			Supported:     true,
		},
	}
	svc.mu.Unlock()

	result := svc.TransformParams("test_mapping", map[string]interface{}{
		"safe_mode": true,
	})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result["safety_level"] != "strict" {
		t.Errorf("expected safety_level=strict, got=%v", result["safety_level"])
	}
}

// ─── toBool 辅助函数测试 ───

func TestToBool(t *testing.T) {
	cases := []struct {
		input    interface{}
		expected bool
	}{
		{true, true},
		{false, false},
		{"true", true},
		{"false", false},
		{"1", true},
		{"yes", true},
		{"no", false},
		{float64(1), true},
		{float64(0), false},
		{0, false},
		{1, true},
		{nil, false},
	}

	for _, tc := range cases {
		got := toBool(tc.input)
		if got != tc.expected {
			t.Errorf("toBool(%v) = %v, expected %v", tc.input, got, tc.expected)
		}
	}
}

// ─── 缓存测试 ───

func TestCacheInvalidation(t *testing.T) {
	svc := &ParamMappingService{db: testDB, cache: make(map[string][]mappingEntry)}

	// 手动填充缓存
	svc.mu.Lock()
	svc.cache["cached_supplier"] = []mappingEntry{
		{PlatformParam: "test", VendorParam: "test", TransformType: "direct", Supported: true},
	}
	svc.mu.Unlock()

	// 验证缓存存在
	svc.mu.RLock()
	_, exists := svc.cache["cached_supplier"]
	svc.mu.RUnlock()
	if !exists {
		t.Fatal("expected cache to contain cached_supplier")
	}

	// 失效缓存
	svc.invalidateCache()

	svc.mu.RLock()
	_, exists = svc.cache["cached_supplier"]
	svc.mu.RUnlock()
	if exists {
		t.Error("expected cache to be empty after invalidation")
	}
}
