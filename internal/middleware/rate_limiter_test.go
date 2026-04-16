package middleware

import (
	"context"
	"testing"
	"time"
)

// ========== TPMLimiter 单元测试 ==========

func TestTPMLimiter_NilRedis(t *testing.T) {
	// Redis 不可用时应 fail-open
	limiter := &TPMLimiter{
		redis:     nil,
		memberSvc: nil,
	}

	ok, _ := limiter.CheckTPM(context.Background(), 1, 1000)
	if !ok {
		t.Error("CheckTPM with nil redis should return true (fail-open)")
	}

	// RecordTPM with nil redis should not panic
	limiter.RecordTPM(context.Background(), 1, 1000)
}

func TestTPMLimiter_ZeroTokens(t *testing.T) {
	limiter := &TPMLimiter{
		redis:     nil,
		memberSvc: nil,
	}

	// Zero or negative tokens should always pass
	ok, _ := limiter.CheckTPM(context.Background(), 1, 0)
	if !ok {
		t.Error("CheckTPM with 0 tokens should return true")
	}

	ok, _ = limiter.CheckTPM(context.Background(), 1, -1)
	if !ok {
		t.Error("CheckTPM with negative tokens should return true")
	}
}

// ========== 全局 QPS 检查 ==========

func TestCheckGlobalQPS_AllowsWithinLimit(t *testing.T) {
	// Reset counters
	globalRequestCounter = 0
	globalCounterResetTime = time.Now().Unix()

	// Should allow requests within limit
	for i := 0; i < 10; i++ {
		if !checkGlobalQPS(100) {
			t.Errorf("request %d should be allowed within limit of 100", i+1)
		}
	}
}

func TestCheckGlobalQPS_ZeroLimit(t *testing.T) {
	// Zero limit should allow all
	if !checkGlobalQPS(0) {
		t.Error("zero limit should allow all requests")
	}
}

func TestCheckGlobalQPS_NegativeLimit(t *testing.T) {
	// Negative limit should allow all
	if !checkGlobalQPS(-1) {
		t.Error("negative limit should allow all requests")
	}
}

// ========== RateLimiterConfig 测试 ==========

func TestDefaultRateLimiterConfig(t *testing.T) {
	cfg := DefaultRateLimiterConfig()

	if cfg.IPRPM != defaultIPRPM {
		t.Errorf("IPRPM expected %d, got %d", defaultIPRPM, cfg.IPRPM)
	}
	if cfg.UserRPM != defaultUserRPM {
		t.Errorf("UserRPM expected %d, got %d", defaultUserRPM, cfg.UserRPM)
	}
	if cfg.APIKeyRPM != defaultAPIKeyRPM {
		t.Errorf("APIKeyRPM expected %d, got %d", defaultAPIKeyRPM, cfg.APIKeyRPM)
	}
	if cfg.GlobalQPS != defaultGlobalQPS {
		t.Errorf("GlobalQPS expected %d, got %d", defaultGlobalQPS, cfg.GlobalQPS)
	}
}
