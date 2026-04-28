package invoice

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
)

func newInvoiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
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
	return db
}

func validSubmitInput(paymentID uint) SubmitInput {
	return SubmitInput{
		UserID:      7,
		TenantID:    1,
		PaymentID:   paymentID,
		Region:      model.InvoiceRegionCN,
		InvoiceType: model.InvoiceTypePersonal,
		Title:       "Alice",
		Email:       "alice@example.com",
	}
}

func createInvoicePayment(t *testing.T, db *gorm.DB, pay model.Payment) model.Payment {
	t.Helper()
	if pay.UserID == 0 {
		pay.UserID = 7
	}
	if pay.TenantID == 0 {
		pay.TenantID = 1
	}
	if pay.Gateway == "" {
		pay.Gateway = "stripe"
	}
	if pay.OrderNo == nil || *pay.OrderNo == "" {
		orderNo := fmt.Sprintf("order-%d", time.Now().UnixNano())
		pay.OrderNo = &orderNo
	}
	if pay.OriginalCurrency == "" {
		pay.OriginalCurrency = "CNY"
	}
	if pay.Status == "" {
		pay.Status = model.PaymentStatusCompleted
	}
	if pay.Amount == 0 {
		pay.Amount = 100
	}
	if pay.RMBAmount == 0 {
		pay.RMBAmount = pay.Amount
	}
	if err := db.Create(&pay).Error; err != nil {
		t.Fatalf("create payment: %v", err)
	}
	return pay
}

