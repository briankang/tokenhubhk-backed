package auth_test

import (
	"context"
	"os"
	"testing"
	"fmt"
	"time"
	"math/rand"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/auth"
)

var (
	testDB    *gorm.DB
	testRedis *goredis.Client
	jwtCfg    config.JWTConfig
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

	jwtCfg = config.JWTConfig{
		Secret:          "test-jwt-secret-key-for-unit-tests",
		AccessTokenTTL:  3600,
		RefreshTokenTTL: 86400,
	}

	_ = testDB.AutoMigrate(&model.User{}, &model.UserBalance{})
	code := m.Run()
	os.Exit(code)
}

func uniqueEmail() string {
	return fmt.Sprintf("test_%d_%d@unittest.com", time.Now().UnixMilli(), rand.Intn(10000))
}

func TestAuthService_Register(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := auth.NewAuthService(testDB, testRedis, jwtCfg)
	ctx := context.Background()

	req := &auth.RegisterRequest{
		Email:    uniqueEmail(),
		Password: "Test@123456",
		Name:     "Unit Test User",
	}

	user, err := svc.Register(ctx, req)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	if user == nil {
		t.Fatal("user should not be nil")
	}
	if user.Email != req.Email {
		t.Errorf("expected email %s, got %s", req.Email, user.Email)
	}
}

func TestAuthService_Register_DuplicateEmail(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := auth.NewAuthService(testDB, testRedis, jwtCfg)
	ctx := context.Background()

	email := uniqueEmail()
	req := &auth.RegisterRequest{
		Email:    email,
		Password: "Test@123456",
		Name:     "Dup Test User",
	}

	// 第一次注册
	_, _ = svc.Register(ctx, req)

	// 第二次注册同一邮箱 — 应失败
	_, err := svc.Register(ctx, req)
	if err == nil {
		t.Error("duplicate email registration should fail")
	}
}

func TestAuthService_Login(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := auth.NewAuthService(testDB, testRedis, jwtCfg)
	ctx := context.Background()

	email := uniqueEmail()
	password := "Test@123456"

	// 先注册
	_, err := svc.Register(ctx, &auth.RegisterRequest{
		Email:    email,
		Password: password,
		Name:     "Login Test User",
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	// 登录
	loginReq := &auth.LoginRequest{
		Email:    email,
		Password: password,
	}
	tokenPair, err := svc.Login(ctx, loginReq)
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}

	if tokenPair == nil {
		t.Fatal("token pair should not be nil")
	}
	if tokenPair.AccessToken == "" {
		t.Error("access token should not be empty")
	}
	if tokenPair.RefreshToken == "" {
		t.Error("refresh token should not be empty")
	}
}

func TestAuthService_Login_WrongPassword(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := auth.NewAuthService(testDB, testRedis, jwtCfg)
	ctx := context.Background()

	email := uniqueEmail()
	_, _ = svc.Register(ctx, &auth.RegisterRequest{
		Email:    email,
		Password: "Test@123456",
		Name:     "Wrong Pwd User",
	})

	_, err := svc.Login(ctx, &auth.LoginRequest{
		Email:    email,
		Password: "WrongPassword123",
	})
	if err == nil {
		t.Error("login with wrong password should fail")
	}
}

func TestAuthService_Login_NonExistentUser(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := auth.NewAuthService(testDB, testRedis, jwtCfg)
	ctx := context.Background()

	_, err := svc.Login(ctx, &auth.LoginRequest{
		Email:    "nonexistent@unittest.com",
		Password: "Test@123456",
	})
	if err == nil {
		t.Error("login with non-existent user should fail")
	}
}

func TestAuthService_ValidateToken(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := auth.NewAuthService(testDB, testRedis, jwtCfg)
	ctx := context.Background()

	email := uniqueEmail()
	_, _ = svc.Register(ctx, &auth.RegisterRequest{
		Email:    email,
		Password: "Test@123456",
		Name:     "Token Validate User",
	})

	tokenPair, _ := svc.Login(ctx, &auth.LoginRequest{
		Email:    email,
		Password: "Test@123456",
	})

	claims, err := svc.ValidateToken(tokenPair.AccessToken)
	if err != nil {
		t.Fatalf("ValidateToken failed: %v", err)
	}

	if claims == nil {
		t.Fatal("claims should not be nil")
	}
}

func TestAuthService_ValidateToken_Invalid(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := auth.NewAuthService(testDB, testRedis, jwtCfg)

	_, err := svc.ValidateToken("invalid-token-string")
	if err == nil {
		t.Error("invalid token should fail validation")
	}
}

func TestAuthService_RefreshToken(t *testing.T) {
	if testDB == nil || testRedis == nil {
		t.Skip("database or redis not available")
	}

	svc := auth.NewAuthService(testDB, testRedis, jwtCfg)
	ctx := context.Background()

	email := uniqueEmail()
	_, _ = svc.Register(ctx, &auth.RegisterRequest{
		Email:    email,
		Password: "Test@123456",
		Name:     "Refresh Token User",
	})

	tokenPair, _ := svc.Login(ctx, &auth.LoginRequest{
		Email:    email,
		Password: "Test@123456",
	})

	// 刷新令牌
	newTokenPair, err := svc.RefreshToken(ctx, tokenPair.RefreshToken)
	if err != nil {
		t.Fatalf("RefreshToken failed: %v", err)
	}

	if newTokenPair == nil {
		t.Fatal("new token pair should not be nil")
	}
	if newTokenPair.AccessToken == "" {
		t.Error("new access token should not be empty")
	}
}

func TestAuthService_ChangePassword(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := auth.NewAuthService(testDB, testRedis, jwtCfg)
	ctx := context.Background()

	email := uniqueEmail()
	oldPass := "Test@123456"
	newPass := "NewPass@123456"

	user, _ := svc.Register(ctx, &auth.RegisterRequest{
		Email:    email,
		Password: oldPass,
		Name:     "Change Pwd User",
	})

	// 修改密码
	err := svc.ChangePassword(ctx, user.ID, oldPass, newPass)
	if err != nil {
		t.Fatalf("ChangePassword failed: %v", err)
	}

	// 旧密码登录应失败
	_, err = svc.Login(ctx, &auth.LoginRequest{Email: email, Password: oldPass})
	if err == nil {
		t.Error("old password should no longer work")
	}

	// 新密码登录应成功
	_, err = svc.Login(ctx, &auth.LoginRequest{Email: email, Password: newPass})
	if err != nil {
		t.Errorf("new password login failed: %v", err)
	}
}

func TestAuthService_ChangePassword_WrongOld(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := auth.NewAuthService(testDB, testRedis, jwtCfg)
	ctx := context.Background()

	email := uniqueEmail()
	user, _ := svc.Register(ctx, &auth.RegisterRequest{
		Email:    email,
		Password: "Test@123456",
		Name:     "Wrong Old Pwd User",
	})

	err := svc.ChangePassword(ctx, user.ID, "WrongOldPassword", "NewPass@123456")
	if err == nil {
		t.Error("change password with wrong old password should fail")
	}
}
