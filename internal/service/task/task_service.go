package task

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/service/aimodel"
	"tokenhub-server/internal/service/audit"
	"tokenhub-server/internal/service/modeldiscovery"
	"tokenhub-server/internal/service/pricescraper"
)

// TaskService 后台任务管理服务
type TaskService struct {
	db               *gorm.DB
	discoveryService *modeldiscovery.DiscoveryService
	modelChecker     *aimodel.ModelChecker
	scraperService   *pricescraper.PriceScraperService
	auditService     *audit.AuditService
	mu               sync.Mutex
	runningTasks     map[uint]context.CancelFunc // taskID → cancel
}

// NewTaskService 创建后台任务服务
func NewTaskService(db *gorm.DB) *TaskService {
	svc := &TaskService{
		db:               db,
		discoveryService: modeldiscovery.NewDiscoveryService(db),
		modelChecker:     aimodel.NewModelChecker(db),
		scraperService:   pricescraper.NewPriceScraperService(db),
		auditService:     audit.NewAuditService(db),
		runningTasks:     make(map[uint]context.CancelFunc),
	}
	svc.recoverStaleTasks()
	return svc
}

// recoverStaleTasks 服务启动时将遗留的 pending/running 任务标记为 failed
// 防止服务重启后任务永久卡在"等待中"或"运行中"
func (s *TaskService) recoverStaleTasks() {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	now := time.Now()
	result := s.db.Model(&model.BackgroundTask{}).
		Where("status IN ?", []string{model.TaskStatusPending, model.TaskStatusRunning}).
		Updates(map[string]interface{}{
			"status":           model.TaskStatusFailed,
			"error_message":    "服务重启，任务中断",
			"progress_message": "服务重启，任务已中断，请重新触发",
			"completed_at":     now,
		})
	if result.RowsAffected > 0 {
		log.Warn("启动时发现遗留任务，已标记为失败",
			zap.Int64("count", result.RowsAffected))
	}
}

// CreateAndRun 创建任务并在后台运行
func (s *TaskService) CreateAndRun(taskType string, params map[string]interface{}, operatorID uint) (*model.BackgroundTask, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	// 检查是否有同类型任务正在运行
	var runningCount int64
	s.db.Model(&model.BackgroundTask{}).
		Where("task_type = ? AND status = ?", taskType, model.TaskStatusRunning).
		Count(&runningCount)
	if runningCount > 0 {
		return nil, fmt.Errorf("已有同类型任务正在运行，请等待完成后再试")
	}

	// 序列化参数
	paramsJSON, _ := json.Marshal(params)

	task := &model.BackgroundTask{
		TaskType:   taskType,
		Status:     model.TaskStatusPending,
		Params:     string(paramsJSON),
		OperatorID: operatorID,
	}

	if err := s.db.Create(task).Error; err != nil {
		return nil, fmt.Errorf("创建任务失败: %w", err)
	}

	// 后台运行
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.runningTasks[task.ID] = cancel
	s.mu.Unlock()

	go func() {
		defer func() {
			// 捕获 panic，防止 goroutine 崩溃导致任务永久卡死
			if r := recover(); r != nil {
				log.Error("后台任务 goroutine panic",
					zap.Uint("task_id", task.ID),
					zap.Any("panic", r))
				now := time.Now()
				s.db.Model(&model.BackgroundTask{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
					"status":           model.TaskStatusFailed,
					"error_message":    fmt.Sprintf("任务执行异常: %v", r),
					"progress_message": "任务执行异常，请查看日志",
					"completed_at":     now,
				})
			}
			s.mu.Lock()
			delete(s.runningTasks, task.ID)
			s.mu.Unlock()
			cancel()
		}()
		s.executeTask(ctx, task, operatorID)
	}()

	log.Info("后台任务已创建",
		zap.Uint("task_id", task.ID),
		zap.String("type", taskType))

	return task, nil
}

// executeTask 执行具体任务
func (s *TaskService) executeTask(ctx context.Context, task *model.BackgroundTask, operatorID uint) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	// 标记为运行中
	now := time.Now()
	task.Status = model.TaskStatusRunning
	task.StartedAt = &now
	s.db.Model(&model.BackgroundTask{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"status":     model.TaskStatusRunning,
		"started_at": now,
	})

	var result interface{}
	var err error

	switch task.TaskType {
	case model.TaskTypeModelSync:
		result, err = s.runModelSync(ctx, task, operatorID)
	case model.TaskTypeModelCheck:
		result, err = s.runModelCheck(ctx, task)
	case model.TaskTypePriceScrape:
		result, err = s.runPriceScrape(ctx, task)
	default:
		err = fmt.Errorf("未知任务类型: %s", task.TaskType)
	}

	// 更新最终状态
	completedAt := time.Now()
	updates := map[string]interface{}{
		"completed_at": completedAt,
		"progress":     100,
	}

	if err != nil {
		updates["status"] = model.TaskStatusFailed
		updates["error_message"] = err.Error()
		updates["progress_message"] = "任务失败: " + err.Error()
		log.Error("后台任务失败",
			zap.Uint("task_id", task.ID),
			zap.String("type", task.TaskType),
			zap.Error(err))
	} else {
		updates["status"] = model.TaskStatusCompleted
		updates["progress_message"] = "任务完成"
		if result != nil {
			resultJSON, _ := json.Marshal(result)
			updates["result"] = string(resultJSON)
		}
		log.Info("后台任务完成",
			zap.Uint("task_id", task.ID),
			zap.String("type", task.TaskType),
			zap.Duration("duration", completedAt.Sub(*task.StartedAt)))
	}

	s.db.Model(&model.BackgroundTask{}).Where("id = ?", task.ID).Updates(updates)
}

