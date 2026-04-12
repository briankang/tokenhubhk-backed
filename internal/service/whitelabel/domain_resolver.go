package whitelabel

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	redisPkg "tokenhub-server/internal/pkg/redis"
)

const (
	domainCachePrefix = "domain:"
	domainCacheTTL    = 30 * time.Minute
)

// DomainResolver 域名解析器，通过自定义域名匹配租户
type DomainResolver struct {
	db    *gorm.DB
	redis *goredis.Client
}

// NewDomainResolver 创建域名解析器实例
func NewDomainResolver(db *gorm.DB, redis *goredis.Client) *DomainResolver {
	return &DomainResolver{db: db, redis: redis}
}

// Resolve 根据自定义域名查找对应租户
// 1. 优先从 Redis 缓存查找（key: "domain:{domain}", TTL: 30 min）
// 2. 缓存未命中则查询 Tenant 表
// 3. 找到则返回租户
func (r *DomainResolver) Resolve(ctx context.Context, domain string) (*model.Tenant, error) {
	if domain == "" {
		return nil, fmt.Errorf("domain is empty")
	}

	// Try Redis cache
	cacheKey := domainCachePrefix + domain
	var cached model.Tenant
	if err := redisPkg.GetJSON(ctx, cacheKey, &cached); err == nil {
		return &cached, nil
	}

	// Cache miss – query DB
	var tenant model.Tenant
	if err := r.db.WithContext(ctx).Where("domain = ? AND is_active = ?", domain, true).First(&tenant).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil // not found is not an error; the middleware decides how to handle
		}
		return nil, fmt.Errorf("failed to resolve domain %s: %w", domain, err)
	}

	// Write to cache
	_ = redisPkg.SetJSON(ctx, cacheKey, &tenant, domainCacheTTL)

	return &tenant, nil
}

// InvalidateCache 清除指定域名的缓存映射
func (r *DomainResolver) InvalidateCache(ctx context.Context, domain string) error {
	if domain == "" {
		return nil
	}
	return redisPkg.Del(ctx, domainCachePrefix+domain)
}
