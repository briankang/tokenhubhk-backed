package balance

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/service/referral"
)

const (
	balanceCachePrefix = "balance:user:"
	balanceCacheTTL    = 5 * time.Minute
)

// BalanceService 用户余额服务，管理充值、扣款、查询等操作
// 核心规则：1 RMB = 10,000 credits，所有计算以 credits(int64) 为主，RMB 为辅助展示字段
type BalanceService struct {
	db    *gorm.DB
	redis *goredis.Client
}

// NewBalanceService 创建余额服务实例，db 不能为 nil 否则 panic
func NewBalanceService(db *gorm.DB, redis *goredis.Client) *BalanceService {
	if db == nil {
		panic("balance service: db is nil")
	}
	return &BalanceService{db: db, redis: redis}
}

// GetBalance 获取指定用户的余额记录，若不存在则自动创建
func (s *BalanceService) GetBalance(ctx context.Context, userID, tenantID uint) (*model.UserBalance, error) {
	var ub model.UserBalance
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&ub).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			// 余额记录不存在，自动创建
			ub = model.UserBalance{
				UserID:   userID,
				TenantID: tenantID,
				Currency: "CREDIT",
			}
			if err := s.db.WithContext(ctx).Create(&ub).Error; err != nil {
				return nil, fmt.Errorf("create balance: %w", err)
			}
			return &ub, nil
		}
		return nil, fmt.Errorf("get balance: %w", err)
	}
	return &ub, nil
}

// GetBalanceCached 带 Redis 缓存的余额查询
func (s *BalanceService) GetBalanceCached(ctx context.Context, userID, tenantID uint) (*model.UserBalance, error) {
	if s.redis != nil {
		key := balanceCachePrefix + strconv.FormatUint(uint64(userID), 10)
		// 尝试从缓存读取（仅检查是否存在）
		val, err := s.redis.Get(ctx, key).Result()
		if err == nil && val != "" {
			// 缓存命中 — 仍查询数据库保证一致性
		}
		defer func() {
			_ = s.redis.Set(ctx, key, "1", balanceCacheTTL).Err()
		}()
	}
	return s.GetBalance(ctx, userID, tenantID)
}

// InvalidateCache 清除指定用户的余额缓存
func (s *BalanceService) InvalidateCache(ctx context.Context, userID uint) {
	if s.redis != nil {
		key := balanceCachePrefix + strconv.FormatUint(uint64(userID), 10)
		_ = s.redis.Del(ctx, key).Err()
	}
}

// InitBalance 为新注册用户初始化余额，包含默认免费额度
// 参数 freeCredits: 赠送的积分数量（int64）
func (s *BalanceService) InitBalance(ctx context.Context, userID, tenantID uint) error {
	// 获取当前生效的额度配置
	quota := s.getActiveQuotaConfig(ctx)
	freeCredits := quota.DefaultFreeQuota + quota.RegistrationBonus

	ub := &model.UserBalance{
		UserID:       userID,
		TenantID:     tenantID,
		Balance:      0,
		BalanceRMB:   0,
		FreeQuota:    freeCredits,
		FreeQuotaRMB: credits.CreditsToRMB(freeCredits),
		Currency:     "CREDIT",
	}

	err := s.db.WithContext(ctx).Create(ub).Error
	if err != nil {
		return fmt.Errorf("init balance: %w", err)
	}

	// 记录赠送流水
	if freeCredits > 0 {
		freeRMB := credits.CreditsToRMB(freeCredits)
		record := &model.BalanceRecord{
			UserID:        userID,
			TenantID:      tenantID,
			Type:          "GIFT",
			Amount:        freeCredits,
			AmountRMB:     freeRMB,
			BeforeBalance: 0,
			AfterBalance:  freeCredits,
			Remark:        "Registration free quota",
		}
		_ = s.db.WithContext(ctx).Create(record).Error
	}

	return nil
}

