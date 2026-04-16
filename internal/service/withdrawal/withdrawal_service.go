package withdrawal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/service/balance"
)

// Service 佣金提现 + 自动结算服务
// v3.1 职责:
//  1. AutoSettleAndCredit — 定时把 N 天前的 PENDING 佣金变 SETTLED 并入账到用户 Balance
//  2. CreateWithdrawal — 用户申请提现(校验门槛与可提余额)
//  3. Approve / Reject / MarkPaid — 管理员审核
//  4. 拒绝时自动回退用户余额
type Service struct {
	db      *gorm.DB
	balance *balance.BalanceService
}

// NewService 创建提现服务
func NewService(db *gorm.DB, balanceSvc *balance.BalanceService) *Service {
	return &Service{db: db, balance: balanceSvc}
}

// ---------- 自动结算 ----------

// AutoSettleAndCredit 结算窗口外的 PENDING 佣金:标记为 SETTLED,并通过 BalanceService.Recharge 入账
// 结算天数从 ReferralConfig.SettleDays 读取(默认 7 天)
// 幂等:只处理 status=PENDING 且 credited=false 的记录
func (s *Service) AutoSettleAndCredit(ctx context.Context) (settledCount, creditedCount int, err error) {
	log := logger.L

	var cfg model.ReferralConfig
	if e := s.db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error; e != nil {
		return 0, 0, fmt.Errorf("load referral_config: %w", e)
	}
	settleDays := cfg.SettleDays
	if settleDays <= 0 {
		settleDays = 7
	}
	threshold := time.Now().AddDate(0, 0, -settleDays)

	var pendings []model.CommissionRecord
	if e := s.db.WithContext(ctx).
		Where("status = ? AND credited = ? AND created_at <= ?", "PENDING", false, threshold).
		Order("id ASC").
		Limit(1000). // 单次最多结 1000 条
		Find(&pendings).Error; e != nil {
		return 0, 0, fmt.Errorf("list pending: %w", e)
	}

	for i := range pendings {
		rec := pendings[i]
		if err := s.settleOne(ctx, &rec); err != nil {
			if log != nil {
				log.Warn("结算单条佣金失败",
					zap.Uint("recID", rec.ID),
					zap.Error(err))
			}
			continue
		}
		settledCount++
		if rec.Credited {
			creditedCount++
		}
	}

	if log != nil && settledCount > 0 {
		log.Info("佣金结算完成",
			zap.Int("settled", settledCount),
			zap.Int("credited", creditedCount),
			zap.Int("settleDays", settleDays))
	}
	return settledCount, creditedCount, nil
}

// settleOne 结算单条:事务内 UPDATE 状态 + 入账
func (s *Service) settleOne(ctx context.Context, rec *model.CommissionRecord) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 乐观锁:仅当仍是 PENDING+!credited 才处理,避免重复入账
		now := time.Now()
		res := tx.Model(&model.CommissionRecord{}).
			Where("id = ? AND status = ? AND credited = ?", rec.ID, "PENDING", false).
			Updates(map[string]interface{}{
				"status":    "SETTLED",
				"credited":  true,
				"settle_at": &now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// 已被其他进程处理
			return nil
		}
		// 直接在事务内入账 user_balances 的 balance(可提现部分)
		if err := tx.Model(&model.UserBalance{}).
			Where("user_id = ?", rec.UserID).
			Updates(map[string]interface{}{
				"balance":     gorm.Expr("balance + ?", rec.CommissionAmount),
				"balance_rmb": gorm.Expr("balance_rmb + ?", credits.CreditsToRMB(rec.CommissionAmount)),
			}).Error; err != nil {
			return fmt.Errorf("credit balance: %w", err)
		}
		// 写流水
		br := &model.BalanceRecord{
			UserID:    rec.UserID,
			TenantID:  rec.TenantID,
			Type:      "COMMISSION_SETTLE",
			Amount:    rec.CommissionAmount,
			AmountRMB: rec.CommissionAmountRMB,
			Remark:    "佣金结算入账",
			RelatedID: fmt.Sprintf("comm:%d", rec.ID),
		}
		if err := tx.Create(br).Error; err != nil {
			return fmt.Errorf("write balance record: %w", err)
		}
		rec.Status = "SETTLED"
		rec.Credited = true
		rec.SettleAt = &now
		return nil
	})
}

