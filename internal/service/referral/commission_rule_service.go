package referral

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// CommissionRuleService CommissionRule CRUD + 关联表维护 + 缓存失效
type CommissionRuleService struct {
	db       *gorm.DB
	resolver *RuleResolver // 可为 nil（无 Redis 环境降级）
}

// NewCommissionRuleService 构造
func NewCommissionRuleService(db *gorm.DB, resolver *RuleResolver) *CommissionRuleService {
	return &CommissionRuleService{db: db, resolver: resolver}
}

// CreateInput 新建规则入参
type CreateInput struct {
	Name           string
	CommissionRate float64
	Priority       int
	EffectiveFrom  time.Time
	EffectiveTo    *time.Time
	Note           string
	UserIDs        []uint
	ModelIDs       []uint
	CreatedBy      uint
}

// UpdateInput 更新规则入参（除 ID 外所有字段可选——nil 代表不改）
type UpdateInput struct {
	Name           *string
	CommissionRate *float64
	Priority       *int
	IsActive       *bool
	EffectiveFrom  *time.Time
	EffectiveTo    *time.Time // 特殊语义：指针非 nil 且 time.Time{}零值 = 清空
	Note           *string
	UserIDs        *[]uint // nil=不改；非 nil=整体替换
	ModelIDs       *[]uint
}

// RuleDetail 带完整关联的规则详情
type RuleDetail struct {
	model.CommissionRule
	UserIDs  []uint `json:"user_ids"`
	ModelIDs []uint `json:"model_ids"`
}

// validateRate 校验比例范围
func validateRate(rate float64) error {
	if rate < 0 || rate > 0.80 {
		return fmt.Errorf("commission rate must be in [0, 0.80], got %f", rate)
	}
	return nil
}

// Create 新建规则及关联
func (s *CommissionRuleService) Create(ctx context.Context, in CreateInput) (*RuleDetail, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, errors.New("name is required")
	}
	if err := validateRate(in.CommissionRate); err != nil {
		return nil, err
	}
	if len(in.UserIDs) == 0 || len(in.ModelIDs) == 0 {
		return nil, errors.New("user_ids and model_ids cannot be empty")
	}
	if in.EffectiveFrom.IsZero() {
		in.EffectiveFrom = time.Now()
	}

	rule := model.CommissionRule{
		Name:           strings.TrimSpace(in.Name),
		CommissionRate: in.CommissionRate,
		Priority:       in.Priority,
		IsActive:       true,
		EffectiveFrom:  in.EffectiveFrom,
		EffectiveTo:    in.EffectiveTo,
		Note:           strings.TrimSpace(in.Note),
		CreatedBy:      in.CreatedBy,
	}
	if rule.Priority == 0 {
		rule.Priority = 100
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&rule).Error; err != nil {
			return fmt.Errorf("create rule: %w", err)
		}
		if err := insertUsers(tx, rule.ID, in.UserIDs); err != nil {
			return err
		}
		if err := insertModels(tx, rule.ID, in.ModelIDs); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.invalidateCache(ctx, in.UserIDs, in.ModelIDs)
	return &RuleDetail{CommissionRule: rule, UserIDs: in.UserIDs, ModelIDs: in.ModelIDs}, nil
}

// Get 读取规则详情（含关联 ID 列表）
func (s *CommissionRuleService) Get(ctx context.Context, id uint) (*RuleDetail, error) {
	var rule model.CommissionRule
	if err := s.db.WithContext(ctx).First(&rule, id).Error; err != nil {
		return nil, err
	}
	users, models := s.loadAssociations(ctx, id)
	return &RuleDetail{CommissionRule: rule, UserIDs: users, ModelIDs: models}, nil
}

