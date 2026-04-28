package apikey

import (
	"sync"
	"testing"
)

func TestShouldUpdateLastUsedLocalThrottle(t *testing.T) {
	lastUsedUpdateCache = sync.Map{}
	t.Cleanup(func() {
		lastUsedUpdateCache = sync.Map{}
	})

	if !shouldUpdateLastUsed("hash-a", nil) {
		t.Fatal("first update should pass")
	}
	if shouldUpdateLastUsed("hash-a", nil) {
		t.Fatal("second update inside throttle window should be skipped")
	}
	if !shouldUpdateLastUsed("hash-b", nil) {
		t.Fatal("different key should pass independently")
	}
}
