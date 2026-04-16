package channel

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// RouteRefreshJob 表示一次默认渠道路由刷新任务的状态快照
type RouteRefreshJob struct {
	ID         string               `json:"id"`
	Status     string               `json:"status"` // running/success/failed
	StartedAt  time.Time            `json:"started_at"`
	FinishedAt *time.Time           `json:"finished_at,omitempty"`
	Steps      []JobStep            `json:"steps"`
	Summary    *RouteRefreshSummary `json:"summary,omitempty"`
	Error      string               `json:"error,omitempty"`
	TriggerBy  string               `json:"trigger_by,omitempty"` // manual/cron
	mu         sync.Mutex
}

// JobStep 一条步骤日志
type JobStep struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"` // info/warn/error
	Message string    `json:"message"`
}

// RouteRefreshSummary 刷新结果摘要
type RouteRefreshSummary struct {
	TotalModels     int      `json:"total_models"`     // 涉及的标准模型数
	TotalRoutes     int      `json:"total_routes"`     // 生成的路由条数
	ChannelsScanned int      `json:"channels_scanned"` // 扫描的 channel_models 数
	NewModels       []string `json:"new_models"`       // 本次新增 alias（相比上次）
	RemovedModels   []string `json:"removed_models"`   // 本次移除 alias
	DurationMs      int64    `json:"duration_ms"`
}

// NewRouteRefreshJob 构造一个新 Job（running 状态）
func NewRouteRefreshJob(triggerBy string) *RouteRefreshJob {
	return &RouteRefreshJob{
		ID:        uuid.NewString(),
		Status:    "running",
		StartedAt: time.Now(),
		Steps:     make([]JobStep, 0, 16),
		TriggerBy: triggerBy,
	}
}

// AddStep 追加一条步骤日志（线程安全）
func (j *RouteRefreshJob) AddStep(level, message string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Steps = append(j.Steps, JobStep{Time: time.Now(), Level: level, Message: message})
}

// Info/Warn/Error 快捷方法
func (j *RouteRefreshJob) Info(msg string)  { j.AddStep("info", msg) }
func (j *RouteRefreshJob) Warn(msg string)  { j.AddStep("warn", msg) }
func (j *RouteRefreshJob) Errorf(msg string) { j.AddStep("error", msg) }

// Complete 标记完成
func (j *RouteRefreshJob) Complete(summary *RouteRefreshSummary) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	j.Status = "success"
	j.FinishedAt = &now
	if summary != nil {
		summary.DurationMs = now.Sub(j.StartedAt).Milliseconds()
		j.Summary = summary
	}
}

// Fail 标记失败
func (j *RouteRefreshJob) Fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	j.Status = "failed"
	j.FinishedAt = &now
	if err != nil {
		j.Error = err.Error()
	}
}

// Snapshot 返回一份线程安全的拷贝（用于 JSON 序列化）
func (j *RouteRefreshJob) Snapshot() RouteRefreshJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	steps := make([]JobStep, len(j.Steps))
	copy(steps, j.Steps)
	cp := RouteRefreshJob{
		ID:         j.ID,
		Status:     j.Status,
		StartedAt:  j.StartedAt,
		FinishedAt: j.FinishedAt,
		Steps:      steps,
		Summary:    j.Summary,
		Error:      j.Error,
		TriggerBy:  j.TriggerBy,
	}
	return cp
}

