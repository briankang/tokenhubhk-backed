package pricing

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// PricingService 定价服务，提供模型定价、等级折扣和代理商定价的CRUD操作
type PricingService struct {
	db         *gorm.DB
	calculator *PricingCalculator
}

// NewPricingService 创建定价服务实例，db和calculator不允许为nil
func NewPricingService(db *gorm.DB, calculator *PricingCalculator) *PricingService {
	if db == nil {
		panic("PricingService: db must not be nil")
	}
	if calculator == nil {
		panic("PricingService: calculator must not be nil")
	}
	return &PricingService{db: db, calculator: calculator}
}

// ---- ModelPricing CRUD ----

// SetModelPricing 创建或更新模型定价，已存在则更新价格，否则新建
func (s *PricingService) SetModelPricing(ctx context.Context, pricing *model.ModelPricing) error {
	if pricing == nil {
		return fmt.Errorf("pricing must not be nil")
	}
	if pricing.ModelID == 0 {
		return fmt.Errorf("model_id is required")
	}

	var existing model.ModelPricing
	err := s.db.WithContext(ctx).Where("model_id = ?", pricing.ModelID).First(&existing).Error
	if err == nil {
		// Update existing
		if err := s.db.WithContext(ctx).Model(&model.ModelPricing{}).Where("id = ?", existing.ID).Updates(map[string]interface{}{
			"input_price_per_token":  pricing.InputPricePerToken,
			"output_price_per_token": pricing.OutputPricePerToken,
			"currency":               pricing.Currency,
		}).Error; err != nil {
			return fmt.Errorf("update model pricing: %w", err)
		}
		pricing.ID = existing.ID
	} else if err == gorm.ErrRecordNotFound {
		if err := s.db.WithContext(ctx).Create(pricing).Error; err != nil {
			return fmt.Errorf("create model pricing: %w", err)
		}
	} else {
		return fmt.Errorf("query model pricing: %w", err)
	}

	s.calculator.InvalidateCache(ctx, pricing.ModelID, nil)
	// 配置售价后自动恢复被 NeedsSellPrice 标签禁用的模型
	s.autoEnableIfNeedsSellPrice(ctx, pricing.ModelID)
	return nil
}

// GetModelPricing 根据模型ID查询定价信息，不存在返回nil
func (s *PricingService) GetModelPricing(ctx context.Context, modelID uint) (*model.ModelPricing, error) {
	if modelID == 0 {
		return nil, fmt.Errorf("model_id is required")
	}
	var mp model.ModelPricing
	if err := s.db.WithContext(ctx).Preload("Model").Where("model_id = ?", modelID).First(&mp).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("query model pricing: %w", err)
	}
	return &mp, nil
}

// ListModelPricings 分页获取模型定价列表，返回列表和总数
func (s *PricingService) ListModelPricings(ctx context.Context, page, pageSize int) ([]model.ModelPricing, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var total int64
	if err := s.db.WithContext(ctx).Model(&model.ModelPricing{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count model pricings: %w", err)
	}

	var list []model.ModelPricing
	offset := (page - 1) * pageSize
	if err := s.db.WithContext(ctx).
		Preload("Model").
		Order("id DESC").
		Offset(offset).Limit(pageSize).
		Find(&list).Error; err != nil {
		return nil, 0, fmt.Errorf("list model pricings: %w", err)
	}
	return list, total, nil
}

// UpdateModelPricing 根据ID更新模型定价信息
func (s *PricingService) UpdateModelPricing(ctx context.Context, id uint, pricing *model.ModelPricing) error {
	if id == 0 {
		return fmt.Errorf("id is required")
	}
	if pricing == nil {
		return fmt.Errorf("pricing must not be nil")
	}

	var existing model.ModelPricing
	if err := s.db.WithContext(ctx).First(&existing, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("model pricing %d not found", id)
		}
		return fmt.Errorf("query model pricing: %w", err)
	}

	if err := s.db.WithContext(ctx).Model(&model.ModelPricing{}).Where("id = ?", existing.ID).Updates(map[string]interface{}{
		"input_price_per_token":  pricing.InputPricePerToken,
		"output_price_per_token": pricing.OutputPricePerToken,
		"currency":               pricing.Currency,
		"effective_from":         pricing.EffectiveFrom,
	}).Error; err != nil {
		return fmt.Errorf("update model pricing: %w", err)
	}

	s.calculator.InvalidateCache(ctx, existing.ModelID, nil)
	// 更新售价后同样自动恢复被 NeedsSellPrice 标签禁用的模型
	s.autoEnableIfNeedsSellPrice(ctx, existing.ModelID)
	return nil
}

