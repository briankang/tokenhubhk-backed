package referral

import (
	"context"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/logger"
)

// TryGrantInviteeBonus 尝试向被邀者发放一次性奖励
// 业务规则(v3.1):
//   - 仅当归因 InviteeBonusGranted=false
//   - 累计消费达 QuotaConfig.InviteeUnlockCredits 时发放 QuotaConfig.InviteeBonus 到被邀者余额
//   - 幂等:失败/重复调用无副作用
//   - 直接操作 user_balances 表(避免 import balance service 产生循环依赖)
func TryGrantInviteeBonus(ctx context.Context, db *gorm.DB, userID uint) {
	if db == nil || userID == 0 {
		return
	}
	log := logger.L

	// 1) 查询归因:未发放过 InviteeBonus
	var attr model.ReferralAttribution
	err := db.WithContext(ctx).
		Where("user_id = ? AND is_valid = ? AND invitee_bonus_granted = ?", userID, true, false).
		First(&attr).Error
	if err != nil {
		return
	}

	// 2) 读取配置
	var qc model.QuotaConfig
	if err := db.WithContext(ctx).Where("is_active = ?", true).First(&qc).Error; err != nil {
		return
	}
	if qc.InviteeBonus <= 0 {
		return // 未启用被邀者奖励
	}

	// 3) 查累计消费
	var ub model.UserBalance
	if err := db.WithContext(ctx).Where("user_id = ?", userID).First(&ub).Error; err != nil {
		return
	}
	if qc.InviteeUnlockCredits > 0 && ub.TotalConsumed < qc.InviteeUnlockCredits {
		return
	}

	// 4) 原子加钱 + 标记已发放(事务保证幂等)
	now := time.Now()
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 乐观并发:只更新未标记过的归因
		res := tx.Model(&model.ReferralAttribution{}).
			Where("id = ? AND invitee_bonus_granted = ?", attr.ID, false).
			Updates(map[string]interface{}{
				"invitee_bonus_granted": true,
				"invitee_bonus_at":      &now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// 已被其他协程发放,幂等跳过
			return nil
		}

		// 发放到 free_quota(视为赠送额度,非付费余额)
		if err := tx.Model(&model.UserBalance{}).
			Where("user_id = ?", userID).
			Updates(map[string]interface{}{
				"free_quota":     gorm.Expr("free_quota + ?", qc.InviteeBonus),
				"free_quota_rmb": gorm.Expr("free_quota_rmb + ?", credits.CreditsToRMB(qc.InviteeBonus)),
			}).Error; err != nil {
			return err
		}

		// 写入流水
		record := &model.BalanceRecord{
			UserID:        userID,
			TenantID:      ub.TenantID,
			Type:          "INVITEE_BONUS",
			Amount:        qc.InviteeBonus,
			AmountRMB:     credits.CreditsToRMB(qc.InviteeBonus),
			BeforeBalance: ub.Balance,
			AfterBalance:  ub.Balance,
			Remark:        "邀请奖励:注册邀请达标赠送",
			RelatedID:     attr.ReferralCode,
		}
		return tx.Create(record).Error
	})
	if err != nil {
		if log != nil {
			log.Error("发放被邀者奖励失败", zap.Uint("userID", userID), zap.Error(err))
		}
		return
	}
	if log != nil {
		log.Info("被邀者奖励已发放",
			zap.Uint("userID", userID),
			zap.Uint("attrID", attr.ID),
			zap.Int64("bonus", qc.InviteeBonus),
		)
	}
}

