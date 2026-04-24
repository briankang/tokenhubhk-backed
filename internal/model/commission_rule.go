package model

import "time"

// CommissionRule 特殊返佣规则
// 按 (用户集合 × 模型集合) 维度批量配置返佣比例，优先级高于 UserCommissionOverride 和 ReferralConfig。
//
// 典型场景：
//   - 对成本价 > 90% 售价的高成本模型（Claude Opus / Seedance 视频）单独降低返佣比例，避免亏本
//   - 对特定战略合作伙伴针对指定低成本模型提高返佣比例
//
// 匹配逻辑：CommissionCalculator 在计算单笔消费佣金时：
//   1) 先按 (邀请人ID, 消费模型ID) 查 CommissionRule（走 Redis 缓存）
//   2) 命中则使用 rule.CommissionRate，CommissionRecord 写 RuleID + Type="REFERRAL_RULE"
//   3) 未命中则回退 UserCommissionOverride → ReferralConfig.CommissionRate
//
// 多规则冲突：同一 (user, model) 匹配多条规则时按 Priority ASC（小的优先），再按 ID DESC。
type CommissionRule struct {
	BaseModel
	Name           string     `gorm:"size:100;not null" json:"name"`                     // 规则名（管理员可读标识）
	CommissionRate float64    `gorm:"type:decimal(5,4);not null" json:"commission_rate"` // 返佣比例 0~0.80
	Priority       int        `gorm:"default:100;index" json:"priority"`                 // 优先级：小的优先
	IsActive       bool       `gorm:"default:true;index" json:"is_active"`               // 是否启用
	EffectiveFrom  time.Time  `gorm:"not null" json:"effective_from"`                    // 生效起始
	EffectiveTo    *time.Time `json:"effective_to"`                                      // 生效截止（NULL=永久）
	Note           string     `gorm:"size:500" json:"note"`                              // 备注
	CreatedBy      uint       `gorm:"index" json:"created_by"`                           // 创建管理员 ID
}

// TableName 指定表名
func (CommissionRule) TableName() string {
	return "commission_rules"
}

// CommissionRuleUser 规则-用户关联（组合主键，自动带索引）
type CommissionRuleUser struct {
	RuleID uint `gorm:"primaryKey" json:"rule_id"`
	UserID uint `gorm:"primaryKey;index" json:"user_id"` // 作为"邀请人"匹配
}

// TableName 指定表名
func (CommissionRuleUser) TableName() string {
	return "commission_rule_users"
}

// CommissionRuleModel 规则-模型关联（组合主键）
type CommissionRuleModel struct {
	RuleID  uint `gorm:"primaryKey" json:"rule_id"`
	ModelID uint `gorm:"primaryKey;index" json:"model_id"` // ai_models.id
}

// TableName 指定表名
func (CommissionRuleModel) TableName() string {
	return "commission_rule_models"
}
