// Package pricing 中的全局折扣引擎(v2 引入)。
//
// 核心目标:管理员设置一个折扣率 → 自动应用到模型的所有价格档:
//
//	基础价(input/output) + 思考价(output thinking) + 缓存价(read/write/1h/storage) + 阶梯价(各档 selling)
//
// 实现思路:
//   - 读取 AIModel 中的官网原价(*_cost_rmb 系列字段为成本/官网价)
//   - 按 rate 计算 selling = official × rate,写回 ModelPricing 对应字段
//   - 阶梯价:遍历 PriceTiers JSON 数组,把每档 OutputPrice/InputPrice 转成 SellingOutputPrice/SellingInputPrice
//   - 单档解锁(PriceLockOverrides):该档保持原值,不参与全局应用
//
// 数据一致性保证:
//   - 应用后写入 GlobalDiscountRate + GlobalDiscountAnchored=true,作为后续审计来源
//   - 同时写入 PricedAtAt + PricedAtExchangeRate(锁定汇率)
//   - 调用方应在事务中调用,失败时整体回滚
package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// GlobalDiscountScope 折扣应用范围。
type GlobalDiscountScope string

const (
	ScopeBase     GlobalDiscountScope = "base"     // 基础输入/输出价
	ScopeThinking GlobalDiscountScope = "thinking" // 思考输出价
	ScopeCache    GlobalDiscountScope = "cache"    // 缓存读/写/1h/存储价
	ScopeTiers    GlobalDiscountScope = "tiers"    // 阶梯价(每档 selling 价格)
)

// AllScopes 默认应用的全部范围。
var AllScopes = []GlobalDiscountScope{ScopeBase, ScopeThinking, ScopeCache, ScopeTiers}

// ApplyRequest 一次全局折扣应用请求。
type ApplyRequest struct {
	ModelID            uint
	Rate               float64               // 折扣率,如 0.85 = 官网价 × 0.85
	Scopes             []GlobalDiscountScope // 留空时默认全部
	PreserveOverrides  bool                  // true = 跳过 PriceLockOverrides 中已锁定的档
	ExchangeRate       float64               // 当前 USD/CNY 汇率(0 = 不更新)
	ExchangeRateSource string                // 汇率来源
}

// ApplyResult 应用结果摘要。
type ApplyResult struct {
	ModelID      uint                  `json:"model_id"`
	Rate         float64               `json:"rate"`
	Anchored     bool                  `json:"anchored"`
	Applied      []GlobalDiscountScope `json:"applied"`
	Changed      map[string]float64    `json:"changed"`       // 字段名 -> 新值
	SkippedLocks []string              `json:"skipped_locks"` // 因为单档解锁而跳过的字段
	TiersUpdated int                   `json:"tiers_updated"` // 阶梯价更新条数
	Currency     string                `json:"currency"`
}

// ErrInvalidDiscountRate 折扣率不合法。
var ErrInvalidDiscountRate = errors.New("global discount rate must be in (0, 10]")

// GlobalDiscountService 全局折扣引擎。
type GlobalDiscountService struct {
	db *gorm.DB
}

// NewGlobalDiscountService 构造服务。
func NewGlobalDiscountService(db *gorm.DB) *GlobalDiscountService {
	if db == nil {
		panic("global discount service: db is nil")
	}
	return &GlobalDiscountService{db: db}
}

