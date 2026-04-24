package referral

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/safego"
)

// CommissionCalculator v4.x 返佣计算器
// 业务规则:
//  1. 三级优先级决策:CommissionRule (用户×模型) > UserCommissionOverride (用户级) > ReferralConfig.CommissionRate (全局)
//  2. 以 ReferralAttribution 为归因依据(而非 User.ReferredBy):需在归因窗口期内且已解锁
//  3. 终身上限:同一邀请关系累计已写入佣金(积分)超过 LifetimeCapCredits 则不再发放
//  4. 代理机制已移除:不再查询 UserAgentProfile / AgentLevel / ModelCommissionConfig
type CommissionCalculator struct {
	db       *gorm.DB
	resolver *RuleResolver // 可选;nil 时降级为直接查 DB(老路径,不走 CommissionRule)
}

// NewCommissionCalculator 创建佣金计算器实例(签名不变,向后兼容现有调用)
// 若全局 Default resolver 已初始化,会自动注入;手动注入用 WithResolver。
func NewCommissionCalculator(db *gorm.DB) *CommissionCalculator {
	return &CommissionCalculator{db: db, resolver: Default}
}

// WithResolver 注入 RuleResolver 启用特殊返佣规则
// bootstrap 中初始化 Resolver 后调用
func (c *CommissionCalculator) WithResolver(r *RuleResolver) *CommissionCalculator {
	c.resolver = r
	return c
}

// CalculateCommissions 计算消费事件的佣金
// 参数: sourceUserID 消费用户ID, sourceTenantID 消费租户ID, orderCredits 消费积分
//
//	modelName/supplierID 保留占位(向后兼容),实际决策通过 modelID 参数(CalculateCommissionsWithModel)
//
// 本方法设计为异步调用(不阻塞主请求)
func (c *CommissionCalculator) CalculateCommissions(ctx context.Context, sourceUserID, sourceTenantID uint, orderCredits int64, modelName string, supplierID uint) {
	c.calculateInternal(ctx, sourceUserID, sourceTenantID, orderCredits, 0)
}

// CalculateCommissionsWithModel 计算消费事件的佣金(支持模型级特殊规则)
// modelID=0 时退化为无模型的老路径
func (c *CommissionCalculator) CalculateCommissionsWithModel(ctx context.Context, sourceUserID, sourceTenantID uint, orderCredits int64, modelID uint) {
	c.calculateInternal(ctx, sourceUserID, sourceTenantID, orderCredits, modelID)
}