// Update 更新规则
func (s *CommissionRuleService) Update(ctx context.Context, id uint, in UpdateInput) (*RuleDetail, error) {
	var rule model.CommissionRule
	if err := s.db.WithContext(ctx).First(&rule, id).Error; err != nil {
		return nil, err
	}

	// 记录旧关联用于缓存失效
	oldUsers, oldModels := s.loadAssociations(ctx, id)

	updates := map[string]interface{}{}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" {
			return nil, errors.New("name cannot be empty")
		}
		updates["name"] = n
	}
	if in.CommissionRate != nil {
		if err := validateRate(*in.CommissionRate); err != nil {
			return nil, err
		}
		updates["commission_rate"] = *in.CommissionRate
	}
	if in.Priority != nil {
		updates["priority"] = *in.Priority
	}
	if in.IsActive != nil {
		updates["is_active"] = *in.IsActive
	}
	if in.EffectiveFrom != nil {
		updates["effective_from"] = *in.EffectiveFrom
	}
	if in.EffectiveTo != nil {
		if in.EffectiveTo.IsZero() {
			updates["effective_to"] = nil
		} else {
			updates["effective_to"] = *in.EffectiveTo
		}
	}
	if in.Note != nil {
		updates["note"] = strings.TrimSpace(*in.Note)
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(updates) > 0 {
			if err := tx.Model(&model.CommissionRule{}).Where("id = ?", id).Updates(updates).Error; err != nil {
				return err
			}
		}
		if in.UserIDs != nil {
			if len(*in.UserIDs) == 0 {
				return errors.New("user_ids cannot be empty when provided")
			}
			if err := tx.Where("rule_id = ?", id).Delete(&model.CommissionRuleUser{}).Error; err != nil {
				return err
			}
			if err := insertUsers(tx, id, *in.UserIDs); err != nil {
				return err
			}
		}
		if in.ModelIDs != nil {
			if len(*in.ModelIDs) == 0 {
				return errors.New("model_ids cannot be empty when provided")
			}
			if err := tx.Where("rule_id = ?", id).Delete(&model.CommissionRuleModel{}).Error; err != nil {
				return err
			}
			if err := insertModels(tx, id, *in.ModelIDs); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 失效旧 + 新关联的缓存
	newUsers, newModels := s.loadAssociations(ctx, id)
	s.invalidateCache(ctx, mergeUnique(oldUsers, newUsers), mergeUnique(oldModels, newModels))

	return s.Get(ctx, id)
}

// Delete 硬删除（避免软删除占用唯一索引/语义混乱）
func (s *CommissionRuleService) Delete(ctx context.Context, id uint) error {
	users, models := s.loadAssociations(ctx, id)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("rule_id = ?", id).Delete(&model.CommissionRuleUser{}).Error; err != nil {
			return err
		}
		if err := tx.Where("rule_id = ?", id).Delete(&model.CommissionRuleModel{}).Error; err != nil {
			return err
		}
		return tx.Unscoped().Delete(&model.CommissionRule{}, id).Error
	})
	if err != nil {
		return err
	}
	s.invalidateCache(ctx, users, models)
	return nil
}

// Toggle 切换启用状态
func (s *CommissionRuleService) Toggle(ctx context.Context, id uint) (*RuleDetail, error) {
	var rule model.CommissionRule
	if err := s.db.WithContext(ctx).First(&rule, id).Error; err != nil {
		return nil, err
	}
	newActive := !rule.IsActive
	if err := s.db.WithContext(ctx).Model(&model.CommissionRule{}).Where("id = ?", id).
		Update("is_active", newActive).Error; err != nil {
		return nil, err
	}
	users, models := s.loadAssociations(ctx, id)
	s.invalidateCache(ctx, users, models)
	return s.Get(ctx, id)
}

// ListInput 列表过滤
type ListInput struct {
	UserID   uint   // 可选：按关联用户过滤
	ModelID  uint   // 可选：按关联模型过滤
	Keyword  string // name / note 关键字
	IsActive *bool  // 可选
	Page     int
	PageSize int
}

// ListOutput 列表输出
type ListOutput struct {
	List  []RuleDetail `json:"list"`
	Total int64        `json:"total"`
}

// List 分页列表
func (s *CommissionRuleService) List(ctx context.Context, in ListInput) (*ListOutput, error) {
	if in.Page <= 0 {
		in.Page = 1
	}
	if in.PageSize <= 0 {
		in.PageSize = 20
	}
	if in.PageSize > 200 {
		in.PageSize = 200
	}

	tx := s.db.WithContext(ctx).Model(&model.CommissionRule{})
	if in.UserID > 0 {
		var ruleIDs []uint
		s.db.WithContext(ctx).Model(&model.CommissionRuleUser{}).
			Where("user_id = ?", in.UserID).Distinct("rule_id").Pluck("rule_id", &ruleIDs)
		if len(ruleIDs) == 0 {
			return &ListOutput{List: []RuleDetail{}, Total: 0}, nil
		}
		tx = tx.Where("id IN ?", ruleIDs)
	}
	if in.ModelID > 0 {
		var ruleIDs []uint
		s.db.WithContext(ctx).Model(&model.CommissionRuleModel{}).
			Where("model_id = ?", in.ModelID).Distinct("rule_id").Pluck("rule_id", &ruleIDs)
		if len(ruleIDs) == 0 {
			return &ListOutput{List: []RuleDetail{}, Total: 0}, nil
		}
		tx = tx.Where("id IN ?", ruleIDs)
	}
	if kw := strings.TrimSpace(in.Keyword); kw != "" {
		like := "%" + kw + "%"
		tx = tx.Where("name LIKE ? OR note LIKE ?", like, like)
	}
	if in.IsActive != nil {
		tx = tx.Where("is_active = ?", *in.IsActive)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, err
	}

	var rules []model.CommissionRule
	offset := (in.Page - 1) * in.PageSize
	if err := tx.Order("priority ASC, id DESC").Offset(offset).Limit(in.PageSize).Find(&rules).Error; err != nil {
		return nil, err
	}

	out := &ListOutput{List: make([]RuleDetail, 0, len(rules)), Total: total}
	for _, r := range rules {
		users, models := s.loadAssociations(ctx, r.ID)
		out.List = append(out.List, RuleDetail{CommissionRule: r, UserIDs: users, ModelIDs: models})
	}
	return out, nil
}

