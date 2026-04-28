package usercache

import (
	"context"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	pkgredis "tokenhub-server/internal/pkg/redis"
)

type cachedProfile struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}

type cachedPaidStatus struct {
	IsPaid bool `json:"is_paid"`
}

func withUserCacheRedis(t *testing.T) *goredis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	old := pkgredis.Client
	pkgredis.Client = client
	t.Cleanup(func() {
		pkgredis.Client = old
		_ = client.Close()
	})
	return client
}

func TestUserCacheGetOrLoadMissHitAndCorruptCache(t *testing.T) {
	client := withUserCacheRedis(t)
	ctx := context.Background()
	calls := 0
	loader := func(context.Context) (cachedProfile, error) {
		calls++
		return cachedProfile{ID: 7, Name: "fresh"}, nil
	}

	first, err := GetOrLoadProfile(ctx, 7, loader)
	if err != nil {
		t.Fatalf("first GetOrLoadProfile: %v", err)
	}
	if first.Name != "fresh" || calls != 1 {
		t.Fatalf("first result=%#v calls=%d, want fresh and 1 loader call", first, calls)
	}
	second, err := GetOrLoadProfile(ctx, 7, loader)
	if err != nil {
		t.Fatalf("second GetOrLoadProfile: %v", err)
	}
	if second.Name != "fresh" || calls != 1 {
		t.Fatalf("cached result=%#v calls=%d, want no extra loader call", second, calls)
	}

	if err := client.Set(ctx, KeyProfile(7), "{bad-json", 0).Err(); err != nil {
		t.Fatalf("write corrupt cache: %v", err)
	}
	third, err := GetOrLoadProfile(ctx, 7, loader)
	if err != nil {
		t.Fatalf("corrupt GetOrLoadProfile: %v", err)
	}
	if third.Name != "fresh" || calls != 2 {
		t.Fatalf("corrupt cache should reload once, result=%#v calls=%d", third, calls)
	}
}

func TestUserCacheInvalidateAllDeletesOnlyOneUserDimensionKeys(t *testing.T) {
	client := withUserCacheRedis(t)
	ctx := context.Background()
	for _, key := range []string{KeyProfile(7), KeyBalance(7), KeyApiKeys(7), KeyNotifUnread(7), KeyPaidStatus(7), KeyProfile(8)} {
		if err := client.Set(ctx, key, "1", 0).Err(); err != nil {
			t.Fatalf("seed key %s: %v", key, err)
		}
	}

	InvalidateAll(ctx, 7)
	for _, key := range []string{KeyProfile(7), KeyBalance(7), KeyApiKeys(7), KeyNotifUnread(7), KeyPaidStatus(7)} {
		if n, err := client.Exists(ctx, key).Result(); err != nil || n != 0 {
			t.Fatalf("key %s exists=%d err=%v, want deleted", key, n, err)
		}
	}
	if n, err := client.Exists(ctx, KeyProfile(8)).Result(); err != nil || n != 1 {
		t.Fatalf("other user key exists=%d err=%v, want preserved", n, err)
	}
}

func TestUserCacheInvalidatePatternAllDeletesKnownPrefixesOnly(t *testing.T) {
	client := withUserCacheRedis(t)
	ctx := context.Background()
	keys := []string{KeyProfile(1), KeyBalance(2), KeyApiKeys(3), KeyNotifUnread(4), KeyPaidStatus(5), "system:cache:keep"}
	for _, key := range keys {
		if err := client.Set(ctx, key, "1", 0).Err(); err != nil {
			t.Fatalf("seed key %s: %v", key, err)
		}
	}

	deleted, err := InvalidatePatternAll(ctx)
	if err != nil {
		t.Fatalf("InvalidatePatternAll: %v", err)
	}
	if deleted != 5 {
		t.Fatalf("deleted = %d, want 5", deleted)
	}
	if n, err := client.Exists(ctx, "system:cache:keep").Result(); err != nil || n != 1 {
		t.Fatalf("unrelated key exists=%d err=%v, want preserved", n, err)
	}
}

func TestUserCacheGetOrLoadPaidStatusAndInvalidate(t *testing.T) {
	client := withUserCacheRedis(t)
	ctx := context.Background()
	calls := 0
	loader := func(context.Context) (cachedPaidStatus, error) {
		calls++
		return cachedPaidStatus{IsPaid: calls > 1}, nil
	}

	first, err := GetOrLoadPaidStatus(ctx, 7, loader)
	if err != nil {
		t.Fatalf("first GetOrLoadPaidStatus: %v", err)
	}
	if first.IsPaid || calls != 1 {
		t.Fatalf("first result=%#v calls=%d, want free and 1 loader call", first, calls)
	}
	second, err := GetOrLoadPaidStatus(ctx, 7, loader)
	if err != nil {
		t.Fatalf("second GetOrLoadPaidStatus: %v", err)
	}
	if second.IsPaid || calls != 1 {
		t.Fatalf("cached result=%#v calls=%d, want cached free and no extra loader call", second, calls)
	}

	InvalidatePaidStatus(ctx, 7)
	if n, err := client.Exists(ctx, KeyPaidStatus(7)).Result(); err != nil || n != 0 {
		t.Fatalf("paid status key exists=%d err=%v, want deleted", n, err)
	}
	third, err := GetOrLoadPaidStatus(ctx, 7, loader)
	if err != nil {
		t.Fatalf("reload GetOrLoadPaidStatus: %v", err)
	}
	if !third.IsPaid || calls != 2 {
		t.Fatalf("after invalidation result=%#v calls=%d, want paid and 2 loader calls", third, calls)
	}
}
