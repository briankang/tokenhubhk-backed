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

func newMatrixTestDB(t *testing.T) *gorm.DB {
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

// TestGetMatrix_DefaultTemplate 模型未存矩阵 → 返回默认模板。
func TestGetMatrix_DefaultTemplate(t *testing.T) {
	db := newMatrixTestDB(t)
	m := model.AIModel{
		ModelName:   "seedance-2.0-test",
		ModelType:   model.ModelTypeVideoGeneration,
		PricingUnit: model.UnitPerMillionTokens,
		IsActive:    true,
	}
	db.Create(&m)

	svc := NewPriceMatrixService(db)
	pm, ai, isDefault, err := svc.GetMatrix(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("GetMatrix: %v", err)
	}
	if ai == nil {
		t.Fatal("ai model nil")
	}
	if pm == nil {
		t.Fatal("matrix nil")
	}
	if !isDefault {
		t.Fatal("isDefault should be true for unsaved matrix")
	}
	// Seedance 2.0 默认 3 维度(resolution × input_has_video × inference_mode)
	if len(pm.Dimensions) != 3 {
		t.Fatalf("default dims = %d, want 3", len(pm.Dimensions))
	}
	// cells 应为 3×2×2 = 12 个笛卡尔积
	if len(pm.Cells) != 12 {
		t.Fatalf("default cells = %d, want 12", len(pm.Cells))
	}
}

// TestGetMatrix_TTSDefault TTS 默认 voice_tier × stream_mode。
func TestGetMatrix_TTSDefault(t *testing.T) {
	db := newMatrixTestDB(t)
	m := model.AIModel{
		ModelName:   "tts-test",
		ModelType:   model.ModelTypeTTS,
		PricingUnit: model.UnitPer10kCharacters,
	}
	db.Create(&m)

	svc := NewPriceMatrixService(db)
	pm, _, _, err := svc.GetMatrix(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("GetMatrix: %v", err)
	}
	if len(pm.Dimensions) != 2 {
		t.Fatalf("TTS dims = %d, want 2", len(pm.Dimensions))
	}
	if pm.Dimensions[0].Key != "voice_tier" {
		t.Fatalf("first dim = %s, want voice_tier", pm.Dimensions[0].Key)
	}
}

// TestGetMatrix_EmbeddingNoDimensions Embedding 无维度,单 cell。
func TestGetMatrix_EmbeddingNoDimensions(t *testing.T) {
	db := newMatrixTestDB(t)
	m := model.AIModel{
		ModelName:   "embed-test",
		ModelType:   model.ModelTypeEmbedding,
		PricingUnit: model.UnitPerMillionTokens,
	}
	db.Create(&m)

	svc := NewPriceMatrixService(db)
	pm, _, _, err := svc.GetMatrix(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("GetMatrix: %v", err)
	}
	if len(pm.Dimensions) != 0 {
		t.Fatalf("embedding dims = %d, want 0", len(pm.Dimensions))
	}
	if len(pm.Cells) != 1 {
		t.Fatalf("embedding cells = %d, want 1", len(pm.Cells))
	}
}

// TestGetMatrix_AfterSaveIsNotDefault 保存矩阵后再读取,isDefault 应为 false。
func TestGetMatrix_AfterSaveIsNotDefault(t *testing.T) {
	db := newMatrixTestDB(t)
	m := model.AIModel{
		ModelName:   "saved-matrix",
		ModelType:   model.ModelTypeLLM,
		PricingUnit: model.UnitPerMillionTokens,
	}
	db.Create(&m)
	svc := NewPriceMatrixService(db)

	// 先保存一个简单矩阵
	pm := &model.PriceMatrix{
		SchemaVersion: 1,
		Currency:      "RMB",
		Unit:          model.UnitPerMillionTokens,
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{}, OfficialInput: ptrFloat(2.0), OfficialOutput: ptrFloat(8.0),
				SellingInput: ptrFloat(1.5), SellingOutput: ptrFloat(6.0), Supported: true},
		},
	}
	if err := svc.UpdateMatrix(context.Background(), m.ID, pm); err != nil {
		t.Fatalf("UpdateMatrix: %v", err)
	}
	// 再读取
	got, _, isDefault, err := svc.GetMatrix(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("GetMatrix: %v", err)
	}
	if isDefault {
		t.Fatal("isDefault should be false after save")
	}
	if len(got.Cells) != 1 || got.Cells[0].SellingInput == nil || *got.Cells[0].SellingInput != 1.5 {
		t.Fatalf("retrieved cells did not match saved values: %+v", got.Cells)
	}
}

// TestGetMatrix_DefaultPrefilledFromAIModel 模型未保存矩阵但有成本数据 →
// 默认模板的 cells 应被预填 OfficialInput/OfficialOutput,SellingInput/Output 走折扣 fallback。
func TestGetMatrix_DefaultPrefilledFromAIModel(t *testing.T) {
	db := newMatrixTestDB(t)
	m := model.AIModel{
		ModelName:     "prefill-llm",
		ModelType:     model.ModelTypeLLM,
		PricingUnit:   model.UnitPerMillionTokens,
		InputCostRMB:  3.0,
		OutputCostRMB: 9.0,
	}
	db.Create(&m)

	svc := NewPriceMatrixService(db)
	pm, _, isDefault, err := svc.GetMatrix(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("GetMatrix: %v", err)
	}
	if !isDefault {
		t.Fatal("isDefault should be true for unsaved matrix")
	}
	if len(pm.Cells) != 1 {
		t.Fatalf("cells=%d want 1", len(pm.Cells))
	}
	c := pm.Cells[0]
	if c.OfficialInput == nil || *c.OfficialInput != 3.0 {
		t.Errorf("OfficialInput=%v want 3.0", c.OfficialInput)
	}
	if c.OfficialOutput == nil || *c.OfficialOutput != 9.0 {
		t.Errorf("OfficialOutput=%v want 9.0", c.OfficialOutput)
	}
	// 无 mp,Selling 走默认折扣 0.85
	if c.SellingInput == nil || roundTo6(*c.SellingInput) != roundTo6(3.0*0.85) {
		t.Errorf("SellingInput=%v want %v", c.SellingInput, 3.0*0.85)
	}
}

