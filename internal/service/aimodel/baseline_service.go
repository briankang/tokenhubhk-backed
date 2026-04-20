package aimodel

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// ============================================================
// BaselineService: 回归基线管理
// - PromoteTaskAsBaseline：将某任务的所有 passed 结果固化为基线
// - DetectRegressions：基于基线表生成回归列表
// ============================================================

type BaselineService struct {
	db     *gorm.DB
	logger *zap.Logger
}

func NewBaselineService(db *gorm.DB) *BaselineService {
	return &BaselineService{
		db:     db,
		logger: logger.L.With(zap.String("module", "baseline_service")),
	}
}

// PromoteTaskAsBaseline 将任务内所有 passed 结果提升为基线
// 逐条 Upsert：同一 (model_id, case_id) 更新，否则插入
func (b *BaselineService) PromoteTaskAsBaseline(ctx context.Context, taskID uint, adminID uint) (int, error) {
	var results []model.CapabilityTestResult
	if err := b.db.WithContext(ctx).
		Where("task_id = ? AND status IN ?", taskID, []string{"passed"}).
		Find(&results).Error; err != nil {
		return 0, fmt.Errorf("加载任务结果失败: %w", err)
	}

	now := time.Now()
	count := 0
	for _, r := range results {
		baseline := model.CapabilityTestBaseline{
			ModelID:            r.ModelID,
			CaseID:             r.CaseID,
			Outcome:            "pass",
			LatencyMS:          r.LatencyMS,
			ResponseSnippet:    r.ResponseSnippet,
			PromotedFromTaskID: taskID,
			PromotedByAdminID:  adminID,
			PromotedAt:         now,
			ModelName:          r.ModelName,
			CaseName:           r.CaseName,
		}

		var existing model.CapabilityTestBaseline
		err := b.db.WithContext(ctx).
			Where("model_id = ? AND case_id = ?", r.ModelID, r.CaseID).
			First(&existing).Error

		if err == gorm.ErrRecordNotFound {
			if err := b.db.WithContext(ctx).Create(&baseline).Error; err == nil {
				count++
			}
		} else if err == nil {
			baseline.ID = existing.ID
			if err := b.db.WithContext(ctx).Save(&baseline).Error; err == nil {
				count++
			}
		}
	}

	b.logger.Info("提升基线完成",
		zap.Uint("task_id", taskID),
		zap.Uint("admin_id", adminID),
		zap.Int("count", count))
	return count, nil
}

// ListBaselines 列出所有基线（管理后台"基线管理"Tab）
func (b *BaselineService) ListBaselines(ctx context.Context, modelID *uint) ([]model.CapabilityTestBaseline, error) {
	var items []model.CapabilityTestBaseline
	q := b.db.WithContext(ctx).Order("promoted_at desc")
	if modelID != nil && *modelID > 0 {
		q = q.Where("model_id = ?", *modelID)
	}
	if err := q.Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// DeleteBaseline 删除某条基线
func (b *BaselineService) DeleteBaseline(ctx context.Context, id uint) error {
	return b.db.WithContext(ctx).Delete(&model.CapabilityTestBaseline{}, id).Error
}

// ListTaskRegressions 返回任务内的回归列表（status=regression 的结果）
func (b *BaselineService) ListTaskRegressions(ctx context.Context, taskID uint) ([]model.CapabilityTestResult, error) {
	var items []model.CapabilityTestResult
	if err := b.db.WithContext(ctx).
		Where("task_id = ? AND status = ?", taskID, "regression").
		Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}