// RefreshDefaultRoutes 核心刷新逻辑：重新生成默认渠道的路由规则
// 该函数同时被 admin handler（手动触发）和 cron（定时巡检）调用
func RefreshDefaultRoutes(ctx context.Context, db *gorm.DB, rds *goredis.Client, job *RouteRefreshJob) error {
	if job == nil {
		return fmt.Errorf("job is nil")
	}
	job.Info("开始刷新默认渠道路由")

	// 1. 查找默认渠道
	var defaultCC model.CustomChannel
	if err := db.WithContext(ctx).Where("is_default = ?", true).First(&defaultCC).Error; err != nil {
		job.Errorf(fmt.Sprintf("未找到默认渠道: %v", err))
		return fmt.Errorf("未找到默认渠道: %w", err)
	}
	job.Info(fmt.Sprintf("定位默认渠道 ID=%d name=%q", defaultCC.ID, defaultCC.Name))

	// 2. 查询所有已激活的接入点及其供应商/模型映射
	var channelModels []model.ChannelModel
	if err := db.WithContext(ctx).
		Joins("JOIN channels ON channels.id = channel_models.channel_id").
		Joins("JOIN suppliers ON suppliers.id = channels.supplier_id").
		Where("channels.status = ? AND channel_models.is_active = ?", "active", true).
		Preload("Channel").
		Preload("Channel.Supplier").
		Find(&channelModels).Error; err != nil {
		job.Errorf(fmt.Sprintf("查询 channel_models 失败: %v", err))
		return fmt.Errorf("查询 channel_models: %w", err)
	}
	if len(channelModels) == 0 {
		job.Errorf("没有可用的接入点模型映射（请先启用渠道和模型）")
		return fmt.Errorf("没有可用的接入点模型映射")
	}
	// 统计活跃供应商数
	supplierSet := map[uint]struct{}{}
	for _, cm := range channelModels {
		supplierSet[cm.Channel.SupplierID] = struct{}{}
	}
	job.Info(fmt.Sprintf("扫描完成：共 %d 条 channel_models，%d 个供应商", len(channelModels), len(supplierSet)))

	// 3. 按 standard_model_id 聚合，计算每条候选路由的综合成本
	type costEntry struct {
		ChannelModel model.ChannelModel
		Cost         float64
	}
	modelCandidates := make(map[string][]costEntry)
	for _, cm := range channelModels {
		supplier := cm.Channel.Supplier
		cost := (supplier.InputPricePerM + supplier.OutputPricePerM) * supplier.Discount
		modelCandidates[cm.StandardModelID] = append(modelCandidates[cm.StandardModelID], costEntry{
			ChannelModel: cm,
			Cost:         cost,
		})
	}
	job.Info(fmt.Sprintf("聚合完成：共 %d 个标准模型", len(modelCandidates)))

	// 4. 生成路由规则（成本倒数分配 weight；成本最低 priority=10）
	var newRoutes []model.CustomChannelRoute
	for aliasModel, candidates := range modelCandidates {
		if len(candidates) == 0 {
			continue
		}
		minCost := math.MaxFloat64
		for _, ce := range candidates {
			if ce.Cost > 0 && ce.Cost < minCost {
				minCost = ce.Cost
			}
		}
		if minCost <= 0 || minCost == math.MaxFloat64 {
			minCost = 1.0
		}
		for _, ce := range candidates {
			weight := 100
			priority := 0
			if ce.Cost > 0 {
				ratio := minCost / ce.Cost
				weight = int(math.Round(ratio * 100))
				if weight < 1 {
					weight = 1
				}
				if weight >= 100 {
					priority = 10
				}
			}
			newRoutes = append(newRoutes, model.CustomChannelRoute{
				CustomChannelID: defaultCC.ID,
				AliasModel:      aliasModel,
				ChannelID:       ce.ChannelModel.ChannelID,
				ActualModel:     ce.ChannelModel.VendorModelID,
				Weight:          weight,
				Priority:        priority,
				IsActive:        true,
			})
		}
	}
	job.Info(fmt.Sprintf("生成路由规则 %d 条", len(newRoutes)))

	// 5. 读取旧 alias 快照（用于 diff）
	var oldAliases []string
	if err := db.WithContext(ctx).Model(&model.CustomChannelRoute{}).
		Where("custom_channel_id = ?", defaultCC.ID).
		Distinct("alias_model").
		Pluck("alias_model", &oldAliases).Error; err != nil {
		job.Warn(fmt.Sprintf("读取旧路由 alias 失败（将跳过 diff）: %v", err))
		oldAliases = nil
	}
	oldSet := make(map[string]struct{}, len(oldAliases))
	for _, a := range oldAliases {
		oldSet[a] = struct{}{}
	}
	newSet := make(map[string]struct{}, len(modelCandidates))
	for a := range modelCandidates {
		newSet[a] = struct{}{}
	}
	var added, removed []string
	for a := range newSet {
		if _, ok := oldSet[a]; !ok {
			added = append(added, a)
		}
	}
	for a := range oldSet {
		if _, ok := newSet[a]; !ok {
			removed = append(removed, a)
		}
	}
	job.Info(fmt.Sprintf("diff 完成：新增 %d 个 alias，移除 %d 个 alias", len(added), len(removed)))

	// 6. 事务：清空旧路由 + 批量写入新路由
	txErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("custom_channel_id = ?", defaultCC.ID).
			Delete(&model.CustomChannelRoute{}).Error; err != nil {
			return fmt.Errorf("删除旧路由失败: %w", err)
		}
		job.Info("已清空旧路由")
		if len(newRoutes) > 0 {
			if err := tx.CreateInBatches(newRoutes, 200).Error; err != nil {
				return fmt.Errorf("批量写入新路由失败: %w", err)
			}
		}
		job.Info(fmt.Sprintf("批量写入新路由 %d 条（分批大小 200）", len(newRoutes)))
		return nil
	})
	if txErr != nil {
		job.Errorf(txErr.Error())
		return txErr
	}

	// 7. 清理相关 Redis 缓存
	if rds != nil {
		cleaned := clearRouteCaches(ctx, rds, defaultCC.ID)
		job.Info(fmt.Sprintf("Redis 缓存清理完成：删除 %d 个 key", cleaned))
	} else {
		job.Warn("Redis 未配置，跳过缓存清理")
	}

	// 8. 完成
	job.Complete(&RouteRefreshSummary{
		TotalModels:     len(modelCandidates),
		TotalRoutes:     len(newRoutes),
		ChannelsScanned: len(channelModels),
		NewModels:       added,
		RemovedModels:   removed,
	})
	zap.L().Info("默认渠道路由刷新完成",
		zap.String("trigger", job.TriggerBy),
		zap.Int("total_routes", len(newRoutes)),
		zap.Int("new_aliases", len(added)),
		zap.Int("removed_aliases", len(removed)),
	)
	return nil
}

// clearRouteCaches 清理所有与默认渠道路由相关的 Redis 缓存
// 返回删除的 key 数量
func clearRouteCaches(ctx context.Context, rds *goredis.Client, ccID uint) int {
	var total int
	// 1. 精确 key
	exactKeys := []string{
		"custom_channel:default",
		fmt.Sprintf("custom_channel:id:%d", ccID),
	}
	for _, k := range exactKeys {
		if n, err := rds.Del(ctx, k).Result(); err == nil {
			total += int(n)
		}
	}
	// 2. 通配符 key（使用 SCAN，避免阻塞）
	patterns := []string{
		fmt.Sprintf("custom_channel_routes:%d:*", ccID),
		"cache:/api/v1/public/models*",
	}
	for _, pat := range patterns {
		iter := rds.Scan(ctx, 0, pat, 100).Iterator()
		for iter.Next(ctx) {
			if n, err := rds.Del(ctx, iter.Val()).Result(); err == nil {
				total += int(n)
			}
		}
	}
	return total
}