// updateProgress 更新任务进度
func (s *TaskService) updateProgress(task *model.BackgroundTask, progress int, message string) {
	s.db.Model(&model.BackgroundTask{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"progress":         progress,
		"progress_message": message,
	})
}

// ─── 模型同步 ───

func (s *TaskService) runModelSync(ctx context.Context, task *model.BackgroundTask, operatorID uint) (interface{}, error) {
	s.updateProgress(task, 10, "正在同步所有活跃渠道...")

	result, err := s.discoveryService.SyncAllActive()
	if err != nil {
		return nil, err
	}

	s.updateProgress(task, 60, fmt.Sprintf("同步完成，共%d个渠道，正在检测新增模型...", result.Total))

	// 收集新增模型 ID
	var newModelIDs []uint
	totalAdded := 0
	totalFound := 0
	for _, r := range result.Results {
		newModelIDs = append(newModelIDs, r.NewModelIDs...)
		totalAdded += r.ModelsAdded
		totalFound += r.ModelsFound
	}

	resp := map[string]interface{}{
		"results":       result.Results,
		"total":         result.Total,
		"models_found":  totalFound,
		"models_added":  totalAdded,
	}

	// 增量检测新增模型
	if len(newModelIDs) > 0 {
		s.updateProgress(task, 70, fmt.Sprintf("正在检测 %d 个新增模型...", len(newModelIDs)))
		checkResults, checkErr := s.modelChecker.CheckByIDs(ctx, newModelIDs, nil)
		if checkErr == nil && len(checkResults) > 0 {
			available := 0
			for _, cr := range checkResults {
				if cr.Available {
					available++
				}
			}
			resp["models_checked"] = len(checkResults)
			resp["models_available"] = available
			resp["models_unavailable"] = len(checkResults) - available
		}
	}

	// 写入审计日志
	details, _ := json.Marshal(resp)
	auditLog := &model.AuditLog{
		UserID:     operatorID,
		OperatorID: operatorID,
		Action:     "SYNC",
		Resource:   "MODEL",
		Details:    details,
		Remark:     fmt.Sprintf("同步%d个渠道，发现%d个模型，新增%d个", result.Total, totalFound, totalAdded),
	}
	_ = s.auditService.Create(ctx, auditLog)

	return resp, nil
}

// ─── 模型检测 ───

func (s *TaskService) runModelCheck(ctx context.Context, task *model.BackgroundTask) (interface{}, error) {
	s.updateProgress(task, 10, "正在加载在线模型列表...")

	progressCh := make(chan aimodel.BatchCheckProgress, 100)

	// 在子 goroutine 中监听进度
	go func() {
		for p := range progressCh {
			pct := 10
			if p.Total > 0 {
				pct = 10 + int(float64(p.Checked)/float64(p.Total)*85)
			}
			msg := fmt.Sprintf("已检测 %d/%d，可用 %d，失败 %d", p.Checked, p.Total, p.Available, p.Failed)
			s.updateProgress(task, pct, msg)
		}
	}()

	results, err := s.modelChecker.BatchCheck(ctx, progressCh)
	if err != nil {
		return nil, err
	}

	available := 0
	failed := 0
	disabled := 0
	for _, r := range results {
		if r.Available {
			available++
		} else {
			failed++
		}
		if r.AutoDisabled {
			disabled++
		}
	}

	return map[string]interface{}{
		"total":     len(results),
		"available": available,
		"failed":    failed,
		"disabled":  disabled,
	}, nil
}

// ─── 价格抓取 ───

func (s *TaskService) runPriceScrape(ctx context.Context, task *model.BackgroundTask) (interface{}, error) {
	// 解析参数
	var params struct {
		SupplierID uint `json:"supplier_id"`
	}
	if err := json.Unmarshal([]byte(task.Params), &params); err != nil {
		return nil, fmt.Errorf("解析参数失败: %w", err)
	}
	if params.SupplierID == 0 {
		return nil, fmt.Errorf("supplier_id 不能为空")
	}

	s.updateProgress(task, 10, "正在爬取供应商价格...")

	result, err := s.scraperService.ScrapeAndPreview(ctx, params.SupplierID)
	if err != nil {
		return nil, err
	}

	s.updateProgress(task, 90, fmt.Sprintf("爬取完成，共 %d 个模型，%d 个有价格变更", result.TotalModels, result.ChangedCount))

	return result, nil
}

