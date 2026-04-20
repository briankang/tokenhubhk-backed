package payment

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func setupELTest(t *testing.T) (*EventLogger, *gorm.DB) {
	t.Helper()
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	db.AutoMigrate(&model.PaymentEventLog{})
	return NewEventLogger(db), db
}

func TestEventLogger_LogSync_Success(t *testing.T) {
	el, db := setupELTest(t)
	pid := uint64(99)
	err := el.LogSync(context.Background(), PaymentEvent{
		PaymentID: &pid,
		OrderNo:   "ORD1",
		EventType: model.EventPaymentCreated,
		ActorType: model.ActorUser,
		Payload:   map[string]interface{}{"k": "v"},
		Success:   true,
	})
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	var count int64
	db.Model(&model.PaymentEventLog{}).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 log, got %d", count)
	}
}

func TestEventLogger_LogSync_WithError(t *testing.T) {
	el, db := setupELTest(t)
	err := el.LogSync(context.Background(), PaymentEvent{
		EventType: model.EventPaymentCallbackFailed,
		ActorType: model.ActorGateway,
		Success:   false,
		Err:       errors.New("signature invalid"),
	})
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	var row model.PaymentEventLog
	db.First(&row)
	if !strings.Contains(row.ErrorMsg, "signature invalid") {
		t.Errorf("error msg not stored: %s", row.ErrorMsg)
	}
}

func TestEventLogger_NilPayload_NoCrash(t *testing.T) {
	el, _ := setupELTest(t)
	err := el.LogSync(context.Background(), PaymentEvent{
		EventType: "test.event",
		ActorType: model.ActorSystem,
		Payload:   nil, // nil safe?
		Success:   true,
	})
	if err != nil {
		t.Fatalf("nil payload failed: %v", err)
	}
}

func TestEventLogger_LargePayload_Truncated(t *testing.T) {
	el, db := setupELTest(t)
	bigPayload := strings.Repeat("x", 20*1024) // 20KB
	_ = el.LogSync(context.Background(), PaymentEvent{
		EventType: "test.big",
		ActorType: model.ActorSystem,
		Payload:   map[string]string{"data": bigPayload},
		Success:   true,
	})
	var row model.PaymentEventLog
	db.First(&row)
	if len(row.PayloadJSON) > maxPayloadSize+50 {
		t.Errorf("payload not truncated: len=%d", len(row.PayloadJSON))
	}
}

func TestEventLogger_List_WithFilters(t *testing.T) {
	el, _ := setupELTest(t)
	ctx := context.Background()
	pid1 := uint64(1)
	_ = el.LogSync(ctx, PaymentEvent{PaymentID: &pid1, EventType: model.EventPaymentCreated, ActorType: model.ActorUser, Success: true})
	_ = el.LogSync(ctx, PaymentEvent{PaymentID: &pid1, EventType: model.EventPaymentCredited, ActorType: model.ActorSystem, Success: true})
	pid2 := uint64(2)
	_ = el.LogSync(ctx, PaymentEvent{PaymentID: &pid2, EventType: model.EventPaymentCreated, ActorType: model.ActorUser, Success: true})

	list, total, err := el.List(ctx, QueryFilters{PaymentID: &pid1, Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Errorf("expected 2 for pid1, got total=%d len=%d", total, len(list))
	}
}

func TestEventLogger_ListByPayment_Order(t *testing.T) {
	el, _ := setupELTest(t)
	ctx := context.Background()
	pid := uint64(10)
	_ = el.LogSync(ctx, PaymentEvent{PaymentID: &pid, EventType: "first", ActorType: "u", Success: true})
	time.Sleep(10 * time.Millisecond)
	_ = el.LogSync(ctx, PaymentEvent{PaymentID: &pid, EventType: "second", ActorType: "u", Success: true})

	list, _ := el.ListByPayment(ctx, pid)
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
	if list[0].EventType != "first" {
		t.Errorf("order wrong: %s", list[0].EventType)
	}
}

func TestEventLogger_Truncate(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want int // expected length
	}{
		{"hello", 10, 5},
		{"helloworld", 5, 5},
	}
	for _, c := range cases {
		got := truncate(c.in, c.max)
		if len(got) != c.want {
			t.Errorf("truncate(%q, %d) len=%d want=%d", c.in, c.max, len(got), c.want)
		}
	}
}

func TestEventLogger_LogExchangeEvent(t *testing.T) {
	el, db := setupELTest(t)
	el.LogExchangeEvent(context.Background(), model.EventExchangeRateFetched, "aliyun_primary", true, nil, 7.2345, nil, 123)
	// async; give it time
	time.Sleep(100 * time.Millisecond)
	var cnt int64
	db.Model(&model.PaymentEventLog{}).Where("event_type = ?", model.EventExchangeRateFetched).Count(&cnt)
	if cnt != 1 {
		t.Errorf("expected 1 exchange event, got %d", cnt)
	}
}
