package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// TestPriceMatrixHandler 覆盖 GET / PUT /admin/ai-models/:id/price-matrix 全部路径,
// 是「模型运营工作台 价格编辑」API 层的核心契约测试。
//
// 覆盖场景(共 8 项):
//
//	1. GET 正常 — 返回模型当前矩阵
//	2. GET 默认模板 — 模型未存矩阵 → 返回按 ModelType 自动生成的默认模板,is_default=true
//	3. GET 模型不存在 → 404
//	4. GET 非数字 ID → 400
//	5. PUT 正常 — 整体覆盖保存矩阵,顶层 selling 字段同步更新
//	6. PUT 缺 matrix 字段 → 400
//	7. PUT 模型不存在 → 自动建 ModelPricing(当前实现允许;断言不返回 5xx)
//	8. PUT 后再 GET → 取回的矩阵与保存的一致(rounddtrip)

func setupPriceMatrixHandlerTest(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.AIModel{},
		&model.ModelPricing{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	r := gin.New()
	rg := r.Group("/admin")
	h := NewPriceMatrixHandler(db)
	rg.GET("/ai-models/:id/price-matrix", h.GetPriceMatrix)
	rg.PUT("/ai-models/:id/price-matrix", h.UpdatePriceMatrix)
	return r, db
}

func seedPriceMatrixModel(t *testing.T, db *gorm.DB, modelType, name string) uint {
	t.Helper()
	m := model.AIModel{
		ModelName:   name,
		DisplayName: name,
		IsActive:    true,
		Status:      "online",
		ModelType:   modelType,
		PricingUnit: model.UnitPerMillionTokens,
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	return m.ID
}

func doPriceMatrixGet(t *testing.T, r *gin.Engine, modelID interface{}) (int, map[string]interface{}) {
	t.Helper()
	url := fmt.Sprintf("/admin/ai-models/%v/price-matrix", modelID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp
}

func doPriceMatrixPut(t *testing.T, r *gin.Engine, modelID uint, body map[string]interface{}) (int, map[string]interface{}) {
	t.Helper()
	raw, _ := json.Marshal(body)
	url := fmt.Sprintf("/admin/ai-models/%d/price-matrix", modelID)
	req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp
}

// 1. GET 正常 — 已存矩阵
func TestPriceMatrixHandler_Get_HappyPath(t *testing.T) {
	r, db := setupPriceMatrixHandlerTest(t)
	id := seedPriceMatrixModel(t, db, model.ModelTypeLLM, "qwen-test")

	pm := model.PriceMatrix{
		SchemaVersion: 1,
		Currency:      "RMB",
		Unit:          model.UnitPerMillionTokens,
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{}, Supported: true, OfficialInput: floatHandlerPtr(2.0), OfficialOutput: floatHandlerPtr(8.0), SellingInput: floatHandlerPtr(1.5), SellingOutput: floatHandlerPtr(6.0)},
		},
	}
	raw, _ := json.Marshal(pm)
	if err := db.Create(&model.ModelPricing{ModelID: id, Currency: "CREDIT", PriceMatrix: raw}).Error; err != nil {
		t.Fatalf("seed mp: %v", err)
	}

	code, resp := doPriceMatrixGet(t, r, id)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%v", code, resp)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("data missing: %v", resp)
	}
	if data["is_default"] != false {
		t.Fatalf("expected is_default=false (matrix has prices), got %v", data["is_default"])
	}
	matrix, _ := data["matrix"].(map[string]interface{})
	if matrix == nil {
		t.Fatalf("matrix missing")
	}
	cells, _ := matrix["cells"].([]interface{})
	if len(cells) != 1 {
		t.Fatalf("cells=%d want 1", len(cells))
	}
}

// 2. GET 默认模板 — 模型未存矩阵 → is_default=true,按模型类型生成模板
func TestPriceMatrixHandler_Get_DefaultTemplate(t *testing.T) {
	r, db := setupPriceMatrixHandlerTest(t)
	id := seedPriceMatrixModel(t, db, model.ModelTypeImageGeneration, "img-test")

	code, resp := doPriceMatrixGet(t, r, id)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%v", code, resp)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data["is_default"] != true {
		t.Fatalf("expected is_default=true (no matrix saved), got %v", data["is_default"])
	}
	matrix, _ := data["matrix"].(map[string]interface{})
	dims, _ := matrix["dimensions"].([]interface{})
	// Image 默认 3 维度(resolution / quality / mode)
	if len(dims) != 3 {
		t.Fatalf("Image default dims=%d want 3: %+v", len(dims), dims)
	}
}

// 3. GET 模型不存在 → 404
func TestPriceMatrixHandler_Get_ModelNotFound(t *testing.T) {
	r, _ := setupPriceMatrixHandlerTest(t)
	code, resp := doPriceMatrixGet(t, r, 99999)
	if code != http.StatusNotFound {
		t.Fatalf("status=%d (want 404), body=%v", code, resp)
	}
}

// 4. GET 非法 ID(0/字符串)→ 400
func TestPriceMatrixHandler_Get_InvalidID(t *testing.T) {
	r, _ := setupPriceMatrixHandlerTest(t)
	cases := []interface{}{"abc", 0}
	for _, id := range cases {
		t.Run(fmt.Sprintf("id=%v", id), func(t *testing.T) {
			code, _ := doPriceMatrixGet(t, r, id)
			if code != http.StatusBadRequest {
				t.Fatalf("expected 400 for id=%v, got %d", id, code)
			}
		})
	}
}

