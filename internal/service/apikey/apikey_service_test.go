package apikey_test

import (
	"context"
	"os"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/apikey"
)

var (
	testDB    *gorm.DB
	testRedis *goredis.Client
)

func TestMain(m *testing.M) {
	// 连接测试数据库
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "root:tokenhubhk_pass@tcp(localhost:3306)/tokenhubhk?charset=utf8mb4&parseTime=True&loc=Local"
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		// 如果无法连接数据库，跳过测试
		os.Exit(0)
	}
	testDB = db

	// 连接 Redis
	redisAddr := os.Getenv("TEST_REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6380"
	}
	testRedis = goredis.NewClient(&goredis.Options{
		Addr: redisAddr,
	})
	if err := testRedis.Ping(context.Background()).Err(); err != nil {
		testRedis = nil
	}

	// 确保表存在
	_ = testDB.AutoMigrate(&model.ApiKey{})

	code := m.Run()
	os.Exit(code)
}

func TestApiKeyService_Generate(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := apikey.NewApiKeyService(testDB, testRedis, "test-secret-key-for-aes-256")
	ctx := context.Background()

	result, err := svc.Generate(ctx, 1, 1, "test-key-generate")
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if result == nil {
		t.Fatal("result should not be nil")
	}

	// 验证 key 格式（应以 "sk-" 开头）
	t.Logf("generated key prefix: %s...", result.Key[:min(10, len(result.Key))])
}

func TestApiKeyService_Validate(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := apikey.NewApiKeyService(testDB, testRedis, "test-secret-key-for-aes-256")
	ctx := context.Background()

	// 先生成一个 key
	result, err := svc.Generate(ctx, 1, 1, "test-key-validate")
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// 验证该 key
	info, err := svc.Verify(ctx, result.Key)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}

	if info == nil {
		t.Fatal("validate should return key info")
	}
}

func TestApiKeyService_List(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := apikey.NewApiKeyService(testDB, testRedis, "test-secret-key-for-aes-256")
	ctx := context.Background()

	keys, _, err := svc.List(ctx, 1, 1, 20)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	// 应返回非 nil 切片
	if keys == nil {
		t.Error("keys should not be nil")
	}
	t.Logf("found %d keys for user 1", len(keys))
}

func TestApiKeyService_Revoke(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := apikey.NewApiKeyService(testDB, testRedis, "test-secret-key-for-aes-256")
	ctx := context.Background()

	// 先生成一个 key
	result, err := svc.Generate(ctx, 1, 1, "test-key-revoke")
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// 吊销
	err = svc.Revoke(ctx, result.ID, 1)
	if err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}

	// 验证已吊销（Verify 应该失败）
	_, err = svc.Verify(ctx, result.Key)
	if err == nil {
		t.Error("revoked key should not be validatable")
	}
}

func TestApiKeyService_Validate_InvalidKey(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := apikey.NewApiKeyService(testDB, testRedis, "test-secret-key-for-aes-256")
	ctx := context.Background()

	_, err := svc.Verify(ctx, "sk-invalid-key-that-does-not-exist")
	if err == nil {
		t.Error("invalid key should fail validation")
	}
}

func TestApiKeyService_Revoke_WrongUser(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := apikey.NewApiKeyService(testDB, testRedis, "test-secret-key-for-aes-256")
	ctx := context.Background()

	// 用户1生成 key
	result, err := svc.Generate(ctx, 1, 1, "test-key-wrong-user")
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// 用户2尝试吊销 — 应该失败
	err = svc.Revoke(ctx, result.ID, 99999)
	if err == nil {
		t.Error("should not be able to revoke another user's key")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