// Recharge 用户充值（管理员操作或支付回调），向用户余额添加积分
// 参数 creditsAmount: 充值的积分数量（int64）
func (s *BalanceService) Recharge(ctx context.Context, userID, tenantID uint, creditsAmount int64, remark, relatedID string) (*model.UserBalance, error) {
	if creditsAmount <= 0 {
		return nil, fmt.Errorf("recharge amount must be positive")
	}

	amountRMB := credits.CreditsToRMB(creditsAmount)

	var ub model.UserBalance
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 行级锁保证并发安全
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", userID).First(&ub).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				// 余额记录不存在，先创建
				ub = model.UserBalance{UserID: userID, TenantID: tenantID, Currency: "CREDIT"}
				if err := tx.Create(&ub).Error; err != nil {
					return fmt.Errorf("create balance: %w", err)
				}
				// 重新加锁
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					Where("user_id = ?", userID).First(&ub).Error; err != nil {
					return fmt.Errorf("lock balance: %w", err)
				}
			} else {
				return fmt.Errorf("lock balance: %w", err)
			}
		}

		before := ub.Balance
		ub.Balance += creditsAmount
		ub.BalanceRMB = credits.CreditsToRMB(ub.Balance)
		ub.FreeQuotaRMB = credits.CreditsToRMB(ub.FreeQuota)

		if err := tx.Save(&ub).Error; err != nil {
			return fmt.Errorf("update balance: %w", err)
		}

		// 记录充值流水
		record := &model.BalanceRecord{
			UserID:        userID,
			TenantID:      tenantID,
			Type:          "RECHARGE",
			Amount:        creditsAmount,
			AmountRMB:     amountRMB,
			BeforeBalance: before,
			AfterBalance:  ub.Balance,
			Remark:        remark,
			RelatedID:     relatedID,
		}
		return tx.Create(record).Error
	})

	if err != nil {
		return nil, err
	}

	s.InvalidateCache(ctx, userID)
	return &ub, nil
}

// RechargeRMB 用户充值（人民币金额），内部转换为积分
// 参数 rmbAmount: 充值的人民币金额（float64）
func (s *BalanceService) RechargeRMB(ctx context.Context, userID, tenantID uint, rmbAmount float64, remark, relatedID string) (*model.UserBalance, error) {
	creditsAmount := credits.RMBToCredits(rmbAmount)
	return s.Recharge(ctx, userID, tenantID, creditsAmount, remark, relatedID)
}

// Deduct 消费扣款，余额不足时返回错误。优先扣减免费额度，再扣减充值余额
// 参数 creditsAmount: 扣减的积分数量（int64）
func (s *BalanceService) Deduct(ctx context.Context, userID, tenantID uint, creditsAmount int64, remark, relatedID string) (*model.UserBalance, error) {
	if creditsAmount <= 0 {
		return nil, fmt.Errorf("deduct amount must be positive")
	}

	amountRMB := credits.CreditsToRMB(creditsAmount)

	var ub model.UserBalance
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 行级锁保证并发安全
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", userID).First(&ub).Error; err != nil {
			return fmt.Errorf("lock balance: %w", err)
		}

		available := ub.Balance + ub.FreeQuota - ub.FrozenAmount
		if available < creditsAmount {
			return fmt.Errorf("insufficient balance: available=%d credits, required=%d credits", available, creditsAmount)
		}

		before := ub.Balance

		// 优先扣减免费额度，再扣减余额
		remaining := creditsAmount
		if ub.FreeQuota > 0 {
			if ub.FreeQuota >= remaining {
				ub.FreeQuota -= remaining
				remaining = 0
			} else {
				remaining -= ub.FreeQuota
				ub.FreeQuota = 0
			}
		}
		if remaining > 0 {
			ub.Balance -= remaining
			ub.BalanceRMB = credits.CreditsToRMB(ub.Balance)
		}
		ub.TotalConsumed += creditsAmount
		ub.TotalConsumedRMB = credits.CreditsToRMB(ub.TotalConsumed)

		if err := tx.Save(&ub).Error; err != nil {
			return fmt.Errorf("update balance: %w", err)
		}

		record := &model.BalanceRecord{
			UserID:        userID,
			TenantID:      tenantID,
			Type:          "CONSUME",
			Amount:        -creditsAmount,
			AmountRMB:     -amountRMB,
			BeforeBalance: before,
			AfterBalance:  ub.Balance,
			Remark:        remark,
			RelatedID:     relatedID,
		}
		return tx.Create(record).Error
	})

	if err != nil {
		return nil, err
	}

	s.InvalidateCache(ctx, userID)

	// v3.1: 尝试解锁邀请归因(被邀者累计消费达门槛后邀请人才能拿佣金)
	// 失败不阻塞消费流程
	referral.TryUnlockAttribution(ctx, s.db, userID)
	// v3.1: 尝试发放被邀者一次性奖励(累计消费达 InviteeUnlockCredits)
	referral.TryGrantInviteeBonus(ctx, s.db, userID)

	return &ub, nil
}

