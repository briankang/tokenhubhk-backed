package pricing

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

const discountMissCacheTTL = 10 * time.Minute

type discountMissCacheKey struct {
	db       *gorm.DB
	kind     string
	userID   uint
	tenantID uint
	modelID  uint
	level    int
}

type discountMissCacheEntry struct {
	expiresAt time.Time
}

var discountMissCache sync.Map
var discountMissLocks sync.Map

func loadDiscountMiss(key discountMissCacheKey, now time.Time) bool {
	v, ok := discountMissCache.Load(key)
	if !ok {
		return false
	}
	entry, ok := v.(discountMissCacheEntry)
	if !ok || now.After(entry.expiresAt) {
		discountMissCache.Delete(key)
		return false
	}
	return true
}

func storeDiscountMiss(key discountMissCacheKey, now time.Time) {
	discountMissCache.Store(key, discountMissCacheEntry{expiresAt: now.Add(discountMissCacheTTL)})
}

func clearDiscountMissCache() {
	discountMissCache.Range(func(key, _ interface{}) bool {
		discountMissCache.Delete(key)
		return true
	})
	discountMissLocks.Range(func(key, _ interface{}) bool {
		discountMissLocks.Delete(key)
		return true
	})
}

func discountMissLockFor(key discountMissCacheKey) *sync.Mutex {
	v, _ := discountMissLocks.LoadOrStore(key, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func withDiscountMissLock(key discountMissCacheKey, now time.Time, query func(time.Time) (bool, error)) (bool, error) {
	if loadDiscountMiss(key, now) {
		return false, nil
	}
	lock := discountMissLockFor(key)
	lock.Lock()
	defer lock.Unlock()

	queryNow := time.Now()
	if loadDiscountMiss(key, queryNow) {
		return false, nil
	}
	found, err := query(queryNow)
	if err != nil {
		return false, err
	}
	if !found {
		storeDiscountMiss(key, queryNow)
	}
	return found, nil
}

// DiscountResult 折扣解析结果，包含租户/模型/代理层级/用户级的折扣信息
type DiscountResult struct {
	Type           string   `json:"type"`           // "user_custom" / "agent_custom" / "level_discount" / "none"
	PricingType    string   `json:"pricing_type"`   // FIXED / MARKUP / DISCOUNT / INHERIT / NONE
	InputDiscount  float64  `json:"input_discount"` // e.g. 0.8 means 20% off
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
//
// **2026-04-28 简化**:代理折扣体系(AgentPricing / AgentLevelDiscount)已物理移除,
// 仅保留 UserModelDiscount(用户级特殊折扣)。tenantID/agentLevel 参数保留以兼容调用方,
// 但仅 UserModelDiscount 路径生效。
//
// 查找顺序：
//  0. UserModelDiscount(用户+模型精确匹配,生效期内)
//  1. 无折扣(原价)
func (r *DiscountResolver) ResolveDiscount(ctx context.Context, userID uint, tenantID uint, modelID uint, agentLevel int) (*DiscountResult, error) {
	_ = tenantID  // deprecated: 代理体系已移除
	_ = agentLevel // deprecated: 代理体系已移除
	if modelID == 0 {
		return &DiscountResult{Type: "none", PricingType: "NONE", InputDiscount: 1.0, OutputDiscount: 1.0}, nil
	}
	now := time.Now()

	// Step 0: Check UserModelDiscount (user-level override, highest priority)
	if userID > 0 {
		key := discountMissCacheKey{db: r.db, kind: "user_model_discount", userID: userID, modelID: modelID}
		var userDiscount model.UserModelDiscount
		found, err := withDiscountMissLock(key, now, func(queryNow time.Time) (bool, error) {
			err := r.db.WithContext(ctx).
				Where("user_id = ? AND model_id = ? AND is_active = ?", userID, modelID, true).
				First(&userDiscount).Error
			if err == nil && userDiscount.IsEffective(queryNow) {
				return true, nil
			}
			if err != nil && err != gorm.ErrRecordNotFound {
				return false, fmt.Errorf("query user model discount: %w", err)
			}
			return false, nil
		})
		if err != nil {
			return nil, err
		}
		if found {
			return r.buildUserDiscountResult(&userDiscount), nil
		}
	}

	// Step 1: No discount (代理折扣已移除,只剩 user 级)
	return &DiscountResult{
		Type:           "none",
		PricingType:    "NONE",
		InputDiscount:  1.0,
		OutputDiscount: 1.0,
	}, nil
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
