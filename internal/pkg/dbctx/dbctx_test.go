package dbctx

import (
	"context"
	"testing"
	"time"
)

func TestShort_Has2sTimeout(t *testing.T) {
	ctx, cancel := Short(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline")
	}
	remaining := time.Until(deadline)
	if remaining > ShortTimeout+100*time.Millisecond || remaining < ShortTimeout-100*time.Millisecond {
		t.Fatalf("expected ~2s, got %v", remaining)
	}
}

func TestMedium_Has5sTimeout(t *testing.T) {
	ctx, cancel := Medium(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline")
	}
	if d := time.Until(deadline); d < 4*time.Second || d > 6*time.Second {
		t.Fatalf("expected 5s, got %v", d)
	}
}

func TestLong_Has15sTimeout(t *testing.T) {
	ctx, cancel := Long(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline")
	}
	if d := time.Until(deadline); d < 14*time.Second || d > 16*time.Second {
		t.Fatalf("expected 15s, got %v", d)
	}
}

func TestTimeout_InheritsParent(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer parentCancel()
	ctx, cancel := Timeout(parent, 10*time.Second)
	defer cancel()
	// 父 ctx 先到期，子 ctx deadline 会是父的值（取更小）
	deadline, _ := ctx.Deadline()
	if d := time.Until(deadline); d > 1*time.Second {
		t.Fatalf("expected inherited shorter deadline, got %v", d)
	}
}

func TestTimeout_NilParent(t *testing.T) {
	ctx, cancel := Timeout(nil, 1*time.Second)
	defer cancel()
	if ctx == nil {
		t.Fatal("expected non-nil ctx")
	}
}

func TestTimeout_ZeroDefaultsToShort(t *testing.T) {
	ctx, cancel := Timeout(context.Background(), 0)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if d := time.Until(deadline); d > ShortTimeout+100*time.Millisecond {
		t.Fatalf("expected short default, got %v", d)
	}
}