// DeleteModelPricing 根据ID软删除模型定价记录
func (s *PricingService) DeleteModelPricing(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("id is required")
	}
	var existing model.ModelPricing
	if err := s.db.WithContext(ctx).First(&existing, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("model pricing %d not found", id)
		}
		return fmt.Errorf("query model pricing: %w", err)
	}
	if err := s.db.WithContext(ctx).Delete(&existing).Error; err != nil {
		return fmt.Errorf("delete model pricing: %w", err)
	}
	s.calculator.InvalidateCache(ctx, existing.ModelID, nil)
	return nil
}

// autoEnableIfNeedsSellPrice 若模型因未配置售价（NeedsSellPrice 标签）而被停用，
// 在售价配置完成后自动恢复 is_active=true 并移除该标签。
// 仅影响因缺少售价被禁用的模型，不会影响因其他原因（能力测试失败、管理员手动禁用）停用的模型。
func (s *PricingService) autoEnableIfNeedsSellPrice(ctx context.Context, modelID uint) {
	var m model.AIModel
	if err := s.db.WithContext(ctx).Select("id, is_active, tags").First(&m, modelID).Error; err != nil {
		return
	}
	if m.IsActive || !strings.Contains(m.Tags, "NeedsSellPrice") {
		return
	}
	newTags := removePricingTag(m.Tags, "NeedsSellPrice")
	s.db.WithContext(ctx).Table("ai_models").Where("id = ?", modelID).
		Updates(map[string]interface{}{"is_active": true, "tags": newTags})
}

// removePricingTag 从逗号分隔的标签字符串中移除指定标签
func removePricingTag(tags, tag string) string {
	if tags == "" {
		return ""
	}
	parts := strings.Split(tags, ",")
	result := parts[:0]
	for _, t := range parts {
		if strings.TrimSpace(t) != tag {
			result = append(result, t)
		}
	}
	return strings.Join(result, ",")
}

// ---- AgentLevelDiscount CRUD ----

// SetLevelDiscount 创建或更新等级折扣，级别范围1-3，支持按模型粒度配置
func (s *PricingService) SetLevelDiscount(ctx context.Context, discount *model.AgentLevelDiscount) error {
	if discount == nil {
		return fmt.Errorf("discount must not be nil")
	}
	if discount.Level < 1 || discount.Level > 3 {
		return fmt.Errorf("level must be between 1 and 3")
	}

	query := s.db.WithContext(ctx).Where("level = ?", discount.Level)
	if discount.ModelID != nil {
		query = query.Where("model_id = ?", *discount.ModelID)
	} else {
		query = query.Where("model_id IS NULL")
	}

	var existing model.AgentLevelDiscount
	err := query.First(&existing).Error
	if err == nil {
		if err := s.db.WithContext(ctx).Model(&model.AgentLevelDiscount{}).Where("id = ?", existing.ID).Updates(map[string]interface{}{
			"input_discount":  discount.InputDiscount,
			"output_discount": discount.OutputDiscount,
		}).Error; err != nil {
			return fmt.Errorf("update level discount: %w", err)
		}
		discount.ID = existing.ID
	} else if err == gorm.ErrRecordNotFound {
		if err := s.db.WithContext(ctx).Create(discount).Error; err != nil {
			return fmt.Errorf("create level discount: %w", err)
		}
	} else {
		return fmt.Errorf("query level discount: %w", err)
	}

	// Invalidate caches broadly since level discounts affect many tenants
	if discount.ModelID != nil {
		s.calculator.InvalidateCache(ctx, *discount.ModelID, nil)
	}
	return nil
}

