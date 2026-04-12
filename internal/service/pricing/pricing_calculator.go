package pricing

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/redis"
)

const (
	priceCacheTTL    = 5 * time.Minute
	priceCachePrefix = "pricing"
)

// PriceResult 模型计算价格结果（积分为主，RMB 为辅助展示）
type PriceResult struct {
	InputPricePerMillion  int64   `json:"input_price_per_million"`  // 每百万 token 输入价格（积分）
	OutputPricePerMillion int64   `json:"output_price_per_million"` // 每百万 token 输出价格（积分）
	InputPriceRMB         float64 `json:"input_price_rmb"`          // 每百万 token 输入价格（人民币）
	OutputPriceRMB        float64 `json:"output_price_rmb"`         // 每百万 token 输出价格（人民币）
	Currency              string  `json:"currency"`                 // 币种统一为 CREDIT
	Source                string  `json:"source"`                   // "platform" / "level_discount" / "agent_custom"
	DiscountInfo          string  `json:"discount_info,omitempty"`
}

// CostResult 单次请求的费用明细（积分为主，RMB 为辅助展示）
type CostResult struct {
	InputCost    int64   `json:"input_cost"`    // 输入成本（积分）
	OutputCost   int64   `json:"output_cost"`   // 输出成本（积分）
	TotalCost    int64   `json:"total_cost"`    // 总成本（积分）
	TotalCostRMB float64 `json:"total_cost_rmb"` // 总成本（人民币）
	PlatformCost int64   `json:"platform_cost"` // 平台基础成本（积分），用于利润计算
	PriceDetail  PriceResult `json:"price_detail"`
}

// PriceMatrixItem 价格矩阵中的单行数据
type PriceMatrixItem struct {
	ModelID               uint    `json:"model_id"`
	ModelName             string  `json:"model_name"`
	DisplayName           string  `json:"display_name"`
	SupplierName          string  `json:"supplier_name"`
	CategoryName          string  `json:"category_name"`
	CostInput             int64   `json:"cost_input"`       // 成本价（积分/百万token）
	CostOutput            int64   `json:"cost_output"`
	PlatformInput         int64   `json:"platform_input"`   // 平台售价（积分/百万token）
	PlatformOutput        int64   `json:"platform_output"`
	FinalInput            int64   `json:"final_input"`      // 最终售价（积分/百万token）
	FinalOutput           int64   `json:"final_output"`
	PlatformInputRMB      float64 `json:"platform_input_rmb"`
	PlatformOutputRMB     float64 `json:"platform_output_rmb"`
	FinalInputRMB         float64 `json:"final_input_rmb"`
	FinalOutputRMB        float64 `json:"final_output_rmb"`
	Currency              string  `json:"currency"`
	Source                string  `json:"source"`
}

// PricingCalculator 核心计价引擎
type PricingCalculator struct {
	db       *gorm.DB
	redis    *goredis.Client
	resolver *DiscountResolver
}

// NewPricingCalculator 创建计价引擎实例，db 不能为 nil
func NewPricingCalculator(db *gorm.DB) *PricingCalculator {
	if db == nil {
		panic("PricingCalculator: db must not be nil")
	}
	return &PricingCalculator{
		db:       db,
		redis:    redis.Client,
		resolver: NewDiscountResolver(db),
	}
}

// cacheKey 构建定价查询的 Redis 缓存键
func cacheKey(modelID uint, tenantID uint, level int) string {
	return fmt.Sprintf("%s:%d:%d:%d", priceCachePrefix, modelID, tenantID, level)
}

// CalculatePrice 计算指定模型/租户/层级的最终用户价格（积分/百万token）
// 优先级：AgentPricing > AgentLevelDiscount > ModelPricing（平台价）
func (c *PricingCalculator) CalculatePrice(ctx context.Context, modelID uint, tenantID uint, agentLevel int) (*PriceResult, error) {
	if modelID == 0 {
		return nil, fmt.Errorf("modelID must not be zero")
	}

	// Try cache first
	key := cacheKey(modelID, tenantID, agentLevel)
	if c.redis != nil {
		var cached PriceResult
		if err := redis.GetJSON(ctx, key, &cached); err == nil {
			return &cached, nil
		}
	}

	// Fetch platform pricing (ModelPricing)
	platformPrice, err := c.getPlatformPrice(ctx, modelID)
	if err != nil {
		return nil, fmt.Errorf("get platform price: %w", err)
	}

	// If no tenant, return platform price directly
	if tenantID == 0 {
		result := &PriceResult{
			InputPricePerMillion:  platformPrice.InputPricePerToken,
			OutputPricePerMillion: platformPrice.OutputPricePerToken,
			InputPriceRMB:         platformPrice.InputPriceRMB,
			OutputPriceRMB:        platformPrice.OutputPriceRMB,
			Currency:              platformPrice.Currency,
			Source:                "platform",
		}
		c.cacheResult(ctx, key, result)
		return result, nil
	}

	// Resolve discount
	discount, err := c.resolver.ResolveDiscount(ctx, tenantID, modelID, agentLevel)
	if err != nil {
		return nil, fmt.Errorf("resolve discount: %w", err)
	}

	result := c.applyDiscount(platformPrice, discount)
	c.cacheResult(ctx, key, result)
	return result, nil
}