func (c *CommissionCalculator) calculateInternal(ctx context.Context, sourceUserID, sourceTenantID uint, orderCredits int64, modelID uint) {
	if orderCredits <= 0 {
		return
	}
	log := logger.L
	if log == nil {
		return
	}

	// 1) 查询归因(以 ReferralAttribution 为准,不再依赖 User.ReferredBy)
	var attr model.ReferralAttribution
	err := c.db.WithContext(ctx).
		Where("user_id = ? AND is_valid = ?", sourceUserID, true).
		First(&attr).Error
	if err != nil {
		// 无归因或已失效,跳过
		return
	}

	// 2) 归因窗口校验
	now := time.Now()
	if !attr.ExpiresAt.IsZero() && now.After(attr.ExpiresAt) {
		// 过期自动置无效
		c.db.WithContext(ctx).Model(&model.ReferralAttribution{}).Where("id = ?", attr.ID).Updates(map[string]interface{}{
			"is_valid":       false,
			"invalid_reason": "EXPIRED",
		})
		return
	}

	// 3) 解锁校验(未解锁的归因不产生佣金)
	if attr.UnlockedAt == nil {
		return
	}

	// 4) 加载活跃的 ReferralConfig 作为兜底
	var cfg model.ReferralConfig
	if err := c.db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error; err != nil {
		log.Debug("未找到活跃的邀请配置,跳过佣金计算")
		return
	}

	// 5) 确定生效比例:三级决策 CommissionRule > UserCommissionOverride > cfg.CommissionRate
	var rate float64
	var overrideIDPtr, ruleIDPtr *uint
	var lifetimeCap int64
	var commType string

	if c.resolver != nil {
		// 走 Resolver（带 Redis 缓存）
		resolved, _ := c.resolver.Resolve(ctx, attr.InviterID, modelID)
		if resolved != nil {
			rate = resolved.Rate
			lifetimeCap = resolved.LifetimeCap
			switch resolved.Source {
			case SourceRule:
				id := resolved.RuleID
				ruleIDPtr = &id
				commType = "REFERRAL_RULE"
			case SourceOverride:
				id := resolved.OverrideID
				overrideIDPtr = &id
				commType = "REFERRAL_OVERRIDE"
			default:
				commType = "REFERRAL"
			}
		}
	} else {
		// 降级路径：老逻辑(仅 override + cfg)
		rate = cfg.CommissionRate
		if rate <= 0 && cfg.PersonalCashbackRate > 0 {
			rate = cfg.PersonalCashbackRate
		}
		lifetimeCap = cfg.LifetimeCapCredits
		var override model.UserCommissionOverride
		err := c.db.WithContext(ctx).
			Where("user_id = ? AND is_active = ? AND effective_from <= ?", attr.InviterID, true, now).
			Where("effective_to IS NULL OR effective_to > ?", now).
			First(&override).Error
		if err == nil {
			rate = override.CommissionRate
			id := override.ID
			overrideIDPtr = &id
			if override.LifetimeCapCredits != nil {
				lifetimeCap = *override.LifetimeCapCredits
			}
			commType = "REFERRAL_OVERRIDE"
		} else {
			commType = "REFERRAL"
		}
	}
	if rate <= 0 {
		return
	}

	// 6) 终身上限校验:统计该归因下已产生的佣金(所有非 REFUNDED 状态)
	if lifetimeCap > 0 {
		var earned int64
		c.db.WithContext(ctx).Model(&model.CommissionRecord{}).
			Where("attribution_id = ? AND status <> ?", attr.ID, "REFUNDED").
			Select("COALESCE(SUM(commission_amount), 0)").
			Scan(&earned)
		if earned >= lifetimeCap {
			return
		}
	}

	// 7) 查询邀请人租户 ID
	var inviter model.User
	if err := c.db.WithContext(ctx).First(&inviter, attr.InviterID).Error; err != nil {
		log.Error("佣金计算: 邀请人不存在", zap.Uint("inviterID", attr.InviterID), zap.Error(err))
		return
	}

	// 8) 计算佣金金额,考虑终身上限裁剪(使用 override 或全局的有效上限)
	commCredits := int64(float64(orderCredits) * rate)
	if lifetimeCap > 0 {
		var earned int64
		c.db.WithContext(ctx).Model(&model.CommissionRecord{}).
			Where("attribution_id = ? AND status <> ?", attr.ID, "REFUNDED").
			Select("COALESCE(SUM(commission_amount), 0)").
			Scan(&earned)
		if earned+commCredits > lifetimeCap {
			commCredits = lifetimeCap - earned
		}
	}
	if commCredits <= 0 {
		return
	}

	orderRMB := credits.CreditsToRMB(orderCredits)
	commRMB := credits.CreditsToRMB(commCredits)

	if commType == "" {
		commType = "REFERRAL"
	}
	attrIDCopy := attr.ID

	rec := model.CommissionRecord{
		TenantID:            inviter.TenantID,
		UserID:              inviter.ID,
		SourceUserID:        sourceUserID,
		SourceTenantID:      sourceTenantID,
		OrderAmount:         orderCredits,
		OrderAmountRMB:      orderRMB,
		CommissionRate:      rate,
		CommissionAmount:    commCredits,
		CommissionAmountRMB: commRMB,
		Type:                commType,
		Status:              "PENDING",
		AttributionID:       &attrIDCopy,
		OverrideID:          overrideIDPtr,
		RuleID:              ruleIDPtr,
		EffectiveRate:       rate,
		Credited:            false,
	}
	if err := c.db.WithContext(ctx).Create(&rec).Error; err != nil {
		log.Error("佣金计算: 写入佣金记录失败", zap.Error(err))
		return
	}
	log.Info("佣金计算: 写入佣金记录",
		zap.Uint("inviter", inviter.ID),
		zap.Uint("sourceUser", sourceUserID),
		zap.Uint("modelID", modelID),
		zap.Int64("orderCredits", orderCredits),
		zap.Float64("effectiveRate", rate),
		zap.Int64("commCredits", commCredits),
		zap.String("type", commType),
		zap.Bool("hasRule", ruleIDPtr != nil),
		zap.Bool("hasOverride", overrideIDPtr != nil),
	)
}

