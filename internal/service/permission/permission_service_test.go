package permission_test

import (
	"context"
	"os"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/permission"
)

var (
	testDB    *gorm.DB
	testRedis *goredis.Client
)

func TestMain(m *testing.M) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "root:tokenhubhk_pass@tcp(localhost:3306)/tokenhubhk?charset=utf8mb4&parseTime=True&loc=Local"
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		os.Exit(0)
	}
	testDB = db

	redisAddr := os.Getenv("TEST_REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6380"
	}
	testRedis = goredis.NewClient(&goredis.Options{Addr: redisAddr})
	if err := testRedis.Ping(context.Background()).Err(); err != nil {
		testRedis = nil
	}

	_ = testDB.AutoMigrate(&model.User{}, &model.Tenant{})
	code := m.Run()
	os.Exit(code)
}

func TestPermissionService_CanAccessTenant(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := permission.NewPermissionService(testDB, testRedis)
	ctx := context.Background()

	// 未设置用户上下文，应返回 false 或 error
	can, err := svc.CanAccessTenant(ctx, 1)
	if err != nil {
		t.Logf("CanAccessTenant without context: %v (expected)", err)
	} else if can {
		t.Error("should not grant access without user context")
	}
}

func TestPermissionService_CanAccessUser(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := permission.NewPermissionService(testDB, testRedis)
	ctx := context.Background()

	can, err := svc.CanAccessUser(ctx, 1)
	if err != nil {
		t.Logf("CanAccessUser without context: %v (expected)", err)
	} else if can {
		t.Error("should not grant access without user context")
	}
}

func TestPermissionService_CanAccessApiKey(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := permission.NewPermissionService(testDB, testRedis)
	ctx := context.Background()

	can, err := svc.CanAccessApiKey(ctx, 1)
	if err != nil {
		t.Logf("CanAccessApiKey without context: %v (expected)", err)
	} else if can {
		t.Error("should not grant access without user context")
	}
}

func TestPermissionService_FilterSensitiveData(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := permission.NewPermissionService(testDB, testRedis)
	ctx := context.Background()

	data := map[string]interface{}{
		"id":       1,
		"name":     "test",
		"password": "secret",
	}

	filtered := svc.FilterSensitiveData(ctx, data)
	if filtered == nil {
		t.Error("filtered data should not be nil")
	}
}

func TestPermissionService_InvalidateTenantCache(t *testing.T) {
	if testDB == nil || testRedis == nil {
		t.Skip("database or redis not available")
	}

	svc := permission.NewPermissionService(testDB, testRedis)
	ctx := context.Background()

	err := svc.InvalidateTenantCache(ctx, 1)
	if err != nil {
		t.Fatalf("InvalidateTenantCache failed: %v", err)
	}
}