// DeductForRequest 消费扣款（用于无冻结场景的兼容方法）
func (s *BalanceService) DeductForRequest(ctx context.Context, userID, tenantID uint, creditsAmount int64, modelName, requestID string) error {
	_, err := s.Deduct(ctx, userID, tenantID, creditsAmount, fmt.Sprintf("消费 %s", modelName), requestID)
	return err
}

// AdminAdjust 管理员手动调整用户余额（可正可负）
// 参数 creditsAmount: 调整的积分数量（正数增加，负数减少）
func (s *BalanceService) AdminAdjust(ctx context.Context, userID, tenantID uint, creditsAmount int64, remark string) (*model.UserBalance, error) {
	if creditsAmount == 0 {
		return nil, fmt.Errorf("adjust amount cannot be zero")
	}

	amountRMB := credits.CreditsToRMB(creditsAmount)

	var ub model.UserBalance
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", userID).First(&ub).Error; err != nil {
			return fmt.Errorf("lock balance: %w", err)
		}

		before := ub.Balance
		ub.Balance += creditsAmount
		if ub.Balance < 0 {
			ub.Balance = 0
		}
		ub.BalanceRMB = credits.CreditsToRMB(ub.Balance)
		ub.FreeQuotaRMB = credits.CreditsToRMB(ub.FreeQuota)

		if err := tx.Save(&ub).Error; err != nil {
			return fmt.Errorf("update balance: %w", err)
		}

		record := &model.BalanceRecord{
			UserID:        userID,
			TenantID:      tenantID,
			Type:          "ADMIN_ADJUST",
			Amount:        creditsAmount,
			AmountRMB:     amountRMB,
			BeforeBalance: before,
			AfterBalance:  ub.Balance,
			Remark:        remark,
		}
		return tx.Create(record).Error
	})

	if err != nil {
		return nil, err
	}

	s.InvalidateCache(ctx, userID)
	return &ub, nil
}

// HasSufficientBalance 检查用户是否有足够余额，返回是否充足及可用积分
func (s *BalanceService) HasSufficientBalance(ctx context.Context, userID uint) (bool, int64, error) {
	var ub model.UserBalance
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&ub).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, 0, nil
		}
		return false, 0, err
	}
	available := ub.Balance + ub.FreeQuota - ub.FrozenAmount
	return available > 0, available, nil
}

// ListRecords 分页查询用户的余额变动记录
func (s *BalanceService) ListRecords(ctx context.Context, userID uint, page, pageSize int) ([]model.BalanceRecord, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var total int64
	query := s.db.WithContext(ctx).Model(&model.BalanceRecord{}).Where("user_id = ?", userID)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var records []model.BalanceRecord
	offset := (page - 1) * pageSize
	if err := query.Order("created_at DESC").Offset(offset).Limit(pageSize).Find(&records).Error; err != nil {
		return nil, 0, err
	}

	return records, total, nil
}

// GetQuotaConfig 获取当前生效的额度配置
func (s *BalanceService) GetQuotaConfig(ctx context.Context) *model.QuotaConfig {
	return s.getActiveQuotaConfig(ctx)
}