// ExpireAttributions 过期归因置无效(窗口外 ExpiresAt < now)
// 返回处理条数
func (s *Service) ExpireAttributions(ctx context.Context) (int64, error) {
	res := s.db.WithContext(ctx).Model(&model.ReferralAttribution{}).
		Where("is_valid = ? AND expires_at < ?", true, time.Now()).
		Updates(map[string]interface{}{
			"is_valid":       false,
			"invalid_reason": "EXPIRED",
		})
	return res.RowsAffected, res.Error
}

// ---------- 用户提现申请 ----------

// CreateWithdrawal 用户申请提现
// 规则:
//   - 金额 >= ReferralConfig.MinWithdrawAmount(积分)
//   - 用户 Balance >= 提现积分
//   - 立即冻结:Balance 减 amountCredits,新建 PENDING 记录
func (s *Service) CreateWithdrawal(ctx context.Context, userID uint, amountCredits int64, bankInfo string) (*model.WithdrawalRequest, error) {
	if amountCredits <= 0 {
		return nil, errors.New("amount must be positive")
	}

	var cfg model.ReferralConfig
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error; err != nil {
		return nil, fmt.Errorf("load referral_config: %w", err)
	}
	if cfg.MinWithdrawAmount > 0 && amountCredits < cfg.MinWithdrawAmount {
		return nil, fmt.Errorf("amount below minimum withdraw threshold (%d credits)", cfg.MinWithdrawAmount)
	}

	// 校验余额充足
	var ub model.UserBalance
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&ub).Error; err != nil {
		return nil, fmt.Errorf("load user balance: %w", err)
	}
	if ub.Balance < amountCredits {
		return nil, errors.New("insufficient balance")
	}

	var req model.WithdrawalRequest
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 冻结余额(行锁)
		res := tx.Model(&model.UserBalance{}).
			Where("user_id = ? AND balance >= ?", userID, amountCredits).
			Updates(map[string]interface{}{
				"balance":     gorm.Expr("balance - ?", amountCredits),
				"balance_rmb": gorm.Expr("balance_rmb - ?", credits.CreditsToRMB(amountCredits)),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errors.New("freeze balance failed: insufficient balance")
		}

		// 创建提现请求(金额以 RMB 存储,保持与原 WithdrawalRequest 结构一致)
		req = model.WithdrawalRequest{
			UserID:   userID,
			Amount:   credits.CreditsToRMB(amountCredits),
			Status:   "PENDING",
			BankInfo: bankInfo,
		}
		if err := tx.Create(&req).Error; err != nil {
			return err
		}

		// 流水:余额冻结
		br := &model.BalanceRecord{
			UserID:    userID,
			TenantID:  ub.TenantID,
			Type:      "WITHDRAW_FREEZE",
			Amount:    -amountCredits,
			AmountRMB: -credits.CreditsToRMB(amountCredits),
			Remark:    "提现申请冻结",
			RelatedID: fmt.Sprintf("wd:%d", req.ID),
		}
		return tx.Create(br).Error
	})
	if err != nil {
		return nil, err
	}
	return &req, nil
}

// ListUserWithdrawals 用户分页查询自己的提现记录
func (s *Service) ListUserWithdrawals(ctx context.Context, userID uint, page, pageSize int) ([]model.WithdrawalRequest, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	s.db.WithContext(ctx).Model(&model.WithdrawalRequest{}).Where("user_id = ?", userID).Count(&total)
	var list []model.WithdrawalRequest
	err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&list).Error
	return list, total, err
}

// ---------- 管理员审核 ----------

