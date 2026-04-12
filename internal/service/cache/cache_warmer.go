package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// TTL 分级常量
const (
	TTLLong     = 2 * time.Hour   // 长缓存: 模型列表、定价、供应商、付款方式
	TTLStandard = 1 * time.Hour   // 标准缓存: 渠道组、系统配置、用户信息
	TTLShort    = 5 * time.Minute // 短缓存: 用量统计、消费汇总、余额
)

// CacheWarmer 缓存预热器，服务启动时加载高频数据到 Redis
type CacheWarmer struct {
	db      *gorm.DB
	cacheSvc *CacheService
}

// NewCacheWarmer 创建缓存预热器实例
func NewCacheWarmer(db *gorm.DB, cacheSvc *CacheService) *CacheWarmer {
	return &CacheWarmer{db: db, cacheSvc: cacheSvc}
}

// WarmAll 预热所有高频缓存数据，记录预热耗时和结果
func (w *CacheWarmer) WarmAll(ctx context.Context) {
	start := time.Now()
	logger.L.Info("缓存预热开始...")

	var totalItems int
	var errors []string

	// 1. 预热模型列表
	if n, err := w.warmModels(ctx); err != nil {
		errors = append(errors, fmt.Sprintf("模型列表: %v", err))
	} else {
		totalItems += n
	}

	// 2. 预热模型定价
	if n, err := w.warmModelPricings(ctx); err != nil {
		errors = append(errors, fmt.Sprintf("模型定价: %v", err))
	} else {
		totalItems += n
	}

	// 3. 预热供应商列表
	if n, err := w.warmSuppliers(ctx); err != nil {
		errors = append(errors, fmt.Sprintf("供应商列表: %v", err))
	} else {
		totalItems += n
	}

	// 4. 预热渠道组配置
	if n, err := w.warmChannelGroups(ctx); err != nil {
		errors = append(errors, fmt.Sprintf("渠道组配置: %v", err))
	} else {
		totalItems += n
	}

	// 5. 预热付款方式列表
	if n, err := w.warmPaymentMethods(ctx); err != nil {
		errors = append(errors, fmt.Sprintf("付款方式: %v", err))
	} else {
		totalItems += n
	}

	// 6. 预热系统配置（模型分类）
	if n, err := w.warmModelCategories(ctx); err != nil {
		errors = append(errors, fmt.Sprintf("模型分类: %v", err))
	} else {
		totalItems += n
	}

	elapsed := time.Since(start)

	if len(errors) > 0 {
		logger.L.Warn("缓存预热完成（部分失败）",
			zap.Int("total_items", totalItems),
			zap.Duration("elapsed", elapsed),
			zap.Strings("errors", errors))
	} else {
		logger.L.Info("缓存预热完成",
			zap.Int("total_items", totalItems),
			zap.Duration("elapsed", elapsed))
	}
}

// warmModels 预热 AI 模型列表（长缓存 2h）
func (w *CacheWarmer) warmModels(ctx context.Context) (int, error) {
	var models []model.AIModel
	if err := w.db.WithContext(ctx).
		Preload("Category").
		Preload("Supplier").
		Where("is_active = ?", true).
		Order("id ASC").
		Find(&models).Error; err != nil {
		return 0, fmt.Errorf("查询模型列表失败: %w", err)
	}

	data, err := json.Marshal(models)
	if err != nil {
		return 0, fmt.Errorf("序列化模型列表失败: %w", err)
	}

	key := "cache:warm:models:list"
	if err := w.cacheSvc.Set(ctx, key, data, TTLLong); err != nil {
		return 0, fmt.Errorf("写入缓存失败: %w", err)
	}

	logger.L.Info("预热模型列表完成", zap.Int("count", len(models)))
	return len(models), nil
}