// UpdateQuotaConfig 更新或创建额度配置
func (s *BalanceService) UpdateQuotaConfig(ctx context.Context, cfg *model.QuotaConfig) error {
	var existing model.QuotaConfig
	err := s.db.WithContext(ctx).Where("is_active = ?", true).First(&existing).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			cfg.IsActive = true
			return s.db.WithContext(ctx).Create(cfg).Error
		}
		return err
	}
	existing.DefaultFreeQuota = cfg.DefaultFreeQuota
	existing.RegistrationBonus = cfg.RegistrationBonus
	// v3.1 邀请双向奖励字段
	existing.InviteeBonus = cfg.InviteeBonus
	existing.InviteeUnlockCredits = cfg.InviteeUnlockCredits
	existing.InviterBonus = cfg.InviterBonus
	existing.InviterUnlockPaidRMB = cfg.InviterUnlockPaidRMB
	existing.InviterMonthlyCap = cfg.InviterMonthlyCap
	existing.Description = cfg.Description
	return s.db.WithContext(ctx).Save(&existing).Error
}

// getActiveQuotaConfig 获取当前活跃的额度配置，查询失败时返回默认值
func (s *BalanceService) getActiveQuotaConfig(ctx context.Context) *model.QuotaConfig {
	var cfg model.QuotaConfig
	err := s.db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error
	if err != nil {
		// 查询失败时返回默认配置：1元 = 10000积分
		return &model.QuotaConfig{
			DefaultFreeQuota:  10000, // 1元
			RegistrationBonus: 0,
			IsActive:          true,
			Description:       "Default quota config",
		}
	}
	return &cfg
}

// ========== 预扣费 + 精确结算 ==========

const (
	// freezeLockTTL 预扣费分布式锁超时时间
	freezeLockTTL = 10 * time.Second
	// freezeExpiry 冻结记录超时时间（5分钟未结算自动释放）
	freezeExpiry = 5 * time.Minute
)

// FreezeBalance 预扣费：根据预估费用冻结额度
// 参数 estimatedCredits: 预估消费积分数量（int64）
// 返回 freezeID: 冻结记录唯一标识
func (s *BalanceService) FreezeBalance(ctx context.Context, userID, tenantID uint, estimatedCredits int64, modelName, requestID string) (string, error) {
	if estimatedCredits <= 0 {
		return "", nil // 免费请求无需冻结
	}

	estimatedRMB := credits.CreditsToRMB(estimatedCredits)

	// 获取 Redis 分布式锁，防止同一用户并发超扣
	lockKey := fmt.Sprintf("user:%d:balance_lock", userID)
	var lock *pkgredis.DistributedLock
	var err error
	if s.redis != nil {
		lock, err = pkgredis.Lock(ctx, lockKey, freezeLockTTL)
		if err != nil {
			return "", fmt.Errorf("acquire balance lock: %w", err)
		}
		defer lock.Unlock(ctx)
	}

	freezeID := uuid.New().String()

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// SELECT ... FOR UPDATE 行级锁
		var ub model.UserBalance
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", userID).First(&ub).Error; err != nil {
			return fmt.Errorf("lock balance: %w", err)
		}

		// 计算可用余额 = 余额 + 赠送额度 - 已冻结金额
		available := ub.Balance + ub.FreeQuota - ub.FrozenAmount
		if available < estimatedCredits {
			return fmt.Errorf("insufficient balance: available=%d credits, required=%d credits", available, estimatedCredits)
		}

		// 增加冻结金额
		ub.FrozenAmount += estimatedCredits
		if err := tx.Save(&ub).Error; err != nil {
			return fmt.Errorf("update frozen amount: %w", err)
		}

		// 创建冻结记录
		freezeRecord := &model.FreezeRecord{
			FreezeID:        freezeID,
			UserID:          userID,
			TenantID:        tenantID,
			FrozenAmount:    estimatedCredits,
			FrozenAmountRMB: estimatedRMB,
			Status:          "FROZEN",
			ModelName:       modelName,
			RequestID:       requestID,
		}
		if err := tx.Create(freezeRecord).Error; err != nil {
			return fmt.Errorf("create freeze record: %w", err)
		}

		// 记录冻结流水
		balRecord := &model.BalanceRecord{
			UserID:        userID,
			TenantID:      tenantID,
			Type:          "FREEZE",
			Amount:        -estimatedCredits,
			AmountRMB:     -estimatedRMB,
			BeforeBalance: ub.Balance,
			AfterBalance:  ub.Balance,
			Remark:        fmt.Sprintf("预扣费冻结 %s", modelName),
			RelatedID:     freezeID,
		}
		return tx.Create(balRecord).Error
	})

	if err != nil {
		return "", err
	}

	s.InvalidateCache(ctx, userID)
	return freezeID, nil
}