// CalculateCommissionsAsync 异步佣金计算包装(向后兼容,签名不变)
// 参数 orderCredits 为消费积分数量(int64); modelID 可选,不传时走 override/config 降级路径
// 使用 safego 统一 panic 恢复（含 task name + stack trace）
func (c *CommissionCalculator) CalculateCommissionsAsync(sourceUserID, sourceTenantID uint, orderCredits int64, modelID ...uint) {
	var mid uint
	if len(modelID) > 0 {
		mid = modelID[0]
	}
	safego.Go("commission-calculate-async", func() {
		// 3 分钟超时保护 — 佣金计算涉及多表 JOIN，慢查询不应永久占用 DB 连接
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		c.calculateInternal(ctx, sourceUserID, sourceTenantID, orderCredits, mid)
	})
}

// CalculateCommissionsAsyncByModelName 异步佣金计算，内部根据 modelName 解析 model_id
// 供 completions_handler 等场景使用：请求处理时仅知 modelName，无需在 hot path 做 DB lookup
func (c *CommissionCalculator) CalculateCommissionsAsyncByModelName(sourceUserID, sourceTenantID uint, orderCredits int64, modelName string) {
	safego.Go("commission-calculate-async", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		var modelID uint
		if modelName != "" {
			var m model.AIModel
			if err := c.db.WithContext(ctx).Select("id").
				Where("model_name = ?", modelName).
				Order("id ASC").First(&m).Error; err == nil {
				modelID = m.ID
			}
		}
		c.calculateInternal(ctx, sourceUserID, sourceTenantID, orderCredits, modelID)
	})
}

// MarkCommissionRefunded 标记佣金为已退款(退款时冲销对应佣金)
// 参数: relatedID 关联订单ID或请求ID
func (c *CommissionCalculator) MarkCommissionRefunded(ctx context.Context, relatedID string) error {
	if relatedID == "" {
		return nil
	}
	result := c.db.WithContext(ctx).
		Model(&model.CommissionRecord{}).
		Where("related_id = ? AND status = 'PENDING'", relatedID).
		Update("status", "REFUNDED")
	if result.Error != nil {
		return fmt.Errorf("标记佣金退款失败: %w", result.Error)
	}
	if result.RowsAffected > 0 && logger.L != nil {
		logger.L.Info("佣金已标记为退款",
			zap.String("relatedID", relatedID),
			zap.Int64("affected", result.RowsAffected))
	}
	return nil
}

