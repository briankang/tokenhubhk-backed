package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func waitForPaymentEventLogCount(t *testing.T, db *gorm.DB, eventType string, want int64) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		var cnt int64
		db.Model(&model.PaymentEventLog{}).Where("event_type = ?", eventType).Count(&cnt)
		if cnt == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %d event log(s) for %s, got %d", want, eventType, cnt)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// fakeGatewayInvoker mocks gateway calls.
type fakeGatewayInvoker struct {
	shouldFail bool
	called     int
	lastAmount float64
}

func (f *fakeGatewayInvoker) InvokeGatewayRefund(ctx context.Context, payment *model.Payment, amount float64, reason string) (*RefundResult, error) {
	f.called++
	f.lastAmount = amount
	if f.shouldFail {
		return nil, errors.New("gateway failure")
	}
	return &RefundResult{
		OrderNo:         "ORD123",
		RefundNo:        "REF1",
		Amount:          amount,
		Status:          "success",
		GatewayRefundID: "GW_REF_001",
	}, nil
}

func setupRefundTest(t *testing.T) (*RefundService, *gorm.DB, *goredis.Client, *miniredis.Miniredis, *fakeGatewayInvoker) {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", dbName)), &gorm.Config{})
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	// SQLite does not enforce FK; migrate only the tables needed
	err = db.AutoMigrate(
		&model.Payment{},
		&model.PaymentRefundRequest{},
		&model.PaymentEventLog{},
		&model.UserBalance{},
		&model.BalanceRecord{},
	)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})

	el := NewEventLogger(db)
	svc := NewRefundService(db, rdb, el)
	inv := &fakeGatewayInvoker{}
	svc.SetGatewayInvoker(inv)
	return svc, db, rdb, mr, inv
}

// 鏉堝懎濮敍姘灡瀵よ桨绔寸粭鏂垮嚒鐎瑰本鍨氶弨顖欑帛
func createCompletedPayment(t *testing.T, db *gorm.DB, userID uint, amount float64) *model.Payment {
	t.Helper()
	meta, _ := json.Marshal(map[string]interface{}{"order_no": "ORD123"})
	p := &model.Payment{
		UserID:           userID,
		TenantID:         1,
		Amount:           amount,
		Currency:         "CNY",
		OriginalCurrency: "CNY",
		Gateway:          "wechat",
		Status:           model.PaymentStatusCompleted,
		RMBAmount:        amount,
		CreditAmount:     int64(amount * 10000),
		Metadata:         meta,
	}
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("create payment: %v", err)
	}
	return p
}

// createUserBalance creates a user balance for refund tests.
func createUserBalance(t *testing.T, db *gorm.DB, userID uint, balance int64) {
	t.Helper()
	ub := &model.UserBalance{
		UserID:   userID,
		TenantID: 1,
		Balance:  balance,
		Currency: "CREDIT",
	}
	if err := db.Create(ub).Error; err != nil {
		t.Fatalf("create balance: %v", err)
	}
}