// SettleBalance 精确结算：根据实际消费积分计算费用，解冻差额
// 参数 actualCredits: 实际消费积分数量（int64）
func (s *BalanceService) SettleBalance(ctx context.Context, freezeID string, actualCredits int64) error {
	if freezeID == "" {
		return nil // 无冻结记录（免费请求）
	}

	actualRMB := credits.CreditsToRMB(actualCredits)

	// 查询冻结记录获取用户ID
	var fr model.FreezeRecord
	if err := s.db.WithContext(ctx).Where("freeze_id = ? AND status = 'FROZEN'", freezeID).First(&fr).Error; err != nil {
		return fmt.Errorf("find freeze record: %w", err)
	}

	// 获取分布式锁
	lockKey := fmt.Sprintf("user:%d:balance_lock", fr.UserID)
	var lock *pkgredis.DistributedLock
	var err error
	if s.redis != nil {
		lock, err = pkgredis.Lock(ctx, lockKey, freezeLockTTL)
		if err != nil {
			return fmt.Errorf("acquire balance lock: %w", err)
		}
		defer lock.Unlock(ctx)
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 行级锁获取余额
		var ub model.UserBalance
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", fr.UserID).First(&ub).Error; err != nil {
			return fmt.Errorf("lock balance: %w", err)
		}

		before := ub.Balance

		// 释放冻结额度
		ub.FrozenAmount -= fr.FrozenAmount
		if ub.FrozenAmount < 0 {
			ub.FrozenAmount = 0 // 防止负数
		}

		// 实际扣费：优先扣减免费额度，再扣减余额
		if actualCredits > 0 {
			remaining := actualCredits
			if ub.FreeQuota > 0 {
				if ub.FreeQuota >= remaining {
					ub.FreeQuota -= remaining
					remaining = 0
				} else {
					remaining -= ub.FreeQuota
					ub.FreeQuota = 0
				}
			}
			if remaining > 0 {
				ub.Balance -= remaining
				// 负余额保护
				if ub.Balance < 0 {
					ub.Balance = 0
				}
				ub.BalanceRMB = credits.CreditsToRMB(ub.Balance)
			}
			ub.TotalConsumed += actualCredits
			ub.TotalConsumedRMB = credits.CreditsToRMB(ub.TotalConsumed)
			ub.FreeQuotaRMB = credits.CreditsToRMB(ub.FreeQuota)
		}

		if err := tx.Save(&ub).Error; err != nil {
			return fmt.Errorf("update balance: %w", err)
		}

		// 更新冻结记录状态为已结算
		if err := tx.Model(&model.FreezeRecord{}).Where("freeze_id = ?", freezeID).
			Updates(map[string]interface{}{
				"status":          "SETTLED",
				"actual_cost":     actualCredits,
				"actual_cost_rmb": actualRMB,
			}).Error; err != nil {
			return fmt.Errorf("settle freeze record: %w", err)
		}

		// 记录消费流水
		if actualCredits > 0 {
			record := &model.BalanceRecord{
				UserID:        fr.UserID,
				TenantID:      fr.TenantID,
				Type:          "CONSUME",
				Amount:        -actualCredits,
				AmountRMB:     -actualRMB,
				BeforeBalance: before,
				AfterBalance:  ub.Balance,
				Remark:        fmt.Sprintf("结算 %s", fr.ModelName),
				RelatedID:     freezeID,
			}
			if err := tx.Create(record).Error; err != nil {
				return fmt.Errorf("create settle record: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		return err
	}

	s.InvalidateCache(ctx, fr.UserID)

	// v3.1: 结算后尝试解锁邀请归因 + 发放被邀者奖励
	if actualCredits > 0 {
		referral.TryUnlockAttribution(ctx, s.db, fr.UserID)
		referral.TryGrantInviteeBonus(ctx, s.db, fr.UserID)
	}

	return nil
}

// ReleaseFrozen 释放冻结：请求失败时释放预扣的冻结额度
func (s *BalanceService) ReleaseFrozen(ctx context.Context, freezeID string) error {
	if freezeID == "" {
		return nil
	}

	// 查询冻结记录
	var fr model.FreezeRecord
	if err := s.db.WithContext(ctx).Where("freeze_id = ? AND status = 'FROZEN'", freezeID).First(&fr).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil // 已释放或不存在，幂等处理
		}
		return fmt.Errorf("find freeze record: %w", err)
	}

	// 获取分布式锁
	lockKey := fmt.Sprintf("user:%d:balance_lock", fr.UserID)
	var lock *pkgredis.DistributedLock
	var err error
	if s.redis != nil {
		lock, err = pkgredis.Lock(ctx, lockKey, freezeLockTTL)
		if err != nil {
			return fmt.Errorf("acquire balance lock: %w", err)
		}
		defer lock.Unlock(ctx)
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ub model.UserBalance
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", fr.UserID).First(&ub).Error; err != nil {
			return fmt.Errorf("lock balance: %w", err)
		}

		// 释放冻结金额
		ub.FrozenAmount -= fr.FrozenAmount
		if ub.FrozenAmount < 0 {
			ub.FrozenAmount = 0
		}

		if err := tx.Save(&ub).Error; err != nil {
			return fmt.Errorf("update balance: %w", err)
		}

		// 更新冻结记录状态为已释放
		if err := tx.Model(&model.FreezeRecord{}).Where("freeze_id = ?", freezeID).
			Update("status", "RELEASED").Error; err != nil {
			return fmt.Errorf("release freeze record: %w", err)
		}

		// 记录释放流水
		record := &model.BalanceRecord{
			UserID:        fr.UserID,
			TenantID:      fr.TenantID,
			Type:          "UNFREEZE",
			Amount:        fr.FrozenAmount,
			AmountRMB:     fr.FrozenAmountRMB,
			BeforeBalance: ub.Balance,
			AfterBalance:  ub.Balance,
			Remark:        fmt.Sprintf("释放冻结 %s", fr.ModelName),
			RelatedID:     freezeID,
		}
		return tx.Create(record).Error
	})

	if err != nil {
		return err
	}

	s.InvalidateCache(ctx, fr.UserID)
	return nil
}

