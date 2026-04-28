package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
	pkgredis "tokenhub-server/internal/pkg/redis"
	whitelabel "tokenhub-server/internal/service/whitelabel"
)

func setupTenantResolveTest(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Tenant{}); err != nil {
		t.Fatalf("migrate tenant: %v", err)
	}
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	old := pkgredis.Client
	pkgredis.Client = client
	t.Cleanup(func() {
		pkgredis.Client = old
		_ = client.Close()
	})

	resolver := whitelabel.NewDomainResolver(db, client)
	r := gin.New()
	r.Use(TenantResolveMiddleware(resolver, "platform.example.com"))
	r.GET("/models", func(c *gin.Context) {
		tenantID, _ := c.Get("resolvedTenantID")
		domain, _ := c.Get("resolvedTenantDomain")
		c.JSON(http.StatusOK, gin.H{
			"tenant_id": tenantID,
			"domain":    domain,
		})
	})
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r, db
}

func tenantResolveRequest(t *testing.T, r *gin.Engine, host, path string) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var payload map[string]interface{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &payload)
	}
	return w.Code, payload
}

func TestTenantResolveMiddlewareResolvesCustomDomainForPublicRoute(t *testing.T) {
	r, db := setupTenantResolveTest(t)
	tenant := model.Tenant{Name: "Brand", Domain: "brand.example.com", IsActive: true}
	if err := db.Create(&tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	code, resp := tenantResolveRequest(t, r, "brand.example.com:8090", "/models")
	if code != http.StatusOK {
		t.Fatalf("custom domain status=%d resp=%v", code, resp)
	}
	if resp["tenant_id"].(float64) != float64(tenant.ID) || resp["domain"] != "brand.example.com" {
		t.Fatalf("resolved tenant mismatch: %#v", resp)
	}
}

func TestTenantResolveMiddlewareSkipsPlatformLocalPrivateAndHealthHosts(t *testing.T) {
	r, db := setupTenantResolveTest(t)
	tenant := model.Tenant{Name: "Brand", Domain: "brand.example.com", IsActive: true}
	if err := db.Create(&tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	for _, tc := range []struct {
		host string
		path string
	}{
		{"platform.example.com", "/models"},
		{"localhost:8090", "/models"},
		{"127.0.0.1:8090", "/models"},
		{"10.0.0.1:8090", "/models"},
		{"brand.example.com", "/health"},
		{"unknown.example.com", "/models"},
	} {
		t.Run(tc.host+tc.path, func(t *testing.T) {
			code, resp := tenantResolveRequest(t, r, tc.host, tc.path)
			if code != http.StatusOK {
				t.Fatalf("status=%d resp=%v", code, resp)
			}
			if tc.path == "/models" {
				if resp["tenant_id"] != nil || resp["domain"] != nil {
					t.Fatalf("host %s should not resolve tenant, got %#v", tc.host, resp)
				}
			}
		})
	}
}

func TestTenantResolveMiddlewareIgnoresInactiveDomain(t *testing.T) {
	r, db := setupTenantResolveTest(t)
	tenant := model.Tenant{Name: "Inactive", Domain: "inactive.example.com", IsActive: true}
	if err := db.Create(&tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if err := db.Model(&model.Tenant{}).Where("id = ?", tenant.ID).Update("is_active", false).Error; err != nil {
		t.Fatalf("mark inactive: %v", err)
	}

	code, resp := tenantResolveRequest(t, r, "inactive.example.com", "/models")
	if code != http.StatusOK {
		t.Fatalf("inactive domain status=%d resp=%v", code, resp)
	}
	if resp["tenant_id"] != nil || resp["domain"] != nil {
		t.Fatalf("inactive domain should not resolve tenant: %#v", resp)
	}
}
