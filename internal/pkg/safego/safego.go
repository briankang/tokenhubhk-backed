// Package safego 提供带 panic recover 的 goroutine 启动工具。
//
// 背景：未经 recover 保护的 goroutine 若 panic，整个进程直接 exit。
// 这是 Go 程序稳定性的常见杀手 —— 一个 cron 任务 / async consumer / background probe
// 里的空指针解引用，就会拖垮整个后端 Pod，表现为 ALB 502。
//
// 使用方式：
//
//	safego.Go(func() {
//	    // 后台任务代码
//	})
//
//	safego.GoCtx(ctx, "price-scraper", func(ctx context.Context) {
//	    for {
//	        select {
//	        case <-ctx.Done(): return
//	        case <-ticker.C: doScrape(ctx)
//	        }
//	    }
//	})
package safego

import (
	"context"
	"runtime/debug"

	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/logger"
)

// Go 启动带 recover 保护的 goroutine。fn panic 时写入 error 日志但不影响主进程。
// name 用于日志标识（便于排障），留空则填 "anonymous"。
func Go(name string, fn func()) {
	if name == "" {
		name = "anonymous"
	}
	go func() {
		defer Recover(name)
		fn()
	}()
}

// GoCtx 启动带 context 的 goroutine。ctx 用于让外部触发取消（graceful shutdown 等）。
// 如果 fn panic，goroutine 会被 recover 并结束，但 ctx 不会被主动 cancel（由外部控制）。
func GoCtx(ctx context.Context, name string, fn func(ctx context.Context)) {
	if name == "" {
		name = "anonymous"
	}
	go func() {
		defer Recover(name)
		fn(ctx)
	}()
}

// Recover 是给 defer 调用的 panic 拦截器。供外部手写 goroutine 时使用：
//
//	go func() {
//	    defer safego.Recover("my-task")
//	    doWork()
//	}()
func Recover(name string) {
	if r := recover(); r != nil {
		if logger.L != nil {
			logger.L.Error("safego: goroutine panicked",
				zap.String("task", name),
				zap.Any("panic", r),
				zap.String("stack", string(debug.Stack())),
			)
		}
	}
}

// Run 同步执行 fn，panic 时返回错误而非传播。
// 用于在同一协程内隔离可能 panic 的子逻辑（例如 consumer 循环中处理单条消息）。
func Run(name string, fn func()) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			if logger.L != nil {
				logger.L.Error("safego: Run panic recovered",
					zap.String("task", name),
					zap.Any("panic", r),
					zap.String("stack", string(debug.Stack())),
				)
			}
		}
	}()
	fn()
	return false
}
