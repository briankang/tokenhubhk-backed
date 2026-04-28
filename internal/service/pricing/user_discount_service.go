package pricing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// UserDiscountService 封装 UserModelDiscount 的 CRUD 与缓存失效
type UserDiscountService struct {
	db   *gorm.DB
	calc *PricingCalculator
}

// NewUserDiscountService 创建服务实例。calc 可以为 nil（不触发缓存失效）。
func NewUserDiscountService(db *gorm.DB, calc *PricingCalculator) *UserDiscountService {
	if db == nil {
		panic("UserDiscountService: db must not be nil")
	}
	return &UserDiscountService{db: db, calc: calc}
}

// UserDiscountInput 创建/更新 UserModelDiscount 的输入
type UserDiscountInput struct {
	UserID       uint
	ModelID      uint
	PricingType  string // DISCOUNT | FIXED | MARKUP
	DiscountRate *float64
	InputPrice   *float64
	OutputPrice  *float64
	MarkupRate   *float64
	EffectiveAt  *time.Time
	ExpireAt     *time.Time
	Note         string
	IsActive     *bool // nil 时默认 true
	OperatorID   uint
}

// ListFilter 列表查询筛选器
type ListFilter struct {
	UserID   uint
	ModelID  uint
	IsActive *bool
	Keyword  string // 模糊匹配 note/用户邮箱/模型名
	Page     int
	PageSize int
}

// ListResult 列表查询返回
type ListResult struct {
	Items    []model.UserModelDiscount `json:"items"`
	Total    int64                     `json:"total"`
	Page     int                       `json:"page"`
	PageSize int                       `json:"page_size"`
}

// validate 校验输入合法性
func (s *UserDiscountService) validate(in *UserDiscountInput) error {
	if in.UserID == 0 {
		return errors.New("user_id is required")
	}
	if in.ModelID == 0 {
		return errors.New("model_id is required")
	}
	switch in.PricingType {
	case "DISCOUNT":
		if in.DiscountRate == nil || *in.DiscountRate <= 0 || *in.DiscountRate > 2 {
			return errors.New("discount_rate must be in (0, 2]")
		}
	case "FIXED":
		if in.InputPrice == nil && in.OutputPrice == nil {
			return errors.New("FIXED requires input_price or output_price")
		}
	case "MARKUP":
		if in.MarkupRate == nil || *in.MarkupRate <= 0 {
			return errors.New("markup_rate must be > 0")
		}
	default:
		return fmt.Errorf("invalid pricing_type: %s", in.PricingType)
	}
	if in.EffectiveAt != nil && in.ExpireAt != nil && in.ExpireAt.Before(*in.EffectiveAt) {
		return errors.New("expire_at must be after effective_at")
	}
	return nil
}

// Upsert 按 (user_id, model_id) 唯一约束插入或更新
func (s *UserDiscountService) Upsert(ctx context.Context, in *UserDiscountInput) (*model.UserModelDiscount, error) {
	if err := s.validate(in); err != nil {
		return nil, err
	}
	active := true
	if in.IsActive != nil {
		active = *in.IsActive
	}
	row := model.UserModelDiscount{
		UserID:       in.UserID,
		ModelID:      in.ModelID,
		PricingType:  in.PricingType,
		DiscountRate: in.DiscountRate,
		InputPrice:   in.InputPrice,
		OutputPrice:  in.OutputPrice,
		MarkupRate:   in.MarkupRate,
		EffectiveAt:  in.EffectiveAt,
		ExpireAt:     in.ExpireAt,
		Note:         in.Note,
		IsActive:     active,
		OperatorID:   in.OperatorID,
	}

	// 查找现有记录
	var existing model.UserModelDiscount
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND model_id = ?", in.UserID, in.ModelID).
		First(&existing).Error
	if err == nil {
		row.ID = existing.ID
		row.CreatedAt = existing.CreatedAt
		if err := s.db.WithContext(ctx).Save(&row).Error; err != nil {
			return nil, fmt.Errorf("update user discount: %w", err)
		}
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return nil, fmt.Errorf("create user discount: %w", err)
		}
	} else {
		return nil, fmt.Errorf("query existing: %w", err)
	}

	s.invalidate(ctx, in.UserID)
	return &row, nil
}

// BatchUpsert 批量对一个用户的多个模型设置相同折扣
func (s *UserDiscountService) BatchUpsert(ctx context.Context, userID uint, modelIDs []uint, tmpl UserDiscountInput) ([]model.UserModelDiscount, error) {
	if userID == 0 {
		return nil, errors.New("user_id is required")
	}
	if len(modelIDs) == 0 {
		return nil, errors.New("model_ids is required")
	}
	results := make([]model.UserModelDiscount, 0, len(modelIDs))
	for _, mid := range modelIDs {
		in := tmpl
		in.UserID = userID
		in.ModelID = mid
		row, err := s.Upsert(ctx, &in)
		if err != nil {
			return results, fmt.Errorf("upsert model %d: %w", mid, err)
		}
		results = append(results, *row)
	}
	// 已在每次 Upsert 中失效
	return results, nil
}