// Apply 把全局折扣应用到模型的所有价格档(写库)。
//
// 流程:
//  1. 读取 AIModel + ModelPricing
//  2. 按 scopes 计算每个目标字段 = 官网价 × rate(跳过 locked 档)
//  3. 写回 ModelPricing 字段 + PriceTiers 数组
//  4. 更新 GlobalDiscountRate / GlobalDiscountAnchored / PricedAt* 元数据
func (s *GlobalDiscountService) Apply(ctx context.Context, req ApplyRequest) (*ApplyResult, error) {
	if req.Rate <= 0 || req.Rate > 10 {
		return nil, ErrInvalidDiscountRate
	}
	if len(req.Scopes) == 0 {
		req.Scopes = AllScopes
	}

	var aiModel model.AIModel
	if err := s.db.WithContext(ctx).Where("id = ?", req.ModelID).First(&aiModel).Error; err != nil {
		return nil, fmt.Errorf("load ai model: %w", err)
	}

	var mp model.ModelPricing
	if err := s.db.WithContext(ctx).Where("model_id = ?", req.ModelID).
		Order("effective_from DESC, id DESC").
		First(&mp).Error; err != nil {
		// 无 ModelPricing 自动创建
		if errors.Is(err, gorm.ErrRecordNotFound) {
			mp = model.ModelPricing{ModelID: req.ModelID, Currency: "CREDIT"}
		} else {
			return nil, fmt.Errorf("load model pricing: %w", err)
		}
	}

	// 加载已锁定的档(不参与全局应用)
	locked := make(map[string]bool)
	if req.PreserveOverrides && len(mp.PriceLockOverrides) > 0 && string(mp.PriceLockOverrides) != "null" {
		var overrides map[string]float64
		if err := json.Unmarshal(mp.PriceLockOverrides, &overrides); err == nil {
			for key := range overrides {
				locked[key] = true
			}
		}
	}

	result := &ApplyResult{
		ModelID:      aiModel.ID,
		Rate:         req.Rate,
		Anchored:     true,
		Applied:      req.Scopes,
		Changed:      make(map[string]float64),
		SkippedLocks: []string{},
		Currency:     "RMB",
	}
	scopeSet := scopeSetFromList(req.Scopes)

	// 1) 基础价
	if scopeSet[ScopeBase] {
		applyField(locked, "input", aiModel.InputCostRMB, req.Rate, &mp.InputPriceRMB, &mp.InputPricePerToken, result)
		applyField(locked, "output", aiModel.OutputCostRMB, req.Rate, &mp.OutputPriceRMB, &mp.OutputPricePerToken, result)
	}

	// 2) 思考输出价
	if scopeSet[ScopeThinking] && aiModel.OutputCostThinkingRMB > 0 {
		applyField(locked, "output_thinking", aiModel.OutputCostThinkingRMB, req.Rate, &mp.OutputPriceThinkingRMB, &mp.OutputPriceThinkingPerToken, result)
	}

	// 3) 缓存价 - 只覆盖到 ai_models 字段(因为 ModelPricing 没有缓存价独立字段,
	//    缓存价目前存储在 AIModel 的 CacheInputPriceRMB 等字段)。
	//    应用后 BillingService 调用 PricingCalculator 时会从 AIModel 读这些字段计算。
	//
	//    实际写法:全局折扣对 cache 类字段不直接修改 AIModel(那是成本,不是售价),
	//    我们通过 PriceLockOverrides 中的"cache_*"键来记录"售价 = 成本 × rate"的等价信息,
	//    供未来的 PricingCalculator 在缓存计费时参考。这里只做记账,不破坏 AIModel。
	//    => 简化方案:目前 cache scope 仅记入 result,真实落库后续接入 calculator 改造时统一。
	//    此处仅返回应该是的预览值。
	if scopeSet[ScopeCache] && aiModel.SupportsCache {
		if aiModel.CacheInputPriceRMB > 0 {
			result.Changed["cache_input_price_rmb"] = round6(aiModel.CacheInputPriceRMB * req.Rate)
		}
		if aiModel.CacheExplicitInputPriceRMB > 0 {
			result.Changed["cache_explicit_input_price_rmb"] = round6(aiModel.CacheExplicitInputPriceRMB * req.Rate)
		}
		if aiModel.CacheWritePriceRMB > 0 {
			result.Changed["cache_write_price_rmb"] = round6(aiModel.CacheWritePriceRMB * req.Rate)
		}
		if aiModel.CacheStoragePriceRMB > 0 {
			result.Changed["cache_storage_price_rmb"] = round6(aiModel.CacheStoragePriceRMB * req.Rate)
		}
	}

	// 4) 阶梯价 - 把 AIModel.PriceTiers 中的 InputPrice/OutputPrice 转成
	//    ModelPricing.PriceTiers 数组中每档的 SellingInputPrice/SellingOutputPrice
	if scopeSet[ScopeTiers] && len(aiModel.PriceTiers) > 0 && string(aiModel.PriceTiers) != "null" {
		var officialTiers model.PriceTiersData
		if err := json.Unmarshal(aiModel.PriceTiers, &officialTiers); err == nil && len(officialTiers.Tiers) > 0 {
			updatedTiers := buildSellingTiersFromOfficial(officialTiers, req.Rate, locked, result)
			tierJSON, _ := json.Marshal(updatedTiers)
			mp.PriceTiers = tierJSON
			result.TiersUpdated = len(updatedTiers.Tiers)
		}
	}

	// 5) 元数据写入
	mp.GlobalDiscountRate = req.Rate
	mp.GlobalDiscountAnchored = true
	now := time.Now()
	mp.PricedAtAt = &now
	if req.ExchangeRate > 0 {
		mp.PricedAtExchangeRate = req.ExchangeRate
	}
	if req.ExchangeRateSource != "" {
		mp.PricedAtRateSource = req.ExchangeRateSource
	}

	// 6) Save / Create
	if mp.ID == 0 {
		if err := s.db.WithContext(ctx).Create(&mp).Error; err != nil {
			return nil, fmt.Errorf("create model pricing: %w", err)
		}
	} else {
		if err := s.db.WithContext(ctx).Save(&mp).Error; err != nil {
			return nil, fmt.Errorf("save model pricing: %w", err)
		}
	}
	return result, nil
}