// CleanExpiredFreezes 清理超时冻结记录（惰性清理，可由定时任务调用）
// 将超过 freezeExpiry 时间仍为 FROZEN 状态的记录自动释放
func (s *BalanceService) CleanExpiredFreezes(ctx context.Context) (int, error) {
	expireTime := time.Now().Add(-freezeExpiry)

	var records []model.FreezeRecord
	if err := s.db.WithContext(ctx).
		Where("status = 'FROZEN' AND created_at < ?", expireTime).
		Find(&records).Error; err != nil {
		return 0, fmt.Errorf("find expired freezes: %w", err)
	}

	cleaned := 0
	for _, fr := range records {
		if err := s.ReleaseFrozen(ctx, fr.FreezeID); err == nil {
			cleaned++
		}
	}
	return cleaned, nil
}

// GetDailyConsumption 获取用户今日消费总额（返回积分）
func (s *BalanceService) GetDailyConsumption(ctx context.Context, userID uint) (int64, error) {
	// 使用 Redis 缓存今日消费
	today := time.Now().Format("2006-01-02")
	if s.redis != nil {
		key := fmt.Sprintf("daily_consumption:%d:%s", userID, today)
		val, err := s.redis.Get(ctx, key).Int64()
		if err == nil {
			return val, nil
		}
	}

	// 查询数据库：今日 CONSUME 类型的总积分（amount 为负数，取绝对值）
	var total int64
	err := s.db.WithContext(ctx).Model(&model.BalanceRecord{}).
		Where("user_id = ? AND type = 'CONSUME' AND DATE(created_at) = ?", userID, today).
		Select("COALESCE(SUM(ABS(amount)), 0)").Scan(&total).Error
	if err != nil {
		return 0, err
	}

	// 写入 Redis 缓存，5分钟过期
	if s.redis != nil {
		key := fmt.Sprintf("daily_consumption:%d:%s", userID, today)
		_ = s.redis.Set(ctx, key, total, 5*time.Minute).Err()
	}

	return total, nil
}

