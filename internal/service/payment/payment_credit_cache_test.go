package payment

import (
	"context"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/service/usercache"
)

func TestCreditUserBalanceUpdatesTotalRechargedAndInvalidatesPaidStatus(t *testing.T) {
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open("file:payment_credit_cache?mode=memory&cache=shared"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.UserBalance{}, &model.BalanceRecord{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	if err := db.Create(&model.UserBalance{
		UserID:         7,
		TenantID:       1,
		Currency:       "CREDIT",
		TotalRecharged: 0,
	}).Error; err != nil {
		t.Fatalf("seed balance: %v", err)
	}

	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	oldRedis := pkgredis.Client
	pkgredis.Client = client
	t.Cleanup(func() {
		pkgredis.Client = oldRedis
		_ = client.Close()
	})
	if err := client.Set(ctx, usercache.KeyPaidStatus(7), `{"is_paid":false}`, 0).Err(); err != nil {
		t.Fatalf("seed paid status cache: %v", err)
	}

	svc := &PaymentService{db: db, logger: zap.NewNop()}
	if err := svc.creditUserBalance(ctx, 7, 1, 100000, "order-test", "mock"); err != nil {
		t.Fatalf("creditUserBalance: %v", err)
	}

	var balance model.UserBalance
	if err := db.Where("user_id = ?", 7).First(&balance).Error; err != nil {
		t.Fatalf("read balance: %v", err)
	}
	if balance.Balance != 100000 || balance.TotalRecharged != 100000 {
		t.Fatalf("balance=%d total_recharged=%d, want both 100000", balance.Balance, balance.TotalRecharged)
	}
	if n, err := client.Exists(ctx, usercache.KeyPaidStatus(7)).Result(); err != nil || n != 0 {
		t.Fatalf("paid status cache exists=%d err=%v, want deleted", n, err)
	}
}
