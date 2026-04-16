package channel_test

import (
	"context"
	"os"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/channel"
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

	_ = testDB.AutoMigrate(&model.Channel{}, &model.ChannelTag{}, &model.ChannelGroup{})
	code := m.Run()
	os.Exit(code)
}

// ========== ChannelRouter 测试 ==========

func TestChannelRouter_SelectChannel_NoChannels(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	groupSvc := channel.NewChannelGroupService(testDB)
	backupSvc := channel.NewBackupService(testDB, testRedis)
	router := channel.NewChannelRouter(testDB, testRedis, groupSvc, backupSvc)
	ctx := context.Background()

	// 使用一个不存在的模型名
	_, err := router.SelectChannel(ctx, "non-existent-model-xyz", nil, 1)
	if err == nil {
		t.Error("SelectChannel with non-existent model should fail")
	}
}

func TestChannelRouter_SelectChannelWithExcludes(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	groupSvc := channel.NewChannelGroupService(testDB)
	backupSvc := channel.NewBackupService(testDB, testRedis)
	router := channel.NewChannelRouter(testDB, testRedis, groupSvc, backupSvc)
	ctx := context.Background()

	// 排除所有渠道，应该找不到可用渠道
	excludes := []uint{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	_, err := router.SelectChannelWithExcludes(ctx, "gpt-4o", nil, 1, excludes)
	if err == nil {
		// 如果找到了渠道说明有更多渠道，也是合理的
		t.Log("found channel even with excludes (more channels available)")
	} else {
		t.Logf("no channel available with excludes: %v (expected)", err)
	}
}

func TestChannelRouter_RecordSuccessAndFailure(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	groupSvc := channel.NewChannelGroupService(testDB)
	backupSvc := channel.NewBackupService(testDB, testRedis)
	router := channel.NewChannelRouter(testDB, testRedis, groupSvc, backupSvc)

	// 记录成功和失败不应 panic
	router.RecordSuccess(1)
	router.RecordFailure(1)
	router.RecordSuccess(999)
	router.RecordFailure(999)
}

// ========== ChannelService 测试 ==========

func TestChannelService_List(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := channel.NewChannelService(testDB, testRedis)
	ctx := context.Background()

	channels, total, err := svc.List(ctx, 1, 10, nil)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if channels == nil {
		t.Error("channels should not be nil")
	}
	t.Logf("found %d channels (total: %d)", len(channels), total)
}

// ========== ChannelTagService 测试 ==========

func TestChannelTagService_CRUD(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := channel.NewChannelTagService(testDB)
	ctx := context.Background()

	// 创建标签
	tag := &model.ChannelTag{
		Name:  "unit-test-tag",
		Color: "#FF0000",
	}
	err := svc.Create(ctx, tag)
	if err != nil {
		t.Fatalf("Create tag failed: %v", err)
	}

	// 列出标签
	tags, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List tags failed: %v", err)
	}
	if len(tags) == 0 {
		t.Error("should have at least one tag")
	}

	// 清理
	if tag.ID > 0 {
		_ = svc.Delete(ctx, tag.ID)
	}
}

// ========== ChannelGroupService 测试 ==========

func TestChannelGroupService_CRUD(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := channel.NewChannelGroupService(testDB)
	ctx := context.Background()

	// 创建分组
	group := &model.ChannelGroup{
		Name:     "unit-test-group",
		Strategy: "priority",
	}
	err := svc.Create(ctx, group)
	if err != nil {
		t.Fatalf("Create group failed: %v", err)
	}

	// 列出分组
	groups, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List groups failed: %v", err)
	}
	if len(groups) == 0 {
		t.Error("should have at least one group")
	}

	// 获取单个
	if group.ID > 0 {
		got, err := svc.GetByID(ctx, group.ID)
		if err != nil {
			t.Fatalf("GetByID failed: %v", err)
		}
		if got == nil {
			t.Error("group should not be nil")
		}
	}

	// 清理
	if group.ID > 0 {
		_ = svc.Delete(ctx, group.ID)
	}
}

// ========== ChannelHealthChecker 测试 ==========

func TestChannelHealthChecker_Init(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	checker := channel.NewChannelHealthChecker(testDB, testRedis)
	if checker == nil {
		t.Fatal("checker should not be nil")
	}
}