// ListWithdrawals 管理员分页查询所有提现请求(可按 status 过滤)
func (s *Service) ListWithdrawals(ctx context.Context, status string, page, pageSize int) ([]model.WithdrawalRequest, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	q := s.db.WithContext(ctx).Model(&model.WithdrawalRequest{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	q.Count(&total)
	var list []model.WithdrawalRequest
	err := q.Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&list).Error
	return list, total, err
}

// Approve 审核通过;仅更改状态,真正打款需另外 MarkPaid
func (s *Service) Approve(ctx context.Context, id, adminID uint, remark string) error {
	var req model.WithdrawalRequest
	if err := s.db.WithContext(ctx).First(&req, id).Error; err != nil {
		return fmt.Errorf("withdrawal not found: %w", err)
	}
	if req.Status != "PENDING" {
		return fmt.Errorf("cannot approve withdrawal in status %s", req.Status)
	}
	now := time.Now()
	return s.db.WithContext(ctx).Model(&model.WithdrawalRequest{}).Where("id = ?", req.ID).Updates(map[string]interface{}{
		"status":       "APPROVED",
		"admin_id":     adminID,
		"admin_remark": remark,
		"processed_at": &now,
	}).Error
}

// Reject 审核拒绝,同时回退用户被冻结的余额
func (s *Service) Reject(ctx context.Context, id, adminID uint, reason string) error {
	var req model.WithdrawalRequest
	if err := s.db.WithContext(ctx).First(&req, id).Error; err != nil {
		return fmt.Errorf("withdrawal not found: %w", err)
	}
	if req.Status != "PENDING" {
		return fmt.Errorf("cannot reject withdrawal in status %s", req.Status)
	}

	// 金额回退:RMB → credits
	refundCredits := credits.RMBToCredits(req.Amount)
	var ub model.UserBalance
	if err := s.db.WithContext(ctx).Where("user_id = ?", req.UserID).First(&ub).Error; err != nil {
		return fmt.Errorf("load user balance: %w", err)
	}

	now := time.Now()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.WithdrawalRequest{}).Where("id = ?", req.ID).Updates(map[string]interface{}{
			"status":       "REJECTED",
			"admin_id":     adminID,
			"admin_remark": reason,
			"processed_at": &now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.UserBalance{}).
			Where("user_id = ?", req.UserID).
			Updates(map[string]interface{}{
				"balance":     gorm.Expr("balance + ?", refundCredits),
				"balance_rmb": gorm.Expr("balance_rmb + ?", req.Amount),
			}).Error; err != nil {
			return err
		}
		br := &model.BalanceRecord{
			UserID:    req.UserID,
			TenantID:  ub.TenantID,
			Type:      "WITHDRAW_REFUND",
			Amount:    refundCredits,
			AmountRMB: req.Amount,
			Remark:    "提现拒绝退款:" + reason,
			RelatedID: fmt.Sprintf("wd:%d", req.ID),
		}
		return tx.Create(br).Error
	})
}

// MarkPaid 标记已打款(APPROVED → COMPLETED),记录 bankTxnID 到 admin_remark
func (s *Service) MarkPaid(ctx context.Context, id, adminID uint, bankTxnID string) error {
	var req model.WithdrawalRequest
	if err := s.db.WithContext(ctx).First(&req, id).Error; err != nil {
		return fmt.Errorf("withdrawal not found: %w", err)
	}
	if req.Status != "APPROVED" {
		return fmt.Errorf("cannot mark paid for withdrawal in status %s", req.Status)
	}
	now := time.Now()
	remark := req.AdminRemark
	if bankTxnID != "" {
		remark = fmt.Sprintf("%s | txn:%s", remark, bankTxnID)
	}
	return s.db.WithContext(ctx).Model(&model.WithdrawalRequest{}).Where("id = ?", req.ID).Updates(map[string]interface{}{
		"status":       "COMPLETED",
		"admin_id":     adminID,
		"admin_remark": remark,
		"processed_at": &now,
	}).Error
}
