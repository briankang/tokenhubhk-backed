package whitelabel

import (
	"context"
	"fmt"
	"strings"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
	pkgredis "tokenhub-server/internal/pkg/redis"
)

func newWhiteLabelTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", name)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Tenant{}); err != nil {
		t.Fatalf("migrate tenant: %v", err)
	}
	return db
}

func withWhiteLabelRedis(t *testing.T) *goredis.Client {
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

func TestWhiteLabelValidateDomainBoundaries(t *testing.T) {
	db := newWhiteLabelTestDB(t)
	redisClient := withWhiteLabelRedis(t)
	svc := NewWhiteLabelService(db, redisClient)
	ctx := context.Background()

	tenant := model.Tenant{Name: "Used", Domain: "used.example.com", IsActive: true}
	if err := db.Create(&tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	for _, domain := range []string{"", "bad_domain", "-bad.example.com", "bad..example.com"} {
		if err := svc.ValidateDomain(ctx, domain, 0); err == nil {
			t.Fatalf("ValidateDomain(%q) expected error", domain)
		}
	}
	if err := svc.ValidateDomain(ctx, " USED.EXAMPLE.COM ", 0); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("duplicate domain error = %v, want already in use", err)
	}
	if err := svc.ValidateDomain(ctx, " used.example.com ", tenant.ID); err != nil {
		t.Fatalf("same tenant domain should be allowed when excluded: %v", err)
	}
	if err := svc.ValidateDomain(ctx, "brand.example.com", 0); err != nil {
		t.Fatalf("valid domain rejected: %v", err)
	}
}

func TestWhiteLabelUpdateConfigValidationAndPersistence(t *testing.T) {
	db := newWhiteLabelTestDB(t)
	redisClient := withWhiteLabelRedis(t)
	svc := NewWhiteLabelService(db, redisClient)
	ctx := context.Background()

	tenant := model.Tenant{Name: "Old", Domain: "old.example.com", IsActive: true}
	if err := db.Create(&tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	if err := svc.UpdateConfig(ctx, tenant.ID, &WhiteLabelConfig{PrimaryColor: "red"}); err == nil || !strings.Contains(err.Error(), "invalid primary_color") {
		t.Fatalf("invalid color error = %v, want invalid primary_color", err)
	}
	if err := svc.UpdateConfig(ctx, 9999, &WhiteLabelConfig{Domain: "missing.example.com", PrimaryColor: "#abc"}); err == nil || !strings.Contains(err.Error(), "tenant not found") {
		t.Fatalf("missing tenant error = %v, want tenant not found", err)
	}
	if err := svc.UpdateConfig(ctx, tenant.ID, &WhiteLabelConfig{
		Domain:       "brand.example.com",
		BrandName:    "Brand",
		LogoURL:      "https://cdn.example.com/logo.png",
		PrimaryColor: "#AABBCC",
	}); err != nil {
		t.Fatalf("UpdateConfig valid: %v", err)
	}
	var updated model.Tenant
	if err := db.First(&updated, tenant.ID).Error; err != nil {
		t.Fatalf("load updated tenant: %v", err)
	}
	if updated.Domain != "brand.example.com" || updated.Name != "Brand" || updated.LogoURL == "" {
		t.Fatalf("tenant not updated correctly: %#v", updated)
	}
}

func TestDomainResolverResolveCachesActiveTenantOnly(t *testing.T) {
	db := newWhiteLabelTestDB(t)
	redisClient := withWhiteLabelRedis(t)
	resolver := NewDomainResolver(db, redisClient)
	ctx := context.Background()

	active := model.Tenant{Name: "Active", Domain: "active.example.com", IsActive: true}
	if err := db.Create(&active).Error; err != nil {
		t.Fatalf("create active tenant: %v", err)
	}
	inactive := model.Tenant{Name: "Inactive", Domain: "inactive.example.com", IsActive: false}
	if err := db.Create(&inactive).Error; err != nil {
		t.Fatalf("create inactive tenant: %v", err)
	}
	if err := db.Model(&model.Tenant{}).Where("id = ?", inactive.ID).Update("is_active", false).Error; err != nil {
		t.Fatalf("force inactive tenant: %v", err)
	}

	if _, err := resolver.Resolve(ctx, ""); err == nil || !strings.Contains(err.Error(), "domain is empty") {
		t.Fatalf("empty domain error = %v, want domain is empty", err)
	}
	missing, err := resolver.Resolve(ctx, "missing.example.com")
	if err != nil || missing != nil {
		t.Fatalf("missing domain result=%#v err=%v, want nil nil", missing, err)
	}
	inactiveResult, err := resolver.Resolve(ctx, "inactive.example.com")
	if err != nil || inactiveResult != nil {
		t.Fatalf("inactive domain result=%#v err=%v, want nil nil", inactiveResult, err)
	}

	first, err := resolver.Resolve(ctx, "active.example.com")
	if err != nil {
		t.Fatalf("Resolve active first: %v", err)
	}
	if first == nil || first.Name != "Active" {
		t.Fatalf("active tenant = %#v, want Active", first)
	}
	if err := db.Model(&model.Tenant{}).Where("id = ?", active.ID).Update("name", "Changed").Error; err != nil {
		t.Fatalf("change active tenant name: %v", err)
	}
	cached, err := resolver.Resolve(ctx, "active.example.com")
	if err != nil {
		t.Fatalf("Resolve active cached: %v", err)
	}
	if cached == nil || cached.Name != "Active" {
		t.Fatalf("cached tenant = %#v, want old cached Active", cached)
	}
	if err := resolver.InvalidateCache(ctx, "active.example.com"); err != nil {
		t.Fatalf("InvalidateCache: %v", err)
	}
	refreshed, err := resolver.Resolve(ctx, "active.example.com")
	if err != nil {
		t.Fatalf("Resolve active refreshed: %v", err)
	}
	if refreshed == nil || refreshed.Name != "Changed" {
		t.Fatalf("refreshed tenant = %#v, want Changed", refreshed)
	}
}
