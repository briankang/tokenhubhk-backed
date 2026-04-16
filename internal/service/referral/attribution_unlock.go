package referral

import (
	"context"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// TryUnlockAttribution 尝试解锁用户的邀请归因
// 业务规则(v3.1):
//   - 仅当用户存在 is_valid=true 且 unlocked_at IS NULL 的归因快照时才处理
//   - 累计消费(UserBalance.TotalConsumed)达到 ReferralConfig.MinPaidCreditsUnlock
//     则将 UnlockedAt 设为当前时间,之后的消费事件可产生佣金
//   - 本方法设计为幂等,重复调用不会产生副作用
//   - 失败不抛出错误(仅记录日志),避免阻塞主消费流程
func TryUnlockAttribution(ctx context.Context, db *gorm.DB, userID uint) {
	if db == nil || userID == 0 {
		return
	}
	log := logger.L

	// 1) 查询该用户是否有未解锁的归因
	var attr model.ReferralAttribution
	err := db.WithContext(ctx).
		Where("user_id = ? AND is_valid = ? AND unlocked_at IS NULL", userID, true).
		First(&attr).Error
	if err != nil {
		// 无需解锁或查询失败,静默跳过
		return
	}

	// 2) 加载配置读取解锁门槛
	var cfg model.ReferralConfig
	if err := db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error; err != nil {
		return
	}

	// 2.1) 若邀请人存在活跃 override 且设置了 MinPaidCreditsUnlock,则使用 override 值
	// 注意:这里检查的是邀请人的 override(attr.InviterID),覆盖该邀请关系下的被邀者解锁门槛
	threshold := cfg.MinPaidCreditsUnlock
	now := time.Now()
	var ov model.UserCommissionOverride
	ovErr := db.WithContext(ctx).
		Where("user_id = ? AND is_active = ? AND effective_from <= ?", attr.InviterID, true, now).
		Where("effective_to IS NULL OR effective_to > ?", now).
		First(&ov).Error
	if ovErr == nil && ov.MinPaidCreditsUnlock != nil {
		threshold = *ov.MinPaidCreditsUnlock
	}

	// 3) 查询被邀者累计消费
	var ub model.UserBalance
	if err := db.WithContext(ctx).Where("user_id = ?", userID).First(&ub).Error; err != nil {
		return
	}
	if threshold > 0 && ub.TotalConsumed < threshold {
		return
	}

	// 4) 原子更新 unlocked_at,仅当仍为 NULL 时生效(防并发)
	res := db.WithContext(ctx).Model(&model.ReferralAttribution{}).
		Where("id = ? AND unlocked_at IS NULL", attr.ID).
		Update("unlocked_at", now)
	if res.Error != nil {
		if log != nil {
			log.Error("解锁邀请归因失败", zap.Uint("attrID", attr.ID), zap.Error(res.Error))
		}
		return
	}
	if res.RowsAffected > 0 && log != nil {
		log.Info("邀请归因已解锁",
			zap.Uint("attrID", attr.ID),
			zap.Uint("userID", userID),
			zap.Uint("inviterID", attr.InviterID),
			zap.Int64("totalConsumed", ub.TotalConsumed),
			zap.Int64("threshold", threshold),
		)
	}
}