// ListForInviter 返回邀请人当前生效的全部规则详情（供 ReferralPage 使用）
// 仅返回 inviter 命中的规则 + 命中的 modelID
func (s *CommissionRuleService) ListForInviter(ctx context.Context, inviterID uint) ([]RuleDetail, error) {
	now := time.Now()
	var ruleIDs []uint
	if err := s.db.WithContext(ctx).
		Table("commission_rules").
		Joins("JOIN commission_rule_users cru ON cru.rule_id = commission_rules.id").
		Where("cru.user_id = ? AND commission_rules.is_active = ? AND commission_rules.effective_from <= ?", inviterID, true, now).
		Where("commission_rules.effective_to IS NULL OR commission_rules.effective_to > ?", now).
		Where("commission_rules.deleted_at IS NULL").
		Distinct("commission_rules.id").
		Pluck("commission_rules.id", &ruleIDs).Error; err != nil {
		return nil, err
	}
	if len(ruleIDs) == 0 {
		return []RuleDetail{}, nil
	}

	var rules []model.CommissionRule
	if err := s.db.WithContext(ctx).Where("id IN ?", ruleIDs).Order("priority ASC").Find(&rules).Error; err != nil {
		return nil, err
	}

	out := make([]RuleDetail, 0, len(rules))
	for _, r := range rules {
		// 关联模型（用户端不暴露其他用户列表，只需模型列表）
		var modelIDs []uint
		s.db.WithContext(ctx).Model(&model.CommissionRuleModel{}).
			Where("rule_id = ?", r.ID).Pluck("model_id", &modelIDs)
		out = append(out, RuleDetail{CommissionRule: r, ModelIDs: modelIDs})
	}
	return out, nil
}

// loadAssociations 加载规则的 users/models ID 列表
func (s *CommissionRuleService) loadAssociations(ctx context.Context, ruleID uint) ([]uint, []uint) {
	var users, models []uint
	s.db.WithContext(ctx).Model(&model.CommissionRuleUser{}).
		Where("rule_id = ?", ruleID).Pluck("user_id", &users)
	s.db.WithContext(ctx).Model(&model.CommissionRuleModel{}).
		Where("rule_id = ?", ruleID).Pluck("model_id", &models)
	return users, models
}

func (s *CommissionRuleService) invalidateCache(ctx context.Context, users, models []uint) {
	if s.resolver == nil {
		return
	}
	s.resolver.InvalidateByRule(ctx, users, models)
}

func insertUsers(tx *gorm.DB, ruleID uint, userIDs []uint) error {
	rows := make([]model.CommissionRuleUser, 0, len(userIDs))
	seen := make(map[uint]struct{})
	for _, uid := range userIDs {
		if uid == 0 {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		rows = append(rows, model.CommissionRuleUser{RuleID: ruleID, UserID: uid})
	}
	if len(rows) == 0 {
		return errors.New("no valid user_ids")
	}
	return tx.Create(&rows).Error
}

func insertModels(tx *gorm.DB, ruleID uint, modelIDs []uint) error {
	rows := make([]model.CommissionRuleModel, 0, len(modelIDs))
	seen := make(map[uint]struct{})
	for _, mid := range modelIDs {
		if mid == 0 {
			continue
		}
		if _, ok := seen[mid]; ok {
			continue
		}
		seen[mid] = struct{}{}
		rows = append(rows, model.CommissionRuleModel{RuleID: ruleID, ModelID: mid})
	}
	if len(rows) == 0 {
		return errors.New("no valid model_ids")
	}
	return tx.Create(&rows).Error
}

func mergeUnique(a, b []uint) []uint {
	seen := make(map[uint]struct{}, len(a)+len(b))
	out := make([]uint, 0, len(a)+len(b))
	for _, v := range append(a, b...) {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
