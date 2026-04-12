package referral

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/logger"
)

// CommissionCalculator 佣金计算器
// 业务规则：代理商只享受直推会员消费产生的佣金，无多级佣金
// 从推荐人的 UserAgentProfile → AgentLevel 动态获取佣金比例
// 普通用户（无代理档案）使用 ReferralConfig 的 PersonalCashbackRate 向后兼容
// 核心规则：佣金按实际已消费积分 ÷ 10000 = RMB 计算，未消费不参与
// 注意：L2/L3 佣金逻辑已停用，保留代码仅用于向后兼容
// 新增：支持模型级别佣金配置，支持按"供应商+模型"维度配置不同代理等级的佣金比例
// 优先级：模型+供应商级别配置 > 仅模型名配置 > 代理等级默认比例
type CommissionCalculator struct {
	db *gorm.DB
}

// NewCommissionCalculator 创建佣金计算器实例
func NewCommissionCalculator(db *gorm.DB) *CommissionCalculator {
	return &CommissionCalculator{db: db}
}

// CalculateCommissions 计算消费事件的佣金
// 仅计算直推佣金（第1层），L2/L3 佣金已停用
// 参数: sourceUserID 消费用户ID, sourceTenantID 消费租户ID, orderCredits 消费积分数量
//       modelName 模型名称（可选，用于模型级别佣金配置）
//       supplierID 供应商ID（可选，用于精确匹配供应商+模型配置）
// 本方法设计为异步调用（在 goroutine 中执行，不阻塞主请求）
func (c *CommissionCalculator) CalculateCommissions(ctx context.Context, sourceUserID, sourceTenantID uint, orderCredits int64, modelName string, supplierID uint) {
	if orderCredits <= 0 {
		return
	}

	log := logger.L
	if log == nil {
		return
	}

	// 计算消费的人民币等值
	orderRMB := credits.CreditsToRMB(orderCredits)

	// 获取活跃的邀请配置（用作普通用户邀请的兜底比例）
	var cfg model.ReferralConfig
	if err := c.db.WithContext(ctx).Where("is_active = ?", true).First(&cfg).Error; err != nil {
		log.Debug("未找到活跃的邀请配置，跳过佣金计算")
		return
	}

	// 查找消费用户
	var sourceUser model.User
	if err := c.db.WithContext(ctx).First(&sourceUser, sourceUserID).Error; err != nil {
		log.Error("佣金计算: 消费用户不存在", zap.Uint("userID", sourceUserID), zap.Error(err))
		return
	}

	// 用户没有推荐人，无需计算佣金
	if sourceUser.ReferredBy == nil {
		return
	}

	// 向上遍历推荐链，最多 3 层
	var records []model.CommissionRecord
	// 记录每层佣金接收人的 UserAgentProfile ID，用于后续累加 TotalEarnings
	type earningsUpdate struct {
		profileID uint
		credits   int64
		rmb       float64
	}
	var earningsUpdates []earningsUpdate

	// === 第 1 层：直推推荐人 ===
	referrerID := *sourceUser.ReferredBy
	var referrer model.User
	if err := c.db.WithContext(ctx).First(&referrer, referrerID).Error; err != nil {
		log.Error("佣金计算: 推荐人不存在", zap.Uint("referrerID", referrerID), zap.Error(err))
		return
	}

	// 计算佣金：优先使用模型+供应商级别配置，其次使用模型级别配置，最后使用代理等级默认比例
	commType, rate, agentProfileID := c.resolveCommissionWithModelAndSupplier(ctx, &referrer, 1, &cfg, modelName, supplierID)
	if rate > 0 {
		commCredits := int64(float64(orderCredits) * rate)
		commRMB := credits.CreditsToRMB(commCredits)
		records = append(records, model.CommissionRecord{
			TenantID:            referrer.TenantID,
			UserID:              referrer.ID,
			SourceUserID:        sourceUserID,
			SourceTenantID:      sourceTenantID,
			OrderAmount:         orderCredits,
			OrderAmountRMB:      orderRMB,
			CommissionRate:      rate,
			CommissionAmount:    commCredits,
			CommissionAmountRMB: commRMB,
			Type:                commType,
			Status:              "PENDING",
		})
		// 如果是代理，记录需要累加的 TotalEarnings
		if agentProfileID > 0 {
			earningsUpdates = append(earningsUpdates, earningsUpdate{profileID: agentProfileID, credits: commCredits, rmb: commRMB})
		}
	}

	// === 第 2 层和第 3 层佣金已停用 ===
	// 业务规则：代理商只享受直推会员消费产生的佣金
	// 以下代码保留但注释掉，如需恢复可取消注释
	/*
	if referrer.ReferredBy != nil {
		var l2Referrer model.User
		if err := c.db.WithContext(ctx).First(&l2Referrer, *referrer.ReferredBy).Error; err == nil {
			commType2, rate2, agentProfileID2 := c.resolveCommission(ctx, &l2Referrer, 2, &cfg)
			if rate2 > 0 {
				commCredits2 := int64(float64(orderCredits) * rate2)
				commRMB2 := credits.CreditsToRMB(commCredits2)
				records = append(records, model.CommissionRecord{...})
				if agentProfileID2 > 0 {
					earningsUpdates = append(earningsUpdates, earningsUpdate{profileID: agentProfileID2, credits: commCredits2, rmb: commRMB2})
				}
			}
		}
	}
	*/

	// 批量写入佣金记录
	if len(records) > 0 {
		if err := c.db.WithContext(ctx).Create(&records).Error; err != nil {
			log.Error("佣金计算: 写入佣金记录失败", zap.Error(err))
			return
		}
		log.Info("佣金计算: 成功创建佣金记录",
			zap.Int("count", len(records)),
			zap.Uint("sourceUser", sourceUserID),
			zap.Int64("orderCredits", orderCredits),
			zap.Float64("orderRMB", orderRMB))

		// 异步更新代理的累计收益 TotalEarnings
		for _, eu := range earningsUpdates {
			if err := c.db.WithContext(ctx).Model(&model.UserAgentProfile{}).
				Where("id = ?", eu.profileID).
				Updates(map[string]interface{}{
					"total_earnings": gorm.Expr("total_earnings + ?", eu.rmb),
				}).Error; err != nil {
				log.Error("佣金计算: 更新代理累计收益失败",
					zap.Uint("profileID", eu.profileID), zap.Error(err))
			}
		}
	}
}

