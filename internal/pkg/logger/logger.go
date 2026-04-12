package logger

import (
	"context"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

const requestIDKey = "X-Request-ID"

var (
	// L 全局日志器实例
	L      *zap.Logger
	Sugar  *zap.SugaredLogger
	access *zap.Logger
	slow   *zap.Logger
	audit  *zap.Logger
)

// Config 日志配置结构体
type Config struct {
	Level      string
	Dir        string
	MaxSize    int // MB
	MaxAge     int // days
	MaxBackups int
}

// Init 初始化全局日志器，按类型分离日志文件
func Init(cfg Config) error {
	if cfg.Dir == "" {
		cfg.Dir = "./logs"
	}
	if cfg.MaxSize == 0 {
		cfg.MaxSize = 100
	}
	if cfg.MaxAge == 0 {
		cfg.MaxAge = 30
	}
	if cfg.MaxBackups == 0 {
		cfg.MaxBackups = 5
	}

	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return err
	}

	level := parseLevel(cfg.Level)
	encoderCfg := productionEncoderConfig()

	// app.log - 应用主日志 (info及以上)
	appWriter := newLumberjack(filepath.Join(cfg.Dir, "app.log"), cfg)
	appCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.AddSync(appWriter),
		level,
	)

	// error.log - 错误及以上级别日志
	errWriter := newLumberjack(filepath.Join(cfg.Dir, "error.log"), cfg)
	errCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.AddSync(errWriter),
		zap.ErrorLevel,
	)

	// 组合核心：app + error + stdout
	stdoutCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderCfg),
		zapcore.AddSync(os.Stdout),
		level,
	)

	L = zap.New(
		zapcore.NewTee(appCore, errCore, stdoutCore),
		zap.AddCaller(),
		zap.AddStacktrace(zap.ErrorLevel),
	)
	Sugar = L.Sugar()

	// access.log - HTTP请求日志
	accessWriter := newLumberjack(filepath.Join(cfg.Dir, "access.log"), cfg)
	access = zap.New(zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.AddSync(accessWriter),
		zap.InfoLevel,
	))

	// slow.log - 慢请求日志
	slowWriter := newLumberjack(filepath.Join(cfg.Dir, "slow.log"), cfg)
	slow = zap.New(zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.AddSync(slowWriter),
		zap.WarnLevel,
	))

	// audit.log - 审计日志
	auditWriter := newLumberjack(filepath.Join(cfg.Dir, "audit.log"), cfg)
	audit = zap.New(zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.AddSync(auditWriter),
		zap.InfoLevel,
	))

	return nil
}

// Access 返回访问日志记录器
func Access() *zap.Logger {
	if access == nil {
		return zap.NewNop()
	}
	return access
}

// Slow 返回慢查询日志记录器
func Slow() *zap.Logger {
	if slow == nil {
		return zap.NewNop()
	}
	return slow
}

// Audit 返回审计日志记录器
func Audit() *zap.Logger {
	if audit == nil {
		return zap.NewNop()
	}
	return audit
}

// WithRequestID 创建包含请求ID的子日志器
func WithRequestID(ctx context.Context) *zap.Logger {
	if ctx == nil {
		return L
	}
	if rid, ok := ctx.Value(requestIDKey).(string); ok && rid != "" {
		return L.With(zap.String("request_id", rid))
	}
	return L
}

// Sync 刷新所有日志器缓冲
func Sync() {
	if L != nil {
		_ = L.Sync()
	}
	if access != nil {
		_ = access.Sync()
	}
	if slow != nil {
		_ = slow.Sync()
	}
	if audit != nil {
		_ = audit.Sync()
	}
}

func newLumberjack(path string, cfg Config) *lumberjack.Logger {
	return &lumberjack.Logger{
		Filename:   path,
		MaxSize:    cfg.MaxSize,
		MaxAge:     cfg.MaxAge,
		MaxBackups: cfg.MaxBackups,
		Compress:   true,
	}
}

func productionEncoderConfig() zapcore.EncoderConfig {
	return zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.MillisDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
}

func parseLevel(lvl string) zapcore.Level {
	switch lvl {
	case "debug":
		return zap.DebugLevel
	case "info":
		return zap.InfoLevel
	case "warn":
		return zap.WarnLevel
	case "error":
		return zap.ErrorLevel
	default:
		return zap.InfoLevel
	}
}