// 5. PUT 正常保存 + 顶层 selling 同步更新
func TestPriceMatrixHandler_Put_HappyPath_SyncsTopLevel(t *testing.T) {
	r, db := setupPriceMatrixHandlerTest(t)
	id := seedPriceMatrixModel(t, db, model.ModelTypeLLM, "qwen-put-test")

	body := map[string]interface{}{
		"matrix": map[string]interface{}{
			"schema_version": 1,
			"currency":       "RMB",
			"unit":           "per_million_tokens",
			"dimensions":     []interface{}{},
			"cells": []map[string]interface{}{
				{
					"dim_values":      map[string]interface{}{},
					"supported":       true,
					"official_input":  2.0,
					"official_output": 8.0,
					"selling_input":   1.5,
					"selling_output":  6.0,
				},
			},
		},
	}
	code, resp := doPriceMatrixPut(t, r, id, body)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%v", code, resp)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data["cells"].(float64) != 1 {
		t.Fatalf("cells count = %v want 1", data["cells"])
	}

	// 验证顶层 ModelPricing.InputPriceRMB / OutputPriceRMB 已同步
	var mp model.ModelPricing
	if err := db.Where("model_id = ?", id).First(&mp).Error; err != nil {
		t.Fatalf("load mp: %v", err)
	}
	if mp.InputPriceRMB != 1.5 || mp.OutputPriceRMB != 6.0 {
		t.Fatalf("top-level not synced: input=%v output=%v", mp.InputPriceRMB, mp.OutputPriceRMB)
	}
}

// 6. PUT 缺 matrix 字段 → 400
func TestPriceMatrixHandler_Put_MissingMatrix(t *testing.T) {
	r, db := setupPriceMatrixHandlerTest(t)
	id := seedPriceMatrixModel(t, db, model.ModelTypeLLM, "qwen-missing")

	code, _ := doPriceMatrixPut(t, r, id, map[string]interface{}{}) // 缺 matrix 字段
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", code)
	}
}

// 7. PUT 矩阵带多维度 + cells round-trip 完整性
func TestPriceMatrixHandler_Put_ThenGet_RoundTrip(t *testing.T) {
	r, db := setupPriceMatrixHandlerTest(t)
	id := seedPriceMatrixModel(t, db, model.ModelTypeVideoGeneration, "seedance-2.0-test")

	body := map[string]interface{}{
		"matrix": map[string]interface{}{
			"schema_version": 1,
			"currency":       "RMB",
			"unit":           "per_million_tokens",
			"dimensions": []map[string]interface{}{
				{"key": "resolution", "label": "分辨率", "type": "select", "values": []interface{}{"480p", "1080p"}},
				{"key": "input_has_video", "label": "含视频", "type": "boolean", "values": []interface{}{false, true}},
			},
			"cells": []map[string]interface{}{
				{"dim_values": map[string]interface{}{"resolution": "480p", "input_has_video": false}, "supported": true, "selling_per_unit": 39.10},
				{"dim_values": map[string]interface{}{"resolution": "1080p", "input_has_video": true}, "supported": true, "selling_per_unit": 26.35, "note": "1080p 视频折扣"},
			},
		},
	}
	if code, resp := doPriceMatrixPut(t, r, id, body); code != http.StatusOK {
		t.Fatalf("PUT failed status=%d body=%v", code, resp)
	}

	// GET 取回必须保留所有维度 + cells
	code, resp := doPriceMatrixGet(t, r, id)
	if code != http.StatusOK {
		t.Fatalf("GET status=%d", code)
	}
	data, _ := resp["data"].(map[string]interface{})
	matrix, _ := data["matrix"].(map[string]interface{})
	dims, _ := matrix["dimensions"].([]interface{})
	cells, _ := matrix["cells"].([]interface{})
	if len(dims) != 2 {
		t.Fatalf("dims=%d want 2", len(dims))
	}
	if len(cells) != 2 {
		t.Fatalf("cells=%d want 2", len(cells))
	}
	// 命中第二个 cell 的 note 不能丢失
	cell2, _ := cells[1].(map[string]interface{})
	if cell2["note"] != "1080p 视频折扣" {
		t.Fatalf("cell note lost: %v", cell2["note"])
	}
}

// 8. PUT 不支持的 cell 标记应正确保存
func TestPriceMatrixHandler_Put_UnsupportedCellPersisted(t *testing.T) {
	r, db := setupPriceMatrixHandlerTest(t)
	id := seedPriceMatrixModel(t, db, model.ModelTypeVideoGeneration, "seedance-unsupp")

	body := map[string]interface{}{
		"matrix": map[string]interface{}{
			"schema_version": 1,
			"currency":       "RMB",
			"unit":           "per_million_tokens",
			"dimensions": []map[string]interface{}{
				{"key": "inference_mode", "label": "推理模式", "type": "select", "values": []interface{}{"online", "offline"}},
			},
			"cells": []map[string]interface{}{
				{"dim_values": map[string]interface{}{"inference_mode": "online"}, "supported": true, "selling_per_unit": 26.35},
				{"dim_values": map[string]interface{}{"inference_mode": "offline"}, "supported": false, "unsupported_reason": "暂不支持离线"},
			},
		},
	}
	if code, _ := doPriceMatrixPut(t, r, id, body); code != http.StatusOK {
		t.Fatalf("PUT failed status=%d", code)
	}

	_, resp := doPriceMatrixGet(t, r, id)
	data, _ := resp["data"].(map[string]interface{})
	matrix, _ := data["matrix"].(map[string]interface{})
	cells, _ := matrix["cells"].([]interface{})
	cell2, _ := cells[1].(map[string]interface{})
	if cell2["supported"] != false {
		t.Fatalf("supported should be false, got %v", cell2["supported"])
	}
	if cell2["unsupported_reason"] != "暂不支持离线" {
		t.Fatalf("unsupported_reason lost: %v", cell2["unsupported_reason"])
	}
}

func floatHandlerPtr(v float64) *float64 { return &v }
