package channel

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func setupTestRedis(t *testing.T) (*goredis.Client, func()) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	return client, func() {
		client.Close()
		mr.Close()
	}
}

func TestRedisCircuitBreaker_InitialState(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	cb := NewRedisCircuitBreaker(client, 1, DefaultBreakerConfig())
	ctx := context.Background()

	state := cb.State(ctx)
	if state != BreakerClosed {
		t.Errorf("initial state = %q, want %q", state, BreakerClosed)
	}
	if cb.IsOpen(ctx) {
		t.Error("IsOpen should be false initially")
	}
}

func TestRedisCircuitBreaker_TripsOnConsecutiveFailures(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	cfg := DefaultBreakerConfig()
	cfg.MaxFailures = 3 // 降低阈值便于测试
	cb := NewRedisCircuitBreaker(client, 42, cfg)
	ctx := context.Background()

	// 3 次连续失败应触发熔断
	for i := 0; i < 3; i++ {
		cb.RecordFailure(ctx)
	}

	if !cb.IsOpen(ctx) {
		t.Error("breaker should be open after 3 consecutive failures")
	}
}

func TestRedisCircuitBreaker_TripsOnFailRate(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	cfg := DefaultBreakerConfig()
	cfg.MaxFailures = 100 // 提高连续失败阈值，测试失败率触发
	cfg.MinRequests = 5
	cfg.FailRate = 0.5
	cb := NewRedisCircuitBreaker(client, 99, cfg)
	ctx := context.Background()

	// 2 成功 + 4 失败 = 6 请求，失败率 66% > 50%
	cb.RecordSuccess(ctx)
	cb.RecordSuccess(ctx)
	for i := 0; i < 4; i++ {
		cb.RecordFailure(ctx)
	}

	if !cb.IsOpen(ctx) {
		t.Error("breaker should be open with 66% failure rate")
	}
}

func TestRedisCircuitBreaker_SuccessResetsConsecutiveFailures(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	cfg := DefaultBreakerConfig()
	cfg.MaxFailures = 3
	cb := NewRedisCircuitBreaker(client, 10, cfg)
	ctx := context.Background()

	// 2 次失败 + 1 次成功 → 重置连续失败
	cb.RecordFailure(ctx)
	cb.RecordFailure(ctx)
	cb.RecordSuccess(ctx)

	// 再 2 次失败不应触发（连续失败只有 2）
	cb.RecordFailure(ctx)
	cb.RecordFailure(ctx)

	if cb.IsOpen(ctx) {
		t.Error("breaker should not be open; success reset consecutive count")
	}
}

func TestRedisCircuitBreaker_HalfOpenRecovery(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	cfg := DefaultBreakerConfig()
	cfg.MaxFailures = 2
	cfg.OpenTimeout = 1 // 1 秒超时便于测试
	cb := NewRedisCircuitBreaker(client, 20, cfg)
	ctx := context.Background()

	// 触发熔断
	cb.RecordFailure(ctx)
	cb.RecordFailure(ctx)
	if !cb.IsOpen(ctx) {
		t.Fatal("breaker should be open")
	}

	// 等待 Open 超时
	time.Sleep(1100 * time.Millisecond)

	// 应自动进入 HalfOpen
	state := cb.State(ctx)
	if state != BreakerHalfOpen {
		t.Errorf("state = %q, want %q after timeout", state, BreakerHalfOpen)
	}

	// HalfOpen 下成功 → 恢复 Closed
	cb.RecordSuccess(ctx)
	state = cb.State(ctx)
	if state != BreakerClosed {
		t.Errorf("state = %q, want %q after half-open success", state, BreakerClosed)
	}
}

func TestRedisCircuitBreaker_Reset(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	cfg := DefaultBreakerConfig()
	cfg.MaxFailures = 2
	cb := NewRedisCircuitBreaker(client, 30, cfg)
	ctx := context.Background()

	// 触发熔断
	cb.RecordFailure(ctx)
	cb.RecordFailure(ctx)
	if !cb.IsOpen(ctx) {
		t.Fatal("breaker should be open")
	}

	// Reset 恢复
	cb.Reset(ctx)
	if cb.IsOpen(ctx) {
		t.Error("breaker should be closed after reset")
	}
}

func TestRedisCircuitBreaker_FailOpen_RedisUnavailable(t *testing.T) {
	// 连接一个不存在的 Redis
	client := goredis.NewClient(&goredis.Options{
		Addr:        "localhost:1",
		DialTimeout: 10 * time.Millisecond,
	})
	defer client.Close()

	cb := NewRedisCircuitBreaker(client, 50, DefaultBreakerConfig())
	ctx := context.Background()

	// Redis 不可用时应 fail-open（返回 closed，不阻止请求）
	state := cb.State(ctx)
	if state != BreakerClosed {
		t.Errorf("fail-open: state = %q, want %q", state, BreakerClosed)
	}

	// RecordFailure/Success 不应 panic
	cb.RecordFailure(ctx)
	cb.RecordSuccess(ctx)
}

func TestRedisCircuitBreaker_MultipleInstances_SharedState(t *testing.T) {
	client, cleanup := setupTestRedis(t)
	defer cleanup()

	cfg := DefaultBreakerConfig()
	cfg.MaxFailures = 3

	// 两个 breaker 实例指向同一 channelID（模拟两个 Pod）
	cb1 := NewRedisCircuitBreaker(client, 100, cfg)
	cb2 := NewRedisCircuitBreaker(client, 100, cfg)
	ctx := context.Background()

	// Pod1 记录 2 次失败
	cb1.RecordFailure(ctx)
	cb1.RecordFailure(ctx)

	// Pod2 记录第 3 次失败 → 应触发熔断
	cb2.RecordFailure(ctx)

	// 两个 Pod 都应看到 Open 状态
	if !cb1.IsOpen(ctx) {
		t.Error("cb1 should see breaker as open")
	}
	if !cb2.IsOpen(ctx) {
		t.Error("cb2 should see breaker as open")
	}
}