// Update 按 ID 更新单条记录（支持部分更新：只传需要改的字段，其余从现有记录继承）
func (s *UserDiscountService) Update(ctx context.Context, id uint, in *UserDiscountInput) (*model.UserModelDiscount, error) {
	var existing model.UserModelDiscount
	if err := s.db.WithContext(ctx).First(&existing, id).Error; err != nil {
		return nil, fmt.Errorf("discount %d not found: %w", id, err)
	}
	// 锁定 user_id/model_id 不可变更
	in.UserID = existing.UserID
	in.ModelID = existing.ModelID
	// 部分更新：未传的关键字段从现有记录继承，保证 validate 通过
	if in.PricingType == "" {
		in.PricingType = existing.PricingType
	}
	if in.DiscountRate == nil {
		in.DiscountRate = existing.DiscountRate
	}
	if in.InputPrice == nil {
		in.InputPrice = existing.InputPrice
	}
	if in.OutputPrice == nil {
		in.OutputPrice = existing.OutputPrice
	}
	if in.MarkupRate == nil {
		in.MarkupRate = existing.MarkupRate
	}
	if in.EffectiveAt == nil {
		in.EffectiveAt = existing.EffectiveAt
	}
	// ExpireAt 明确传 null（zero time）表示清除过期时间；不传时继承
	if in.ExpireAt == nil {
		in.ExpireAt = existing.ExpireAt
	}
	if in.Note == "" {
		in.Note = existing.Note
	}
	if err := s.validate(in); err != nil {
		return nil, err
	}
	active := existing.IsActive
	if in.IsActive != nil {
		active = *in.IsActive
	}
	existing.PricingType = in.PricingType
	existing.DiscountRate = in.DiscountRate
	existing.InputPrice = in.InputPrice
	existing.OutputPrice = in.OutputPrice
	existing.MarkupRate = in.MarkupRate
	existing.EffectiveAt = in.EffectiveAt
	existing.ExpireAt = in.ExpireAt
	existing.Note = in.Note
	existing.IsActive = active
	existing.OperatorID = in.OperatorID
	if err := s.db.WithContext(ctx).Save(&existing).Error; err != nil {
		return nil, fmt.Errorf("save discount: %w", err)
	}
	s.invalidate(ctx, existing.UserID)
	return &existing, nil
}

// Delete 删除单条记录
func (s *UserDiscountService) Delete(ctx context.Context, id uint) error {
	var existing model.UserModelDiscount
	if err := s.db.WithContext(ctx).First(&existing, id).Error; err != nil {
		return fmt.Errorf("discount %d not found: %w", id, err)
	}
	// 硬删除，避免软删除残留与唯一索引 uk_user_model 冲突，导致重建时报 Duplicate entry
	if err := s.db.WithContext(ctx).Unscoped().Delete(&existing).Error; err != nil {
		return fmt.Errorf("delete discount: %w", err)
	}
	s.invalidate(ctx, existing.UserID)
	return nil
}

// Get 获取单条记录，带 User/Model 关联数据
func (s *UserDiscountService) Get(ctx context.Context, id uint) (*model.UserModelDiscount, error) {
	var row model.UserModelDiscount
	if err := s.db.WithContext(ctx).
		Preload("User").
		Preload("Model").
		First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// List 分页查询
func (s *UserDiscountService) List(ctx context.Context, f ListFilter) (*ListResult, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 200 {
		f.PageSize = 20
	}

	tx := s.db.WithContext(ctx).Model(&model.UserModelDiscount{})
	if f.UserID > 0 {
		tx = tx.Where("user_id = ?", f.UserID)
	}
	if f.ModelID > 0 {
		tx = tx.Where("model_id = ?", f.ModelID)
	}
	if f.IsActive != nil {
		tx = tx.Where("is_active = ?", *f.IsActive)
	}
	if f.Keyword != "" {
		like := "%" + f.Keyword + "%"
		// 避免 GORM 将子查询包裹 SET NAMES 触发 MySQL 1064 错误，改为先查 ID 列表再 IN 过滤
		var userIDs []uint
		s.db.WithContext(ctx).Model(&model.User{}).Select("id").Where("email LIKE ? OR name LIKE ?", like, like).Scan(&userIDs)
		var modelIDs []uint
		s.db.WithContext(ctx).Model(&model.AIModel{}).Select("id").Where("model_name LIKE ? OR display_name LIKE ?", like, like).Scan(&modelIDs)
		switch {
		case len(userIDs) > 0 && len(modelIDs) > 0:
			tx = tx.Where("note LIKE ? OR user_id IN ? OR model_id IN ?", like, userIDs, modelIDs)
		case len(userIDs) > 0:
			tx = tx.Where("note LIKE ? OR user_id IN ?", like, userIDs)
		case len(modelIDs) > 0:
			tx = tx.Where("note LIKE ? OR model_id IN ?", like, modelIDs)
		default:
			tx = tx.Where("note LIKE ?", like)
		}
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("count: %w", err)
	}

	var items []model.UserModelDiscount
	if err := tx.
		Preload("User").
		Preload("Model").
		Order("id DESC").
		Limit(f.PageSize).
		Offset((f.Page - 1) * f.PageSize).
		Find(&items).Error; err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	return &ListResult{Items: items, Total: total, Page: f.Page, PageSize: f.PageSize}, nil
}

// invalidate 触发用户级缓存失效
func (s *UserDiscountService) invalidate(ctx context.Context, userID uint) {
	clearDiscountMissCache()
	if s.calc == nil {
		return
	}
	s.calc.InvalidateUserCache(ctx, userID)
}