// CalculateCost 计算单次请求的费用（根据 Token 数量，返回积分）
// 参数: modelID 模型ID, tenantID 租户ID, agentLevel 代理等级, inputTokens 输入token数, outputTokens 输出token数
func (c *PricingCalculator) CalculateCost(ctx context.Context, modelID uint, tenantID uint, agentLevel int, inputTokens, outputTokens int) (*CostResult, error) {
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}

	priceResult, err := c.CalculatePrice(ctx, modelID, tenantID, agentLevel)
	if err != nil {
		return nil, err
	}

	// Get platform base price for profit calculation
	platformPrice, err := c.getPlatformPrice(ctx, modelID)
	if err != nil {
		return nil, fmt.Errorf("get platform price for cost: %w", err)
	}

	// 计算费用：价格单位是"每百万token的积分"，需要乘以 token 数再除以 1,000,000
	// 为避免浮点运算，使用整数运算：cost = price * tokens / 1_000_000
	inputCost := priceResult.InputPricePerMillion * int64(inputTokens) / 1_000_000
	outputCost := priceResult.OutputPricePerMillion * int64(outputTokens) / 1_000_000
	platformCost := (platformPrice.InputPricePerToken*int64(inputTokens) + platformPrice.OutputPricePerToken*int64(outputTokens)) / 1_000_000

	totalCost := inputCost + outputCost
	totalCostRMB := credits.CreditsToRMB(totalCost)

	return &CostResult{
		InputCost:    inputCost,
		OutputCost:   outputCost,
		TotalCost:    totalCost,
		TotalCostRMB: totalCostRMB,
		PlatformCost: platformCost,
		PriceDetail:  *priceResult,
	}, nil
}

// GetPriceMatrix 获取指定租户/层级下所有活跃模型的价格矩阵
func (c *PricingCalculator) GetPriceMatrix(ctx context.Context, tenantID uint, agentLevel int) ([]PriceMatrixItem, error) {
	var models []model.AIModel
	if err := c.db.WithContext(ctx).
		Preload("Supplier").
		Preload("Category").
		Where("is_active = ?", true).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list ai models: %w", err)
	}

	items := make([]PriceMatrixItem, 0, len(models))
	for _, m := range models {
		price, err := c.CalculatePrice(ctx, m.ID, tenantID, agentLevel)
		if err != nil {
			// Skip models without pricing configured
			continue
		}

		// Fetch platform price for the matrix display
		pp, _ := c.getPlatformPrice(ctx, m.ID)

		items = append(items, PriceMatrixItem{
			ModelID:           m.ID,
			ModelName:         m.ModelName,
			DisplayName:       m.DisplayName,
			SupplierName:      m.Supplier.Name,
			CategoryName:      m.Category.Name,
			CostInput:         m.InputPricePerToken,
			CostOutput:        m.OutputPricePerToken,
			PlatformInput:     pp.InputPricePerToken,
			PlatformOutput:    pp.OutputPricePerToken,
			PlatformInputRMB:  pp.InputPriceRMB,
			PlatformOutputRMB: pp.OutputPriceRMB,
			FinalInput:        price.InputPricePerMillion,
			FinalOutput:       price.OutputPricePerMillion,
			FinalInputRMB:     price.InputPriceRMB,
			FinalOutputRMB:    price.OutputPriceRMB,
			Currency:          price.Currency,
			Source:            price.Source,
		})
	}
	return items, nil
}

// InvalidateCache 清除定价缓存
// tenantID 为 nil 时清除该模型的所有缓存，否则清除特定条目
func (c *PricingCalculator) InvalidateCache(ctx context.Context, modelID uint, tenantID *uint) {
	if c.redis == nil {
		return
	}
	if tenantID != nil {
		// Clear all level variants for this model+tenant
		for level := 0; level <= 3; level++ {
			key := cacheKey(modelID, *tenantID, level)
			_ = redis.Del(ctx, key)
		}
	} else {
		// Wildcard delete: scan keys with prefix pricing:{modelID}:*
		pattern := fmt.Sprintf("%s:%d:*", priceCachePrefix, modelID)
		c.deleteByPattern(ctx, pattern)
	}
}

