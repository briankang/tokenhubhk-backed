package payment

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// setupTestDBAndRedis 测试基础设施
func setupTestDBAndRedis(t *testing.T) (*gorm.DB, *goredis.Client, *miniredis.Miniredis) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.PaymentProviderAccount{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	mr := miniredis.RunT(t)
	return db, goredis.NewClient(&goredis.Options{Addr: mr.Addr()}), mr
}

func TestAccountRouter_SingleAccount_Return(t *testing.T) {
	db, rdb, _ := setupTestDBAndRedis(t)
	db.Create(&model.PaymentProviderAccount{
		ProviderType: "STRIPE", AccountName: "Main", IsActive: true, Weight: 10,
	})
	r := NewAccountRouter(db, rdb)
	acc, err := r.SelectAccount(context.Background(), SelectAccountRequest{ProviderType: "STRIPE"})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if acc.AccountName != "Main" {
		t.Errorf("expected Main, got %s", acc.AccountName)
	}
}

func TestAccountRouter_MultipleAccounts_WeightedDistribution(t *testing.T) {
	db, rdb, _ := setupTestDBAndRedis(t)
	db.Create(&model.PaymentProviderAccount{ProviderType: "STRIPE", AccountName: "A", IsActive: true, Weight: 70})
	db.Create(&model.PaymentProviderAccount{ProviderType: "STRIPE", AccountName: "B", IsActive: true, Weight: 30})
	r := NewAccountRouter(db, rdb)

	// 采样 1000 次
	counts := map[string]int{"A": 0, "B": 0}
	for i := 0; i < 1000; i++ {
		acc, err := r.SelectAccount(context.Background(), SelectAccountRequest{ProviderType: "STRIPE"})
		if err != nil {
			t.Fatalf("select %d: %v", i, err)
		}
		counts[acc.AccountName]++
	}
	// 70/30 ± 10% 偏差
	if counts["A"] < 600 || counts["A"] > 800 {
		t.Errorf("A distribution out of range: %d", counts["A"])
	}
	if counts["B"] < 200 || counts["B"] > 400 {
		t.Errorf("B distribution out of range: %d", counts["B"])
	}
}

func TestAccountRouter_CurrencyFilter(t *testing.T) {
	db, rdb, _ := setupTestDBAndRedis(t)
	db.Create(&model.PaymentProviderAccount{
		ProviderType: "STRIPE", AccountName: "USD-only", IsActive: true,
		SupportedCurrencies: "USD", Weight: 10,
	})
	db.Create(&model.PaymentProviderAccount{
		ProviderType: "STRIPE", AccountName: "EUR-only", IsActive: true,
		SupportedCurrencies: "EUR", Weight: 10,
	})
	r := NewAccountRouter(db, rdb)
	acc, err := r.SelectAccount(context.Background(), SelectAccountRequest{
		ProviderType: "STRIPE", Currency: "USD",
	})
	if err != nil {
		t.Fatalf("select usd: %v", err)
	}
	if acc.AccountName != "USD-only" {
		t.Errorf("expected USD-only, got %s", acc.AccountName)
	}
}

func TestAccountRouter_RegionFilter(t *testing.T) {
	db, rdb, _ := setupTestDBAndRedis(t)
	db.Create(&model.PaymentProviderAccount{
		ProviderType: "STRIPE", AccountName: "US", IsActive: true,
		SupportedRegions: "US", Weight: 10,
	})
	db.Create(&model.PaymentProviderAccount{
		ProviderType: "STRIPE", AccountName: "EU", IsActive: true,
		SupportedRegions: "EU,UK", Weight: 10,
	})
	r := NewAccountRouter(db, rdb)
	acc, err := r.SelectAccount(context.Background(), SelectAccountRequest{
		ProviderType: "STRIPE", Region: "EU",
	})
	if err != nil {
		t.Fatalf("select eu: %v", err)
	}
	if acc.AccountName != "EU" {
		t.Errorf("expected EU, got %s", acc.AccountName)
	}
}

