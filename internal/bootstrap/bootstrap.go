// Package bootstrap 提供各服务角色共享的初始化逻辑。
// gateway / backend / worker / monolith 四种角色均通过此包完成基础设施初始化，
// 避免在各 cmd/ 入口中重复初始化代码。
package bootstrap

import (
	"context"
	"fmt"
	"os"

	"go.uber.org/zap"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/database"
	pkgi18n "tokenhub-server/internal/pkg/i18n"
	"tokenhub-server/internal/pkg/logger"
	pkgredis "tokenhub-server/internal/pkg/redis"
)

// Result 保存初始化后的基础设施引用，供调用方获取清理函数
type Result struct {
	// Cleanup 在 graceful shutdown 时调用，依次关闭 DB → Redis → Logger
	Cleanup func()
}

// InitConfig 加载配置文件，支持 CONFIG_FILE 环境变量覆盖路径
func InitConfig() error {
	cfgFile := "configs/config.yaml"
	if envCfg := os.Getenv("CONFIG_FILE"); envCfg != "" {
		cfgFile = envCfg
	}
	return config.Load(cfgFile)
}

// InitLogger 初始化 Zap 结构化日志
func InitLogger() error {
	return logger.Init(logger.Config{
		Level:      config.Global.Log.Level,
		Dir:        config.Global.Log.Dir,
		MaxSize:    config.Global.Log.MaxSize,
		MaxAge:     config.Global.Log.MaxAge,
		MaxBackups: config.Global.Log.MaxBackups,
	})
}

// InitDatabase 初始化 MySQL 连接。
// 如果 runMigrations=true，执行 AutoMigrate + 种子数据（仅 backend/monolith 需要）。
func InitDatabase(runMigrations bool) error {
	if err := database.Init(config.Global.Database, logger.L); err != nil {
		return fmt.Errorf("database init: %w", err)
	}

	if runMigrations {
		runSeeds()
	}

	return nil
}

// runSeeds 执行所有种子数据和数据迁移（从 cmd/server/main.go 中提取）
func runSeeds() {
	database.RunSeed(database.DB)
	database.RunSeedDocs(database.DB)
	database.RunSeedLevels(database.DB)
	database.RunSeedParams()
	database.RunCachePriceMigration(database.DB)

	if err := database.MigrateChannelCapabilities(database.DB); err != nil {
		logger.L.Warn("channel capabilities migration failed", zap.Error(err))
	}
	if err := database.DropAgentTables(database.DB); err != nil {
		logger.L.Warn("drop agent tables migration failed", zap.Error(err))
	}

	database.RunSeedNonTokenModels(database.DB)
	database.RunSeedQianfan(database.DB)
	database.RunSeedHunyuan(database.DB)

	if err := database.MigrateVolcengineBatch8Deprecation(database.DB); err != nil {
		logger.L.Warn("volcengine batch8 deprecation migration failed", zap.Error(err))
	}
}

// InitRedis 初始化 Redis 连接
func InitRedis() error {
	if err := pkgredis.Init(pkgredis.Config{
		Addr:     config.Global.Redis.Addr,
		Password: config.Global.Redis.Password,
		DB:       config.Global.Redis.DB,
	}); err != nil {
		return fmt.Errorf("redis init: %w", err)
	}
	return nil
}

// PostRedisInit 在 Redis 初始化后执行的清理操作（种子数据更新后清除等级缓存）
func PostRedisInit() {
	_ = pkgredis.Client.Del(context.Background(), "member:levels:all", "agent:levels:all").Err()
}

// InitI18n 初始化国际化
func InitI18n() error {
	return pkgi18n.Init(pkgi18n.Config{
		DefaultLang: config.Global.I18n.DefaultLang,
		LocalesDir:  config.Global.I18n.LocalesDir,
	})
}

// InitAll 按顺序初始化所有基础设施，返回 Result 供 graceful shutdown 使用。
// 这是最常用的一站式初始化方法。
func InitAll() (*Result, error) {
	// 1. 配置
	if err := InitConfig(); err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	// 2. 日志
	if err := InitLogger(); err != nil {
		return nil, fmt.Errorf("init logger: %w", err)
	}
	logger.L.Info("logger initialized",
		zap.String("role", config.Global.Service.Role),
	)

	// 3. 数据库
	shouldMigrate := config.Global.Service.ShouldRunMigrations()
	if err := InitDatabase(shouldMigrate); err != nil {
		logger.L.Fatal("init database failed", zap.Error(err))
	}
	logger.L.Info("database initialized",
		zap.Bool("migrations", shouldMigrate),
	)

	// 4. Redis
	if err := InitRedis(); err != nil {
		logger.L.Fatal("init redis failed", zap.Error(err))
	}
	logger.L.Info("redis initialized")

	// 4.1 种子数据更新后清缓存（Redis 初始化后执行）
	if shouldMigrate {
		PostRedisInit()
	}

	// 5. i18n（gateway 不需要 locale 文件，但初始化不影响）
	if err := InitI18n(); err != nil {
		logger.L.Fatal("init i18n failed", zap.Error(err))
	}
	logger.L.Info("i18n initialized")

	return &Result{
		Cleanup: func() {
			if err := database.Close(); err != nil {
				logger.L.Error("close database failed", zap.Error(err))
			}
			if err := pkgredis.Close(); err != nil {
				logger.L.Error("close redis failed", zap.Error(err))
			}
			logger.Sync()
		},
	}, nil
}