// resolveCommissionWithModelAndSupplier 根据推荐人的代理档案、层级、模型和供应商动态确定佣金类型和比例
// 支持按"供应商+模型"维度配置不同代理等级的佣金比例
// 返回值: commType 佣金类型, rate 佣金比例, agentProfileID 代理档案ID（0表示非代理）
// 优先级逻辑:
//   1. 如果提供了模型名称和供应商ID，先查询该供应商+模型的专属配置
//   2. 如果未找到或已禁用，查询仅按模型名称的配置（不限制供应商）
//   3. 如果找到启用的配置，根据推荐人的代理等级返回对应比例的佣金
//   4. 如果没有模型级别配置或已禁用，回退到代理等级默认比例
//   5. 推荐人无代理档案或状态非 ACTIVE → 使用 ReferralConfig 的 PersonalCashbackRate（仅第1层）
func (c *CommissionCalculator) resolveCommissionWithModelAndSupplier(ctx context.Context, user *model.User, level int, cfg *model.ReferralConfig, modelName string, supplierID uint) (string, float64, uint) {
	// 第 2、3 层佣金已停用，直接返回 0
	if level != 1 {
		return "", 0, 0
	}

	// 尝试查询推荐人的代理档案（含关联的 AgentLevel）
	var profile model.UserAgentProfile
	err := c.db.WithContext(ctx).
		Preload("AgentLevel").
		Where("user_id = ? AND status = 'ACTIVE'", user.ID).
		First(&profile).Error

	// 获取代理等级代码，用于查询模型级别配置
	var agentLevelCode string
	if err == nil && profile.AgentLevel.LevelCode != "" {
		agentLevelCode = profile.AgentLevel.LevelCode
	}

	// 优先级1: 如果提供了供应商ID和模型名称，先查询供应商+模型的专属配置
	if supplierID > 0 && modelName != "" {
		var modelConfig model.ModelCommissionConfig
		if err := c.db.WithContext(ctx).
			Where("supplier_id = ? AND model_name = ? AND is_active = ?", supplierID, modelName, true).
			First(&modelConfig).Error; err == nil {
			// 找到启用的供应商+模型配置，根据代理等级获取对应比例
			rate := modelConfig.GetRateByLevel(agentLevelCode)
			if rate > 0 {
				if agentLevelCode != "" {
					// 推荐人是代理，返回模型级别佣金
					return "L1_MODEL_SUPPLIER", rate, profile.ID
				}
				// 推荐人不是代理，返回普通用户模型级别返现
				return "REFERRAL_MODEL_SUPPLIER", rate, 0
			}
		}
	}

	// 优先级2: 如果提供了模型名称，查询仅按模型名称的配置（不限制供应商）
	if modelName != "" {
		var modelConfig model.ModelCommissionConfig
		if err := c.db.WithContext(ctx).
			Where("model_name = ? AND is_active = ?", modelName, true).
			First(&modelConfig).Error; err == nil {
			// 找到启用的模型级别配置，根据代理等级获取对应比例
			rate := modelConfig.GetRateByLevel(agentLevelCode)
			if rate > 0 {
				if agentLevelCode != "" {
					// 推荐人是代理，返回模型级别佣金
					return "L1_MODEL", rate, profile.ID
				}
				// 推荐人不是代理，返回普通用户模型级别返现
				return "REFERRAL_MODEL", rate, 0
			}
		}
	}

	// 优先级3: 没有模型级别配置或已禁用，回退到代理等级默认比例
	if err == nil {
		// 推荐人是激活状态的代理，从 AgentLevel 动态获取佣金比例
		if profile.AgentLevel.DirectCommission > 0 {
			return "L1", profile.AgentLevel.DirectCommission, profile.ID
		}
	}

	// 优先级4: 推荐人无代理档案或档案非 ACTIVE —— 向后兼容 ReferralConfig
	// 普通用户邀请返现，使用全局 PersonalCashbackRate
	return "REFERRAL", cfg.PersonalCashbackRate, 0
}

