package middleware

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
)

// ----- IsProtectedAdminEmail -----

func TestIsProtectedAdminEmail(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"exact match", "admin@tokenhubhk.com", true},
		{"uppercase domain", "admin@TokenHubHK.com", true},
		{"uppercase local", "ADMIN@tokenhubhk.com", true},
		{"with spaces", "  admin@tokenhubhk.com  ", true},
		{"different account", "user@tokenhubhk.com", false},
		{"different domain", "admin@example.com", false},
		{"empty", "", false},
		{"legacy admin", "admin@tokenhub.ai", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsProtectedAdminEmail(tc.in); got != tc.want {
				t.Errorf("IsProtectedAdminEmail(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// ----- ShouldBlockProtectedAdminCritical -----

func TestShouldBlockProtectedAdminCritical(t *testing.T) {
	if !ShouldBlockProtectedAdminCritical("admin@tokenhubhk.com") {
		t.Error("admin email should be blocked from critical operations")
	}
	if !ShouldBlockProtectedAdminCritical("ADMIN@tokenhubhk.com") {
		t.Error("admin email (case insensitive) should be blocked from critical operations")
	}
	if ShouldBlockProtectedAdminCritical("user@tokenhubhk.com") {
		t.Error("non-admin should not be blocked from critical operations")
	}
	if ShouldBlockProtectedAdminCritical("") {
		t.Error("empty email should not be blocked")
	}
}

// ----- IsCurrentUserProtectedAdmin / ShouldBlockProtectedAdminWrite -----

func setupAdminProtectionTest(t *testing.T) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Tenant{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newCtxWithUserID(uid uint) *gin.Context {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if uid != 0 {
		c.Set("userId", uid)
	}
	return c
}

func TestIsCurrentUserProtectedAdmin_NilDB(t *testing.T) {
	c := newCtxWithUserID(1)
	if IsCurrentUserProtectedAdmin(c, nil) {
		t.Error("nil db should return false")
	}
}

func TestIsCurrentUserProtectedAdmin_NoUserID(t *testing.T) {
	db := setupAdminProtectionTest(t)
	c := newCtxWithUserID(0)
	if IsCurrentUserProtectedAdmin(c, db) {
		t.Error("ctx without userId should return false")
	}
}

func TestIsCurrentUserProtectedAdmin_AdminUser(t *testing.T) {
	db := setupAdminProtectionTest(t)
	admin := model.User{
		Email:        ProtectedAdminEmail,
		PasswordHash: "fake-hash",
		Name:         "admin",
		IsActive:     true,
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	c := newCtxWithUserID(admin.ID)
	if !IsCurrentUserProtectedAdmin(c, db) {
		t.Error("admin user should be detected as protected")
	}
}

func TestIsCurrentUserProtectedAdmin_NonAdminUser(t *testing.T) {
	db := setupAdminProtectionTest(t)
	user := model.User{
		Email:        "user@tokenhubhk.com",
		PasswordHash: "fake-hash",
		Name:         "user",
		IsActive:     true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	c := newCtxWithUserID(user.ID)
	if IsCurrentUserProtectedAdmin(c, db) {
		t.Error("non-admin user should not be detected as protected")
	}
}

func TestIsCurrentUserProtectedAdmin_NonexistentUser(t *testing.T) {
	db := setupAdminProtectionTest(t)
	c := newCtxWithUserID(99999)
	if IsCurrentUserProtectedAdmin(c, db) {
		t.Error("nonexistent user should return false")
	}
}

// ShouldBlockProtectedAdminWrite 三场景验证

func TestShouldBlockProtectedAdminWrite_TargetIsNotAdmin(t *testing.T) {
	db := setupAdminProtectionTest(t)
	c := newCtxWithUserID(1)
	if ShouldBlockProtectedAdminWrite(c, db, "user@tokenhubhk.com") {
		t.Error("non-admin target should not be blocked")
	}
}

func TestShouldBlockProtectedAdminWrite_AdminModifiesSelf(t *testing.T) {
	db := setupAdminProtectionTest(t)
	admin := model.User{
		Email:        ProtectedAdminEmail,
		PasswordHash: "fake",
		Name:         "admin",
		IsActive:     true,
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	c := newCtxWithUserID(admin.ID)
	if ShouldBlockProtectedAdminWrite(c, db, ProtectedAdminEmail) {
		t.Error("admin modifying self should NOT be blocked")
	}
}

func TestShouldBlockProtectedAdminWrite_OtherUserModifiesAdmin(t *testing.T) {
	db := setupAdminProtectionTest(t)
	admin := model.User{
		Email:        ProtectedAdminEmail,
		PasswordHash: "fake",
		Name:         "admin",
		IsActive:     true,
	}
	other := model.User{
		Email:        "another-super-admin@tokenhubhk.com",
		PasswordHash: "fake",
		Name:         "other",
		IsActive:     true,
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other: %v", err)
	}
	c := newCtxWithUserID(other.ID)
	if !ShouldBlockProtectedAdminWrite(c, db, ProtectedAdminEmail) {
		t.Error("non-admin user modifying admin should be blocked")
	}
}

func TestShouldBlockProtectedAdminWrite_AnonymousModifiesAdmin(t *testing.T) {
	db := setupAdminProtectionTest(t)
	admin := model.User{
		Email:        ProtectedAdminEmail,
		PasswordHash: "fake",
		Name:         "admin",
		IsActive:     true,
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	c := newCtxWithUserID(0) // 未登录
	if !ShouldBlockProtectedAdminWrite(c, db, ProtectedAdminEmail) {
		t.Error("anonymous request modifying admin should be blocked")
	}
}
