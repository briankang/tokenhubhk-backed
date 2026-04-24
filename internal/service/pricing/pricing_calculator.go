package pricing

import (
	"context"
	"errors"
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

// ErrModelNotPriced 模型未配置 ModelPricing 售价，禁止计费调用。
// 上游 handler 应据此返回 503/402 并提示用户该模型不可用。
var ErrModelNotPriced = errors.New("model has no published sale price (model_pricings missing)")

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
	InputCost    int64       `json:"input_cost"`     // 输入成本（积分）
	OutputCost   int64       `json:"output_cost"`    // 输出成本（积分）
	TotalCost    int64       `json:"total_cost"`     // 总成本（积分）
	TotalCostRMB float64     `json:"total_cost_rmb"` // 总成本（人民币）
	PlatformCost int64       `json:"platform_cost"`  // 平台基础成本（积分），用于利润计算
	PriceDetail  PriceResult `json:"price_detail"`

	// 阶梯定价命中信息（未命中时 MatchedTierIdx=-1）
	MatchedTier    string `json:"matched_tier,omitempty"`
	MatchedTierIdx int    `json:"matched_tier_idx"`

	// 缓存计费明细（仅 CalculateCostWithCache 路径填充）
	CacheReadTokens    int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens   int64 `json:"cache_write_tokens,omitempty"`
	CacheReadCost      int64 `json:"cache_read_cost,omitempty"`      // 缓存命中部分扣费（积分）
	CacheWriteCost     int64 `json:"cache_write_cost,omitempty"`     // 缓存写入部分扣费（积分）
	RegularInputCost   int64 `json:"regular_input_cost,omitempty"`   // 非缓存输入部分扣费（积分）
	CacheSavingCredits int64 `json:"cache_saving_credits,omitempty"` // 对比无缓存路径节省的积分

	// 用户级特殊折扣命中信息（UserModelDiscount 命中时回填，供 api_call_logs 审计）
	UserDiscountID   *uint    `json:"user_discount_id,omitempty"`
	UserDiscountRate *float64 `json:"user_discount_rate,omitempty"`
	UserDiscountType string   `json:"user_discount_type,omitempty"`
}

// UsageInput 多计费单位的用量输入
// 根据 AIModel.PricingUnit 选择对应字段：
//   - per_million_tokens:     InputTokens / OutputTokens
//   - per_image:              ImageCount（生成的图片张数）
//   - per_second:             DurationSec（视频/音频秒数）
//   - per_minute:             DurationSec（按分钟折算，如 whisper）
//   - per_10k_characters:     CharCount（按万字符折算，豆包 TTS）
//   - per_million_characters: CharCount（按百万字符折算，qwen-tts / openai-tts）
//   - per_call:               CallCount（请求次数，Rerank 等）
//   - per_hour:               DurationSec（按小时折算，ASR）
type UsageInput struct {
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	ImageCount   int     `json:"image_count,omitempty"`
	CharCount    int     `json:"char_count,omitempty"`
	DurationSec  float64 `json:"duration_sec,omitempty"`
	CallCount    int     `json:"call_count,omitempty"`
}