// ProcessReferralOnRegister 注册时处理邀请绑定(v3.1:建立 ReferralAttribution 记录)
// 应在用户创建后使用注册时的邀请码调用
// 同时保留 User.ReferredBy 写入以兼容现有 UI(邀请统计页);权威归因改为 ReferralAttribution
func ProcessReferralOnRegister(db *gorm.DB, ctx context.Context, user *model.User, referralCode string) error {
	if referralCode == "" || user == nil {
		return nil
	}

	var inviterID uint
	var found bool

	// 1. 首先尝试通过 ReferralLink 查找
	var link model.ReferralLink
	if err := db.WithContext(ctx).Where("code = ?", referralCode).First(&link).Error; err == nil {
		if link.UserID == user.ID {
			return nil // 不允许自邀请
		}
		inviterID = link.UserID
		found = true

		// 增加 link 注册计数（先执行，不受后续步骤失败影响）
		db.WithContext(ctx).Model(&model.ReferralLink{}).
			Where("id = ?", link.ID).
			UpdateColumn("register_count", gorm.Expr("register_count + 1"))
	} else {
		// 2. 尝试通过 User.ReferralCode 查找
		var referrer model.User
		if err := db.WithContext(ctx).Where("referral_code = ? AND id != ?", referralCode, user.ID).First(&referrer).Error; err == nil {
			inviterID = referrer.ID
			found = true
		} else {
			// 3. 尝试通过 Tenant.Domain 查找（白标邀请）
			var tenant model.Tenant
			if err := db.WithContext(ctx).Where("domain = ? AND is_active = ?", referralCode, true).First(&tenant).Error; err == nil {
				// 白标邀请没有具体的邀请人，使用租户ID作为标记
				found = true
			}
		}
	}

	if !found {
		return fmt.Errorf("invalid referral code")
	}

	// 兼容写入 User.ReferredBy(保留邀请统计页的旧数据源)
	// 注意：user.ID 为 0 时 GORM 会报 WHERE conditions required，忽略此错误不影响主流程
	if user.ID > 0 && inviterID > 0 {
		db.WithContext(ctx).Model(&model.User{}).Where("id = ?", user.ID).Update("referred_by", inviterID)
	}

	// v3.1 核心:建立归因快照(读取 ReferralConfig 计算 ExpiresAt)
	var cfg model.ReferralConfig
	attributionDays := 90 // 兜底默认值
	if err := db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error; err == nil {
		if cfg.AttributionDays > 0 {
			attributionDays = cfg.AttributionDays
		}
	}
	now := time.Now()

	// 若邀请人存在特殊 override 且配置了 AttributionDays,则使用 override 的归因周期
	// 注意:这是归因建立时的快照语义,override 变更不会追溯修改已存在的 ExpiresAt
	if inviterID > 0 {
		var ov model.UserCommissionOverride
		ovErr := db.WithContext(ctx).
			Where("user_id = ? AND is_active = ? AND effective_from <= ?", inviterID, true, now).
			Where("effective_to IS NULL OR effective_to > ?", now).
			First(&ov).Error
		if ovErr == nil && ov.AttributionDays != nil && *ov.AttributionDays > 0 {
			attributionDays = *ov.AttributionDays
		}
	}

	// 若已存在则不重复创建
	var existing model.ReferralAttribution
	err := db.WithContext(ctx).Where("user_id = ?", user.ID).First(&existing).Error
	if err == nil {
		// 已存在,更新邀请码字段避免被改掉
		return nil
	}

	attr := model.ReferralAttribution{
		UserID:       user.ID,
		InviterID:    inviterID,
		ReferralCode: referralCode,
		AttributedAt: now,
		ExpiresAt:    now.AddDate(0, 0, attributionDays),
		UnlockedAt:   nil, // 初始未解锁,需消费达到 MinPaidCreditsUnlock 才解锁
		IsValid:      true,
	}
	if err := db.WithContext(ctx).Create(&attr).Error; err != nil {
		return fmt.Errorf("create referral attribution: %w", err)
	}
	return nil
}

// CreateReferralAttribution 兼容旧测试/调用方的命名，内部复用注册归因处理。
func CreateReferralAttribution(ctx context.Context, db *gorm.DB, user *model.User, referralCode string) error {
	return ProcessReferralOnRegister(db, ctx, user, referralCode)
}
