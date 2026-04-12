package guard

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"go.uber.org/zap"
)

// SafeGo 启动带自动panic恢复的goroutine
func SafeGo(logger *zap.Logger, fn func()) {
	if fn == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				if logger != nil {
					logger.Error("SafeGo recovered from panic",
						zap.Any("panic", r),
						zap.String("stack", string(debug.Stack())),
					)
				}
			}
		}()
		fn()
	}()
}

// WithTimeout 在指定超时时间内执行fn，超时或fn失败则返回错误
func WithTimeout(ctx context.Context, duration time.Duration, fn func(ctx context.Context) error) error {
	if fn == nil {
		return nil
	}
	if duration <= 0 {
		duration = 30 * time.Second
	}
	tCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	ch := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- fmt.Errorf("panic in WithTimeout: %v", r)
			}
		}()
		ch <- fn(tCtx)
	}()

	select {
	case err := <-ch:
		return err
	case <-tCtx.Done():
		return fmt.Errorf("operation timed out after %v", duration)
	}
}

// Retry 重试执行fn，最多maxAttempts次，每次间隔interval
func Retry(maxAttempts int, interval time.Duration, fn func() error) error {
	if fn == nil {
		return nil
	}
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		if err := fn(); err != nil {
			lastErr = err
			if i < maxAttempts-1 {
				time.Sleep(interval)
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("failed after %d attempts: %w", maxAttempts, lastErr)
}

// CircuitState 熔断器状态枚举
type CircuitState int

const (
	StateClosed   CircuitState = iota // 正常运行
	StateOpen                         // 熔断器触发，拒绝调用
	StateHalfOpen                     // 测试服务是否已恢复
)

// CircuitBreaker 简单熔断器实现，提供服务降级保护能力
type CircuitBreaker struct {
	mu               sync.Mutex
	state            CircuitState
	failureCount     int
	failureThreshold int
	successThreshold int
	successCount     int
	timeout          time.Duration
	lastFailureTime  time.Time
}

// NewCircuitBreaker 创建新的熔断器实例
// failureThreshold: 连续失败多少次触发熔断
// successThreshold: 半开状态下连续成功多少次关闭熔断
// timeout: 从开启状态转入半开状态的等待时间
func NewCircuitBreaker(failureThreshold, successThreshold int, timeout time.Duration) *CircuitBreaker {
	if failureThreshold < 1 {
		failureThreshold = 5
	}
	if successThreshold < 1 {
		successThreshold = 2
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &CircuitBreaker{
		state:            StateClosed,
		failureThreshold: failureThreshold,
		successThreshold: successThreshold,
		timeout:          timeout,
	}
}

// Execute 通过熔断器执行fn，根据状态决定是否允许调用
func (cb *CircuitBreaker) Execute(fn func() error) error {
	if fn == nil {
		return nil
	}

	cb.mu.Lock()
	state := cb.state

	if state == StateOpen {
		if time.Since(cb.lastFailureTime) > cb.timeout {
			cb.state = StateHalfOpen
			cb.successCount = 0
			state = StateHalfOpen
		} else {
			cb.mu.Unlock()
			return fmt.Errorf("circuit breaker is open")
		}
	}
	cb.mu.Unlock()

	err := fn()

	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err != nil {
		cb.failureCount++
		cb.successCount = 0
		cb.lastFailureTime = time.Now()
		if cb.failureCount >= cb.failureThreshold {
			cb.state = StateOpen
		}
		return err
	}

	if state == StateHalfOpen {
		cb.successCount++
		if cb.successCount >= cb.successThreshold {
			cb.state = StateClosed
			cb.failureCount = 0
		}
	} else {
		cb.failureCount = 0
	}
	return nil
}