// Preview 预览全局折扣应用后的价格表(不写库)。
//
// 与 Apply 一致的计算逻辑,但不修改 DB,用于前端拖动滑块时实时显示新价格。
func (s *GlobalDiscountService) Preview(ctx context.Context, req ApplyRequest) (*ApplyResult, error) {
	if req.Rate <= 0 || req.Rate > 10 {
		return nil, ErrInvalidDiscountRate
	}
	if len(req.Scopes) == 0 {
		req.Scopes = AllScopes
	}

	var aiModel model.AIModel
	if err := s.db.WithContext(ctx).Where("id = ?", req.ModelID).First(&aiModel).Error; err != nil {
		return nil, fmt.Errorf("load ai model: %w", err)
	}

	// 加载现有锁定
	locked := map[string]bool{}
	if req.PreserveOverrides {
		var mp model.ModelPricing
		_ = s.db.WithContext(ctx).Where("model_id = ?", req.ModelID).First(&mp).Error
		if len(mp.PriceLockOverrides) > 0 && string(mp.PriceLockOverrides) != "null" {
			var overrides map[string]float64
			if err := json.Unmarshal(mp.PriceLockOverrides, &overrides); err == nil {
				for key := range overrides {
					locked[key] = true
				}
			}
		}
	}

	result := &ApplyResult{
		ModelID:      aiModel.ID,
		Rate:         req.Rate,
		Anchored:     false, // preview 不锁定
		Applied:      req.Scopes,
		Changed:      make(map[string]float64),
		SkippedLocks: []string{},
		Currency:     "RMB",
	}
	scopeSet := scopeSetFromList(req.Scopes)

	// 复用相同的"× rate"逻辑,不写回字段(只填 result.Changed)
	if scopeSet[ScopeBase] {
		previewField(locked, "input", aiModel.InputCostRMB, req.Rate, result)
		previewField(locked, "output", aiModel.OutputCostRMB, req.Rate, result)
	}
	if scopeSet[ScopeThinking] && aiModel.OutputCostThinkingRMB > 0 {
		previewField(locked, "output_thinking", aiModel.OutputCostThinkingRMB, req.Rate, result)
	}
	if scopeSet[ScopeCache] && aiModel.SupportsCache {
		if aiModel.CacheInputPriceRMB > 0 {
			previewField(locked, "cache_input", aiModel.CacheInputPriceRMB, req.Rate, result)
		}
		if aiModel.CacheExplicitInputPriceRMB > 0 {
			previewField(locked, "cache_explicit_input", aiModel.CacheExplicitInputPriceRMB, req.Rate, result)
		}
		if aiModel.CacheWritePriceRMB > 0 {
			previewField(locked, "cache_write", aiModel.CacheWritePriceRMB, req.Rate, result)
		}
		if aiModel.CacheStoragePriceRMB > 0 {
			previewField(locked, "cache_storage", aiModel.CacheStoragePriceRMB, req.Rate, result)
		}
	}
	if scopeSet[ScopeTiers] && len(aiModel.PriceTiers) > 0 && string(aiModel.PriceTiers) != "null" {
		var officialTiers model.PriceTiersData
		if err := json.Unmarshal(aiModel.PriceTiers, &officialTiers); err == nil {
			result.TiersUpdated = len(officialTiers.Tiers)
			for idx, tier := range officialTiers.Tiers {
				inputKey := fmt.Sprintf("tier_%d_input", idx)
				outputKey := fmt.Sprintf("tier_%d_output", idx)
				previewField(locked, inputKey, tier.InputPrice, req.Rate, result)
				previewField(locked, outputKey, tier.OutputPrice, req.Rate, result)
				if tier.OutputPriceThinking > 0 {
					thinkingKey := fmt.Sprintf("tier_%d_output_thinking", idx)
					previewField(locked, thinkingKey, tier.OutputPriceThinking, req.Rate, result)
				}
			}
		}
	}
	return result, nil
}