// resolveCommissionWithModel 根据推荐人的代理档案、层级和模型名称动态确定佣金类型和比例（向后兼容）
// 支持模型级别佣金配置，优先级：模型级别配置 > 代理等级默认比例
// 返回值: commType 佣金类型, rate 佣金比例, agentProfileID 代理档案ID（0表示非代理）
// 注意：此方法已弃用，请使用 resolveCommissionWithModelAndSupplier 以支持供应商维度
func (c *CommissionCalculator) resolveCommissionWithModel(ctx context.Context, user *model.User, level int, cfg *model.ReferralConfig, modelName string) (string, float64, uint) {
	// 委托给新方法，不传入供应商ID
	return c.resolveCommissionWithModelAndSupplier(ctx, user, level, cfg, modelName, 0)
}

// resolveCommission 根据推荐人的代理档案和层级动态确定佣金类型和比例（向后兼容）
// 返回值: commType 佣金类型, rate 佣金比例, agentProfileID 代理档案ID（0表示非代理）
// 优先级逻辑:
//   - 推荐人有 ACTIVE 状态的 UserAgentProfile → 从 AgentLevel 读取对应层级佣金比例
//   - 推荐人无代理档案或状态非 ACTIVE → 使用 ReferralConfig 的 PersonalCashbackRate（仅第1层）
// 注意：此方法已弃用，请使用 resolveCommissionWithModelAndSupplier 以支持模型级别佣金配置
func (c *CommissionCalculator) resolveCommission(ctx context.Context, user *model.User, level int, cfg *model.ReferralConfig) (string, float64, uint) {
	// 委托给新方法，不传入模型名称和供应商ID（使用默认逻辑）
	return c.resolveCommissionWithModelAndSupplier(ctx, user, level, cfg, "", 0)
}

// CalculateCommissionsAsync 异步佣金计算包装，在独立 goroutine 中执行
// 参数 orderCredits 为消费积分数量（int64）
// 注意：此方法为向后兼容，新代码应直接使用 CalculateCommissions
func (c *CommissionCalculator) CalculateCommissionsAsync(sourceUserID, sourceTenantID uint, orderCredits int64) {
	go func() {
		ctx := context.Background()
		defer func() {
			if r := recover(); r != nil {
				if log := logger.L; log != nil {
					log.Error("commission calc panic recovered", zap.Any("panic", r))
				}
			}
		}()
		// 向后兼容调用，不传入模型和供应商信息
		c.CalculateCommissions(ctx, sourceUserID, sourceTenantID, orderCredits, "", 0)
	}()
}

// MarkCommissionRefunded 标记佣金为已退款（退款时冲销对应佣金）
// 参数: orderID 关联订单ID或请求ID
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

	if result.RowsAffected > 0 {
		logger.L.Info("佣金已标记为退款",
			zap.String("relatedID", relatedID),
			zap.Int64("affected", result.RowsAffected))
	}

	return nil
}

// ProcessReferralOnRegister 注册时处理邀请链接绑定
// 应在用户创建后使用注册时的邀请码调用
func ProcessReferralOnRegister(db *gorm.DB, ctx context.Context, user *model.User, referralCode string) error {
	if referralCode == "" || user == nil {
		return nil
	}

	var link model.ReferralLink
	if err := db.WithContext(ctx).Where("code = ?", referralCode).First(&link).Error; err != nil {
		return fmt.Errorf("invalid referral code: %w", err)
	}

	// Don't allow self-referral
	if link.UserID == user.ID {
		return nil
	}

	// Set user's ReferredBy
	if err := db.WithContext(ctx).Model(user).Update("referred_by", link.UserID).Error; err != nil {
		return fmt.Errorf("set referred_by: %w", err)
	}

	// Increment register count
	db.WithContext(ctx).Model(&model.ReferralLink{}).
		Where("id = ?", link.ID).
		UpdateColumn("register_count", gorm.Expr("register_count + 1"))

	return nil
}
