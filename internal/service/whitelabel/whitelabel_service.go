package whitelabel

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	redisPkg "tokenhub-server/internal/pkg/redis"
)

const (
	whitelabelCachePrefix = "whitelabel:"
	whitelabelCacheTTL    = 10 * time.Minute
)

// WhiteLabelConfig 租户白标品牌配置
type WhiteLabelConfig struct {
	TenantID     uint   `json:"tenant_id"`
	Domain       string `json:"domain"`
	BrandName    string `json:"brand_name"`
	LogoURL      string `json:"logo_url"`
	FaviconURL   string `json:"favicon_url"`
	PrimaryColor string `json:"primary_color"`
	FooterText   string `json:"footer_text"`
	CustomCSS    string `json:"custom_css"`
}

// PublicWhiteLabelConfig 公开的白标配置子集（不含敏感信息）
type PublicWhiteLabelConfig struct {
	BrandName    string `json:"brand_name"`
	LogoURL      string `json:"logo_url"`
	FaviconURL   string `json:"favicon_url"`
	PrimaryColor string `json:"primary_color"`
	FooterText   string `json:"footer_text"`
	CustomCSS    string `json:"custom_css"`
}

// WhiteLabelService 白标品牌配置管理服务
type WhiteLabelService struct {
	db    *gorm.DB
	redis *goredis.Client
}

// NewWhiteLabelService 创建白标服务实例
func NewWhiteLabelService(db *gorm.DB, redis *goredis.Client) *WhiteLabelService {
	return &WhiteLabelService{db: db, redis: redis}
}

// GetConfig 获取指定租户的白标配置
func (s *WhiteLabelService) GetConfig(ctx context.Context, tenantID uint) (*WhiteLabelConfig, error) {
	if tenantID == 0 {
		return nil, fmt.Errorf("tenant_id is required")
	}

	// Try cache first
	cacheKey := fmt.Sprintf("%s%d", whitelabelCachePrefix, tenantID)
	var cached WhiteLabelConfig
	if err := redisPkg.GetJSON(ctx, cacheKey, &cached); err == nil {
		return &cached, nil
	}

	var tenant model.Tenant
	if err := s.db.WithContext(ctx).First(&tenant, tenantID).Error; err != nil {
		return nil, fmt.Errorf("tenant not found: %w", err)
	}

	cfg := tenantToConfig(&tenant)

	// Cache the result
	_ = redisPkg.SetJSON(ctx, cacheKey, cfg, whitelabelCacheTTL)

	return cfg, nil
}

// UpdateConfig 更新租户的白标品牌配置
func (s *WhiteLabelService) UpdateConfig(ctx context.Context, tenantID uint, cfg *WhiteLabelConfig) error {
	if tenantID == 0 {
		return fmt.Errorf("tenant_id is required")
	}
	if cfg == nil {
		return fmt.Errorf("config is required")
	}

	// Validate domain if provided
	if cfg.Domain != "" {
		if err := s.ValidateDomain(ctx, cfg.Domain, tenantID); err != nil {
			return err
		}
	}

	// Validate primary color format
	if cfg.PrimaryColor != "" {
		if !isValidHexColor(cfg.PrimaryColor) {
			return fmt.Errorf("invalid primary_color format, expected #hex (e.g. #FF5500)")
		}
	}

	// Build update map
	updates := map[string]interface{}{
		"domain":   cfg.Domain,
		"logo_url": cfg.LogoURL,
		"name":     cfg.BrandName,
	}

	result := s.db.WithContext(ctx).Model(&model.Tenant{}).Where("id = ?", tenantID).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to update tenant: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("tenant not found")
	}

	// Invalidate caches
	cacheKey := fmt.Sprintf("%s%d", whitelabelCachePrefix, tenantID)
	_ = redisPkg.Del(ctx, cacheKey)

	// Also invalidate domain cache if domain changed
	if cfg.Domain != "" {
		_ = redisPkg.Del(ctx, fmt.Sprintf("%s%s", domainCachePrefix, cfg.Domain))
	}

	return nil
}

// ValidateDomain 校验域名格式和唯一性
func (s *WhiteLabelService) ValidateDomain(ctx context.Context, domain string, excludeTenantID uint) error {
	if domain == "" {
		return fmt.Errorf("domain is required")
	}

	domain = strings.ToLower(strings.TrimSpace(domain))

	// Basic domain format validation
	domainRegex := regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)
	if !domainRegex.MatchString(domain) {
		return fmt.Errorf("invalid domain format: %s", domain)
	}

	// Check uniqueness
	var count int64
	query := s.db.WithContext(ctx).Model(&model.Tenant{}).Where("domain = ?", domain)
	if excludeTenantID > 0 {
		query = query.Where("id != ?", excludeTenantID)
	}
	if err := query.Count(&count).Error; err != nil {
		return fmt.Errorf("failed to check domain: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("domain %s is already in use", domain)
	}

	return nil
}

// GetPublicConfig 获取公开的白标配置（不含敏感信息）
func (s *WhiteLabelService) GetPublicConfig(ctx context.Context, tenantID uint) (*PublicWhiteLabelConfig, error) {
	cfg, err := s.GetConfig(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return &PublicWhiteLabelConfig{
		BrandName:    cfg.BrandName,
		LogoURL:      cfg.LogoURL,
		FaviconURL:   cfg.FaviconURL,
		PrimaryColor: cfg.PrimaryColor,
		FooterText:   cfg.FooterText,
		CustomCSS:    cfg.CustomCSS,
	}, nil
}

// tenantToConfig 将 Tenant 模型转换为 WhiteLabelConfig
func tenantToConfig(t *model.Tenant) *WhiteLabelConfig {
	return &WhiteLabelConfig{
		TenantID:  t.ID,
		Domain:    t.Domain,
		BrandName: t.Name,
		LogoURL:   t.LogoURL,
	}
}

// isValidHexColor 校验是否为有效的十六进制颜色值（如 #FF5500）
func isValidHexColor(s string) bool {
	matched, _ := regexp.MatchString(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`, s)
	return matched
}