// GetLevelDiscounts 获取指定等级的所有折扣配置
func (s *PricingService) GetLevelDiscounts(ctx context.Context, level int) ([]model.AgentLevelDiscount, error) {
	if level < 1 || level > 3 {
		return nil, fmt.Errorf("level must be between 1 and 3")
	}
	var list []model.AgentLevelDiscount
	if err := s.db.WithContext(ctx).
		Preload("Model").
		Where("level = ?", level).
		Find(&list).Error; err != nil {
		return nil, fmt.Errorf("list level discounts: %w", err)
	}
	return list, nil
}

// ListAllDiscounts 获取所有等级折扣配置
func (s *PricingService) ListAllDiscounts(ctx context.Context) ([]model.AgentLevelDiscount, error) {
	var list []model.AgentLevelDiscount
	if err := s.db.WithContext(ctx).
		Preload("Model").
		Order("level ASC, id ASC").
		Find(&list).Error; err != nil {
		return nil, fmt.Errorf("list all discounts: %w", err)
	}
	return list, nil
}

// UpdateLevelDiscount 根据ID更新等级折扣配置
func (s *PricingService) UpdateLevelDiscount(ctx context.Context, id uint, discount *model.AgentLevelDiscount) error {
	if id == 0 {
		return fmt.Errorf("id is required")
	}
	var existing model.AgentLevelDiscount
	if err := s.db.WithContext(ctx).First(&existing, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("level discount %d not found", id)
		}
		return fmt.Errorf("query level discount: %w", err)
	}

	if err := s.db.WithContext(ctx).Model(&model.AgentLevelDiscount{}).Where("id = ?", existing.ID).Updates(map[string]interface{}{
		"level":           discount.Level,
		"model_id":        discount.ModelID,
		"input_discount":  discount.InputDiscount,
		"output_discount": discount.OutputDiscount,
	}).Error; err != nil {
		return fmt.Errorf("update level discount: %w", err)
	}

	if existing.ModelID != nil {
		s.calculator.InvalidateCache(ctx, *existing.ModelID, nil)
	}
	return nil
}

// DeleteLevelDiscount 根据ID软删除等级折扣记录
func (s *PricingService) DeleteLevelDiscount(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("id is required")
	}
	var existing model.AgentLevelDiscount
	if err := s.db.WithContext(ctx).First(&existing, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("level discount %d not found", id)
		}
		return fmt.Errorf("query level discount: %w", err)
	}
	if err := s.db.WithContext(ctx).Delete(&existing).Error; err != nil {
		return fmt.Errorf("delete level discount: %w", err)
	}
	if existing.ModelID != nil {
		s.calculator.InvalidateCache(ctx, *existing.ModelID, nil)
	}
	return nil
}

// ---- AgentPricing CRUD ----