func TestRefundService_SubmitUserRequest_Success(t *testing.T) {
	svc, db, _, _, _ := setupRefundTest(t)
	p := createCompletedPayment(t, db, 1, 100.0)

	req, err := svc.SubmitUserRequest(context.Background(), SubmitUserRequestInput{
		UserID:    1,
		PaymentID: uint64(p.ID),
		AmountRMB: 50.0,
		Reason:    "product not as described",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if req.Status != model.RefundStatusPending {
		t.Errorf("status = %s, want pending", req.Status)
	}
	waitForPaymentEventLogCount(t, db, model.EventRefundRequested, 1)
}

func TestRefundService_SubmitUserRequest_NotOwner(t *testing.T) {
	svc, db, _, _, _ := setupRefundTest(t)
	p := createCompletedPayment(t, db, 1, 100.0)

	_, err := svc.SubmitUserRequest(context.Background(), SubmitUserRequestInput{
		UserID:    999, // 闂?owner
		PaymentID: uint64(p.ID),
		AmountRMB: 50.0,
		Reason:    "x should be rejected!!",
	})
	if err == nil {
		t.Errorf("expected owner check error")
	}
}

func TestRefundService_SubmitUserRequest_NotCompleted(t *testing.T) {
	svc, db, _, _, _ := setupRefundTest(t)
	p := createCompletedPayment(t, db, 1, 100.0)
	db.Model(p).Update("status", model.PaymentStatusPending)

	_, err := svc.SubmitUserRequest(context.Background(), SubmitUserRequestInput{
		UserID:    1,
		PaymentID: uint64(p.ID),
		AmountRMB: 50,
		Reason:    "need refund reason ten+",
	})
	if err == nil {
		t.Errorf("expected status check error")
	}
}

func TestRefundService_SubmitUserRequest_ExceedAmount(t *testing.T) {
	svc, db, _, _, _ := setupRefundTest(t)
	p := createCompletedPayment(t, db, 1, 100.0)

	_, err := svc.SubmitUserRequest(context.Background(), SubmitUserRequestInput{
		UserID:    1,
		PaymentID: uint64(p.ID),
		AmountRMB: 200.0, // 鐡掑懘顤?		Reason:    "too much ten chars",
	})
	if err == nil {
		t.Errorf("expected amount check error")
	}
}

func TestRefundService_SubmitUserRequest_DuplicatePending(t *testing.T) {
	svc, db, _, _, _ := setupRefundTest(t)
	p := createCompletedPayment(t, db, 1, 100.0)

	// 閸忓牆鍨卞杞扮缁?pending
	_, err := svc.SubmitUserRequest(context.Background(), SubmitUserRequestInput{
		UserID:    1,
		PaymentID: uint64(p.ID),
		AmountRMB: 10,
		Reason:    "first request tenchars",
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// A second pending refund for the same payment should fail.
	_, err = svc.SubmitUserRequest(context.Background(), SubmitUserRequestInput{
		UserID:    1,
		PaymentID: uint64(p.ID),
		AmountRMB: 10,
		Reason:    "second request tenchars",
	})
	if err == nil {
		t.Errorf("expected duplicate pending error")
	}
}

func TestRefundService_RejectByAdmin_Success(t *testing.T) {
	svc, db, _, _, _ := setupRefundTest(t)
	p := createCompletedPayment(t, db, 1, 100.0)
	req, err := svc.SubmitUserRequest(context.Background(), SubmitUserRequestInput{
		UserID: 1, PaymentID: uint64(p.ID), AmountRMB: 50, Reason: "reason tencharshere",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	if err := svc.RejectByAdmin(context.Background(), req.ID, 999, "denied reason"); err != nil {
		t.Fatalf("reject: %v", err)
	}

	var r model.PaymentRefundRequest
	db.First(&r, req.ID)
	if r.Status != model.RefundStatusRejected {
		t.Errorf("status = %s, want rejected", r.Status)
	}
	if r.AdminID == nil || *r.AdminID != 999 {
		t.Errorf("admin id not set")
	}
}

func TestRefundService_ApproveByAdmin_AlreadyRejected(t *testing.T) {
	svc, db, _, _, _ := setupRefundTest(t)
	p := createCompletedPayment(t, db, 1, 100.0)
	req, _ := svc.SubmitUserRequest(context.Background(), SubmitUserRequestInput{
		UserID: 1, PaymentID: uint64(p.ID), AmountRMB: 50, Reason: "reason tencharshere",
	})
	_ = svc.RejectByAdmin(context.Background(), req.ID, 1, "reject")

	// Approving a rejected refund should fail.
	if err := svc.ApproveByAdmin(context.Background(), req.ID, 2, "approve"); err == nil {
		t.Errorf("expected status transition error")
	}
}

func TestRefundService_ListUserRequests(t *testing.T) {
	svc, db, _, _, _ := setupRefundTest(t)
	p := createCompletedPayment(t, db, 1, 100.0)
	_, _ = svc.SubmitUserRequest(context.Background(), SubmitUserRequestInput{
		UserID: 1, PaymentID: uint64(p.ID), AmountRMB: 10, Reason: "tenchars reason here",
	})

	list, total, err := svc.ListUserRequests(context.Background(), 1, 1, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Errorf("expected 1 record, got total=%d len=%d", total, len(list))
	}
}

func TestRefundService_BatchReject(t *testing.T) {
	svc, db, _, _, _ := setupRefundTest(t)
	var ids []uint64
	for i := 0; i < 3; i++ {
		p := createCompletedPayment(t, db, uint(i+1), 100)
		req, _ := svc.SubmitUserRequest(context.Background(), SubmitUserRequestInput{
			UserID: uint64(i + 1), PaymentID: uint64(p.ID), AmountRMB: 10, Reason: "reason tencharshere",
		})
		ids = append(ids, req.ID)
	}

	ok, failed, _ := svc.BatchReject(context.Background(), ids, 999, "bulk reject")
	if len(ok) != 3 || len(failed) != 0 {
		t.Errorf("ok=%d failed=%d", len(ok), len(failed))
	}
}

func TestRefundService_AmountZero_Rejected(t *testing.T) {
	svc, db, _, _, _ := setupRefundTest(t)
	p := createCompletedPayment(t, db, 1, 100.0)
	_, err := svc.SubmitUserRequest(context.Background(), SubmitUserRequestInput{
		UserID: 1, PaymentID: uint64(p.ID), AmountRMB: 0, Reason: "zero amt test",
	})
	if err == nil {
		t.Errorf("expected amount > 0 error")
	}
}

func TestRefundService_Reason_TooShort(t *testing.T) {
	svc, db, _, _, _ := setupRefundTest(t)
	p := createCompletedPayment(t, db, 1, 100.0)
	_, err := svc.SubmitUserRequest(context.Background(), SubmitUserRequestInput{
		UserID: 1, PaymentID: uint64(p.ID), AmountRMB: 10, Reason: "  ",
	})
	if err == nil {
		t.Errorf("expected empty reason error")
	}
}
