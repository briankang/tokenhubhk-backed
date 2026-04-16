package aimodel

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// AIModelService AI 模型服务，处理模型的增删改查操作
type AIModelService struct {
	db *gorm.DB
}

// NewAIModelService 创建 AI 模型服务实例，db 不能为 nil 否则 panic
func NewAIModelService(db *gorm.DB) *AIModelService {
	if db == nil {
		panic("ai model service: db is nil")
	}
	return &AIModelService{db: db}
}

// Create 创建新的 AI 模型，校验模型名、分类 ID、供应商 ID 不能为空
// 新模型默认状态为 offline，需验证后才能上线
func (s *AIModelService) Create(ctx context.Context, m *model.AIModel) error {
	if m == nil {
		return fmt.Errorf("ai model is nil")
	}
	if m.ModelName == "" {
		return fmt.Errorf("model name is required")
	}
	if m.CategoryID == 0 {
		return fmt.Errorf("category id is required")
	}
	if m.SupplierID == 0 {
		return fmt.Errorf("supplier id is required")
	}
	// 新模型默认状态为 offline
	if m.Status == "" {
		m.Status = "offline"
	}
	return s.db.WithContext(ctx).Create(m).Error
}

// GetByID 根据 ID 查询 AI 模型，预加载分类和供应商信息
func (s *AIModelService) GetByID(ctx context.Context, id uint) (*model.AIModel, error) {
	if id == 0 {
		return nil, fmt.Errorf("model id is required")
	}
	var m model.AIModel
	if err := s.db.WithContext(ctx).Preload("Category").Preload("Supplier").First(&m, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("ai model not found")
		}
		return nil, fmt.Errorf("failed to get ai model: %w", err)
	}
	return &m, nil
}

// List 分页查询 AI 模型列表，预加载关联数据
func (s *AIModelService) List(ctx context.Context, page, pageSize int) ([]model.AIModel, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	query := s.db.WithContext(ctx).Model(&model.AIModel{})
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count ai models: %w", err)
	}
	var models []model.AIModel
	offset := (page - 1) * pageSize
	if err := query.Preload("Category").Preload("Supplier").Offset(offset).Limit(pageSize).Order("id DESC").Find(&models).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list ai models: %w", err)
	}
	return models, total, nil
}

// ListWithFilter 带供应商和搜索过滤的分页模型列表
func (s *AIModelService) ListWithFilter(ctx context.Context, page, pageSize int, supplierID uint, search string) ([]model.AIModel, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 500 {
		pageSize = 20
	}
	var total int64
	query := s.db.WithContext(ctx).Model(&model.AIModel{})
	if supplierID > 0 {
		query = query.Where("supplier_id = ?", supplierID)
	}
	if search != "" {
		like := "%" + search + "%"
		query = query.Where("model_name LIKE ? OR display_name LIKE ? OR tags LIKE ?", like, like, like)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count ai models: %w", err)
	}
	var models []model.AIModel
	offset := (page - 1) * pageSize
	if err := query.Preload("Category").Preload("Supplier").Preload("Pricing").Offset(offset).Limit(pageSize).Order("id DESC").Find(&models).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list ai models: %w", err)
	}
	return models, total, nil
}

// Update 根据 ID 更新 AI 模型信息
func (s *AIModelService) Update(ctx context.Context, id uint, updates map[string]interface{}) error {
	if id == 0 {
		return fmt.Errorf("model id is required")
	}
	delete(updates, "id")
	res := s.db.WithContext(ctx).Model(&model.AIModel{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("failed to update ai model: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("ai model not found")
	}
	return nil
}

// Delete 根据 ID 软删除 AI 模型
func (s *AIModelService) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("model id is required")
	}
	res := s.db.WithContext(ctx).Delete(&model.AIModel{}, id)
	if res.Error != nil {
		return fmt.Errorf("failed to delete ai model: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("ai model not found")
	}
	return nil
}

// SetStatus 更新模型状态（offline/online/error）
// 用于手动设置模型状态或验证后更新
// 特别地：当 status=online 时同时激活 is_active=true，
// 这样管理员在后台点"启用"即可直接对外展示，无需再单独勾选激活位。
func (s *AIModelService) SetStatus(ctx context.Context, id uint, status string) error {
	if id == 0 {
		return fmt.Errorf("model id is required")
	}
	if status != "offline" && status != "online" && status != "error" {
		return fmt.Errorf("invalid status: %s, must be offline/online/error", status)
	}
	updates := map[string]interface{}{"status": status}
	if status == "online" {
		updates["is_active"] = true
	}
	res := s.db.WithContext(ctx).Model(&model.AIModel{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("failed to update model status: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("ai model not found")
	}
	return nil
}

// ListOnline 查询所有在线模型（is_active=true 且 status=online）
// 用于公开模型列表接口
// modelTypes: 可选类型过滤（如 []string{"ImageGeneration"}）
//   - 不传 / 空切片 → 默认返回聊天类模型（LLM/VLM）且有价格
//   - 传入具体类型 → 覆盖默认过滤，不做价格限制（图像/视频多为按张/按秒计费）
func (s *AIModelService) ListOnline(ctx context.Context, page, pageSize int, modelTypes ...string) ([]model.AIModel, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 500 {
		pageSize = 20
	}

	typeFilter := []string{"LLM", "VLM"}
	priceFilter := true
	if len(modelTypes) > 0 {
		typeFilter = modelTypes
		priceFilter = false
	}

	var total int64
	query := s.db.WithContext(ctx).Model(&model.AIModel{}).
		Joins("JOIN suppliers ON suppliers.id = ai_models.supplier_id").
		Where("ai_models.is_active = ? AND ai_models.status = ?", true, "online").
		Where("suppliers.status = ? AND suppliers.is_active = ?", "active", true).
		Where("ai_models.model_type IN ?", typeFilter)
	if priceFilter {
		query = query.Where("ai_models.input_cost_rmb > 0 OR ai_models.output_cost_rmb > 0")
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count online models: %w", err)
	}
	var models []model.AIModel
	offset := (page - 1) * pageSize
	if err := query.Preload("Category").Preload("Supplier").Preload("Pricing").Offset(offset).Limit(pageSize).Order("ai_models.id DESC").Find(&models).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list online models: %w", err)
	}
	return models, total, nil
}

// ListOnlineByPopularity 按调用次数降序返回在线模型（热门排序）
func (s *AIModelService) ListOnlineByPopularity(ctx context.Context, page, pageSize int) ([]model.AIModel, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 500 {
		pageSize = 20
	}
	var total int64
	query := s.db.WithContext(ctx).Model(&model.AIModel{}).
		Joins("JOIN suppliers ON suppliers.id = ai_models.supplier_id").
		Where("ai_models.is_active = ? AND ai_models.status = ?", true, "online").
		Where("suppliers.status = ? AND suppliers.is_active = ?", "active", true)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count online models: %w", err)
	}
	var models []model.AIModel
	offset := (page - 1) * pageSize
	if err := query.Preload("Category").Preload("Supplier").Preload("Pricing").
		Offset(offset).Limit(pageSize).Order("ai_models.call_count DESC").Find(&models).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list online models by popularity: %w", err)
	}
	return models, total, nil
}
