package aimodel

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// ModelStats 模型统计数量，直接从数据库聚合，不受分页限制
type ModelStats struct {
	Total   int64 `json:"total"`
	Enabled int64 `json:"enabled"`
	Online  int64 `json:"online"`
}

// GetStats 返回全量模型统计：总数、已启用数、在线数
func (s *AIModelService) GetStats(ctx context.Context) (*ModelStats, error) {
	var stats ModelStats
	db := s.db.WithContext(ctx).Model(&model.AIModel{})
	if err := db.Count(&stats.Total).Error; err != nil {
		return nil, fmt.Errorf("failed to count total models: %w", err)
	}
	if err := s.db.WithContext(ctx).Model(&model.AIModel{}).Where("is_active = ?", true).Count(&stats.Enabled).Error; err != nil {
		return nil, fmt.Errorf("failed to count enabled models: %w", err)
	}
	if err := s.db.WithContext(ctx).Model(&model.AIModel{}).Where("status = ?", "online").Count(&stats.Online).Error; err != nil {
		return nil, fmt.Errorf("failed to count online models: %w", err)
	}
	return &stats, nil
}

// ListWithFilter 带供应商和搜索过滤的分页模型列表
//
// v3.5 排序优先级（从高到低）：
//  1. label_priority DESC（标签优先级：热卖 100 > 优惠 80 > 新品 70 > 免费 60 > 推荐 50）
//  2. is_active DESC（已启用在前）
//  3. has_price DESC（有价格在前）
//  4. call_count DESC（调用次数高在前）
//  5. id DESC（最新创建兜底）
//
// 标签优先级通过子查询 JOIN label_dictionary 取该模型所有标签中 priority 的最大值。
func (s *AIModelService) ListWithFilter(ctx context.Context, page, pageSize int, supplierID uint, search string) ([]model.AIModel, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 10000 {
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

	// v3.5 多级排序：标签优先级 > is_active > has_price > call_count > id
	// 注意：MySQL 保留字 `key` 必须加反引号；sql_mode=only_full_group_by 下 CASE 表达式需单独排序列
	orderClause := "COALESCE((SELECT MAX(ld.priority) FROM model_labels ml " +
		"JOIN label_dictionary ld ON ld.`key` = ml.label_key AND ld.is_active = 1 " +
		"WHERE ml.model_id = ai_models.id AND ml.deleted_at IS NULL), 0) DESC, " +
		"is_active DESC, " +
		"(CASE WHEN input_cost_rmb > 0 OR output_cost_rmb > 0 THEN 1 ELSE 0 END) DESC, " +
		"call_count DESC, " +
		"id DESC"

	if err := query.
		Preload("Category").Preload("Supplier").Preload("Pricing").
		Offset(offset).Limit(pageSize).
		Order(orderClause).
		Find(&models).Error; err != nil {
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
	if status == "online" {
		report, err := s.PreflightModelEnable(ctx, id)
		if err != nil {
			return err
		}
		if !report.CanEnable {
			return fmt.Errorf("model preflight failed: %s", report.BlockerMessage())
		}
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

func IsTemporaryModelName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(lower, "tmp-model_") ||
		strings.HasPrefix(lower, "qa-offline-") ||
		strings.HasPrefix(lower, "updated-model_")
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

	typeFilter := []string{"LLM", "VLM", "Reasoning"}
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

// BatchUpdateFreeTier 批量更新模型的 IsFreeTier 状态
func (s *AIModelService) BatchUpdateFreeTier(ctx context.Context, ids []uint, isFree bool) error {
	if len(ids) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Model(&model.AIModel{}).
		Where("id IN ?", ids).
		Update("is_free_tier", isFree).Error
}