// PriceMatrixItem 价格矩阵中的单行数据
type PriceMatrixItem struct {
	ModelID           uint    `json:"model_id"`
	ModelName         string  `json:"model_name"`
	DisplayName       string  `json:"display_name"`
	SupplierName      string  `json:"supplier_name"`
	CategoryName      string  `json:"category_name"`
	CostInput         int64   `json:"cost_input"` // 成本价（积分/百万token）
	CostOutput        int64   `json:"cost_output"`
	PlatformInput     int64   `json:"platform_input"` // 平台售价（积分/百万token）
	PlatformOutput    int64   `json:"platform_output"`
	FinalInput        int64   `json:"final_input"` // 最终售价（积分/百万token）
	FinalOutput       int64   `json:"final_output"`
	PlatformInputRMB  float64 `json:"platform_input_rmb"`
	PlatformOutputRMB float64 `json:"platform_output_rmb"`
	FinalInputRMB     float64 `json:"final_input_rmb"`
	FinalOutputRMB    float64 `json:"final_output_rmb"`
	Currency          string  `json:"currency"`
	Source            string  `json:"source"`
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

// cacheKey 构建定价查询的 Redis 缓存键（包含 userID 支持用户级覆盖）
func cacheKey(userID uint, modelID uint, tenantID uint, level int) string {
	return fmt.Sprintf("%s:%d:%d:%d:%d", priceCachePrefix, userID, modelID, tenantID, level)
}

// CalculatePrice 计算指定用户/模型/租户/层级的最终用户价格（积分/百万token）
// 优先级：UserModelDiscount > AgentPricing > AgentLevelDiscount > ModelPricing（平台价）
func (c *PricingCalculator) CalculatePrice(ctx context.Context, userID uint, modelID uint, tenantID uint, agentLevel int) (*PriceResult, error) {
	if modelID == 0 {
		return nil, fmt.Errorf("modelID must not be zero")
	}

	// Try cache first
	key := cacheKey(userID, modelID, tenantID, agentLevel)
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

	// If no tenant and no user, return platform price directly
	if tenantID == 0 && userID == 0 {
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

	// Resolve discount (user-level override checked first inside resolver)
	discount, err := c.resolver.ResolveDiscount(ctx, userID, tenantID, modelID, agentLevel)
	if err != nil {
		return nil, fmt.Errorf("resolve discount: %w", err)
	}

	result := c.applyDiscount(platformPrice, discount)
	c.cacheResult(ctx, key, result)
	return result, nil
}

// CalculateCost 计算单次请求的费用（根据 Token 数量，返回积分）
// 参数: modelID 模型ID, tenantID 租户ID, agentLevel 代理等级, inputTokens 输入token数, outputTokens 输出token数
//
// 计费路径（优先级从高到低）：
//  1. selectPriceForTokens 命中阶梯 → 使用阶梯价格
//     - SellingOverride=true: 跳过 FIXED/MARKUP，仅叠加 DISCOUNT 代理折扣
//     - SellingOverride=false: 完整走 applyDiscount 链路（阶梯价代替平台基础价）
//  2. 未命中阶梯 → 旧路径：CalculatePrice + 单价
func (c *PricingCalculator) CalculateCost(ctx context.Context, userID uint, modelID uint, tenantID uint, agentLevel int, inputTokens, outputTokens int) (*CostResult, error) {
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}

	// Get platform base price for profit calculation
	platformPrice, err := c.getPlatformPrice(ctx, modelID)
	if err != nil {
		return nil, fmt.Errorf("get platform price for cost: %w", err)
	}

	// 1. 尝试阶梯选择
	tierSel, _ := c.selectPriceForTokens(ctx, modelID, int64(inputTokens), int64(outputTokens))

	var (
		inputPerMillion  int64
		outputPerMillion int64
		inputPriceRMB    float64
		outputPriceRMB   float64
		source           = "platform"
		matchedTier      string
		matchedTierIdx   = -1
		userDiscountID   *uint
		userDiscountRate *float64
		userDiscountType string
	)

	if tierSel != nil && tierSel.FromTier {
		inputPerMillion = tierSel.InputPricePerMillion
		outputPerMillion = tierSel.OutputPricePerMillion
		inputPriceRMB = tierSel.InputPriceRMB
		outputPriceRMB = tierSel.OutputPriceRMB
		source = "platform+tier"
		matchedTier = tierSel.MatchedTier
		matchedTierIdx = tierSel.MatchedTierIdx

		// 叠加代理折扣（SellingOverride=true 时仅 DISCOUNT；否则完整链路）
		if tenantID > 0 || userID > 0 {
			discount, derr := c.resolver.ResolveDiscount(ctx, userID, tenantID, modelID, agentLevel)
			if derr == nil && discount != nil && discount.Type != "none" {
				if discount.UserDiscountID != nil {
					userDiscountID = discount.UserDiscountID
					userDiscountRate = discount.UserDiscountRate
					userDiscountType = discount.UserDiscountType
				}
				if tierSel.SellingOverride {
					// 只叠加 DISCOUNT（阶梯已是终价）
					if discount.PricingType == "DISCOUNT" && discount.InputDiscount > 0 {
						inputPerMillion = int64(float64(inputPerMillion) * discount.InputDiscount)
						inputPriceRMB = inputPriceRMB * discount.InputDiscount
						source = "agent_discount+tier"
					}
					if discount.PricingType == "DISCOUNT" && discount.OutputDiscount > 0 {
						outputPerMillion = int64(float64(outputPerMillion) * discount.OutputDiscount)
						outputPriceRMB = outputPriceRMB * discount.OutputDiscount
					}
				} else {
					// 以 tier 价为基础价完整走 applyDiscount
					tierAsPlatform := &model.ModelPricing{
						InputPricePerToken:  inputPerMillion,
						OutputPricePerToken: outputPerMillion,
						InputPriceRMB:       inputPriceRMB,
						OutputPriceRMB:      outputPriceRMB,
						Currency:            platformPrice.Currency,
					}
					adjusted := c.applyDiscount(tierAsPlatform, discount)
					inputPerMillion = adjusted.InputPricePerMillion
					outputPerMillion = adjusted.OutputPricePerMillion
					inputPriceRMB = adjusted.InputPriceRMB
					outputPriceRMB = adjusted.OutputPriceRMB
					source = adjusted.Source + "+tier"
				}
			}
		}
	} else {
		// 2. 旧路径：无阶梯，走 CalculatePrice + 单价
		priceResult, err := c.CalculatePrice(ctx, userID, modelID, tenantID, agentLevel)
		if err != nil {
			return nil, err
		}
		inputPerMillion = priceResult.InputPricePerMillion
		outputPerMillion = priceResult.OutputPricePerMillion
		inputPriceRMB = priceResult.InputPriceRMB
		outputPriceRMB = priceResult.OutputPriceRMB
		source = priceResult.Source

		// 补充 UserDiscount 审计信息（非阶梯路径 CalculatePrice 未透出该字段）
		if userID > 0 {
			if d, derr := c.resolver.ResolveDiscount(ctx, userID, tenantID, modelID, agentLevel); derr == nil && d != nil && d.UserDiscountID != nil {
				userDiscountID = d.UserDiscountID
				userDiscountRate = d.UserDiscountRate
				userDiscountType = d.UserDiscountType
			}
		}
	}

	// 计算费用：价格单位是"每百万token的积分"，需要乘以 token 数再除以 1,000,000
	// 为避免浮点运算，使用整数运算：cost = price * tokens / 1_000_000
	// 最小1积分保底：有token消耗时至少扣1积分，防止小请求因整数截断免费
	inputCost := inputPerMillion * int64(inputTokens) / 1_000_000
	outputCost := outputPerMillion * int64(outputTokens) / 1_000_000
	platformCost := (platformPrice.InputPricePerToken*int64(inputTokens) + platformPrice.OutputPricePerToken*int64(outputTokens)) / 1_000_000

	totalCost := inputCost + outputCost
	// 有实际token消耗但计算结果为0时（整数截断），保底收取1积分
	if totalCost == 0 && (inputTokens > 0 || outputTokens > 0) {
		totalCost = 1
	}
	totalCostRMB := credits.CreditsToRMB(totalCost)

	return &CostResult{
		InputCost:    inputCost,
		OutputCost:   outputCost,
		TotalCost:    totalCost,
		TotalCostRMB: totalCostRMB,
		PlatformCost: platformCost,
		PriceDetail: PriceResult{
			InputPricePerMillion:  inputPerMillion,
			OutputPricePerMillion: outputPerMillion,
			InputPriceRMB:         inputPriceRMB,
			OutputPriceRMB:        outputPriceRMB,
			Currency:              platformPrice.Currency,
			Source:                source,
		},
		MatchedTier:      matchedTier,
		MatchedTierIdx:   matchedTierIdx,
		UserDiscountID:   userDiscountID,
		UserDiscountRate: userDiscountRate,
		UserDiscountType: userDiscountType,
	}, nil
}

// CalculateCostByUnit 根据模型的 PricingUnit 使用对应的用量字段计算费用
//
// 单位分支（8 种 + per_k_chars 兼容）：
//   - per_million_tokens:     复用 CalculateCost（InputTokens + OutputTokens）
//   - per_image:              input_cost_rmb * image_count
//   - per_second:             input_cost_rmb * duration_sec
//   - per_minute:             input_cost_rmb * duration_sec / 60
//   - per_10k_characters:     input_cost_rmb * char_count / 10000（别名 per_k_chars）
//   - per_million_characters: input_cost_rmb * char_count / 1_000_000
//   - per_call:               input_cost_rmb * call_count
//   - per_hour:               input_cost_rmb * duration_sec / 3600
//
// 对于非 Token 单位，以模型的平台基础价 InputCostRMB 计算。
// 三级折扣（DISCOUNT）仍生效（通过 DiscountResolver 解析后乘折扣率）。
// MARKUP/FIXED 在非 Token 类单位下暂不支持，按平台基础价收取。
func (c *PricingCalculator) CalculateCostByUnit(ctx context.Context, userID uint, modelID uint, tenantID uint, agentLevel int, usage UsageInput) (*CostResult, error) {
	var m model.AIModel
	if err := c.db.WithContext(ctx).First(&m, modelID).Error; err != nil {
		return nil, fmt.Errorf("model %d not found: %w", modelID, err)
	}

	// per_million_tokens 走原有路径，保证 FIXED/MARKUP/DISCOUNT 三层定价完全兼容
	if m.PricingUnit == "" || m.PricingUnit == model.UnitPerMillionTokens {
		return c.CalculateCost(ctx, userID, modelID, tenantID, agentLevel, usage.InputTokens, usage.OutputTokens)
	}

	// 非 token 单位：以模型 InputCostRMB 为基础价
	platformPrice, platformErr := c.getPlatformPrice(ctx, modelID)
	sellPriceRMB := m.InputCostRMB
	if platformErr == nil && platformPrice != nil && platformPrice.InputPriceRMB > 0 {
		sellPriceRMB = platformPrice.InputPriceRMB
	}
	baseCostRMB := m.InputCostRMB
	if baseCostRMB <= 0 && sellPriceRMB > 0 {
		baseCostRMB = sellPriceRMB
	}
	basePriceRMB := sellPriceRMB
	if basePriceRMB <= 0 {
		// 价格未配置，返回 0 费用（不阻塞请求）
		return &CostResult{TotalCost: 0, TotalCostRMB: 0}, nil
	}

	// 尝试解析折扣（仅应用 DISCOUNT 类型，其他类型回退为平台价）
	discount, derr := c.resolver.ResolveDiscount(ctx, userID, tenantID, modelID, agentLevel)
	discountRate := 1.0
	source := "platform"
	var udID *uint
	var udRate *float64
	var udType string
	if derr == nil && discount != nil {
		if discount.UserDiscountID != nil {
			udID = discount.UserDiscountID
			udRate = discount.UserDiscountRate
			udType = discount.UserDiscountType
		}
		if discount.PricingType == "DISCOUNT" && discount.InputDiscount > 0 {
			discountRate = discount.InputDiscount
			switch discount.Type {
			case "user_custom":
				source = "user_custom"
			case "agent_custom":
				source = "agent_custom"
			default:
				source = "level_discount"
			}
		}
	}

	var quantity float64
	switch m.PricingUnit {
	case model.UnitPerImage:
		quantity = float64(usage.ImageCount)
	case model.UnitPerSecond:
		quantity = usage.DurationSec
	case model.UnitPerMinute:
		quantity = usage.DurationSec / 60.0
	case model.UnitPer10kCharacters, model.UnitPerKChars:
		// "元/万字符"：10000 字符为 1 单位
		quantity = float64(usage.CharCount) / 10000.0
	case model.UnitPerMillionCharacters:
		quantity = float64(usage.CharCount) / 1_000_000.0
	case model.UnitPerCall:
		quantity = float64(usage.CallCount)
	case model.UnitPerHour:
		quantity = usage.DurationSec / 3600.0
	default:
		// 未知单位，回退到 token 路径避免漏扣
		return c.CalculateCost(ctx, userID, modelID, tenantID, agentLevel, usage.InputTokens, usage.OutputTokens)
	}

	if quantity <= 0 {
		return &CostResult{TotalCost: 0, TotalCostRMB: 0, PriceDetail: PriceResult{Currency: "CREDIT", Source: source}}, nil
	}

	costRMB := basePriceRMB * quantity * discountRate
	totalCost := credits.RMBToCredits(costRMB)

	// 有实际消耗但因四舍五入取整为 0，保底 1 积分
	if totalCost == 0 && quantity > 0 && costRMB > 0 {
		totalCost = 1
	}
	totalCostRMB := credits.CreditsToRMB(totalCost)

	// 平台成本（用于利润分析）使用同一基础价
	platformCost := credits.RMBToCredits(baseCostRMB * quantity)

	return &CostResult{
		InputCost:    totalCost,
		OutputCost:   0,
		TotalCost:    totalCost,
		TotalCostRMB: totalCostRMB,
		PlatformCost: platformCost,
		PriceDetail: PriceResult{
			InputPricePerMillion:  credits.RMBToCredits(sellPriceRMB * discountRate),
			OutputPricePerMillion: 0,
			InputPriceRMB:         sellPriceRMB * discountRate,
			OutputPriceRMB:        0,
			Currency:              "CREDIT",
			Source:                source,
		},
		UserDiscountID:   udID,
		UserDiscountRate: udRate,
		UserDiscountType: udType,
	}, nil
}

// CacheUsageInput 含缓存信息的用量输入
type CacheUsageInput struct {
	InputTokens      int // 总输入Token（含缓存命中+写入+普通）
	OutputTokens     int // 输出Token
	CacheReadTokens  int // 缓存命中Token（来自供应商响应）
	CacheWriteTokens int // 缓存写入Token（Anthropic cache_creation_input_tokens）
}

// CalculateCostWithCache 按缓存比率扣除用户积分（本次修复的核心路径）
//
// 用户侧计费逻辑：
//  1. 先调用 CalculateCost 得到用户售价（含阶梯 / 代理折扣 / 会员折扣）
//  2. 根据 AIModel 的成本侧比率（cache_input_price_rmb / input_cost_rmb）
//     按同等折扣推导出用户侧缓存价（售价 × 比率）
//  3. 将 input tokens 拆分为三段：
//       - regular    = InputTokens - CacheReadTokens - CacheWriteTokens
//       - cache_read  = CacheReadTokens（auto/explicit/both 命中）
//       - cache_write = CacheWriteTokens（explicit/both 写入溢价）
//  4. 三段分别按对应单价结算后加总
//
// 返回的 CostResult 附带 CacheReadCost / CacheWriteCost / RegularInputCost /
// CacheSavingCredits 便于日志记录与对账展示。
func (c *PricingCalculator) CalculateCostWithCache(
	ctx context.Context,
	userID uint,
	aiModel *model.AIModel,
	tenantID uint, agentLevel int,
	usage CacheUsageInput,
) (*CostResult, error) {
	if aiModel == nil {
		return nil, fmt.Errorf("aiModel is nil")
	}
	// 不支持缓存或无缓存用量 → 走普通路径
	if !aiModel.SupportsCache || aiModel.CacheMechanism == "none" ||
		(usage.CacheReadTokens == 0 && usage.CacheWriteTokens == 0) {
		return c.CalculateCost(ctx, userID, aiModel.ID, tenantID, agentLevel,
			usage.InputTokens, usage.OutputTokens)
	}

	// 1) 先按售价计算基础 CostResult（用户侧售价，已含阶梯/代理折扣）
	base, err := c.CalculateCost(ctx, userID, aiModel.ID, tenantID, agentLevel,
		usage.InputTokens, usage.OutputTokens)
	if err != nil {
		return nil, err
	}

	// 2) 从成本侧反推缓存比率
	baseCostRMB := aiModel.InputCostRMB
	ratio := func(cachePriceRMB, fallback float64) float64 {
		if baseCostRMB > 0 && cachePriceRMB > 0 {
			return cachePriceRMB / baseCostRMB
		}
		return fallback
	}

	var cacheReadRatio, cacheWriteRatio float64
	switch aiModel.CacheMechanism {
	case "both":
		if usage.CacheWriteTokens > 0 {
			cacheReadRatio = ratio(aiModel.CacheExplicitInputPriceRMB, 0.10)
		} else {
			cacheReadRatio = ratio(aiModel.CacheInputPriceRMB, 0.20)
		}
		cacheWriteRatio = ratio(aiModel.CacheWritePriceRMB, 1.25)
	case "explicit":
		cacheReadRatio = ratio(aiModel.CacheInputPriceRMB, 0.10)
		cacheWriteRatio = ratio(aiModel.CacheWritePriceRMB, 1.25)
	default: // auto
		cacheReadRatio = ratio(aiModel.CacheInputPriceRMB, 0.50)
		cacheWriteRatio = 1.0 // auto 无写入溢价
	}

	// 3) 按售价 × 比率得到用户侧缓存单价（每百万 token 积分）
	inputPerMillion := base.PriceDetail.InputPricePerMillion
	outputPerMillion := base.PriceDetail.OutputPricePerMillion
	cacheReadPerMillion := int64(float64(inputPerMillion) * cacheReadRatio)
	cacheWritePerMillion := int64(float64(inputPerMillion) * cacheWriteRatio)

	readTokens := int64(usage.CacheReadTokens)
	writeTokens := int64(usage.CacheWriteTokens)
	totalInput := int64(usage.InputTokens)
	regularTokens := totalInput - readTokens - writeTokens
	if regularTokens < 0 {
		regularTokens = 0
	}

	regularCost := inputPerMillion * regularTokens / 1_000_000
	cacheReadCost := cacheReadPerMillion * readTokens / 1_000_000
	cacheWriteCost := cacheWritePerMillion * writeTokens / 1_000_000
	inputCost := regularCost + cacheReadCost + cacheWriteCost
	outputCost := outputPerMillion * int64(usage.OutputTokens) / 1_000_000

	totalCost := inputCost + outputCost
	if totalCost == 0 && (usage.InputTokens > 0 || usage.OutputTokens > 0) {
		totalCost = 1
	}

	// 节省 = 无缓存路径的扣费 - 实际扣费
	savings := base.TotalCost - totalCost
	if savings < 0 {
		savings = 0
	}

	return &CostResult{
		InputCost:          inputCost,
		OutputCost:         outputCost,
		TotalCost:          totalCost,
		TotalCostRMB:       credits.CreditsToRMB(totalCost),
		PlatformCost:       base.PlatformCost,
		PriceDetail:        base.PriceDetail,
		MatchedTier:        base.MatchedTier,
		MatchedTierIdx:     base.MatchedTierIdx,
		CacheReadTokens:    readTokens,
		CacheWriteTokens:   writeTokens,
		CacheReadCost:      cacheReadCost,
		CacheWriteCost:     cacheWriteCost,
		RegularInputCost:   regularCost,
		CacheSavingCredits: savings,
		UserDiscountID:     base.UserDiscountID,
		UserDiscountRate:   base.UserDiscountRate,
		UserDiscountType:   base.UserDiscountType,
	}, nil
}

// CalculateWithCache 计算含缓存的总成本（用户付费侧不变，额外返回平台侧节省金额）
//
// 计费逻辑（使用 AIModel 中的成本价字段）：
//   - auto/explicit 缓存命中：cache_input_price_rmb（节省80%-90%）
//   - explicit 缓存写入（Anthropic/阿里云显式）：cache_write_price_rmb（+25%溢价）
//   - both 机制（阿里云）：CacheWriteTokens>0 走显式价格，否则走隐式价格
//   - 普通输入Token（未命中缓存）：正常 input_cost_rmb
//
// savingsRMB 表示平台从供应商侧节省的成本（不影响用户计费）。
func (c *PricingCalculator) CalculateWithCache(
	ctx context.Context,
	userID uint,
	aiModel *model.AIModel,
	tenantID uint,
	agentLevel int,
	cacheUsage CacheUsageInput,
) (costResult *CostResult, savingsRMB float64, err error) {
	// 不支持缓存、或无缓存命中/写入，直接走普通计费路径
	if !aiModel.SupportsCache || aiModel.CacheMechanism == "none" ||
		(cacheUsage.CacheReadTokens == 0 && cacheUsage.CacheWriteTokens == 0) {
		costResult, err = c.CalculateCost(ctx, userID, aiModel.ID, tenantID, agentLevel,
			cacheUsage.InputTokens, cacheUsage.OutputTokens)
		return costResult, 0, err
	}

	// 先按普通价格计算用户侧费用（保持计费一致性）
	costResult, err = c.CalculateCost(ctx, userID, aiModel.ID, tenantID, agentLevel,
		cacheUsage.InputTokens, cacheUsage.OutputTokens)
	if err != nil {
		return nil, 0, err
	}

	// 计算平台侧（供应商）成本节省
	// 基础输入成本价（元/百万Token）
	baseInputCostRMB := aiModel.InputCostRMB

	// 根据缓存机制选择命中价格
	var cacheReadPriceRMB, cacheWritePriceRMB float64
	switch aiModel.CacheMechanism {
	case "both":
		// 阿里云：有写入Token则认为是显式缓存，否则是隐式
		if cacheUsage.CacheWriteTokens > 0 {
			cacheReadPriceRMB = aiModel.CacheExplicitInputPriceRMB
			if cacheReadPriceRMB <= 0 && baseInputCostRMB > 0 {
				cacheReadPriceRMB = baseInputCostRMB * 0.10 // fallback: 显式命中=10%
			}
		} else {
			cacheReadPriceRMB = aiModel.CacheInputPriceRMB
			if cacheReadPriceRMB <= 0 && baseInputCostRMB > 0 {
				cacheReadPriceRMB = baseInputCostRMB * 0.20 // fallback: 隐式命中=20%
			}
		}
		cacheWritePriceRMB = aiModel.CacheWritePriceRMB
		if cacheWritePriceRMB <= 0 && baseInputCostRMB > 0 {
			cacheWritePriceRMB = baseInputCostRMB * 1.25 // fallback: 写入=125%
		}
	case "explicit":
		// Anthropic：cache_control 触发
		cacheReadPriceRMB = aiModel.CacheInputPriceRMB
		if cacheReadPriceRMB <= 0 && baseInputCostRMB > 0 {
			cacheReadPriceRMB = baseInputCostRMB * 0.10
		}
		cacheWritePriceRMB = aiModel.CacheWritePriceRMB
		if cacheWritePriceRMB <= 0 && baseInputCostRMB > 0 {
			cacheWritePriceRMB = baseInputCostRMB * 1.25
		}
	default: // auto
		// OpenAI/DeepSeek/Moonshot/智谱/火山引擎：自动缓存
		cacheReadPriceRMB = aiModel.CacheInputPriceRMB
		if cacheReadPriceRMB <= 0 && baseInputCostRMB > 0 {
			cacheReadPriceRMB = baseInputCostRMB * 0.50 // fallback: 50%（OpenAI默认）
		}
		cacheWritePriceRMB = 0 // 自动缓存无写入溢价
	}

	// 节省计算：
	//   缓存命中节省 = CacheReadTokens × (baseInputCost - cacheReadPrice) / 1e6
	//   缓存写入额外成本 = CacheWriteTokens × (cacheWritePrice - baseInputCost) / 1e6
	//   净节省 = 命中节省 - 写入额外成本
	if baseInputCostRMB > 0 && cacheReadPriceRMB > 0 {
		readSavings := float64(cacheUsage.CacheReadTokens) * (baseInputCostRMB - cacheReadPriceRMB) / 1_000_000.0
		writeExtra := float64(cacheUsage.CacheWriteTokens) * (cacheWritePriceRMB - baseInputCostRMB) / 1_000_000.0
		savingsRMB = readSavings - writeExtra
	}

	return costResult, savingsRMB, nil
}

// GetPriceMatrix 获取指定租户/层级下所有活跃模型的价格矩阵
func (c *PricingCalculator) GetPriceMatrix(ctx context.Context, userID uint, tenantID uint, agentLevel int) ([]PriceMatrixItem, error) {
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
		price, err := c.CalculatePrice(ctx, userID, m.ID, tenantID, agentLevel)
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
// tenantID 为 nil 时清除该模型的所有缓存（跨所有用户），否则清除该模型+租户的所有缓存
// 缓存 key 格式：pricing:{userID}:{modelID}:{tenantID}:{level}
func (c *PricingCalculator) InvalidateCache(ctx context.Context, modelID uint, tenantID *uint) {
	if c.redis == nil {
		return
	}
	if tenantID != nil {
		// 模式扫描：pricing:*:{modelID}:{tenantID}:*（跨所有用户+所有 level）
		pattern := fmt.Sprintf("%s:*:%d:%d:*", priceCachePrefix, modelID, *tenantID)
		c.deleteByPattern(ctx, pattern)
	} else {
		// 模式扫描：pricing:*:{modelID}:*（跨所有用户+租户+level）
		pattern := fmt.Sprintf("%s:*:%d:*", priceCachePrefix, modelID)
		c.deleteByPattern(ctx, pattern)
	}
}

// InvalidateUserCache 清除指定用户的所有定价缓存
// 适用于 UserModelDiscount 创建/更新/删除后
func (c *PricingCalculator) InvalidateUserCache(ctx context.Context, userID uint) {
	if c.redis == nil {
		return
	}
	pattern := fmt.Sprintf("%s:%d:*", priceCachePrefix, userID)
	c.deleteByPattern(ctx, pattern)
}

// getPlatformPrice 获取模型的平台定价（积分/百万token）
// 严格模式：ModelPricing 不存在时返回 ErrModelNotPriced，禁止用成本价兜底扣费。
// 历史上此处曾用 ai_models.input_cost_rmb 做 fallback，导致未维护售价的模型按成本价扣费，
// 平台利润为 0 甚至为负。已改为强校验，请通过 /admin/models/repair-pricing 一次性补齐。
func (c *PricingCalculator) getPlatformPrice(ctx context.Context, modelID uint) (*model.ModelPricing, error) {
	var mp model.ModelPricing
	err := c.db.WithContext(ctx).
		Where("model_id = ?", modelID).
		Order("effective_from DESC").
		First(&mp).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("model %d: %w", modelID, ErrModelNotPriced)
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