// warmModelPricings 预热模型定价列表（长缓存 2h）
func (w *CacheWarmer) warmModelPricings(ctx context.Context) (int, error) {
	var pricings []model.ModelPricing
	if err := w.db.WithContext(ctx).
		Preload("Model").
		Find(&pricings).Error; err != nil {
		return 0, fmt.Errorf("查询模型定价失败: %w", err)
	}

	data, err := json.Marshal(pricings)
	if err != nil {
		return 0, fmt.Errorf("序列化定价失败: %w", err)
	}

	key := "cache:warm:model-pricings:list"
	if err := w.cacheSvc.Set(ctx, key, data, TTLLong); err != nil {
		return 0, fmt.Errorf("写入缓存失败: %w", err)
	}

	logger.L.Info("预热模型定价完成", zap.Int("count", len(pricings)))
	return len(pricings), nil
}

// warmSuppliers 预热供应商列表（长缓存 2h）
func (w *CacheWarmer) warmSuppliers(ctx context.Context) (int, error) {
	var suppliers []model.Supplier
	if err := w.db.WithContext(ctx).
		Where("is_active = ?", true).
		Order("sort_order ASC, id DESC").
		Find(&suppliers).Error; err != nil {
		return 0, fmt.Errorf("查询供应商失败: %w", err)
	}

	data, err := json.Marshal(suppliers)
	if err != nil {
		return 0, fmt.Errorf("序列化供应商失败: %w", err)
	}

	key := "cache:warm:suppliers:list"
	if err := w.cacheSvc.Set(ctx, key, data, TTLLong); err != nil {
		return 0, fmt.Errorf("写入缓存失败: %w", err)
	}

	logger.L.Info("预热供应商列表完成", zap.Int("count", len(suppliers)))
	return len(suppliers), nil
}

// warmChannelGroups 预热渠道组配置（标准缓存 1h）
func (w *CacheWarmer) warmChannelGroups(ctx context.Context) (int, error) {
	var groups []model.ChannelGroup
	if err := w.db.WithContext(ctx).
		Where("is_active = ?", true).
		Find(&groups).Error; err != nil {
		return 0, fmt.Errorf("查询渠道组失败: %w", err)
	}

	data, err := json.Marshal(groups)
	if err != nil {
		return 0, fmt.Errorf("序列化渠道组失败: %w", err)
	}

	key := "cache:warm:channel-groups:list"
	if err := w.cacheSvc.Set(ctx, key, data, TTLStandard); err != nil {
		return 0, fmt.Errorf("写入缓存失败: %w", err)
	}

	logger.L.Info("预热渠道组配置完成", zap.Int("count", len(groups)))
	return len(groups), nil
}

// warmPaymentMethods 预热付款方式列表（长缓存 2h）
func (w *CacheWarmer) warmPaymentMethods(ctx context.Context) (int, error) {
	var methods []model.PaymentMethod
	if err := w.db.WithContext(ctx).
		Where("is_active = ?", true).
		Order("sort_order ASC").
		Find(&methods).Error; err != nil {
		return 0, fmt.Errorf("查询付款方式失败: %w", err)
	}

	data, err := json.Marshal(methods)
	if err != nil {
		return 0, fmt.Errorf("序列化付款方式失败: %w", err)
	}

	key := "cache:warm:payment-methods:list"
	if err := w.cacheSvc.Set(ctx, key, data, TTLLong); err != nil {
		return 0, fmt.Errorf("写入缓存失败: %w", err)
	}

	logger.L.Info("预热付款方式列表完成", zap.Int("count", len(methods)))
	return len(methods), nil
}

// warmModelCategories 预热模型分类列表（标准缓存 1h）
func (w *CacheWarmer) warmModelCategories(ctx context.Context) (int, error) {
	var categories []model.ModelCategory
	if err := w.db.WithContext(ctx).
		Order("sort_order ASC, id ASC").
		Find(&categories).Error; err != nil {
		return 0, fmt.Errorf("查询模型分类失败: %w", err)
	}

	data, err := json.Marshal(categories)
	if err != nil {
		return 0, fmt.Errorf("序列化模型分类失败: %w", err)
	}

	key := "cache:warm:model-categories:list"
	if err := w.cacheSvc.Set(ctx, key, data, TTLStandard); err != nil {
		return 0, fmt.Errorf("写入缓存失败: %w", err)
	}

	logger.L.Info("预热模型分类完成", zap.Int("count", len(categories)))
	return len(categories), nil
}
