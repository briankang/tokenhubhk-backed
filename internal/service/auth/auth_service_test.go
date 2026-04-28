package auth_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

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
		Secret: "test-jwt-secret-key-for-unit-tests",
		Expire: 1,
	}

	_ = testDB.AutoMigrate(&model.User{}, &model.UserBalance{})
	code := m.Run()
	os.Exit(code)
}

func uniqueEmail() string {
	return fmt.Sprintf("test_%d_%d@unittest.com", time.Now().UnixMilli(), rand.Intn(10000))
}

func testAuthPassword(email, password string) string {
	sum := sha256.Sum256([]byte(password + strings.ToLower(strings.TrimSpace(email))))
	return fmt.Sprintf("%x", sum)
}

func TestAuthService_Register(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := auth.NewAuthService(testDB, testRedis, jwtCfg)
	ctx := context.Background()

	email := uniqueEmail()
	req := &auth.RegisterRequest{
		Email:    email,
		Password: testAuthPassword(email, "Test@123456"),
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
		Password: testAuthPassword(email, "Test@123456"),
		Name:     "Dup Test User",
	}

	// First registration succeeds.
	_, _ = svc.Register(ctx, req)
	// Duplicate registration should fail.
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
	// Register before login.
	_, err := svc.Register(ctx, &auth.RegisterRequest{
		Email:    email,
		Password: testAuthPassword(email, password),
		Name:     "Login Test User",
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	// 鐧诲綍
	loginReq := &auth.LoginRequest{
		Email:    email,
		Password: testAuthPassword(email, password),
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
		Password: testAuthPassword(email, "Test@123456"),
		Name:     "Wrong Pwd User",
	})

	_, err := svc.Login(ctx, &auth.LoginRequest{
		Email:    email,
		Password: testAuthPassword(email, "WrongPassword123"),
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
		Password: testAuthPassword("nonexistent@unittest.com", "Test@123456"),
	})
	if err == nil {
		t.Error("login with non-existent user should fail")
	}
}

func TestAuthService_ValidateToken(t *testing.T) {
	t.Skip("ValidateToken was folded into middleware parsing; login/refresh coverage remains in service and API tests")
}

func TestAuthService_ValidateToken_Invalid(t *testing.T) {
	t.Skip("ValidateToken was folded into middleware parsing; invalid token coverage remains in middleware/API tests")
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
		Password: testAuthPassword(email, "Test@123456"),
		Name:     "Refresh Token User",
	})

	tokenPair, _ := svc.Login(ctx, &auth.LoginRequest{
		Email:    email,
		Password: testAuthPassword(email, "Test@123456"),
	})

	// 鍒锋柊浠ょ墝
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
	t.Skip("ChangePassword service API was removed; password reset is covered by admin/API flows")
}

func TestAuthService_ChangePassword_WrongOld(t *testing.T) {
	t.Skip("ChangePassword service API was removed; password reset is covered by admin/API flows")
}

// 回归测试：注册时 invite_code 解析的三表 fallback
// Bug 历史：v3.1 推荐链路改造后，注册逻辑只查 tenants.domain，导致用户分享的
// referral_link.code（如 egRtFJBi）注册时报 "invalid invite code"。
// 修复后应优先白标 tenant，未命中再回退到 referral_links / users.referral_code。

// uniqueRefCode 生成测试用的唯一邀请码（≤20 字符）
func uniqueRefCode() string {
	return fmt.Sprintf("rk%d%d", time.Now().UnixNano()%1e8, rand.Intn(1000))
}

func TestAuthService_Register_InviteCodeFromReferralLink(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}
	_ = testDB.AutoMigrate(&model.ReferralLink{})

	svc := auth.NewAuthService(testDB, testRedis, jwtCfg)
	ctx := context.Background()

	// 准备：注册一个邀请人，并为其创建 referral_link
	inviterEmail := uniqueEmail()
	inviter, err := svc.Register(ctx, &auth.RegisterRequest{
		Email:    inviterEmail,
		Password: testAuthPassword(inviterEmail, "Test@123456"),
		Name:     "Inviter",
	})
	if err != nil {
		t.Fatalf("inviter register failed: %v", err)
	}
	link := &model.ReferralLink{
		UserID:   inviter.ID,
		TenantID: inviter.TenantID,
		Code:     uniqueRefCode(),
	}
	if err := testDB.Create(link).Error; err != nil {
		t.Fatalf("create referral_link failed: %v", err)
	}

	// 执行：用 referral_link.code 作为 invite_code 注册新用户
	inviteeEmail := uniqueEmail()
	invitee, err := svc.Register(ctx, &auth.RegisterRequest{
		Email:      inviteeEmail,
		Password:   testAuthPassword(inviteeEmail, "Test@123456"),
		Name:       "Invitee FromLink",
		InviteCode: link.Code,
	})
	if err != nil {
		t.Fatalf("invitee register with referral_link code failed: %v", err)
	}
	if invitee == nil || invitee.ID == 0 {
		t.Fatal("invitee should be created")
	}
	// 落到默认租户（不是邀请人的 tenant，因为 referral_link.code != tenants.domain）
	if invitee.TenantID == 0 {
		t.Error("invitee should have a tenant (default tenant)")
	}
}

func TestAuthService_Register_InviteCodeFromUserReferralCode(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := auth.NewAuthService(testDB, testRedis, jwtCfg)
	ctx := context.Background()

	// 准备：注册邀请人后，给其 users.referral_code 字段赋值
	inviterEmail := uniqueEmail()
	inviter, err := svc.Register(ctx, &auth.RegisterRequest{
		Email:    inviterEmail,
		Password: testAuthPassword(inviterEmail, "Test@123456"),
		Name:     "Inviter UserCode",
	})
	if err != nil {
		t.Fatalf("inviter register failed: %v", err)
	}
	refCode := uniqueRefCode()
	if err := testDB.Model(&model.User{}).Where("id = ?", inviter.ID).
		Update("referral_code", refCode).Error; err != nil {
		t.Fatalf("set inviter referral_code failed: %v", err)
	}

	// 执行：用 users.referral_code 作为 invite_code
	inviteeEmail := uniqueEmail()
	invitee, err := svc.Register(ctx, &auth.RegisterRequest{
		Email:      inviteeEmail,
		Password:   testAuthPassword(inviteeEmail, "Test@123456"),
		Name:       "Invitee FromUserCode",
		InviteCode: refCode,
	})
	if err != nil {
		t.Fatalf("invitee register with users.referral_code failed: %v", err)
	}
	if invitee == nil || invitee.ID == 0 {
		t.Fatal("invitee should be created")
	}
}

func TestAuthService_Register_InviteCodeInvalid(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := auth.NewAuthService(testDB, testRedis, jwtCfg)
	ctx := context.Background()

	email := uniqueEmail()
	_, err := svc.Register(ctx, &auth.RegisterRequest{
		Email:      email,
		Password:   testAuthPassword(email, "Test@123456"),
		Name:       "Invalid Invite",
		InviteCode: "noSuchCodeXYZ",
	})
	if err == nil {
		t.Fatal("registration with non-existent invite_code should fail")
	}
	if !strings.Contains(err.Error(), "invalid invite code") {
		t.Errorf("expected 'invalid invite code' error, got: %v", err)
	}
}