// TryGrantInviterBonus 尝试向邀请人发放一次性奖励
// 业务规则(v3.1):
//   - 被邀者首次付费达 QuotaConfig.InviterUnlockPaidRMB 后触发
//   - 仅当归因 InviterBonusGranted=false 时发放
//   - InviterMonthlyCap 本月已领满则不发(但仍可正常计佣金)
//   - 以积分形式发到邀请人的 UserBalance.Balance
//
// 调用时机:支付回调成功后,调用方传入本次支付被邀者 userID
func TryGrantInviterBonus(ctx context.Context, db *gorm.DB, inviteeUserID uint) {
	if db == nil || inviteeUserID == 0 {
		return
	}
	log := logger.L

	// 1) 查询归因:被邀者有效且未发放过邀请人奖励
	var attr model.ReferralAttribution
	err := db.WithContext(ctx).
		Where("user_id = ? AND is_valid = ? AND inviter_bonus_granted = ?", inviteeUserID, true, false).
		First(&attr).Error
	if err != nil {
		return
	}

	// 2) 读配置
	var qc model.QuotaConfig
	if err := db.WithContext(ctx).Where("is_active = ?", true).First(&qc).Error; err != nil {
		return
	}
	if qc.InviterBonus <= 0 {
		return
	}

	// 3) 查被邀者累计付费金额(通过 payments 表,仅 completed)
	var paidCredits int64
	db.WithContext(ctx).
		Table("payments").
		Where("user_id = ? AND status = ?", inviteeUserID, "completed").
		Select("COALESCE(SUM(credit_amount), 0)").
		Scan(&paidCredits)
	if qc.InviterUnlockPaidRMB > 0 && paidCredits < qc.InviterUnlockPaidRMB {
		return
	}

	// 4) 月度上限检查
	if qc.InviterMonthlyCap > 0 {
		now := time.Now()
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		var monthGranted int64
		db.WithContext(ctx).Model(&model.ReferralAttribution{}).
			Where("inviter_id = ? AND inviter_bonus_granted = ? AND inviter_bonus_at >= ?",
				attr.InviterID, true, monthStart).
			Count(&monthGranted)
		if monthGranted >= int64(qc.InviterMonthlyCap) {
			if log != nil {
				log.Info("邀请人月度奖励上限已达,跳过发放",
					zap.Uint("inviterID", attr.InviterID),
					zap.Int64("monthGranted", monthGranted),
					zap.Int("cap", qc.InviterMonthlyCap),
				)
			}
			return
		}
	}

	// 5) 查邀请人 tenant / balance
	var inviter model.User
	if err := db.WithContext(ctx).First(&inviter, attr.InviterID).Error; err != nil {
		return
	}
	var inviterBalance model.UserBalance
	if err := db.WithContext(ctx).Where("user_id = ?", attr.InviterID).First(&inviterBalance).Error; err != nil {
		return
	}

	now := time.Now()
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.ReferralAttribution{}).
			Where("id = ? AND inviter_bonus_granted = ?", attr.ID, false).
			Updates(map[string]interface{}{
				"inviter_bonus_granted": true,
				"inviter_bonus_at":      &now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}

		// 发放到邀请人余额(balance,可提现)
		if err := tx.Model(&model.UserBalance{}).
			Where("user_id = ?", attr.InviterID).
			Updates(map[string]interface{}{
				"balance":     gorm.Expr("balance + ?", qc.InviterBonus),
				"balance_rmb": gorm.Expr("balance_rmb + ?", credits.CreditsToRMB(qc.InviterBonus)),
			}).Error; err != nil {
			return err
		}

		record := &model.BalanceRecord{
			UserID:        attr.InviterID,
			TenantID:      inviter.TenantID,
			Type:          "INVITER_BONUS",
			Amount:        qc.InviterBonus,
			AmountRMB:     credits.CreditsToRMB(qc.InviterBonus),
			BeforeBalance: inviterBalance.Balance,
			AfterBalance:  inviterBalance.Balance + qc.InviterBonus,
			Remark:        "邀请奖励:被邀者首次付费达标",
			RelatedID:     attr.ReferralCode,
		}
		return tx.Create(record).Error
	})
	if err != nil {
		if log != nil {
			log.Error("发放邀请人奖励失败", zap.Uint("inviterID", attr.InviterID), zap.Error(err))
		}
		return
	}
	if log != nil {
		log.Info("邀请人奖励已发放",
			zap.Uint("inviterID", attr.InviterID),
			zap.Uint("inviteeID", inviteeUserID),
			zap.Int64("bonus", qc.InviterBonus),
		)
	}
}
