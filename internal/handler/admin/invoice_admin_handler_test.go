package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
	invoicesvc "tokenhub-server/internal/service/invoice"
)

func setupInvoiceAdminHandlerTest(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", name)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Payment{}, &model.InvoiceRequest{}, &model.InvoiceTitle{}); err != nil {
		t.Fatalf("migrate invoice tables: %v", err)
	}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userId", uint(99))
		c.Next()
	})
	NewInvoiceAdminHandler(invoicesvc.New(db)).Register(r.Group("/admin"))
	return r, db
}

func invoiceAdminRequest(t *testing.T, r *gin.Engine, method, path string, body interface{}) (int, map[string]interface{}) {
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

func seedAdminInvoiceRequest(t *testing.T, db *gorm.DB, status string) model.InvoiceRequest {
	t.Helper()
	req := model.InvoiceRequest{
		TenantID:       1,
		UserID:         7,
		Region:         model.InvoiceRegionCN,
		InvoiceType:    model.InvoiceTypePersonal,
		Title:          "Alice",
		Email:          "alice@example.com",
		OrderIDs:       model.JSON([]byte(`[1]`)),
		AmountRMB:      100,
		AmountOriginal: 100,
		Currency:       "CNY",
		Status:         status,
	}
	if err := db.Create(&req).Error; err != nil {
		t.Fatalf("seed invoice request: %v", err)
	}
	return req
}

func TestInvoiceAdminHandlerListAndValidationBoundaries(t *testing.T) {
	r, db := setupInvoiceAdminHandlerTest(t)
	for i := 0; i < 25; i++ {
		seedAdminInvoiceRequest(t, db, model.InvoiceStatusPending)
	}

	code, resp := invoiceAdminRequest(t, r, http.MethodGet, "/admin/invoices?page=0&page_size=999", nil)
	if code != http.StatusOK {
		t.Fatalf("list status=%d resp=%v", code, resp)
	}
	page := resp["data"].(map[string]interface{})
	if page["page"].(float64) != 1 || page["page_size"].(float64) != 20 || page["total"].(float64) != 25 {
		t.Fatalf("list pagination not normalized: %#v", page)
	}
	if got := len(page["list"].([]interface{})); got != 20 {
		t.Fatalf("list len=%d, want 20", got)
	}

	code, _ = invoiceAdminRequest(t, r, http.MethodGet, "/admin/invoices/not-a-number", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("invalid get id status=%d, want 400", code)
	}
	code, _ = invoiceAdminRequest(t, r, http.MethodPost, "/admin/invoices/not-a-number/approve", map[string]interface{}{})
	if code != http.StatusBadRequest {
		t.Fatalf("invalid approve id status=%d, want 400", code)
	}
}

func TestInvoiceAdminHandlerRejectAndUploadPDFValidation(t *testing.T) {
	r, db := setupInvoiceAdminHandlerTest(t)
	req := seedAdminInvoiceRequest(t, db, model.InvoiceStatusPending)

	code, _ := invoiceAdminRequest(t, r, http.MethodPost, fmt.Sprintf("/admin/invoices/%d/reject", req.ID), map[string]interface{}{
		"reason": "",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("blank reject reason status=%d, want 400", code)
	}
	code, _ = invoiceAdminRequest(t, r, http.MethodPost, fmt.Sprintf("/admin/invoices/%d/upload-pdf", req.ID), map[string]interface{}{
		"pdf_url": "not-a-url",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("invalid pdf url status=%d, want 400", code)
	}
	code, resp := invoiceAdminRequest(t, r, http.MethodPost, fmt.Sprintf("/admin/invoices/%d/reject", req.ID), map[string]interface{}{
		"reason": "missing billing info",
	})
	if code != http.StatusOK {
		t.Fatalf("reject status=%d resp=%v", code, resp)
	}
}