// GetMonthlyConsumption 获取用户本月消费总额（返回积分）
func (s *BalanceService) GetMonthlyConsumption(ctx context.Context, userID uint) (int64, error) {
	month := time.Now().Format("2006-01")
	if s.redis != nil {
		key := fmt.Sprintf("monthly_consumption:%d:%s", userID, month)
		val, err := s.redis.Get(ctx, key).Int64()
		if err == nil {
			return val, nil
		}
	}

	var total int64
	err := s.db.WithContext(ctx).Model(&model.BalanceRecord{}).
		Where("user_id = ? AND type = 'CONSUME' AND DATE_FORMAT(created_at, '%Y-%m') = ?", userID, month).
		Select("COALESCE(SUM(ABS(amount)), 0)").Scan(&total).Error
	if err != nil {
		return 0, err
	}

	if s.redis != nil {
		key := fmt.Sprintf("monthly_consumption:%d:%s", userID, month)
		_ = s.redis.Set(ctx, key, total, 5*time.Minute).Err()
	}

	return total, nil
}

// GetFrozenRecords 获取用户当前所有冻结中的记录
func (s *BalanceService) GetFrozenRecords(ctx context.Context, userID uint) ([]model.FreezeRecord, error) {
	var records []model.FreezeRecord
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND status = 'FROZEN'", userID).
		Order("created_at DESC").Find(&records).Error
	return records, err
}

// GetReconciliationReport 获取余额对账报告（冻结超时记录）
func (s *BalanceService) GetReconciliationReport(ctx context.Context) (map[string]interface{}, error) {
	// 统计各状态冻结记录数
	type statusCount struct {
		Status string
		Count  int64
		Total  int64
	}
	var counts []statusCount
	err := s.db.WithContext(ctx).Model(&model.FreezeRecord{}).
		Select("status, COUNT(*) as count, COALESCE(SUM(frozen_amount), 0) as total").
		Group("status").Scan(&counts).Error
	if err != nil {
		return nil, err
	}

	// 查找超时未结算的冻结记录
	expireTime := time.Now().Add(-freezeExpiry)
	var expiredCount int64
	var expiredTotal int64
	s.db.WithContext(ctx).Model(&model.FreezeRecord{}).
		Where("status = 'FROZEN' AND created_at < ?", expireTime).
		Count(&expiredCount)
	s.db.WithContext(ctx).Model(&model.FreezeRecord{}).
		Where("status = 'FROZEN' AND created_at < ?", expireTime).
		Select("COALESCE(SUM(frozen_amount), 0)").Scan(&expiredTotal)

	return map[string]interface{}{
		"statusSummary": counts,
		"expiredFreezes": map[string]interface{}{
			"count": expiredCount,
			"total": expiredTotal,
		},
	}, nil
}

// CheckQuota 检查用户配额是否足够（余额检查）
func (s *BalanceService) CheckQuota(ctx context.Context, userID uint) error {
	hasBalance, _, err := s.HasSufficientBalance(ctx, userID)
	if err != nil {
		return err
	}
	if !hasBalance {
		return fmt.Errorf("insufficient balance")
	}
	return nil
}

// ========== QuotaLimiter 限额限制器 ==========

// QuotaLimiter 用户额度限制器，检查日限额/月限额/单次Token上限/并发限制
type QuotaLimiter struct {
	db    *gorm.DB
	redis *goredis.Client
}

// NewQuotaLimiter 创建额度限制器实例
func NewQuotaLimiter(db *gorm.DB, redis *goredis.Client) *QuotaLimiter {
	return &QuotaLimiter{db: db, redis: redis}
}