// SetAgentPricing 创建或更新代理商专属定价，支持FIXED/MARKUP/DISCOUNT/INHERIT四种类型
func (s *PricingService) SetAgentPricing(ctx context.Context, pricing *model.AgentPricing) error {
	if pricing == nil {
		return fmt.Errorf("pricing must not be nil")
	}
	if pricing.TenantID == 0 {
		return fmt.Errorf("tenant_id is required")
	}
	if pricing.ModelID == 0 {
		return fmt.Errorf("model_id is required")
	}
	validTypes := map[string]bool{"FIXED": true, "MARKUP": true, "DISCOUNT": true, "INHERIT": true}
	if !validTypes[pricing.PricingType] {
		return fmt.Errorf("invalid pricing_type: %s", pricing.PricingType)
	}

	var existing model.AgentPricing
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND model_id = ?", pricing.TenantID, pricing.ModelID).
		First(&existing).Error
	if err == nil {
		if err := s.db.WithContext(ctx).Model(&model.AgentPricing{}).Where("id = ?", existing.ID).Updates(map[string]interface{}{
			"pricing_type":  pricing.PricingType,
			"input_price":   pricing.InputPrice,
			"output_price":  pricing.OutputPrice,
			"markup_rate":   pricing.MarkupRate,
			"discount_rate": pricing.DiscountRate,
		}).Error; err != nil {
			return fmt.Errorf("update agent pricing: %w", err)
		}
		pricing.ID = existing.ID
	} else if err == gorm.ErrRecordNotFound {
		if err := s.db.WithContext(ctx).Create(pricing).Error; err != nil {
			return fmt.Errorf("create agent pricing: %w", err)
		}
	} else {
		return fmt.Errorf("query agent pricing: %w", err)
	}

	tid := pricing.TenantID
	s.calculator.InvalidateCache(ctx, pricing.ModelID, &tid)
	return nil
}

// GetAgentPricing 根据租户ID和模型ID查询代理商定价
func (s *PricingService) GetAgentPricing(ctx context.Context, tenantID, modelID uint) (*model.AgentPricing, error) {
	if tenantID == 0 || modelID == 0 {
		return nil, fmt.Errorf("tenant_id and model_id are required")
	}
	var ap model.AgentPricing
	if err := s.db.WithContext(ctx).
		Preload("Model").
		Where("tenant_id = ? AND model_id = ?", tenantID, modelID).
		First(&ap).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("query agent pricing: %w", err)
	}
	return &ap, nil
}

// ListAgentPricings 获取指定租户的所有代理商定价配置
func (s *PricingService) ListAgentPricings(ctx context.Context, tenantID uint) ([]model.AgentPricing, error) {
	if tenantID == 0 {
		return nil, fmt.Errorf("tenant_id is required")
	}
	var list []model.AgentPricing
	if err := s.db.WithContext(ctx).
		Preload("Model").
		Where("tenant_id = ?", tenantID).
		Order("id DESC").
		Find(&list).Error; err != nil {
		return nil, fmt.Errorf("list agent pricings: %w", err)
	}
	return list, nil
}

// UpdateAgentPricing 根据ID更新代理商定价配置
func (s *PricingService) UpdateAgentPricing(ctx context.Context, id uint, pricing *model.AgentPricing) error {
	if id == 0 {
		return fmt.Errorf("id is required")
	}
	if pricing == nil {
		return fmt.Errorf("pricing must not be nil")
	}

	var existing model.AgentPricing
	if err := s.db.WithContext(ctx).First(&existing, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("agent pricing %d not found", id)
		}
		return fmt.Errorf("query agent pricing: %w", err)
	}

	if err := s.db.WithContext(ctx).Model(&model.AgentPricing{}).Where("id = ?", existing.ID).Updates(map[string]interface{}{
		"pricing_type":  pricing.PricingType,
		"input_price":   pricing.InputPrice,
		"output_price":  pricing.OutputPrice,
		"markup_rate":   pricing.MarkupRate,
		"discount_rate": pricing.DiscountRate,
	}).Error; err != nil {
		return fmt.Errorf("update agent pricing: %w", err)
	}

	tid := existing.TenantID
	s.calculator.InvalidateCache(ctx, existing.ModelID, &tid)
	return nil
}

// DeleteAgentPricing 根据ID软删除代理商定价记录
func (s *PricingService) DeleteAgentPricing(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("id is required")
	}
	var existing model.AgentPricing
	if err := s.db.WithContext(ctx).First(&existing, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("agent pricing %d not found", id)
		}
		return fmt.Errorf("query agent pricing: %w", err)
	}
	if err := s.db.WithContext(ctx).Delete(&existing).Error; err != nil {
		return fmt.Errorf("delete agent pricing: %w", err)
	}
	tid := existing.TenantID
	s.calculator.InvalidateCache(ctx, existing.ModelID, &tid)
	return nil
}