// ─── 查询接口 ───

// GetTask 获取单个任务详情
func (s *TaskService) GetTask(id uint) (*model.BackgroundTask, error) {
	var task model.BackgroundTask
	if err := s.db.First(&task, id).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

// ListTasks 分页查询任务列表
func (s *TaskService) ListTasks(taskType string, page, pageSize int) ([]model.BackgroundTask, int64, error) {
	query := s.db.Model(&model.BackgroundTask{})
	if taskType != "" {
		query = query.Where("task_type = ?", taskType)
	}

	var total int64
	query.Count(&total)

	var tasks []model.BackgroundTask
	offset := (page - 1) * pageSize
	err := query.Order("id DESC").Offset(offset).Limit(pageSize).Find(&tasks).Error
	return tasks, total, err
}

// ApplyTaskPrices 从已完成的价格抓取任务中应用价格更新
// 解析任务结果中的 PriceDiffResult，过滤指定模型，调用 scraperService.ApplyPrices
func (s *TaskService) ApplyTaskPrices(taskID uint, modelIDs []uint) (*pricescraper.ApplyResult, error) {
	// 1. 加载任务
	var task model.BackgroundTask
	if err := s.db.First(&task, taskID).Error; err != nil {
		return nil, fmt.Errorf("任务不存在 (ID=%d): %w", taskID, err)
	}

	// 2. 校验任务类型和状态
	if task.TaskType != model.TaskTypePriceScrape {
		return nil, fmt.Errorf("任务类型不是价格抓取 (当前: %s)", task.TaskType)
	}
	if task.Status != model.TaskStatusCompleted {
		return nil, fmt.Errorf("任务未完成 (当前状态: %s)", task.Status)
	}
	if task.Result == "" {
		return nil, fmt.Errorf("任务结果为空")
	}

	// 3. 解析任务结果中的 PriceDiffResult
	var diffResult pricescraper.PriceDiffResult
	if err := json.Unmarshal([]byte(task.Result), &diffResult); err != nil {
		return nil, fmt.Errorf("解析任务结果失败: %w", err)
	}

	// 4. 构建 modelIDs 过滤集合
	filterSet := make(map[uint]bool, len(modelIDs))
	for _, mid := range modelIDs {
		filterSet[mid] = true
	}

	// 5. 从差异项构建更新请求，按需过滤
	var updates []pricescraper.PriceUpdateRequest
	for _, item := range diffResult.Items {
		if item.ModelID == 0 || !item.HasChanges {
			continue
		}
		// 如果指定了 modelIDs，只应用指定的模型
		if len(filterSet) > 0 && !filterSet[item.ModelID] {
			continue
		}
		updates = append(updates, pricescraper.PriceUpdateRequest{
			ModelID:       item.ModelID,
			InputCostRMB:  item.ActualInputRMB,
			OutputCostRMB: item.ActualOutputRMB,
			PriceTiers:    item.PriceTiers,
			// v3.5：透传元数据字段，让 ApplyPrices 能正确更新 pricing_unit/model_type
			// 并据此判断缓存类型过滤（LLM/VLM 才能启用 cache）
			PricingUnit:                item.PricingUnit,
			ModelType:                  item.ModelType,
			SupportsCache:              item.SupportsCache,
			CacheMechanism:             item.CacheMechanism,
			CacheMinTokens:             item.CacheMinTokens,
			CacheInputPriceRMB:         item.CacheInputPriceRMB,
			CacheExplicitInputPriceRMB: item.CacheExplicitInputPriceRMB,
			CacheWritePriceRMB:         item.CacheWritePriceRMB,
			CacheStoragePriceRMB:       item.CacheStoragePriceRMB,
			VideoPricingConfig:         item.VideoPricingConfig,
		})
	}

	if len(updates) == 0 {
		return &pricescraper.ApplyResult{
			UpdatedCount: 0,
			SkippedCount: 0,
		}, nil
	}

	// 6. 调用爬虫服务应用价格
	return s.scraperService.ApplyPrices(context.Background(), diffResult.SupplierID, updates)
}

// CancelTask 取消运行中的任务
func (s *TaskService) CancelTask(id uint) error {
	s.mu.Lock()
	cancel, ok := s.runningTasks[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("任务不在运行中")
	}
	cancel()
	s.db.Model(&model.BackgroundTask{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":           model.TaskStatusFailed,
		"error_message":    "任务被手动取消",
		"progress_message": "已取消",
		"completed_at":     time.Now(),
	})
	return nil
}