// CheckQuota 检查用户额度限制
// 参数: userID 用户ID, estimatedCredits 预估消费积分, maxTokens 单次请求最大Token数
func (l *QuotaLimiter) CheckQuota(ctx context.Context, userID uint, estimatedCredits int64, maxTokens int) error {
	// 获取用户限额配置
	var cfg model.UserQuotaConfig
	err := l.db.WithContext(ctx).Where("user_id = ?", userID).First(&cfg).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}

	// 检查日限额
	if cfg.DailyLimit > 0 {
		balSvc := NewBalanceService(l.db, l.redis)
		dailyConsumed, err := balSvc.GetDailyConsumption(ctx, userID)
		if err != nil {
			return err
		}
		if dailyConsumed+estimatedCredits > cfg.DailyLimit {
			return fmt.Errorf("daily limit exceeded: consumed=%d, limit=%d", dailyConsumed, cfg.DailyLimit)
		}
	}

	// 检查月限额
	if cfg.MonthlyLimit > 0 {
		balSvc := NewBalanceService(l.db, l.redis)
		monthlyConsumed, err := balSvc.GetMonthlyConsumption(ctx, userID)
		if err != nil {
			return err
		}
		if monthlyConsumed+estimatedCredits > cfg.MonthlyLimit {
			return fmt.Errorf("monthly limit exceeded: consumed=%d, limit=%d", monthlyConsumed, cfg.MonthlyLimit)
		}
	}

	// 检查单次Token上限
	if cfg.MaxTokensPerReq > 0 && maxTokens > cfg.MaxTokensPerReq {
		return fmt.Errorf("max tokens per request exceeded: requested=%d, limit=%d", maxTokens, cfg.MaxTokensPerReq)
	}

	// 检查并发限制
	if cfg.MaxConcurrent > 0 && l.redis != nil {
		key := fmt.Sprintf("concurrent:%d", userID)
		count, _ := l.redis.Get(ctx, key).Int()
		if count >= cfg.MaxConcurrent {
			return fmt.Errorf("concurrent limit exceeded: current=%d, limit=%d", count, cfg.MaxConcurrent)
		}
	}

	return nil
}

// IncrConcurrency 增加并发计数
func (l *QuotaLimiter) IncrConcurrency(ctx context.Context, userID uint) {
	if l.redis == nil {
		return
	}
	key := fmt.Sprintf("concurrent:%d", userID)
	l.redis.Incr(ctx, key)
	l.redis.Expire(ctx, key, 10*time.Minute) // 10分钟过期防止泄漏
}

// DecrConcurrency 减少并发计数
func (l *QuotaLimiter) DecrConcurrency(ctx context.Context, userID uint) {
	if l.redis == nil {
		return
	}
	key := fmt.Sprintf("concurrent:%d", userID)
	val, _ := l.redis.Get(ctx, key).Int()
	if val > 0 {
		l.redis.Decr(ctx, key)
	}
}

// GetUserQuotaConfig 获取用户限额配置，若不存在则返回默认配置
func (l *QuotaLimiter) GetUserQuotaConfig(ctx context.Context, userID uint) *model.UserQuotaConfig {
	var cfg model.UserQuotaConfig
	err := l.db.WithContext(ctx).Where("user_id = ?", userID).First(&cfg).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			// 返回默认配置
			return &model.UserQuotaConfig{
				UserID:          userID,
				DailyLimit:      0, // 0 = 无限制
				MonthlyLimit:    0,
				MaxTokensPerReq: 32768,
				MaxConcurrent:   5,
				CustomRPM:       0,
			}
		}
		// 查询失败时返回默认配置
		return &model.UserQuotaConfig{
			UserID:          userID,
			DailyLimit:      0,
			MonthlyLimit:    0,
			MaxTokensPerReq: 32768,
			MaxConcurrent:   5,
			CustomRPM:       0,
		}
	}
	return &cfg
}

// UpdateUserQuotaConfig 更新或创建用户限额配置
func (l *QuotaLimiter) UpdateUserQuotaConfig(ctx context.Context, cfg *model.UserQuotaConfig) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	// 使用 upsert 语义：存在则更新，不存在则创建
	var existing model.UserQuotaConfig
	err := l.db.WithContext(ctx).Where("user_id = ?", cfg.UserID).First(&existing).Error
	if err == gorm.ErrRecordNotFound {
		// 创建新记录
		return l.db.WithContext(ctx).Create(cfg).Error
	}
	if err != nil {
		return err
	}
	// 更新现有记录
	cfg.ID = existing.ID
	cfg.CreatedAt = existing.CreatedAt
	return l.db.WithContext(ctx).Save(cfg).Error
}
