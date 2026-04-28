package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/service/usercache"
)

const freeTierAccessTestAPIKey = "sk-test-free-tier"

func setupFreeTierAccessTest(t *testing.T) (*gorm.DB, *goredis.Client) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.ApiKey{}, &model.UserBalance{}, &model.QuotaConfig{}, &model.AIModel{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}

	hash := sha256.Sum256([]byte(freeTierAccessTestAPIKey))
	if err := db.Create(&model.ApiKey{
		TenantID:  1,
		UserID:    7,
		Name:      "test",
		KeyHash:   hex.EncodeToString(hash[:]),
		KeyPrefix: "sk-test",
		IsActive:  true,
	}).Error; err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	if err := db.Create(&model.UserBalance{
		TenantID:       1,
		UserID:         7,
		Currency:       "CREDIT",
		TotalRecharged: 0,
	}).Error; err != nil {
		t.Fatalf("seed balance: %v", err)
	}
	if err := db.Create(&model.QuotaConfig{
		IsActive:             true,
		PaidThresholdCredits: 100000,
	}).Error; err != nil {
		t.Fatalf("seed quota config: %v", err)
	}

	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	oldRedis := pkgredis.Client
	pkgredis.Client = client
	t.Cleanup(func() {
		pkgredis.Client = oldRedis
		_ = client.Close()
	})
	return db, client
}

func authorizedRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+freeTierAccessTestAPIKey)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestOpenAPIAuthSetsPaidUserContextAndHonorsInvalidation(t *testing.T) {
	db, _ := setupFreeTierAccessTest(t)

	r := gin.New()
	r.Use(OpenAPIAuth(db))
	r.GET("/probe", func(c *gin.Context) {
		paid, exists := c.Get("isPaidUser")
		c.JSON(http.StatusOK, gin.H{
			"exists":  exists,
			"is_paid": paid,
		})
	})

	first := httptest.NewRecorder()
	r.ServeHTTP(first, authorizedRequest(http.MethodGet, "/probe", ""))
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"exists":true`) || !strings.Contains(first.Body.String(), `"is_paid":false`) {
		t.Fatalf("first paid context response code=%d body=%s", first.Code, first.Body.String())
	}

	if err := db.Model(&model.UserBalance{}).Where("user_id = ?", 7).Update("total_recharged", int64(100000)).Error; err != nil {
		t.Fatalf("update balance: %v", err)
	}
	usercache.InvalidatePaidStatus(context.Background(), 7)

	second := httptest.NewRecorder()
	r.ServeHTTP(second, authorizedRequest(http.MethodGet, "/probe", ""))
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"is_paid":true`) {
		t.Fatalf("second paid context response code=%d body=%s", second.Code, second.Body.String())
	}
}

func TestFreeTierAccessGuardRejectsPremiumModelForFreeUser(t *testing.T) {
	db, _ := setupFreeTierAccessTest(t)
	if err := db.Create(&model.AIModel{
		CategoryID:  1,
		SupplierID:  1,
		ModelName:   "premium-model",
		ModelType:   model.ModelTypeLLM,
		Status:      "online",
		IsActive:    true,
		IsFreeTier:  false,
		DisplayName: "Premium",
	}).Error; err != nil {
		t.Fatalf("seed premium model: %v", err)
	}

	r := gin.New()
	r.Use(OpenAPIAuth(db))
	r.Use(FreeTierAccessGuard(db))
	r.POST("/v1/chat/completions", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authorizedRequest(http.MethodPost, "/v1/chat/completions", `{"model":"premium-model","messages":[{"role":"user","content":"hi"}]}`))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "premium_model_only") {
		t.Fatalf("premium model response code=%d body=%s, want 403 premium_model_only", w.Code, w.Body.String())
	}
}

func TestFreeTierAccessGuardAllowsFreeTierModelForFreeUser(t *testing.T) {
	db, _ := setupFreeTierAccessTest(t)
	if err := db.Create(&model.AIModel{
		CategoryID:  1,
		SupplierID:  1,
		ModelName:   "free-model",
		ModelType:   model.ModelTypeLLM,
		Status:      "online",
		IsActive:    true,
		IsFreeTier:  true,
		DisplayName: "Free",
	}).Error; err != nil {
		t.Fatalf("seed free model: %v", err)
	}

	r := gin.New()
	r.Use(OpenAPIAuth(db))
	r.Use(FreeTierAccessGuard(db))
	r.POST("/v1/chat/completions", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authorizedRequest(http.MethodPost, "/v1/chat/completions", `{"model":"free-model","messages":[{"role":"user","content":"hi"}]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("free model response code=%d body=%s, want 200", w.Code, w.Body.String())
	}
}
