// Package usercache 封装用户维度的 Redis 缓存读写 + 失效。
//
// Key 命名约定（统一前缀方便 SCAN 批量清理）：
//
//	user:profile:{uid}       5min   /user/profile 完整响应
//	user:balance:{uid}       3min   UserBalance 记录
//	user:apikeys:{uid}       10min  /user/api-keys 列表
//	user:notif:unread:{uid}  2min   未读通知数量
//
// 写操作应在 service 层末尾主动调用 Invalidate*，保证用户下次 GET 能看到最新状态。
package usercache

import (
	"context"
	"fmt"
	"time"

	"tokenhub-server/internal/pkg/cachehelper"
)

// TTL 定义（可通过后台缓存面板"清理用户缓存"立即失效）
const (
	TTLProfile     = 5 * time.Minute
	TTLBalance     = 3 * time.Minute
	TTLApiKeys     = 10 * time.Minute
	TTLNotifUnread = 2 * time.Minute
)

// Key 前缀
const (
	PrefixProfile     = "user:profile:"
	PrefixBalance     = "user:balance:"
	PrefixApiKeys     = "user:apikeys:"
	PrefixNotifUnread = "user:notif:unread:"
)

// Key 生成器
func KeyProfile(uid uint) string     { return fmt.Sprintf("%s%d", PrefixProfile, uid) }
func KeyBalance(uid uint) string     { return fmt.Sprintf("%s%d", PrefixBalance, uid) }
func KeyApiKeys(uid uint) string     { return fmt.Sprintf("%s%d", PrefixApiKeys, uid) }
func KeyNotifUnread(uid uint) string { return fmt.Sprintf("%s%d", PrefixNotifUnread, uid) }

// GetOrLoadProfile 读取/回源 用户 profile 缓存
func GetOrLoadProfile[T any](ctx context.Context, uid uint, loader cachehelper.Loader[T]) (T, error) {
	return cachehelper.GetOrLoad[T](ctx, KeyProfile(uid), TTLProfile, loader)
}

// GetOrLoadBalance 读取/回源 用户余额缓存
func GetOrLoadBalance[T any](ctx context.Context, uid uint, loader cachehelper.Loader[T]) (T, error) {
	return cachehelper.GetOrLoad[T](ctx, KeyBalance(uid), TTLBalance, loader)
}

// GetOrLoadApiKeys 读取/回源 用户 API Key 列表缓存
func GetOrLoadApiKeys[T any](ctx context.Context, uid uint, loader cachehelper.Loader[T]) (T, error) {
	return cachehelper.GetOrLoad[T](ctx, KeyApiKeys(uid), TTLApiKeys, loader)
}

// GetOrLoadNotifUnread 读取/回源 未读通知数量缓存
func GetOrLoadNotifUnread[T any](ctx context.Context, uid uint, loader cachehelper.Loader[T]) (T, error) {
	return cachehelper.GetOrLoad[T](ctx, KeyNotifUnread(uid), TTLNotifUnread, loader)
}

// InvalidateProfile 失效用户 profile 缓存
func InvalidateProfile(ctx context.Context, uid uint) {
	cachehelper.Invalidate(ctx, KeyProfile(uid))
}

// InvalidateBalance 失效用户余额缓存
func InvalidateBalance(ctx context.Context, uid uint) {
	cachehelper.Invalidate(ctx, KeyBalance(uid))
}

// InvalidateApiKeys 失效用户 API Key 缓存
func InvalidateApiKeys(ctx context.Context, uid uint) {
	cachehelper.Invalidate(ctx, KeyApiKeys(uid))
}

// InvalidateNotifUnread 失效用户未读通知数量缓存
func InvalidateNotifUnread(ctx context.Context, uid uint) {
	cachehelper.Invalidate(ctx, KeyNotifUnread(uid))
}

// InvalidateAll 失效该用户全部缓存（登出、角色变更等场景）
func InvalidateAll(ctx context.Context, uid uint) {
	cachehelper.Invalidate(ctx,
		KeyProfile(uid),
		KeyBalance(uid),
		KeyApiKeys(uid),
		KeyNotifUnread(uid),
	)
}

// InvalidatePatternAll 清理全部用户维度缓存（管理员后台操作）
//
// 返回删除的 key 数量。
func InvalidatePatternAll(ctx context.Context) (int, error) {
	total := 0
	for _, p := range []string{PrefixProfile + "*", PrefixBalance + "*", PrefixApiKeys + "*", PrefixNotifUnread + "*"} {
		n, err := cachehelper.InvalidatePattern(ctx, p, 500)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
