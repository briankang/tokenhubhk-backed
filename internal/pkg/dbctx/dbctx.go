// Package dbctx 提供带默认超时的 DB 上下文 helper。
//
// 背景：生产 502 定位发现多处 public / user 端点直接 `db.First()` / `db.Find()`
// 而未用 WithContext + 超时，MySQL 慢查询会导致 handler 永久阻塞，连带 Pod
// readiness probe 失败。
//
// 使用方式：
//
//	ctx, cancel := dbctx.Timeout(c.Request.Context(), 2*time.Second)
//	defer cancel()
//	db.WithContext(ctx).Where(...).First(&obj)
//
// 或者用便捷变体：
//
//	db.WithContext(dbctx.Short(c.Request.Context())).First(&obj)
package dbctx

import (
	"context"
	"time"
)

// 默认超时级别
const (
	ShortTimeout  = 2 * time.Second  // 单表 First/Count
	MediumTimeout = 5 * time.Second  // JOIN / 聚合
	LongTimeout   = 15 * time.Second // 报表 / 导出前半程
)

// Timeout 返回带自定义超时的子 ctx 和 cancel。
// 注意 cancel 必须调用（通常 defer cancel()）。
func Timeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if d <= 0 {
		d = ShortTimeout
	}
	return context.WithTimeout(parent, d)
}

// Short 2 秒超时。适用于单表简单 First/Count/Exists 查询。
// 调用者需 defer cancel()：
//
//	ctx, cancel := dbctx.Short(c.Request.Context())
//	defer cancel()
func Short(parent context.Context) (context.Context, context.CancelFunc) {
	return Timeout(parent, ShortTimeout)
}

// Medium 5 秒超时。适用于 JOIN / 聚合 / 多表查询。
func Medium(parent context.Context) (context.Context, context.CancelFunc) {
	return Timeout(parent, MediumTimeout)
}

// Long 15 秒超时。适用于报表查询首段（不含全量导出）。
func Long(parent context.Context) (context.Context, context.CancelFunc) {
	return Timeout(parent, LongTimeout)
}