// TestUpdateMatrix_PersistsAndSyncsTopLevel 保存矩阵后同步顶层售价字段。
func TestUpdateMatrix_PersistsAndSyncsTopLevel(t *testing.T) {
	db := newMatrixTestDB(t)
	m := model.AIModel{
		ModelName:   "sync-test",
		ModelType:   model.ModelTypeLLM,
		PricingUnit: model.UnitPerMillionTokens,
	}
	db.Create(&m)
	svc := NewPriceMatrixService(db)

	pm := &model.PriceMatrix{
		SchemaVersion: 1,
		Currency:      "RMB",
		Unit:          model.UnitPerMillionTokens,
		Dimensions:    []model.PriceDimension{},
		Cells: []model.PriceMatrixCell{
			{
				DimValues:      map[string]interface{}{},
				OfficialInput:  ptrFloat(2.0),
				OfficialOutput: ptrFloat(8.0),
				SellingInput:   ptrFloat(1.5),
				SellingOutput:  ptrFloat(6.0),
				Supported:      true,
			},
		},
	}
	if err := svc.UpdateMatrix(context.Background(), m.ID, pm); err != nil {
		t.Fatalf("UpdateMatrix: %v", err)
	}
	var mp model.ModelPricing
	if err := db.Where("model_id = ?", m.ID).First(&mp).Error; err != nil {
		t.Fatalf("load mp: %v", err)
	}
	if mp.InputPriceRMB != 1.5 || mp.OutputPriceRMB != 6.0 {
		t.Fatalf("top-level not synced: input=%v output=%v", mp.InputPriceRMB, mp.OutputPriceRMB)
	}
	// 反序列化矩阵,确保完整
	var saved model.PriceMatrix
	if err := json.Unmarshal(mp.PriceMatrix, &saved); err != nil {
		t.Fatalf("unmarshal saved: %v", err)
	}
	if len(saved.Cells) != 1 {
		t.Fatalf("saved cells = %d, want 1", len(saved.Cells))
	}
}

// TestMatchCell_FullMatch 完全匹配的 cell 命中。
func TestMatchCell_FullMatch(t *testing.T) {
	pm := &model.PriceMatrix{
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{"resolution": "480p", "input_has_video": false}, Supported: true, SellingPerUnit: ptrFloat(39.10)},
			{DimValues: map[string]interface{}{"resolution": "1080p", "input_has_video": true}, Supported: true, SellingPerUnit: ptrFloat(26.35)},
		},
	}
	hit := MatchCell(pm, map[string]interface{}{"resolution": "1080p", "input_has_video": true})
	if hit == nil {
		t.Fatal("expected match, got nil")
	}
	if *hit.SellingPerUnit != 26.35 {
		t.Fatalf("SellingPerUnit = %v, want 26.35", *hit.SellingPerUnit)
	}
}

// TestMatchCell_UnsupportedSkipped supported=false 的 cell 不命中。
func TestMatchCell_UnsupportedSkipped(t *testing.T) {
	pm := &model.PriceMatrix{
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{"inference_mode": "offline"}, Supported: false, UnsupportedReason: "暂不支持"},
		},
	}
	hit := MatchCell(pm, map[string]interface{}{"inference_mode": "offline"})
	if hit != nil {
		t.Fatalf("unsupported cell should not match, got %+v", hit)
	}
}

// TestMatchCell_PartialDimensionsExtraInRequest 请求 dim_values 多于 cell 已定义的维度 → 不匹配。
func TestMatchCell_PartialDimensionsExtraInRequest(t *testing.T) {
	pm := &model.PriceMatrix{
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{"resolution": "480p"}, Supported: true, SellingPerUnit: ptrFloat(10)},
		},
	}
	// 请求多了一个 inference_mode,cell 没有该 key,所以请求中 input_has_video=true 不必检查
	// 但请求中 resolution=480p 必须匹配
	hit := MatchCell(pm, map[string]interface{}{"resolution": "480p"})
	if hit == nil {
		t.Fatal("simple request should match")
	}
	// 请求多了 cell 没有的 key
	hit2 := MatchCell(pm, map[string]interface{}{"resolution": "480p", "extra": "x"})
	if hit2 == nil {
		t.Fatal("cell match should ignore request extra keys")
	}
}

// TestMatchCell_NumberStringEquivalence float64 vs int 等价比较。
func TestMatchCell_NumberStringEquivalence(t *testing.T) {
	pm := &model.PriceMatrix{
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{"input_has_video": true}, Supported: true, SellingPerUnit: ptrFloat(20)},
		},
	}
	// JSON 反序列化后布尔类型一致,但即使是 string "true" 也应通过 fmt.Sprint 比较为相等
	hit := MatchCell(pm, map[string]interface{}{"input_has_video": true})
	if hit == nil {
		t.Fatal("boolean match failed")
	}
	hit2 := MatchCell(pm, map[string]interface{}{"input_has_video": "true"})
	if hit2 == nil {
		t.Fatal("string-vs-bool 'true' match failed (fmt.Sprint equivalence expected)")
	}
}

func ptrFloat(v float64) *float64 { return &v }