func TestAccountRouter_ExcludeIDs(t *testing.T) {
	db, rdb, _ := setupTestDBAndRedis(t)
	a1 := model.PaymentProviderAccount{ProviderType: "STRIPE", AccountName: "A", IsActive: true, Weight: 10}
	a2 := model.PaymentProviderAccount{ProviderType: "STRIPE", AccountName: "B", IsActive: true, Weight: 10}
	db.Create(&a1)
	db.Create(&a2)
	r := NewAccountRouter(db, rdb)
	acc, err := r.SelectAccount(context.Background(), SelectAccountRequest{
		ProviderType: "STRIPE", ExcludeIDs: []uint64{a1.ID},
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if acc.ID != a2.ID {
		t.Errorf("expected B to be selected, got id=%d", acc.ID)
	}
}

func TestAccountRouter_InactiveAccountSkipped(t *testing.T) {
	db, rdb, _ := setupTestDBAndRedis(t)
	acc := model.PaymentProviderAccount{ProviderType: "STRIPE", AccountName: "Inactive", IsActive: false, Weight: 10}
	db.Create(&acc)
	db.Model(&model.PaymentProviderAccount{}).Where("id = ?", acc.ID).Update("is_active", false)
	r := NewAccountRouter(db, rdb)
	_, err := r.SelectAccount(context.Background(), SelectAccountRequest{ProviderType: "STRIPE"})
	if err == nil {
		t.Errorf("expected no active account error")
	}
}

func TestAccountRouter_PriorityOrdering(t *testing.T) {
	db, rdb, _ := setupTestDBAndRedis(t)
	db.Create(&model.PaymentProviderAccount{ProviderType: "STRIPE", AccountName: "P0", IsActive: true, Weight: 10, Priority: 0})
	db.Create(&model.PaymentProviderAccount{ProviderType: "STRIPE", AccountName: "P1", IsActive: true, Weight: 10, Priority: 1})
	r := NewAccountRouter(db, rdb)
	// 多次抽样，只应命中 P0
	for i := 0; i < 20; i++ {
		acc, _ := r.SelectAccount(context.Background(), SelectAccountRequest{ProviderType: "STRIPE"})
		if acc.AccountName != "P0" {
			t.Errorf("priority 0 should always win, got %s", acc.AccountName)
			break
		}
	}
}

func TestAccountRouter_PenaltyActive_Skipped(t *testing.T) {
	db, rdb, _ := setupTestDBAndRedis(t)
	a1 := model.PaymentProviderAccount{ProviderType: "STRIPE", AccountName: "Penalized", IsActive: true, Weight: 10}
	a2 := model.PaymentProviderAccount{ProviderType: "STRIPE", AccountName: "Healthy", IsActive: true, Weight: 10}
	db.Create(&a1)
	db.Create(&a2)
	r := NewAccountRouter(db, rdb)
	// 标记 a1 失败
	r.MarkAccountFailed(context.Background(), a1.ID, "test reason")

	// 随机采样，应只命中 Healthy
	for i := 0; i < 20; i++ {
		acc, err := r.SelectAccount(context.Background(), SelectAccountRequest{ProviderType: "STRIPE"})
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if acc.AccountName != "Healthy" {
			t.Errorf("penalized account selected: %s", acc.AccountName)
			break
		}
	}
}

func TestAccountRouter_MarkSuccessClearsPenalty(t *testing.T) {
	db, rdb, _ := setupTestDBAndRedis(t)
	acc := model.PaymentProviderAccount{ProviderType: "STRIPE", AccountName: "X", IsActive: true, Weight: 10}
	db.Create(&acc)
	r := NewAccountRouter(db, rdb)
	r.MarkAccountFailed(context.Background(), acc.ID, "x")
	if !r.isPenaltyActive(context.Background(), acc.ID) {
		t.Errorf("penalty should be active")
	}
	r.MarkAccountSuccess(context.Background(), acc.ID, 100)
	if r.isPenaltyActive(context.Background(), acc.ID) {
		t.Errorf("penalty should be cleared")
	}
}

func TestAccountRouter_NoActiveAccounts_Error(t *testing.T) {
	db, rdb, _ := setupTestDBAndRedis(t)
	r := NewAccountRouter(db, rdb)
	_, err := r.SelectAccount(context.Background(), SelectAccountRequest{ProviderType: "PAYPAL"})
	if err == nil {
		t.Errorf("expected error for no active accounts")
	}
}

func TestAccountRouter_MatchCSV(t *testing.T) {
	cases := []struct {
		csv    string
		target string
		want   bool
	}{
		{"", "USD", true}, // 空 csv = 全部
		{"USD", "USD", true},
		{"USD,EUR", "EUR", true},
		{"USD,EUR", "JPY", false},
		{"usd,eur", "USD", true}, // 大小写无关
	}
	for _, c := range cases {
		if got := matchCSV(c.csv, c.target); got != c.want {
			t.Errorf("matchCSV(%q, %q) = %v, want %v", c.csv, c.target, got, c.want)
		}
	}
}

func TestAccountRouter_CreateUpdateToggleDelete(t *testing.T) {
	db, rdb, _ := setupTestDBAndRedis(t)
	r := NewAccountRouter(db, rdb)
	acc := &model.PaymentProviderAccount{ProviderType: "STRIPE", AccountName: "T", IsActive: true, Weight: 10}
	if err := r.CreateAccount(context.Background(), acc); err != nil {
		t.Fatalf("create: %v", err)
	}
	if acc.ID == 0 {
		t.Error("id not set")
	}
	// Update
	err := r.UpdateAccount(context.Background(), acc.ID, map[string]interface{}{"weight": 99})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := r.GetAccount(context.Background(), acc.ID)
	if got.Weight != 99 {
		t.Errorf("weight not updated: %d", got.Weight)
	}
	// Toggle
	err = r.ToggleAccount(context.Background(), acc.ID)
	if err != nil {
		t.Fatalf("toggle: %v", err)
	}
	got2, _ := r.GetAccount(context.Background(), acc.ID)
	if got2.IsActive {
		t.Errorf("toggle should deactivate")
	}
	// Delete
	err = r.DeleteAccount(context.Background(), acc.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
}
