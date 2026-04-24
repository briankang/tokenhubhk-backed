package pricing

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// DiscountResult 折扣解析结果，包含租户/模型/代理层级/用户级的折扣信息
type DiscountResult struct {
	Type           string   `json:"type"`            // "user_custom" / "agent_custom" / "level_discount" / "none"
	PricingType    string   `json:"pricing_type"`    // FIXED / MARKUP / DISCOUNT / INHERIT / NONE
	InputDiscount  float64  `json:"input_discount"`  // e.g. 0.8 means 20% off
	OutputDiscount float64  `json:"output_discount"`
	FixedInput     *float64 `json:"fixed_input,omitempty"`
	FixedOutput    *float64 `json:"fixed_output,omitempty"`
	MarkupRate     *float64 `json:"markup_rate,omitempty"`

	// 用户级特殊折扣（命中 UserModelDiscount 时回填，供 api_call_logs 审计）
	UserDiscountID   *uint    `json:"user_discount_id,omitempty"`
	UserDiscountRate *float64 `json:"user_discount_rate,omitempty"`
	UserDiscountType string   `json:"user_discount_type,omitempty"` // DISCOUNT / FIXED / MARKUP
}

// DiscountResolver 折扣解析器，根据租户+模型+代理层级确定最终折扣
type DiscountResolver struct {
	db *gorm.DB
}

// NewDiscountResolver 创建折扣解析器实例，db 不能为 nil
func NewDiscountResolver(db *gorm.DB) *DiscountResolver {
	if db == nil {
		panic("DiscountResolver: db must not be nil")
	}
	return &DiscountResolver{db: db}
}

// ResolveDiscount 确定请求的最终折扣
// 查找顺序：
//  0. UserModelDiscount（用户+模型精确匹配，生效期内，优先级最高）
//  1. AgentPricing（租户+模型精确匹配）
//  2. AgentLevelDiscount（层级+模型匹配）
//  3. AgentLevelDiscount（层级+全局匹配，model_id IS NULL）
//  4. 无折扣（原价）
func (r *DiscountResolver) ResolveDiscount(ctx context.Context, userID uint, tenantID uint, modelID uint, agentLevel int) (*DiscountResult, error) {
	if modelID == 0 {
		return &DiscountResult{Type: "none", PricingType: "NONE", InputDiscount: 1.0, OutputDiscount: 1.0}, nil
	}

	// Step 0: Check UserModelDiscount (user-level override, highest priority)
	if userID > 0 {
		var userDiscount model.UserModelDiscount
		err := r.db.WithContext(ctx).
			Where("user_id = ? AND model_id = ? AND is_active = ?", userID, modelID, true).
			First(&userDiscount).Error
		if err == nil && userDiscount.IsEffective(time.Now()) {
			return r.buildUserDiscountResult(&userDiscount), nil
		} else if err != nil && err != gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("query user model discount: %w", err)
		}
	}

	if tenantID == 0 {
		return &DiscountResult{Type: "none", PricingType: "NONE", InputDiscount: 1.0, OutputDiscount: 1.0}, nil
	}

	// Step 1: Check AgentPricing (custom pricing for this tenant + model)
	var agentPricing model.AgentPricing
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND model_id = ?", tenantID, modelID).
		First(&agentPricing).Error
	if err == nil {
		// Found custom pricing
		if agentPricing.PricingType != "INHERIT" {
			return r.buildAgentPricingResult(&agentPricing), nil
		}
		// INHERIT → fall through to level discount
	} else if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("query agent pricing: %w", err)
	}

	// Step 2: Check AgentLevelDiscount with specific model
	if agentLevel < 1 {
		agentLevel = 1
	}
	var modelDiscount model.AgentLevelDiscount
	err = r.db.WithContext(ctx).
		Where("level = ? AND model_id = ?", agentLevel, modelID).
		First(&modelDiscount).Error
	if err == nil {
		return &DiscountResult{
			Type:           "level_discount",
			PricingType:    "DISCOUNT",
			InputDiscount:  modelDiscount.InputDiscount,
			OutputDiscount: modelDiscount.OutputDiscount,
		}, nil
	} else if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("query model-level discount: %w", err)
	}

	// Step 3: Check AgentLevelDiscount global (model_id IS NULL)
	var globalDiscount model.AgentLevelDiscount
	err = r.db.WithContext(ctx).
		Where("level = ? AND model_id IS NULL", agentLevel).
		First(&globalDiscount).Error
	if err == nil {
		return &DiscountResult{
			Type:           "level_discount",
			PricingType:    "DISCOUNT",
			InputDiscount:  globalDiscount.InputDiscount,
			OutputDiscount: globalDiscount.OutputDiscount,
		}, nil
	} else if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("query global-level discount: %w", err)
	}

	// Step 4: No discount
	return &DiscountResult{
		Type:           "none",
		PricingType:    "NONE",
		InputDiscount:  1.0,
		OutputDiscount: 1.0,
	}, nil
}

// buildAgentPricingResult 将 AgentPricing 转换为 DiscountResult
func (r *DiscountResolver) buildAgentPricingResult(ap *model.AgentPricing) *DiscountResult {
	result := &DiscountResult{
		Type:           "agent_custom",
		PricingType:    ap.PricingType,
		InputDiscount:  1.0,
		OutputDiscount: 1.0,
	}
	switch ap.PricingType {
	case "FIXED":
		result.FixedInput = ap.InputPrice
		result.FixedOutput = ap.OutputPrice
	case "MARKUP":
		result.MarkupRate = ap.MarkupRate
	case "DISCOUNT":
		if ap.DiscountRate != nil {
			result.InputDiscount = *ap.DiscountRate
			result.OutputDiscount = *ap.DiscountRate
		}
	}
	return result
}

// buildUserDiscountResult 将 UserModelDiscount 转换为 DiscountResult
func (r *DiscountResolver) buildUserDiscountResult(ud *model.UserModelDiscount) *DiscountResult {
	id := ud.ID
	result := &DiscountResult{
		Type:             "user_custom",
		PricingType:      ud.PricingType,
		InputDiscount:    1.0,
		OutputDiscount:   1.0,
		UserDiscountID:   &id,
		UserDiscountType: ud.PricingType,
	}
	switch ud.PricingType {
	case "FIXED":
		result.FixedInput = ud.InputPrice
		result.FixedOutput = ud.OutputPrice
	case "MARKUP":
		result.MarkupRate = ud.MarkupRate
	case "DISCOUNT":
		if ud.DiscountRate != nil {
			result.InputDiscount = *ud.DiscountRate
			result.OutputDiscount = *ud.DiscountRate
			result.UserDiscountRate = ud.DiscountRate
		}
	}
	return result
}