func TestInvoiceSubmitValidationBoundaries(t *testing.T) {
	db := newInvoiceTestDB(t)
	svc := New(db)
	ctx := context.Background()

	cases := []struct {
		name   string
		mutate func(*SubmitInput)
		want   string
	}{
		{"missing user", func(in *SubmitInput) { in.UserID = 0 }, "invalid user"},
		{"missing payment", func(in *SubmitInput) { in.PaymentID = 0 }, "invalid user"},
		{"invalid region", func(in *SubmitInput) { in.Region = "EU" }, "invalid region"},
		{"invalid invoice type", func(in *SubmitInput) { in.InvoiceType = "receipt" }, "invalid invoice_type"},
		{"blank title", func(in *SubmitInput) { in.Title = "  " }, "title is required"},
		{"blank email", func(in *SubmitInput) { in.Email = "  " }, "email is required"},
		{"company missing tax id", func(in *SubmitInput) { in.InvoiceType = model.InvoiceTypeCompany }, "tax_id is required"},
		{"cn vat missing bank fields", func(in *SubmitInput) {
			in.InvoiceType = model.InvoiceTypeVATInvoice
			in.TaxID = "91310000MA1"
		}, "vat_invoice requires"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validSubmitInput(99)
			tc.mutate(&in)
			_, err := svc.Submit(ctx, in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Submit error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestInvoiceSubmitPaymentBoundaries(t *testing.T) {
	db := newInvoiceTestDB(t)
	svc := New(db)
	ctx := context.Background()

	pending := createInvoicePayment(t, db, model.Payment{Status: model.PaymentStatusPending})
	if _, err := svc.Submit(ctx, validSubmitInput(pending.ID)); err == nil || !strings.Contains(err.Error(), "only completed") {
		t.Fatalf("pending payment error = %v, want only completed", err)
	}

	old := createInvoicePayment(t, db, model.Payment{
		BaseModel: model.BaseModel{CreatedAt: time.Now().Add(-181 * 24 * time.Hour)},
	})
	if _, err := svc.Submit(ctx, validSubmitInput(old.ID)); err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("old payment error = %v, want too old", err)
	}

	refunded := createInvoicePayment(t, db, model.Payment{Amount: 100, RMBAmount: 100, RefundedAmount: 100})
	if _, err := svc.Submit(ctx, validSubmitInput(refunded.ID)); err == nil || !strings.Contains(err.Error(), "no invoiceable amount") {
		t.Fatalf("fully refunded payment error = %v, want no invoiceable amount", err)
	}

	already := createInvoicePayment(t, db, model.Payment{InvoiceStatus: model.PaymentInvoiceStatusRequested})
	if _, err := svc.Submit(ctx, validSubmitInput(already.ID)); err == nil || !strings.Contains(err.Error(), "already requested") {
		t.Fatalf("already requested payment error = %v, want already requested", err)
	}

	partial := createInvoicePayment(t, db, model.Payment{
		Status:         model.PaymentStatusPartialRefunded,
		Amount:         100,
		RMBAmount:      100,
		RefundedAmount: 20,
	})
	req, err := svc.Submit(ctx, validSubmitInput(partial.ID))
	if err != nil {
		t.Fatalf("Submit partial refunded payment: %v", err)
	}
	if req.AmountRMB != 80 {
		t.Fatalf("invoice amount = %v, want 80", req.AmountRMB)
	}
	var updated model.Payment
	if err := db.First(&updated, partial.ID).Error; err != nil {
		t.Fatalf("load updated payment: %v", err)
	}
	if updated.InvoiceStatus != model.PaymentInvoiceStatusRequested || updated.InvoiceRequestID == nil || *updated.InvoiceRequestID != req.ID {
		t.Fatalf("payment invoice link not updated: status=%s request=%v", updated.InvoiceStatus, updated.InvoiceRequestID)
	}
}

func TestInvoiceAdminTransitionsAndPaymentStatus(t *testing.T) {
	db := newInvoiceTestDB(t)
	svc := New(db)
	ctx := context.Background()

	pay := createInvoicePayment(t, db, model.Payment{})
	req, err := svc.Submit(ctx, validSubmitInput(pay.ID))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := svc.Reject(ctx, req.ID, 99, " "); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("blank reject reason error = %v, want reason", err)
	}
	if err := svc.UploadPDF(ctx, req.ID, " "); err == nil || !strings.Contains(err.Error(), "pdf_url") {
		t.Fatalf("blank pdf url error = %v, want pdf_url", err)
	}
	if err := svc.Reject(ctx, req.ID, 99, "missing billing info"); err != nil {
		t.Fatalf("Reject pending request: %v", err)
	}
	var rejected model.InvoiceRequest
	if err := db.First(&rejected, req.ID).Error; err != nil {
		t.Fatalf("load rejected request: %v", err)
	}
	if rejected.Status != model.InvoiceStatusRejected || rejected.RejectReason != "missing billing info" {
		t.Fatalf("request not rejected correctly: %#v", rejected)
	}
	var resetPay model.Payment
	if err := db.First(&resetPay, pay.ID).Error; err != nil {
		t.Fatalf("load reset payment: %v", err)
	}
	if resetPay.InvoiceStatus != model.PaymentInvoiceStatusNone || resetPay.InvoiceRequestID != nil {
		t.Fatalf("payment invoice status not reset: status=%s request=%v", resetPay.InvoiceStatus, resetPay.InvoiceRequestID)
	}

	pay2 := createInvoicePayment(t, db, model.Payment{})
	req2, err := svc.Submit(ctx, validSubmitInput(pay2.ID))
	if err != nil {
		t.Fatalf("Submit second request: %v", err)
	}
	if err := svc.UploadPDF(ctx, req2.ID, "https://cdn.example.com/invoice.pdf"); err != nil {
		t.Fatalf("UploadPDF: %v", err)
	}
	var issued model.InvoiceRequest
	if err := db.First(&issued, req2.ID).Error; err != nil {
		t.Fatalf("load issued request: %v", err)
	}
	if issued.Status != model.InvoiceStatusIssued || issued.PDFURL == "" || issued.IssuedAt == nil {
		t.Fatalf("request not issued correctly: %#v", issued)
	}
	var issuedPay model.Payment
	if err := db.First(&issuedPay, pay2.ID).Error; err != nil {
		t.Fatalf("load issued payment: %v", err)
	}
	if issuedPay.InvoiceStatus != model.PaymentInvoiceStatusIssued {
		t.Fatalf("payment invoice status = %s, want issued", issuedPay.InvoiceStatus)
	}
}

func TestInvoiceTitleOwnershipAndDefaultBoundaries(t *testing.T) {
	db := newInvoiceTestDB(t)
	svc := New(db)
	ctx := context.Background()

	base := TitleInput{
		UserID:      7,
		TenantID:    1,
		Region:      model.InvoiceRegionCN,
		InvoiceType: model.InvoiceTypePersonal,
		Title:       "Personal",
		IsDefault:   true,
	}
	first, err := svc.CreateTitle(ctx, base)
	if err != nil {
		t.Fatalf("CreateTitle first: %v", err)
	}
	base.Title = "Personal New"
	second, err := svc.CreateTitle(ctx, base)
	if err != nil {
		t.Fatalf("CreateTitle second: %v", err)
	}
	var reloadedFirst model.InvoiceTitle
	if err := db.First(&reloadedFirst, first.ID).Error; err != nil {
		t.Fatalf("load first title: %v", err)
	}
	if reloadedFirst.IsDefault {
		t.Fatalf("first title should be unset as default after second default title")
	}
	list, err := svc.ListTitles(ctx, 7)
	if err != nil {
		t.Fatalf("ListTitles: %v", err)
	}
	if len(list) != 2 || list[0].ID != second.ID || !list[0].IsDefault {
		t.Fatalf("default title should be first in list: %#v", list)
	}

	companyWithoutTax := base
	companyWithoutTax.InvoiceType = model.InvoiceTypeCompany
	companyWithoutTax.TaxID = ""
	if _, err := svc.CreateTitle(ctx, companyWithoutTax); err == nil || !strings.Contains(err.Error(), "tax_id") {
		t.Fatalf("company title without tax id error = %v, want tax_id", err)
	}
	if err := svc.UpdateTitle(ctx, second.ID, 8, base); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("cross-user update error = %v, want forbidden", err)
	}
	if err := svc.DeleteTitle(ctx, second.ID, 8); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("cross-user delete error = %v, want forbidden", err)
	}
}