// getPlatformPrice 获取模型的平台定价（积分/百万token）
func (c *PricingCalculator) getPlatformPrice(ctx context.Context, modelID uint) (*model.ModelPricing, error) {
	var mp model.ModelPricing
	err := c.db.WithContext(ctx).
		Where("model_id = ?", modelID).
		Order("effective_from DESC").
		First(&mp).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			// Fallback: use AIModel cost price as platform price
			var m model.AIModel
			if err2 := c.db.WithContext(ctx).First(&m, modelID).Error; err2 != nil {
				return nil, fmt.Errorf("model %d not found: %w", modelID, err2)
			}
			return &model.ModelPricing{
				ModelID:             modelID,
				InputPricePerToken:  m.InputPricePerToken,
				InputPriceRMB:       m.InputCostRMB,
				OutputPricePerToken: m.OutputPricePerToken,
				OutputPriceRMB:      m.OutputCostRMB,
				Currency:            m.Currency,
			}, nil
		}
		return nil, fmt.Errorf("query model pricing: %w", err)
	}
	return &mp, nil
}

// applyDiscount 将解析后的折扣应用到平台价格
// 折扣类型：FIXED(固定价格) / MARKUP(加价) / DISCOUNT(折扣)
func (c *PricingCalculator) applyDiscount(platform *model.ModelPricing, discount *DiscountResult) *PriceResult {
	if discount == nil || discount.Type == "none" {
		return &PriceResult{
			InputPricePerMillion:  platform.InputPricePerToken,
			OutputPricePerMillion: platform.OutputPricePerToken,
			InputPriceRMB:         platform.InputPriceRMB,
			OutputPriceRMB:        platform.OutputPriceRMB,
			Currency:              platform.Currency,
			Source:                "platform",
		}
	}

	result := &PriceResult{Currency: platform.Currency}

	switch discount.PricingType {
	case "FIXED":
		// 固定价格（积分）
		if discount.FixedInput != nil {
			result.InputPricePerMillion = int64(*discount.FixedInput)
			result.InputPriceRMB = credits.CreditsToRMB(result.InputPricePerMillion)
		} else {
			result.InputPricePerMillion = platform.InputPricePerToken
			result.InputPriceRMB = platform.InputPriceRMB
		}
		if discount.FixedOutput != nil {
			result.OutputPricePerMillion = int64(*discount.FixedOutput)
			result.OutputPriceRMB = credits.CreditsToRMB(result.OutputPricePerMillion)
		} else {
			result.OutputPricePerMillion = platform.OutputPricePerToken
			result.OutputPriceRMB = platform.OutputPriceRMB
		}
		result.Source = "agent_custom"
		result.DiscountInfo = "fixed price"

	case "MARKUP":
		// 加价比例（如 0.1 = 加价10%）
		rate := 0.0
		if discount.MarkupRate != nil {
			rate = *discount.MarkupRate
		}
		// 加价后价格 = 原价 * (1 + rate)
		// 注意：这里需要用浮点计算后再转回整数
		result.InputPricePerMillion = int64(float64(platform.InputPricePerToken) * (1 + rate))
		result.OutputPricePerMillion = int64(float64(platform.OutputPricePerToken) * (1 + rate))
		result.InputPriceRMB = credits.CreditsToRMB(result.InputPricePerMillion)
		result.OutputPriceRMB = credits.CreditsToRMB(result.OutputPricePerMillion)
		result.Source = "agent_custom"
		result.DiscountInfo = fmt.Sprintf("markup %.2f%%", rate*100)

	case "DISCOUNT":
		// 折扣比例（如 0.8 = 8折）
		// 折扣后价格 = 原价 * discountRate
		result.InputPricePerMillion = int64(float64(platform.InputPricePerToken) * discount.InputDiscount)
		result.OutputPricePerMillion = int64(float64(platform.OutputPricePerToken) * discount.OutputDiscount)
		result.InputPriceRMB = credits.CreditsToRMB(result.InputPricePerMillion)
		result.OutputPriceRMB = credits.CreditsToRMB(result.OutputPricePerMillion)
		if discount.Type == "agent_custom" {
			result.Source = "agent_custom"
			result.DiscountInfo = fmt.Sprintf("discount %.2f%%", discount.InputDiscount*100)
		} else {
			result.Source = "level_discount"
			result.DiscountInfo = fmt.Sprintf("level discount in=%.2f%% out=%.2f%%",
				discount.InputDiscount*100, discount.OutputDiscount*100)
		}

	default:
		result.InputPricePerMillion = platform.InputPricePerToken
		result.OutputPricePerMillion = platform.OutputPricePerToken
		result.InputPriceRMB = platform.InputPriceRMB
		result.OutputPriceRMB = platform.OutputPriceRMB
		result.Source = "platform"
	}

	return result
}

// cacheResult 将定价结果存入 Redis 缓存
func (c *PricingCalculator) cacheResult(ctx context.Context, key string, result *PriceResult) {
	if c.redis == nil || result == nil {
		return
	}
	_ = redis.SetJSON(ctx, key, result, priceCacheTTL)
}

// deleteByPattern 使用 SCAN 删除匹配模式的所有键
func (c *PricingCalculator) deleteByPattern(ctx context.Context, pattern string) {
	if c.redis == nil {
		return
	}
	var cursor uint64
	for {
		keys, nextCursor, err := c.redis.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return
		}
		if len(keys) > 0 {
			_ = c.redis.Del(ctx, keys...).Err()
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
}
