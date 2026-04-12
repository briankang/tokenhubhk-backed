// Package orchestration 提供多模型编排工作流管理功能
package orchestration

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// OrchestrationStep 定义编排工作流中的单个步骤
type OrchestrationStep struct {
	Name      string                 `json:"name"`
	NodeType  string                 `json:"node_type"`  // 节点类型: llm / code / condition
	Model     string                 `json:"model"`      // LLM节点使用的模型名称
	Prompt    string                 `json:"prompt"`     // 自定义system prompt
	Condition string                 `json:"condition"`  // 路由节点的条件表达式
	Timeout   int                    `json:"timeout"`    // 单步超时时间（秒，默认60）
	Params    map[string]interface{} `json:"params"`     // 额外参数
}

// OrchestrationService 编排工作流的CRUD服务
type OrchestrationService struct {
	db *gorm.DB
}

// NewOrchestrationService 创建编排服务实例，db不允许为nil
func NewOrchestrationService(db *gorm.DB) *OrchestrationService {
	if db == nil {
		panic("OrchestrationService: db must not be nil")
	}
	return &OrchestrationService{db: db}
}

// Create 新建编排工作流，校验名称、编码、模式和步骤JSON格式
func (s *OrchestrationService) Create(ctx context.Context, orch *model.Orchestration) error {
	if orch.Name == "" {
		return fmt.Errorf("orchestration name is required")
	}
	if orch.Code == "" {
		return fmt.Errorf("orchestration code is required")
	}
	// 模式默认为PIPELINE，仅允许PIPELINE/ROUTER/FALLBACK三种
	if orch.Mode == "" {
		orch.Mode = "PIPELINE"
	}
	if orch.Mode != "PIPELINE" && orch.Mode != "ROUTER" && orch.Mode != "FALLBACK" {
		return fmt.Errorf("invalid orchestration mode: %s, must be PIPELINE/ROUTER/FALLBACK", orch.Mode)
	}

	// 校验步骤JSON格式
	if len(orch.Steps) > 0 {
		var steps []OrchestrationStep
		if err := json.Unmarshal(orch.Steps, &steps); err != nil {
			return fmt.Errorf("invalid steps JSON: %w", err)
		}
		if len(steps) == 0 {
			return fmt.Errorf("steps must contain at least one step")
		}
		for i, step := range steps {
			if step.Name == "" {
				return fmt.Errorf("step[%d]: name is required", i)
			}
			if step.NodeType == "" {
				return fmt.Errorf("step[%d]: node_type is required", i)
			}
		}
	}

	return s.db.WithContext(ctx).Create(orch).Error
}

// GetByID 根据ID查询编排工作流
func (s *OrchestrationService) GetByID(ctx context.Context, id uint) (*model.Orchestration, error) {
	var orch model.Orchestration
	if err := s.db.WithContext(ctx).First(&orch, id).Error; err != nil {
		return nil, fmt.Errorf("orchestration %d not found: %w", id, err)
	}
	return &orch, nil
}

// GetByCode 根据唯一编码查询编排工作流
func (s *OrchestrationService) GetByCode(ctx context.Context, code string) (*model.Orchestration, error) {
	var orch model.Orchestration
	if err := s.db.WithContext(ctx).Where("code = ?", code).First(&orch).Error; err != nil {
		return nil, fmt.Errorf("orchestration code %q not found: %w", code, err)
	}
	return &orch, nil
}

// List 分页获取编排工作流列表，支持按模式、激活状态、名称过滤
func (s *OrchestrationService) List(ctx context.Context, page, pageSize int, filters map[string]interface{}) ([]model.Orchestration, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	query := s.db.WithContext(ctx).Model(&model.Orchestration{})

	if v, ok := filters["mode"]; ok {
		query = query.Where("mode = ?", v)
	}
	if v, ok := filters["is_active"]; ok {
		query = query.Where("is_active = ?", v)
	}
	if v, ok := filters["name"]; ok {
		query = query.Where("name LIKE ?", fmt.Sprintf("%%%s%%", v))
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count orchestrations: %w", err)
	}

	var list []model.Orchestration
	offset := (page - 1) * pageSize
	if err := query.Offset(offset).Limit(pageSize).Order("id DESC").Find(&list).Error; err != nil {
		return nil, 0, fmt.Errorf("list orchestrations: %w", err)
	}
	return list, total, nil
}

// Update 更新编排工作流字段，校验模式和步骤格式
func (s *OrchestrationService) Update(ctx context.Context, id uint, updates map[string]interface{}) error {
	if id == 0 {
		return fmt.Errorf("orchestration id is required")
	}

	// 如果提供了mode字段，校验其合法性
	if mode, ok := updates["mode"]; ok {
		m, _ := mode.(string)
		if m != "PIPELINE" && m != "ROUTER" && m != "FALLBACK" {
			return fmt.Errorf("invalid orchestration mode: %v", mode)
		}
	}

	// 如果提供了steps字段，校验JSON格式并序列化
	if stepsRaw, ok := updates["steps"]; ok {
		stepsBytes, err := json.Marshal(stepsRaw)
		if err != nil {
			return fmt.Errorf("invalid steps: %w", err)
		}
		var steps []OrchestrationStep
		if err := json.Unmarshal(stepsBytes, &steps); err != nil {
			return fmt.Errorf("invalid steps format: %w", err)
		}
		updates["steps"] = stepsBytes
	}

	result := s.db.WithContext(ctx).Model(&model.Orchestration{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("update orchestration: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("orchestration %d not found", id)
	}
	return nil
}

// Delete 软删除编排工作流
func (s *OrchestrationService) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("orchestration id is required")
	}
	result := s.db.WithContext(ctx).Delete(&model.Orchestration{}, id)
	if result.Error != nil {
		return fmt.Errorf("delete orchestration: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("orchestration %d not found", id)
	}
	return nil
}
