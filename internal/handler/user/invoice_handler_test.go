package user

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
	invoicesvc "tokenhub-server/internal/service/invoice"
)

func setupUserInvoiceHandlerTest(t *testing.T) (*gin.Engine, *gorm.DB) {
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
		c.Set("userId", uint(7))
		c.Set("tenantId", uint(1))
		c.Next()
	})
	h := NewInvoiceHandler(invoicesvc.New(db))
	g := r.Group("/user")
	g.POST("/invoices", h.Submit)
	g.GET("/invoices", h.List)
	g.GET("/invoices/:id", h.Get)
	g.GET("/invoice-titles", h.ListTitles)
	g.POST("/invoice-titles", h.CreateTitle)
	g.PUT("/invoice-titles/:id", h.UpdateTitle)
	g.DELETE("/invoice-titles/:id", h.DeleteTitle)
	return r, db
}

func userInvoiceRequest(t *testing.T, r *gin.Engine, method, path string, body interface{}) (int, map[string]interface{}) {
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

func seedInvoiceHandlerPayment(t *testing.T, db *gorm.DB, userID uint) model.Payment {
	t.Helper()
	orderNo := fmt.Sprintf("inv-handler-%d", time.Now().UnixNano())
	pay := model.Payment{
		TenantID:          1,
		UserID:            userID,
		OrderNo:           &orderNo,
		Amount:            120,
		RMBAmount:         120,
		OriginalCurrency:  "CNY",
		Gateway:           "stripe",
		Status:            model.PaymentStatusCompleted,
		InvoiceStatus:     model.PaymentInvoiceStatusNone,
		InvoiceRequestID:  nil,
		ExchangeRate:      1,
		DisplayCurrency:   "CNY",
		DisplayAmountCNY:  120,
		ExchangeRateUsed:  1,
		RefundedAmount:    0,
		DisplayAmountUSD:  0,
		ProviderAccountID: nil,
	}
	if err := db.Create(&pay).Error; err != nil {
		t.Fatalf("seed payment: %v", err)
	}
	return pay
}

func TestUserInvoiceHandlerSubmitListAndOwnershipBoundaries(t *testing.T) {
	r, db := setupUserInvoiceHandlerTest(t)
	pay := seedInvoiceHandlerPayment(t, db, 7)

	code, resp := userInvoiceRequest(t, r, http.MethodPost, "/user/invoices", map[string]interface{}{
		"payment_id":   pay.ID,
		"region":       model.InvoiceRegionCN,
		"invoice_type": model.InvoiceTypePersonal,
		"title":        "Alice",
		"email":        "not-an-email",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("invalid email status=%d resp=%v, want 400", code, resp)
	}

	code, resp = userInvoiceRequest(t, r, http.MethodPost, "/user/invoices", map[string]interface{}{
		"payment_id":   pay.ID,
		"region":       model.InvoiceRegionCN,
		"invoice_type": model.InvoiceTypePersonal,
		"title":        "Alice",
		"email":        "alice@example.com",
	})
	if code != http.StatusOK {
		t.Fatalf("submit status=%d resp=%v", code, resp)
	}
	invoiceID := uint(resp["data"].(map[string]interface{})["id"].(float64))

	code, resp = userInvoiceRequest(t, r, http.MethodGet, "/user/invoices?page=0&page_size=999", nil)
	if code != http.StatusOK {
		t.Fatalf("list status=%d resp=%v", code, resp)
	}
	page := resp["data"].(map[string]interface{})
	if page["page"].(float64) != 1 || page["page_size"].(float64) != 20 || page["total"].(float64) != 1 {
		t.Fatalf("list pagination not normalized: %#v", page)
	}

	code, resp = userInvoiceRequest(t, r, http.MethodGet, fmt.Sprintf("/user/invoices/%d", invoiceID), nil)
	if code != http.StatusOK {
		t.Fatalf("get own invoice status=%d resp=%v", code, resp)
	}
	otherPay := seedInvoiceHandlerPayment(t, db, 8)
	otherReq, err := invoicesvc.New(db).Submit(context.Background(), invoicesvc.SubmitInput{
		UserID:      8,
		TenantID:    1,
		PaymentID:   otherPay.ID,
		Region:      model.InvoiceRegionCN,
		InvoiceType: model.InvoiceTypePersonal,
		Title:       "Other",
		Email:       "other@example.com",
	})
	if err != nil {
		t.Fatalf("seed other invoice: %v", err)
	}
	code, _ = userInvoiceRequest(t, r, http.MethodGet, fmt.Sprintf("/user/invoices/%d", otherReq.ID), nil)
	if code != http.StatusForbidden {
		t.Fatalf("get other user invoice status=%d, want 403", code)
	}
}

func TestUserInvoiceTitleHandlerValidationAndOwnership(t *testing.T) {
	r, db := setupUserInvoiceHandlerTest(t)

	code, _ := userInvoiceRequest(t, r, http.MethodPost, "/user/invoice-titles", map[string]interface{}{
		"region":       model.InvoiceRegionCN,
		"invoice_type": model.InvoiceTypeCompany,
		"title":        "Company",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("company title without tax id status=%d, want 400", code)
	}
	code, resp := userInvoiceRequest(t, r, http.MethodPost, "/user/invoice-titles", map[string]interface{}{
		"region":       model.InvoiceRegionCN,
		"invoice_type": model.InvoiceTypePersonal,
		"title":        "Alice",
		"email":        "alice@example.com",
		"is_default":   true,
	})
	if code != http.StatusOK {
		t.Fatalf("create title status=%d resp=%v", code, resp)
	}
	titleID := uint(resp["data"].(map[string]interface{})["id"].(float64))

	code, _ = userInvoiceRequest(t, r, http.MethodPut, "/user/invoice-titles/not-a-number", map[string]interface{}{})
	if code != http.StatusBadRequest {
		t.Fatalf("invalid update id status=%d, want 400", code)
	}

	otherTitle := model.InvoiceTitle{
		UserID:      8,
		TenantID:    1,
		Region:      model.InvoiceRegionCN,
		InvoiceType: model.InvoiceTypePersonal,
		Title:       "Other",
	}
	if err := db.Create(&otherTitle).Error; err != nil {
		t.Fatalf("seed other title: %v", err)
	}
	code, _ = userInvoiceRequest(t, r, http.MethodDelete, fmt.Sprintf("/user/invoice-titles/%d", otherTitle.ID), nil)
	if code != http.StatusBadRequest {
		t.Fatalf("delete other title status=%d, want 400", code)
	}
	code, _ = userInvoiceRequest(t, r, http.MethodDelete, fmt.Sprintf("/user/invoice-titles/%d", titleID), nil)
	if code != http.StatusOK {
		t.Fatalf("delete own title status=%d, want 200", code)
	}
}
