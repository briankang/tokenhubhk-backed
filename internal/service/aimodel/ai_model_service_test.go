package aimodel

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
)

// ==================== 测试基础设施 ====================

func setupAIModelServiceTest(t *testing.T) (*AIModelService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Supplier{},
		&model.ModelCategory{},
		&model.AIModel{},
		&model.ModelPricing{},
		&model.ModelLabel{},
		&model.LabelDictionary{},
		&model.Channel{},
		&model.CustomChannel{},
		&model.CustomChannelRoute{},
		&model.ChannelModel{},
		&model.PlatformParam{},
		&model.SupplierParamMapping{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewAIModelService(db), db
}

func seedTestSupplier(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	sup := &model.Supplier{
		Name:       "测试供应商",
		Code:       "test_supplier",
		Status:     "active",
		IsActive:   true,
		AccessType: "api",
	}
	if err := db.Create(sup).Error; err != nil {
		t.Fatalf("seed supplier: %v", err)
	}
	return sup.ID
}

func seedTestCategory(t *testing.T, db *gorm.DB, supplierID uint) uint {
	t.Helper()
	cat := &model.ModelCategory{
		SupplierID: supplierID,
		Name:       "测试分类",
		Code:       fmt.Sprintf("test_cat_%d", supplierID),
	}
	if err := db.Create(cat).Error; err != nil {
		t.Fatalf("seed category: %v", err)
	}
	return cat.ID
}

func seedUsableChannel(t *testing.T, db *gorm.DB, supplierID uint, capabilities string) uint {
	t.Helper()
	ch := &model.Channel{
		Name:                  fmt.Sprintf("test-channel-%d", supplierID),
		SupplierID:            supplierID,
		Type:                  "openai",
		Endpoint:              "https://example.com/v1",
		APIKey:                "test-key",
		Status:                "active",
		Verified:              true,
		SupportedCapabilities: capabilities,
	}
	if err := db.Create(ch).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	return ch.ID
}

// createTestModel 创建一个基础测试模型
func createTestModel(t *testing.T, svc *AIModelService, supplierID, categoryID uint, name string) *model.AIModel {
	t.Helper()
	m := &model.AIModel{
		ModelName:   name,
		DisplayName: name + " Display",
		SupplierID:  supplierID,
		CategoryID:  categoryID,
		ModelType:   "LLM",
		Status:      "offline",
		IsActive:    true,
	}
	if err := svc.Create(context.Background(), m); err != nil {
		t.Fatalf("create model %q: %v", name, err)
	}
	return m
}

// ==================== U-01: 分页测试 ====================

func TestList_Pagination(t *testing.T) {
	svc, db := setupAIModelServiceTest(t)
	ctx := context.Background()

	supplierID := seedTestSupplier(t, db)
	categoryID := seedTestCategory(t, db, supplierID)

	// 创建 25 个模型
	for i := 1; i <= 25; i++ {
		createTestModel(t, svc, supplierID, categoryID, fmt.Sprintf("model-%03d", i))
	}

	// 第 1 页，每页 10 条
	page1, total1, err := svc.List(ctx, 1, 10)
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if total1 != 25 {
		t.Errorf("total want 25, got %d", total1)
	}
	if len(page1) != 10 {
		t.Errorf("page1 length want 10, got %d", len(page1))
	}

	// 第 3 页（剩余 5 条）
	page3, total3, err := svc.List(ctx, 3, 10)
	if err != nil {
		t.Fatalf("List page3: %v", err)
	}
	if total3 != 25 {
		t.Errorf("total3 want 25, got %d", total3)
	}
	if len(page3) != 5 {
		t.Errorf("page3 length want 5, got %d", len(page3))
	}

	// 边界：page=0 自动纠正为 1
	p0, _, err := svc.List(ctx, 0, 10)
	if err != nil {
		t.Fatalf("List page0: %v", err)
	}
	if len(p0) != 10 {
		t.Errorf("page0 (auto-corrected) length want 10, got %d", len(p0))
	}

	// pageSize 超限自动纠正为 20
	_, total, err := svc.List(ctx, 1, 200)
	if err != nil {
		t.Fatalf("List oversized page: %v", err)
	}
	if total != 25 {
		t.Errorf("total with oversized pageSize want 25, got %d", total)
	}
}

// ==================== U-02: 按供应商过滤 ====================

func TestListWithFilter_FilterBySupplier(t *testing.T) {
	svc, db := setupAIModelServiceTest(t)
	ctx := context.Background()

	// 供应商 A
	supA := &model.Supplier{Name: "供应商A", Code: "sup_a", Status: "active", IsActive: true, AccessType: "api"}
	db.Create(supA)
	catA := seedTestCategory(t, db, supA.ID)

	// 供应商 B
	supB := &model.Supplier{Name: "供应商B", Code: "sup_b", Status: "active", IsActive: true, AccessType: "api"}
	db.Create(supB)
	catB := seedTestCategory(t, db, supB.ID)

	// 供应商 A 创建 3 个模型
	for i := 1; i <= 3; i++ {
		createTestModel(t, svc, supA.ID, catA, fmt.Sprintf("model-a-%d", i))
	}
	// 供应商 B 创建 2 个模型
	for i := 1; i <= 2; i++ {
		createTestModel(t, svc, supB.ID, catB, fmt.Sprintf("model-b-%d", i))
	}

	// 过滤供应商 A
	listA, totalA, err := svc.ListWithFilter(ctx, 1, 20, supA.ID, "")
	if err != nil {
		t.Fatalf("ListWithFilter supplierA: %v", err)
	}
	if totalA != 3 {
		t.Errorf("totalA want 3, got %d", totalA)
	}
	if len(listA) != 3 {
		t.Errorf("listA length want 3, got %d", len(listA))
	}
	for _, m := range listA {
		if m.SupplierID != supA.ID {
			t.Errorf("model %q has wrong supplier_id %d, want %d", m.ModelName, m.SupplierID, supA.ID)
		}
	}

	// 不传供应商 → 返回全部 5 个
	all, totalAll, err := svc.ListWithFilter(ctx, 1, 20, 0, "")
	if err != nil {
		t.Fatalf("ListWithFilter all: %v", err)
	}
	if totalAll != 5 {
		t.Errorf("totalAll want 5, got %d", totalAll)
	}
	_ = all
}

// ==================== U-03: 关键字搜索 ====================

func TestListWithFilter_SearchByName(t *testing.T) {
	svc, db := setupAIModelServiceTest(t)
	ctx := context.Background()

	supplierID := seedTestSupplier(t, db)
	categoryID := seedTestCategory(t, db, supplierID)

	createTestModel(t, svc, supplierID, categoryID, "qwen-max")
	createTestModel(t, svc, supplierID, categoryID, "qwen-plus")
	createTestModel(t, svc, supplierID, categoryID, "gpt-4o")
	createTestModel(t, svc, supplierID, categoryID, "doubao-pro")

	// 搜索 "qwen"
	list, total, err := svc.ListWithFilter(ctx, 1, 20, 0, "qwen")
	if err != nil {
		t.Fatalf("ListWithFilter search: %v", err)
	}
	if total != 2 {
		t.Errorf("search 'qwen' total want 2, got %d", total)
	}
	if len(list) != 2 {
		t.Errorf("search 'qwen' list length want 2, got %d", len(list))
	}
	for _, m := range list {
		if !strings.Contains(strings.ToLower(m.ModelName), "qwen") {
			t.Errorf("unexpected model in search results: %q", m.ModelName)
		}
	}

	// 搜索不存在的关键字
	empty, emptyTotal, err := svc.ListWithFilter(ctx, 1, 20, 0, "nonexistent-model-xyz")
	if err != nil {
		t.Fatalf("ListWithFilter empty search: %v", err)
	}
	if emptyTotal != 0 {
		t.Errorf("empty search total want 0, got %d", emptyTotal)
	}
	if len(empty) != 0 {
		t.Errorf("empty search list want 0, got %d", len(empty))
	}
}

func TestSetStatusRejectsTemporaryModelNames(t *testing.T) {
	svc, db := setupAIModelServiceTest(t)
	ctx := context.Background()

	supplierID := seedTestSupplier(t, db)
	categoryID := seedTestCategory(t, db, supplierID)
	tmp := createTestModel(t, svc, supplierID, categoryID, "tmp-model_1777017073008_2951")

	if err := svc.SetStatus(ctx, tmp.ID, "online"); err == nil {
		t.Fatalf("SetStatus should reject temporary model names")
	}

	var got model.AIModel
	if err := db.First(&got, tmp.ID).Error; err != nil {
		t.Fatalf("reload model: %v", err)
	}
	if got.Status == "online" {
		t.Fatalf("temporary model was enabled unexpectedly")
	}
}

func TestPreflightModelEnableRequiresUsableChannel(t *testing.T) {
	svc, db := setupAIModelServiceTest(t)
	ctx := context.Background()

	supplierID := seedTestSupplier(t, db)
	categoryID := seedTestCategory(t, db, supplierID)
	m := createTestModel(t, svc, supplierID, categoryID, "preflight-no-channel")

	report, err := svc.PreflightModelEnable(ctx, m.ID)
	if err != nil {
		t.Fatalf("PreflightModelEnable: %v", err)
	}
	if report.CanEnable {
		t.Fatalf("report should block enablement without a usable channel: %+v", report)
	}
	if !preflightHasIssue(report, "no_active_channel") {
		t.Fatalf("expected no_active_channel issue, got %+v", report.Issues)
	}
	if err := svc.SetStatus(ctx, m.ID, "online"); err == nil {
		t.Fatalf("SetStatus should reject online status when preflight blocks")
	}
}

func TestPreflightModelEnablePassesWithVerifiedChannel(t *testing.T) {
	svc, db := setupAIModelServiceTest(t)
	ctx := context.Background()

	supplierID := seedTestSupplier(t, db)
	categoryID := seedTestCategory(t, db, supplierID)
	seedUsableChannel(t, db, supplierID, "chat")
	m := createTestModel(t, svc, supplierID, categoryID, "preflight-ok-model")

	report, err := svc.PreflightModelEnable(ctx, m.ID)
	if err != nil {
		t.Fatalf("PreflightModelEnable: %v", err)
	}
	if !report.CanEnable {
		t.Fatalf("report should allow enablement: %+v", report)
	}
	if err := svc.SetStatus(ctx, m.ID, "online"); err != nil {
		t.Fatalf("SetStatus online should pass with a verified channel: %v", err)
	}
}

func TestPreflightModelEnableBlocksRerankUntilEndpointImplemented(t *testing.T) {
	svc, db := setupAIModelServiceTest(t)
	ctx := context.Background()

	supplierID := seedTestSupplier(t, db)
	categoryID := seedTestCategory(t, db, supplierID)
	seedUsableChannel(t, db, supplierID, "rerank")
	m := createTestModel(t, svc, supplierID, categoryID, "rerank-test-model")
	if err := db.Model(&model.AIModel{}).Where("id = ?", m.ID).Update("model_type", "Rerank").Error; err != nil {
		t.Fatalf("update model type: %v", err)
	}

	report, err := svc.PreflightModelEnable(ctx, m.ID)
	if err != nil {
		t.Fatalf("PreflightModelEnable: %v", err)
	}
	if report.CanEnable {
		t.Fatalf("rerank should be blocked until /v1/rerank is implemented: %+v", report)
	}
	if !preflightHasIssue(report, "endpoint_not_implemented") {
		t.Fatalf("expected endpoint_not_implemented issue, got %+v", report.Issues)
	}
}

func preflightHasIssue(report *ModelPreflightReport, code string) bool {
	for _, issue := range report.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

// ==================== U-04: Create 必填字段校验 ====================

func TestCreate_RequiredFields(t *testing.T) {
	svc, _ := setupAIModelServiceTest(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		model   *model.AIModel
		wantErr string
	}{
		{
			name:    "nil model",
			model:   nil,
			wantErr: "nil",
		},
		{
			name:    "missing model_name",
			model:   &model.AIModel{CategoryID: 1, SupplierID: 1},
			wantErr: "model name",
		},
		{
			name:    "missing category_id",
			model:   &model.AIModel{ModelName: "test", SupplierID: 1},
			wantErr: "category",
		},
		{
			name:    "missing supplier_id",
			model:   &model.AIModel{ModelName: "test", CategoryID: 1},
			wantErr: "supplier",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.Create(ctx, tc.model)
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tc.wantErr)
				return
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantErr)) {
				t.Errorf("error %q should contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// ==================== U-06: Create 完整有效模型 ====================

func TestCreate_ValidModel(t *testing.T) {
	svc, db := setupAIModelServiceTest(t)
	ctx := context.Background()

	supplierID := seedTestSupplier(t, db)
	categoryID := seedTestCategory(t, db, supplierID)

	m := &model.AIModel{
		ModelName:     "qwen-max-test",
		DisplayName:   "通义千问 Max",
		SupplierID:    supplierID,
		CategoryID:    categoryID,
		ModelType:     "LLM",
		InputCostRMB:  0.04,
		ContextWindow: 128000,
	}

	if err := svc.Create(ctx, m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if m.ID == 0 {
		t.Error("expected non-zero ID after Create")
	}
	// 默认状态为 offline
	if m.Status != "offline" {
		t.Errorf("default status want 'offline', got %q", m.Status)
	}

	// 通过 GetByID 验证写入
	fetched, err := svc.GetByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if fetched.ModelName != "qwen-max-test" {
		t.Errorf("ModelName want %q, got %q", "qwen-max-test", fetched.ModelName)
	}
	if fetched.InputCostRMB != 0.04 {
		t.Errorf("InputCostRMB want 0.04, got %f", fetched.InputCostRMB)
	}
}

// ==================== U-07: Update 状态流转 ====================

func TestUpdate_StatusTransition(t *testing.T) {
	svc, db := setupAIModelServiceTest(t)
	ctx := context.Background()

	supplierID := seedTestSupplier(t, db)
	categoryID := seedTestCategory(t, db, supplierID)
	m := createTestModel(t, svc, supplierID, categoryID, "status-test-model")

	// offline → online
	if err := svc.Update(ctx, m.ID, map[string]interface{}{"status": "online"}); err != nil {
		t.Fatalf("Update to online: %v", err)
	}
	fetched, _ := svc.GetByID(ctx, m.ID)
	if fetched.Status != "online" {
		t.Errorf("status want 'online', got %q", fetched.Status)
	}

	// online → offline
	if err := svc.Update(ctx, m.ID, map[string]interface{}{"status": "offline"}); err != nil {
		t.Fatalf("Update to offline: %v", err)
	}
	fetched, _ = svc.GetByID(ctx, m.ID)
	if fetched.Status != "offline" {
		t.Errorf("status want 'offline', got %q", fetched.Status)
	}

	// offline → error
	if err := svc.Update(ctx, m.ID, map[string]interface{}{"status": "error"}); err != nil {
		t.Fatalf("Update to error: %v", err)
	}
	fetched, _ = svc.GetByID(ctx, m.ID)
	if fetched.Status != "error" {
		t.Errorf("status want 'error', got %q", fetched.Status)
	}

	// 不存在 ID → error
	err := svc.Update(ctx, 999999, map[string]interface{}{"status": "online"})
	if err == nil {
		t.Error("expected error updating non-existent model, got nil")
	}
}

// ==================== U-08: Update 价格字段 ====================

func TestUpdate_PriceFields(t *testing.T) {
	svc, db := setupAIModelServiceTest(t)
	ctx := context.Background()

	supplierID := seedTestSupplier(t, db)
	categoryID := seedTestCategory(t, db, supplierID)
	m := createTestModel(t, svc, supplierID, categoryID, "price-test-model")

	// 更新 input_cost_rmb
	if err := svc.Update(ctx, m.ID, map[string]interface{}{
		"input_cost_rmb":  1.5,
		"output_cost_rmb": 6.0,
	}); err != nil {
		t.Fatalf("Update price: %v", err)
	}

	fetched, err := svc.GetByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetByID after price update: %v", err)
	}
	if fetched.InputCostRMB != 1.5 {
		t.Errorf("InputCostRMB want 1.5, got %f", fetched.InputCostRMB)
	}
	if fetched.OutputCostRMB != 6.0 {
		t.Errorf("OutputCostRMB want 6.0, got %f", fetched.OutputCostRMB)
	}
}

// ==================== U-09: Delete 软删除 ====================

func TestDelete_SoftDelete(t *testing.T) {
	svc, db := setupAIModelServiceTest(t)
	ctx := context.Background()

	supplierID := seedTestSupplier(t, db)
	categoryID := seedTestCategory(t, db, supplierID)
	m := createTestModel(t, svc, supplierID, categoryID, "delete-test-model")

	// 删除
	if err := svc.Delete(ctx, m.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// GetByID 应返回 not found
	_, err := svc.GetByID(ctx, m.ID)
	if err == nil {
		t.Error("expected error after soft delete, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found', got %q", err.Error())
	}

	// 记录仍在 DB 中（soft delete）
	var count int64
	db.Unscoped().Model(&model.AIModel{}).Where("id = ?", m.ID).Count(&count)
	if count != 1 {
		t.Errorf("soft-deleted record should still exist in DB, count=%d", count)
	}

	// 删除不存在 ID
	err = svc.Delete(ctx, 999999)
	if err == nil {
		t.Error("expected error deleting non-existent model, got nil")
	}
}

// ==================== U-10: SetStatus 非法状态 ====================

func TestSetStatus_InvalidStatus(t *testing.T) {
	svc, db := setupAIModelServiceTest(t)
	ctx := context.Background()

	supplierID := seedTestSupplier(t, db)
	categoryID := seedTestCategory(t, db, supplierID)
	seedUsableChannel(t, db, supplierID, "chat")
	m := createTestModel(t, svc, supplierID, categoryID, "setstatus-test-model")

	invalidStatuses := []string{"unknown", "active", "deleted", "", "ONLINE"}
	for _, s := range invalidStatuses {
		err := svc.SetStatus(ctx, m.ID, s)
		if err == nil {
			t.Errorf("SetStatus(%q) should return error, got nil", s)
		}
	}

	// 合法状态应正常工作
	validStatuses := []string{"online", "offline", "error"}
	for _, s := range validStatuses {
		if err := svc.SetStatus(ctx, m.ID, s); err != nil {
			t.Errorf("SetStatus(%q) should succeed, got %v", s, err)
		}
	}

	// SetStatus(id=0) 应报错
	if err := svc.SetStatus(ctx, 0, "online"); err == nil {
		t.Error("SetStatus(id=0) should return error")
	}
}

// ==================== U-11: GetStats 计数一致性 ====================

func TestGetStats_CountConsistency(t *testing.T) {
	svc, db := setupAIModelServiceTest(t)
	ctx := context.Background()

	supplierID := seedTestSupplier(t, db)
	categoryID := seedTestCategory(t, db, supplierID)

	// 空库
	seedUsableChannel(t, db, supplierID, "chat")

	stats0, err := svc.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats empty: %v", err)
	}
	if stats0.Total != 0 || stats0.Enabled != 0 || stats0.Online != 0 {
		t.Errorf("empty stats should be all zero, got %+v", stats0)
	}

	// 创建 4 个模型：2 在线+激活，1 离线+激活，1 离线+未激活
	m1 := createTestModel(t, svc, supplierID, categoryID, "stat-model-1")
	svc.SetStatus(ctx, m1.ID, "online") // is_active = true (SetStatus online 自动设 true)

	m2 := createTestModel(t, svc, supplierID, categoryID, "stat-model-2")
	svc.SetStatus(ctx, m2.ID, "online") // is_active = true

	m3 := createTestModel(t, svc, supplierID, categoryID, "stat-model-3")
	_ = m3 // only needed for DB side effect
	// m3: offline, is_active = true (默认)

	m4 := createTestModel(t, svc, supplierID, categoryID, "stat-model-4")
	db.Model(&model.AIModel{}).Where("id = ?", m4.ID).Update("is_active", false)
	// m4: offline, is_active = false

	stats, err := svc.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.Total != 4 {
		t.Errorf("Total want 4, got %d", stats.Total)
	}
	if stats.Enabled != 3 { // m1, m2, m3 均 is_active=true
		t.Errorf("Enabled want 3, got %d", stats.Enabled)
	}
	if stats.Online != 2 { // 只有 m1, m2 status=online
		t.Errorf("Online want 2, got %d", stats.Online)
	}

	// 逻辑一致性：Online <= Enabled <= Total
	if stats.Online > stats.Enabled {
		t.Errorf("Online(%d) should be <= Enabled(%d)", stats.Online, stats.Enabled)
	}
	if stats.Enabled > stats.Total {
		t.Errorf("Enabled(%d) should be <= Total(%d)", stats.Enabled, stats.Total)
	}
}

// ==================== U-12: ListOnline 只返回活跃在线模型 ====================

func TestListOnline_OnlyActiveAndOnline(t *testing.T) {
	svc, db := setupAIModelServiceTest(t)
	ctx := context.Background()

	// 供应商 (status=active, is_active=true)
	sup := &model.Supplier{
		Name:       "在线供应商",
		Code:       "online_sup",
		Status:     "active",
		IsActive:   true,
		AccessType: "api",
	}
	db.Create(sup)
	categoryID := seedTestCategory(t, db, sup.ID)

	// 模型 1: online + is_active=true → 应出现
	m1 := createTestModel(t, svc, sup.ID, categoryID, "visible-model")
	db.Model(&model.AIModel{}).Where("id = ?", m1.ID).Updates(map[string]interface{}{
		"status": "online", "is_active": true,
		"input_cost_rmb": 1.0, "output_cost_rmb": 2.0,
	})

	// 模型 2: online + is_active=false → 不应出现
	m2 := createTestModel(t, svc, sup.ID, categoryID, "inactive-model")
	db.Model(&model.AIModel{}).Where("id = ?", m2.ID).Updates(map[string]interface{}{
		"status": "online", "is_active": false,
		"input_cost_rmb": 1.0, "output_cost_rmb": 2.0,
	})

	// 模型 3: offline + is_active=true → 不应出现
	m3 := createTestModel(t, svc, sup.ID, categoryID, "offline-active-model")
	db.Model(&model.AIModel{}).Where("id = ?", m3.ID).Updates(map[string]interface{}{
		"status": "offline", "is_active": true,
		"input_cost_rmb": 1.0, "output_cost_rmb": 2.0,
	})

	// 模型 4: online + is_active=true 但无售价 → price filter 测试
	m4 := createTestModel(t, svc, sup.ID, categoryID, "no-price-model")
	db.Model(&model.AIModel{}).Where("id = ?", m4.ID).Updates(map[string]interface{}{
		"status": "online", "is_active": true,
		"input_cost_rmb": 0, "output_cost_rmb": 0,
	})

	// 模型 5: 非聊天模型有价格 → 默认公开列表也应展示，保证 /models 与数据库公开状态一致
	m5 := createTestModel(t, svc, sup.ID, categoryID, "image-visible-model")
	db.Model(&model.AIModel{}).Where("id = ?", m5.ID).Updates(map[string]interface{}{
		"status": "online", "is_active": true, "model_type": "ImageGeneration",
		"input_cost_rmb": 0.3, "output_cost_rmb": 0,
	})

	list, total, err := svc.ListOnline(ctx, 1, 20)
	if err != nil {
		t.Fatalf("ListOnline: %v", err)
	}

	// m1 和 m5 (online + active + has price) 应该出现
	if total != 2 {
		t.Errorf("ListOnline total want 2, got %d", total)
	}
	if len(list) != 2 {
		t.Errorf("ListOnline list length want 2, got %d", len(list))
	}
	got := map[string]bool{}
	for _, item := range list {
		got[item.ModelName] = true
	}
	for _, name := range []string{"visible-model", "image-visible-model"} {
		if !got[name] {
			t.Errorf("expected %q in ListOnline result, got %#v", name, got)
		}
	}
}

// ==================== 额外: GetByID 边界测试 ====================

func TestGetByID_NotFound(t *testing.T) {
	svc, _ := setupAIModelServiceTest(t)
	ctx := context.Background()

	_, err := svc.GetByID(ctx, 999999)
	if err == nil {
		t.Error("GetByID non-existent should return error")
	}

	// id=0 也应报错
	_, err = svc.GetByID(ctx, 0)
	if err == nil {
		t.Error("GetByID(0) should return error")
	}
}

// ==================== 额外: NewAIModelService 防护 ====================

func TestNewAIModelService_NilDB(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewAIModelService(nil) should panic")
		}
	}()
	NewAIModelService(nil)
}
