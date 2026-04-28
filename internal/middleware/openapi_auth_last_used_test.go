package middleware

import (
	"context"
	"sync"
	"testing"

	pkgredis "tokenhub-server/internal/pkg/redis"
)

func TestShouldUpdateAPIKeyLastUsedLocalThrottle(t *testing.T) {
	oldRedis := pkgredis.Client
	pkgredis.Client = nil
	apiKeyLastUsedCache = sync.Map{}
	t.Cleanup(func() {
		pkgredis.Client = oldRedis
		apiKeyLastUsedCache = sync.Map{}
	})

	if !shouldUpdateAPIKeyLastUsed(context.Background(), "129") {
		t.Fatal("first update should pass")
	}
	if shouldUpdateAPIKeyLastUsed(context.Background(), "129") {
		t.Fatal("second update inside throttle window should be skipped")
	}
	if !shouldUpdateAPIKeyLastUsed(context.Background(), "130") {
		t.Fatal("different key should pass independently")
	}
}