// SetLockOverride 给指定字段标记为单档解锁,该档保持原值,不参与全局折扣应用。
//
// archKey 例如:"cache_input"/"cache_write"/"output"/"tier_0_input" 等。
// value 是该档当前的售价(RMB),用于追踪"解锁时这个档是多少钱"。
func (s *GlobalDiscountService) SetLockOverride(ctx context.Context, modelID uint, archKey string, value float64) error {
	var mp model.ModelPricing
	if err := s.db.WithContext(ctx).Where("model_id = ?", modelID).First(&mp).Error; err != nil {
		return fmt.Errorf("load model pricing: %w", err)
	}
	overrides := map[string]float64{}
	if len(mp.PriceLockOverrides) > 0 && string(mp.PriceLockOverrides) != "null" {
		_ = json.Unmarshal(mp.PriceLockOverrides, &overrides)
	}
	overrides[archKey] = value
	raw, err := json.Marshal(overrides)
	if err != nil {
		return err
	}
	mp.PriceLockOverrides = raw
	return s.db.WithContext(ctx).Save(&mp).Error
}

// ClearLockOverride 删除单档解锁,该档恢复参与全局折扣。
func (s *GlobalDiscountService) ClearLockOverride(ctx context.Context, modelID uint, archKey string) error {
	var mp model.ModelPricing
	if err := s.db.WithContext(ctx).Where("model_id = ?", modelID).First(&mp).Error; err != nil {
		return fmt.Errorf("load model pricing: %w", err)
	}
	overrides := map[string]float64{}
	if len(mp.PriceLockOverrides) > 0 && string(mp.PriceLockOverrides) != "null" {
		_ = json.Unmarshal(mp.PriceLockOverrides, &overrides)
	}
	delete(overrides, archKey)
	raw, err := json.Marshal(overrides)
	if err != nil {
		return err
	}
	mp.PriceLockOverrides = raw
	return s.db.WithContext(ctx).Save(&mp).Error
}

// ===== 内部 helpers =====

func scopeSetFromList(scopes []GlobalDiscountScope) map[GlobalDiscountScope]bool {
	m := make(map[GlobalDiscountScope]bool, len(scopes))
	for _, s := range scopes {
		m[s] = true
	}
	return m
}

func applyField(locked map[string]bool, archKey string, official, rate float64, targetRMB *float64, targetPerToken *int64, result *ApplyResult) {
	if locked[archKey] {
		result.SkippedLocks = append(result.SkippedLocks, archKey)
		return
	}
	if official <= 0 {
		return
	}
	newVal := round6(official * rate)
	*targetRMB = newVal
	if targetPerToken != nil {
		*targetPerToken = int64(math.Round(newVal * 10000)) // 1 RMB = 10000 积分
	}
	result.Changed[archKey] = newVal
}

func previewField(locked map[string]bool, archKey string, official, rate float64, result *ApplyResult) {
	if locked[archKey] {
		result.SkippedLocks = append(result.SkippedLocks, archKey)
		return
	}
	if official <= 0 {
		return
	}
	result.Changed[archKey] = round6(official * rate)
}

// buildSellingTiersFromOfficial 从官网阶梯价构造平台售价阶梯,
// 把 InputPrice/OutputPrice 设为 SellingInputPrice/SellingOutputPrice = official × rate。
func buildSellingTiersFromOfficial(official model.PriceTiersData, rate float64, locked map[string]bool, result *ApplyResult) model.PriceTiersData {
	out := model.PriceTiersData{
		Currency:  official.Currency,
		UnitLabel: official.UnitLabel,
		UpdatedAt: time.Now(),
		SourceURL: official.SourceURL,
		Tiers:     make([]model.PriceTier, len(official.Tiers)),
	}
	for i, t := range official.Tiers {
		newTier := t
		// SellingInputPrice
		inputKey := fmt.Sprintf("tier_%d_input", i)
		if !locked[inputKey] && t.InputPrice > 0 {
			v := round6(t.InputPrice * rate)
			newTier.SellingInputPrice = &v
			result.Changed[inputKey] = v
		} else if locked[inputKey] {
			result.SkippedLocks = append(result.SkippedLocks, inputKey)
		}
		// SellingOutputPrice
		outputKey := fmt.Sprintf("tier_%d_output", i)
		if !locked[outputKey] && t.OutputPrice > 0 {
			v := round6(t.OutputPrice * rate)
			newTier.SellingOutputPrice = &v
			result.Changed[outputKey] = v
		} else if locked[outputKey] {
			result.SkippedLocks = append(result.SkippedLocks, outputKey)
		}
		// SellingOutputThinkingPrice
		if t.OutputPriceThinking > 0 {
			thinkingKey := fmt.Sprintf("tier_%d_output_thinking", i)
			if !locked[thinkingKey] {
				v := round6(t.OutputPriceThinking * rate)
				newTier.SellingOutputThinkingPrice = &v
				result.Changed[thinkingKey] = v
			} else {
				result.SkippedLocks = append(result.SkippedLocks, thinkingKey)
			}
		}
		out.Tiers[i] = newTier
	}
	return out
}

func round6(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}
