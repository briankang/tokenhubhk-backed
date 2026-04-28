package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	modelaliassvc "tokenhub-server/internal/service/modelalias"
)

func setupModelAliasHandlerTest(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Supplier{}, &model.ModelCategory{}, &model.AIModel{}, &model.ModelAlias{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	r := gin.New()
	NewModelAliasHandler(modelaliassvc.NewService(db)).Register(r.Group("/admin"))
	return r, db
}

func aliasRequest(t *testing.T, r *gin.Engine, method, path string, body interface{}) (int, map[string]interface{}) {
	t.Helper()
	buf := bytes.NewBuffer(nil)
	if body != nil {
		data, _ := json.Marshal(body)
		buf = bytes.NewBuffer(data)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var payload map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &payload)
	return w.Code, payload
}

func TestModelAliasHandlerCRUD(t *testing.T) {
	r, db := setupModelAliasHandlerTest(t)
	sup := model.Supplier{Name: "Aliyun", Code: "aliyun_dashscope", IsActive: true, Status: "active", AccessType: "api"}
	if err := db.Create(&sup).Error; err != nil {
		t.Fatalf("create supplier: %v", err)
	}

	code, resp := aliasRequest(t, r, http.MethodPost, "/admin/model-aliases", map[string]interface{}{
		"alias_name":        "qwen3.6-plus",
		"target_model_name": "qwen3.6-plus-2026-04-02",
		"supplier_id":       sup.ID,
		"alias_type":        model.ModelAliasTypeStable,
		"source":            model.ModelAliasSourceManual,
		"is_public":         true,
		"is_active":         true,
	})
	if code != http.StatusOK {
		t.Fatalf("create code=%d resp=%v", code, resp)
	}

	code, resp = aliasRequest(t, r, http.MethodGet, "/admin/model-aliases?model=qwen3.6-plus", nil)
	if code != http.StatusOK {
		t.Fatalf("list code=%d resp=%v", code, resp)
	}
	data := resp["data"].(map[string]interface{})
	list := data["list"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("list len=%d resp=%v", len(list), resp)
	}

	id := int(list[0].(map[string]interface{})["id"].(float64))
	code, resp = aliasRequest(t, r, http.MethodPut, "/admin/model-aliases/"+strconv.Itoa(id), map[string]interface{}{
		"notes": "manual review",
	})
	if code != http.StatusOK {
		t.Fatalf("update code=%d resp=%v", code, resp)
	}

	code, resp = aliasRequest(t, r, http.MethodDelete, "/admin/model-aliases/"+strconv.Itoa(id), nil)
	if code != http.StatusOK {
		t.Fatalf("delete code=%d resp=%v", code, resp)
	}
}
