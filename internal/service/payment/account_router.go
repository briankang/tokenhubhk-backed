package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

const (
	accountPenaltyTTL = 5 * time.Minute
	accountPenaltyKey = "payment:account:penalty:"
)

// SelectAccountRequest 账号选择请求
type SelectAccountRequest struct {
	ProviderType string   // WECHAT/ALIPAY/STRIPE/PAYPAL
	Currency     string   // USD/CNY/EUR/...
	Region       string   // US/EU/APAC/CN/...
	ExcludeIDs   []uint64 // 重试时排除的失败账号
}

// AccountRouter 多账号权重路由
type AccountRouter struct {
	db    *gorm.DB
	redis *goredis.Client
	rng   *rand.Rand
}

// NewAccountRouter 构造函数
func NewAccountRouter(db *gorm.DB, redis *goredis.Client) *AccountRouter {
	return &AccountRouter{
		db:    db,
		redis: redis,
		rng:   rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// SelectAccount 选择一个支付账号
//
// 流程：
//  1. 查询 active=true、provider_type 匹配的账号
//  2. 过滤 ExcludeIDs
//  3. 过滤 supported_currencies / supported_regions 不匹配的（空=全部支持）
//  4. 过滤 Redis penalty 内的账号
//  5. 按 priority 升序分组，同 priority 内按 weight 加权随机
//  6. 严格匹配无结果 → 放宽 region 匹配 → 再放宽 currency 匹配 → 全失败返回错误
func (r *AccountRouter) SelectAccount(ctx context.Context, req SelectAccountRequest) (*model.PaymentProviderAccount, error) {
	if req.ProviderType == "" {
		return nil, fmt.Errorf("provider_type is required")
	}

	all, err := r.loadActiveAccounts(ctx, req.ProviderType)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("no active account for provider %s", req.ProviderType)
	}

	// 三段式 fallback：严格 → 放宽地区 → 放宽币种
	for _, mode := range []int{0, 1, 2} {
		candidates := r.filter(ctx, all, req, mode)
		if len(candidates) > 0 {
			return r.weightedPick(candidates), nil
		}
	}

	return nil, fmt.Errorf("no available account matches provider=%s currency=%s region=%s",
		req.ProviderType, req.Currency, req.Region)
}

func (r *AccountRouter) loadActiveAccounts(ctx context.Context, providerType string) ([]model.PaymentProviderAccount, error) {
	var accounts []model.PaymentProviderAccount
	err := r.db.WithContext(ctx).
		Where("provider_type = ? AND is_active = ?", providerType, true).
		Order("priority ASC, id ASC").
		Find(&accounts).Error
	return accounts, err
}

// filter 根据 mode 过滤账号
//
//	mode=0 严格匹配 currency + region + 排除 + penalty
//	mode=1 放宽 region（忽略 region 限制）
//	mode=2 放宽 currency 和 region
func (r *AccountRouter) filter(ctx context.Context, all []model.PaymentProviderAccount, req SelectAccountRequest, mode int) []model.PaymentProviderAccount {
	excluded := make(map[uint64]bool, len(req.ExcludeIDs))
	for _, id := range req.ExcludeIDs {
		excluded[id] = true
	}

	out := make([]model.PaymentProviderAccount, 0, len(all))
	// 仅保留最高优先级（priority 最小）
	minPriority := -1
	for _, acc := range all {
		if minPriority == -1 || acc.Priority < minPriority {
			minPriority = acc.Priority
		}
	}

	for _, acc := range all {
		if acc.Priority != minPriority {
			continue
		}
		if excluded[acc.ID] {
			continue
		}
		if r.isPenaltyActive(ctx, acc.ID) {
			continue
		}
		if mode <= 1 && req.Currency != "" && !matchCSV(acc.SupportedCurrencies, req.Currency) {
			continue
		}
		if mode == 0 && req.Region != "" && !matchCSV(acc.SupportedRegions, req.Region) {
			continue
		}
		out = append(out, acc)
	}
	return out
}

// weightedPick 加权随机选择
func (r *AccountRouter) weightedPick(candidates []model.PaymentProviderAccount) *model.PaymentProviderAccount {
	if len(candidates) == 1 {
		return &candidates[0]
	}
	totalWeight := 0
	for _, c := range candidates {
		w := c.Weight
		if w <= 0 {
			w = 1
		}
		totalWeight += w
	}
	if totalWeight <= 0 {
		// 全 0 权重退化为均匀随机
		return &candidates[r.rng.Intn(len(candidates))]
	}
	pick := r.rng.Intn(totalWeight)
	for i := range candidates {
		w := candidates[i].Weight
		if w <= 0 {
			w = 1
		}
		pick -= w
		if pick < 0 {
			return &candidates[i]
		}
	}
	return &candidates[len(candidates)-1]
}

// matchCSV 检查 csv 字符串是否包含 target（空 csv = 全部匹配）
func matchCSV(csv, target string) bool {
	if csv == "" {
		return true
	}
	csv = strings.ToUpper(csv)
	target = strings.ToUpper(target)
	for _, item := range strings.Split(csv, ",") {
		if strings.TrimSpace(item) == target {
			return true
		}
	}
	return false
}

// isPenaltyActive 检查 Redis 是否有活跃 penalty
func (r *AccountRouter) isPenaltyActive(ctx context.Context, accountID uint64) bool {
	if r.redis == nil {
		return false
	}
	key := fmt.Sprintf("%s%d", accountPenaltyKey, accountID)
	exists, err := r.redis.Exists(ctx, key).Result()
	if err != nil {
		return false
	}
	return exists > 0
}

// MarkAccountFailed 标记账号失败（Redis penalty + DB FailureCount++）
func (r *AccountRouter) MarkAccountFailed(ctx context.Context, accountID uint64, reason string) {
	if accountID == 0 {
		return
	}
	if r.redis != nil {
		key := fmt.Sprintf("%s%d", accountPenaltyKey, accountID)
		_ = r.redis.Set(ctx, key, reason, accountPenaltyTTL).Err()
	}
	now := time.Now()
	if err := r.db.WithContext(ctx).Model(&model.PaymentProviderAccount{}).
		Where("id = ?", accountID).
		Updates(map[string]interface{}{
			"failure_count":  gorm.Expr("failure_count + 1"),
			"last_failed_at": &now,
		}).Error; err != nil {
		logger.L.Warn("mark account failed update db error", zap.Uint64("id", accountID), zap.Error(err))
	}
}

// MarkAccountSuccess 标记账号成功（清 Redis penalty + 累计统计）
func (r *AccountRouter) MarkAccountSuccess(ctx context.Context, accountID uint64, amountRMB float64) {
	if accountID == 0 {
		return
	}
	if r.redis != nil {
		key := fmt.Sprintf("%s%d", accountPenaltyKey, accountID)
		_ = r.redis.Del(ctx, key).Err()
	}
	if err := r.db.WithContext(ctx).Model(&model.PaymentProviderAccount{}).
		Where("id = ?", accountID).
		Updates(map[string]interface{}{
			"total_orders":     gorm.Expr("total_orders + 1"),
			"total_amount_rmb": gorm.Expr("total_amount_rmb + ?", amountRMB),
		}).Error; err != nil {
		logger.L.Warn("mark account success update db error", zap.Uint64("id", accountID), zap.Error(err))
	}
}

// ==================== 配置管理 CRUD ====================

// CreateAccount 创建账号（config_json 需调用者已加密）
func (r *AccountRouter) CreateAccount(ctx context.Context, acc *model.PaymentProviderAccount) error {
	return r.db.WithContext(ctx).Create(acc).Error
}

// UpdateAccount 更新账号
func (r *AccountRouter) UpdateAccount(ctx context.Context, id uint64, updates map[string]interface{}) error {
	return r.db.WithContext(ctx).Model(&model.PaymentProviderAccount{}).
		Where("id = ?", id).Updates(updates).Error
}

// DeleteAccount 软删除账号（实际是禁用）
func (r *AccountRouter) DeleteAccount(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Delete(&model.PaymentProviderAccount{}, id).Error
}

// ToggleAccount 切换启用状态
func (r *AccountRouter) ToggleAccount(ctx context.Context, id uint64) error {
	var acc model.PaymentProviderAccount
	if err := r.db.WithContext(ctx).First(&acc, id).Error; err != nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&acc).Update("is_active", !acc.IsActive).Error
}

// ListAccounts 列出账号（管理员后台用）
func (r *AccountRouter) ListAccounts(ctx context.Context, providerType string) ([]model.PaymentProviderAccount, error) {
	q := r.db.WithContext(ctx).Order("provider_type ASC, priority ASC, id ASC")
	if providerType != "" {
		q = q.Where("provider_type = ?", providerType)
	}
	var list []model.PaymentProviderAccount
	err := q.Find(&list).Error
	return list, err
}

// GetAccount 按 ID 查询
func (r *AccountRouter) GetAccount(ctx context.Context, id uint64) (*model.PaymentProviderAccount, error) {
	var acc model.PaymentProviderAccount
	if err := r.db.WithContext(ctx).First(&acc, id).Error; err != nil {
		return nil, err
	}
	return &acc, nil
}

// ParseConfigJSON 解析账号 config_json（解密后由调用方传入）
func ParseConfigJSON(plaintext string, target interface{}) error {
	if plaintext == "" {
		return fmt.Errorf("empty config json")
	}
	return json.Unmarshal([]byte(plaintext), target)
}
