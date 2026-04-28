package pricing

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestDiscountResolverCachesMisses(t *testing.T) {
	clearDiscountMissCache()
	t.Cleanup(clearDiscountMissCache)

	db := newPricingCalculatorTestDB(t)
	var userDiscountQueries atomic.Int32
	var agentPricingQueries atomic.Int32
	var levelDiscountQueries atomic.Int32
	registerDiscountQueryCounter(t, db, "count_user_discount_miss_queries", "user_model_discounts", &userDiscountQueries)
	registerDiscountQueryCounter(t, db, "count_agent_pricing_miss_queries", "agent_pricings", &agentPricingQueries)
	registerDiscountQueryCounter(t, db, "count_level_discount_miss_queries", "agent_level_discounts", &levelDiscountQueries)

	resolver := NewDiscountResolver(db)
	for i := 0; i < 2; i++ {
		got, err := resolver.ResolveDiscount(context.Background(), 101, 201, 301, 1)
		if err != nil {
			t.Fatalf("ResolveDiscount #%d returned error: %v", i+1, err)
		}
		if got.Type != "none" || got.PricingType != "NONE" {
			t.Fatalf("ResolveDiscount #%d = %+v, want none", i+1, got)
		}
	}

	if got := userDiscountQueries.Load(); got != 1 {
		t.Fatalf("user_model_discounts query count = %d, want 1", got)
	}
	if got := agentPricingQueries.Load(); got != 1 {
		t.Fatalf("agent_pricings query count = %d, want 1", got)
	}
	if got := levelDiscountQueries.Load(); got != 2 {
		t.Fatalf("agent_level_discounts query count = %d, want 2", got)
	}
}

func TestDiscountResolverCachesConcurrentMisses(t *testing.T) {
	clearDiscountMissCache()
	t.Cleanup(clearDiscountMissCache)

	db := newPricingCalculatorTestDB(t)
	var userDiscountQueries atomic.Int32
	var agentPricingQueries atomic.Int32
	var levelDiscountQueries atomic.Int32
	registerDiscountQueryCounter(t, db, "count_concurrent_user_discount_miss_queries", "user_model_discounts", &userDiscountQueries)
	registerDiscountQueryCounter(t, db, "count_concurrent_agent_pricing_miss_queries", "agent_pricings", &agentPricingQueries)
	registerDiscountQueryCounter(t, db, "count_concurrent_level_discount_miss_queries", "agent_level_discounts", &levelDiscountQueries)

	resolver := NewDiscountResolver(db)
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := resolver.ResolveDiscount(context.Background(), 101, 201, 301, 1)
			if err != nil {
				errs <- err
				return
			}
			if got.Type != "none" || got.PricingType != "NONE" {
				errs <- fmt.Errorf("unexpected discount result: %+v", got)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	if got := userDiscountQueries.Load(); got != 1 {
		t.Fatalf("user_model_discounts query count = %d, want 1", got)
	}
	if got := agentPricingQueries.Load(); got != 1 {
		t.Fatalf("agent_pricings query count = %d, want 1", got)
	}
	if got := levelDiscountQueries.Load(); got != 2 {
		t.Fatalf("agent_level_discounts query count = %d, want 2", got)
	}
}

func TestClearDiscountMissCache(t *testing.T) {
	clearDiscountMissCache()
	t.Cleanup(clearDiscountMissCache)

	db := newPricingCalculatorTestDB(t)
	key := discountMissCacheKey{db: db, kind: "agent_pricing", tenantID: 1, modelID: 2}
	now := time.Now()
	storeDiscountMiss(key, now)
	if !loadDiscountMiss(key, now) {
		t.Fatal("expected discount miss cache hit before clear")
	}

	clearDiscountMissCache()
	if loadDiscountMiss(key, now) {
		t.Fatal("expected discount miss cache miss after clear")
	}
}

func registerDiscountQueryCounter(t *testing.T, db *gorm.DB, name, table string, counter *atomic.Int32) {
	t.Helper()
	if err := db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == table {
			counter.Add(1)
		}
	}); err != nil {
		t.Fatalf("register callback %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(name)
	})
}
