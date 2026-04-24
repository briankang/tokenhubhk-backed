package safego

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestGo_NormalCompletion(t *testing.T) {
	var done int32
	Go("normal-task", func() {
		atomic.StoreInt32(&done, 1)
	})
	time.Sleep(20 * time.Millisecond)
	if atomic.LoadInt32(&done) != 1 {
		t.Fatal("expected goroutine to finish")
	}
}

func TestGo_PanicDoesNotCrash(t *testing.T) {
	// 主测试进程不应被 panic goroutine 崩溃，验证 recover 生效
	Go("panic-task", func() {
		panic("boom")
	})
	time.Sleep(20 * time.Millisecond)
	// 若 recover 失效，进程已崩溃，这行执行不到
}

func TestGoCtx_CancelPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var exited int32

	GoCtx(ctx, "ctx-task", func(ctx context.Context) {
		<-ctx.Done()
		atomic.StoreInt32(&exited, 1)
	})

	cancel()
	time.Sleep(20 * time.Millisecond)
	if atomic.LoadInt32(&exited) != 1 {
		t.Fatal("expected goroutine to exit after ctx.Done()")
	}
}

func TestRun_Returns_False_OnNormal(t *testing.T) {
	var called bool
	panicked := Run("ok", func() { called = true })
	if panicked {
		t.Fatal("expected no panic")
	}
	if !called {
		t.Fatal("expected fn to run")
	}
}

func TestRun_Returns_True_OnPanic(t *testing.T) {
	panicked := Run("oops", func() { panic("boom") })
	if !panicked {
		t.Fatal("expected panicked=true")
	}
}

func TestRecover_WithNoPanic(t *testing.T) {
	// 直接调用 Recover 也必须安全
	func() {
		defer Recover("test")
	}()
}
