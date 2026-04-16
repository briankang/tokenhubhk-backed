package cron

import (
	"context"
	"fmt"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/service/balance"
	channelsvc "tokenhub-server/internal/service/channel"
	"tokenhub-server/internal/service/member"
	"tokenhub-server/internal/service/modeldiscovery"
	"tokenhub-server/internal/service/pricescraper"
)

// shanghaiLoc 上海时区，所有定时任务均使用该时区计算执行时间
var shanghaiLoc *time.Location

func init() {
	var err error
	shanghaiLoc, err = time.LoadLocation("Asia/Shanghai")
	if err != nil {
		// 如果时区文件不存在，使用固定偏移 UTC+8
		shanghaiLoc = time.FixedZone("CST", 8*3600)
	}
}

// Scheduler 定时任务调度器，管理所有周期性后台任务
type Scheduler struct {
	memberSvc    *member.MemberLevelService
	balanceSvc   *balance.BalanceService
	discoverySvc *modeldiscovery.DiscoveryService
	scraperSvc   *pricescraper.PriceScraperService
	db           *gorm.DB
	redis        *goredis.Client
	stopCh       chan struct{} // 停止信号通道

	// 任务状态管理
	tasksMu sync.RWMutex
	tasks   map[string]*TaskInfo
}

// TaskInfo 定时任务信息（对外暴露）
type TaskInfo struct {
	Name     string    `json:"name"`
	Schedule string    `json:"schedule"` // 执行频率描述
	Enabled  bool      `json:"enabled"`
	LastRun  time.Time `json:"last_run,omitempty"`
	LastErr  string    `json:"last_error,omitempty"`
}

// NewScheduler 创建定时任务调度器实例
func NewScheduler(db *gorm.DB, redis *goredis.Client, memberSvc *member.MemberLevelService, balanceSvc *balance.BalanceService, opts ...SchedulerOption) *Scheduler {
	s := &Scheduler{
		memberSvc:  memberSvc,
		balanceSvc: balanceSvc,
		db:         db,
		redis:      redis,
		stopCh:     make(chan struct{}),
		tasks:      make(map[string]*TaskInfo),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// SchedulerOption 调度器可选配置
type SchedulerOption func(*Scheduler)

// WithDiscoveryService 注入模型发现服务
func WithDiscoveryService(svc *modeldiscovery.DiscoveryService) SchedulerOption {
	return func(s *Scheduler) { s.discoverySvc = svc }
}

// WithPriceScraperService 注入价格爬虫服务
func WithPriceScraperService(svc *pricescraper.PriceScraperService) SchedulerOption {
	return func(s *Scheduler) { s.scraperSvc = svc }
}

// Start 启动所有定时任务 goroutine
func (s *Scheduler) Start() {
	// 注册所有任务到状态表
	s.registerTask("frozen_release", "每小时: 冻结超时释放", true)
	s.registerTask("balance_reconcile", "每小时: 余额对账检查", true)
	s.registerTask("commission_settle", "每日06:00: 佣金自动结算", true)
	s.registerTask("model_sync", "每日07:00: 模型自动同步", s.discoverySvc != nil)
	s.registerTask("route_refresh", "每日08:00: 默认渠道路由巡检", true)
	s.registerTask("price_update", "每周日03:00: 价格自动更新", s.scraperSvc != nil)
	s.registerTask("consume_rotate", "每月1号: 月消费轮转", true)
	s.registerTask("member_degrade", "每月1号: 会员降级检查", true)
	s.registerTask("logs_cleanup", "每日04:00: 清理7天前调用日志", true)
	s.registerTask("inflight_reset", "每5分钟: 重置渠道在途请求计数", true)

	// 启动每小时任务（冻结超时释放 + 对账）
	go s.runEveryHour()
	// 启动每日任务（佣金自动结算 + 模型自动同步）
	go s.runDaily()
	// 启动每周任务（价格自动更新）
	go s.runWeekly()
	// 启动每月任务（会员赠送/降级检查/代理升降级/月消费轮转/销售额重置）
	go s.runMonthly()
	// 启动日志清理任务（每日04:00）
	go s.runLogsCleanup()
	// 启动在途请求计数重置（防进程崩溃残留）
	go s.runInflightReset()

	zap.L().Info("定时任务调度器已启动",
		zap.String("timezone", shanghaiLoc.String()),
		zap.String("hourly", "每小时整点: 冻结超时释放+对账检查"),
		zap.String("daily", "每日06:00: 佣金结算, 07:00: 模型同步, 08:00: 路由巡检"),
		zap.String("weekly", "每周日03:00: 价格自动更新"),
		zap.String("monthly", "每月1号00:00-04:00: 轮转/降级/升级/销售额重置"),
	)
}

// registerTask 注册定时任务到状态表
func (s *Scheduler) registerTask(name, schedule string, enabled bool) {
	s.tasksMu.Lock()
	defer s.tasksMu.Unlock()
	s.tasks[name] = &TaskInfo{Name: name, Schedule: schedule, Enabled: enabled}
}

// GetTasks 获取所有定时任务的状态列表
func (s *Scheduler) GetTasks() []TaskInfo {
	s.tasksMu.RLock()
	defer s.tasksMu.RUnlock()
	result := make([]TaskInfo, 0, len(s.tasks))
	for _, t := range s.tasks {
		result = append(result, *t)
	}
	return result
}

// SetTaskEnabled 启用/禁用指定任务
func (s *Scheduler) SetTaskEnabled(name string, enabled bool) error {
	s.tasksMu.Lock()
	defer s.tasksMu.Unlock()
	t, ok := s.tasks[name]
	if !ok {
		return fmt.Errorf("任务 %s 不存在", name)
	}
	t.Enabled = enabled
	zap.L().Info("定时任务状态变更", zap.String("task", name), zap.Bool("enabled", enabled))
	return nil
}

// IsTaskEnabled 检查任务是否启用
func (s *Scheduler) IsTaskEnabled(name string) bool {
	s.tasksMu.RLock()
	defer s.tasksMu.RUnlock()
	t, ok := s.tasks[name]
	return ok && t.Enabled
}

// updateTaskRun 更新任务最后执行时间和错误
func (s *Scheduler) updateTaskRun(name string, err error) {
	s.tasksMu.Lock()
	defer s.tasksMu.Unlock()
	if t, ok := s.tasks[name]; ok {
		t.LastRun = time.Now()
		if err != nil {
			t.LastErr = err.Error()
		} else {
			t.LastErr = ""
		}
	}
}

// Stop 优雅停止所有定时任务
func (s *Scheduler) Stop() {
	close(s.stopCh)
	zap.L().Info("定时任务调度器已停止")
}

// ==================== 每小时任务 ====================

// runEveryHour 每小时整点执行一次任务
func (s *Scheduler) runEveryHour() {
	// 计算到下一个整点的等待时间
	now := time.Now().In(shanghaiLoc)
	next := now.Truncate(time.Hour).Add(time.Hour)
	waitDuration := next.Sub(now)

	zap.L().Info("每小时任务首次执行等待中", zap.Duration("wait", waitDuration))

	select {
	case <-time.After(waitDuration):
	case <-s.stopCh:
		return
	}

	// 首次执行
	s.safeRunNamed("frozen_release", "冻结超时释放", func(ctx context.Context) error {
		return s.releaseFrozenRecords(ctx)
	})
	s.safeRunNamed("balance_reconcile", "余额对账检查", func(ctx context.Context) error {
		return s.reconcileBalances(ctx)
	})

	// 之后每小时执行
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.safeRunNamed("frozen_release", "冻结超时释放", func(ctx context.Context) error {
				return s.releaseFrozenRecords(ctx)
			})
			s.safeRunNamed("balance_reconcile", "余额对账检查", func(ctx context.Context) error {
				return s.reconcileBalances(ctx)
			})
		case <-s.stopCh:
			return
		}
	}
}

// ==================== 每日任务 ====================

// runDaily 每日06:00（上海时间）执行任务
func (s *Scheduler) runDaily() {
	for {
		// 计算到次日06:00的等待时间
		now := time.Now().In(shanghaiLoc)
		next := time.Date(now.Year(), now.Month(), now.Day(), 6, 0, 0, 0, shanghaiLoc)
		if now.After(next) {
			// 今天06:00已过，等到明天06:00
			next = next.AddDate(0, 0, 1)
		}
		waitDuration := next.Sub(now)

		zap.L().Info("每日任务等待中", zap.Time("next_run", next), zap.Duration("wait", waitDuration))

		select {
		case <-time.After(waitDuration):
		case <-s.stopCh:
			return
		}

		// 06:00 执行佣金自动结算
		s.safeRunNamed("commission_settle", "佣金自动结算", func(ctx context.Context) error {
			return s.settleCommissions(ctx)
		})

		// 07:00 执行模型自动同步（每日）
		if s.discoverySvc != nil {
			// 等待1小时到07:00
			select {
			case <-time.After(time.Hour):
			case <-s.stopCh:
				return
			}
			s.safeRunNamed("model_sync", "模型自动同步", func(ctx context.Context) error {
				result, err := s.discoverySvc.SyncAllActive()
				if err != nil {
					return fmt.Errorf("模型同步失败: %w", err)
				}
				// 汇总统计
				var added, updated, errCount int
				for _, r := range result.Results {
					added += r.ModelsAdded
					updated += r.ModelsUpdated
					errCount += len(r.Errors)
				}
				zap.L().Info("模型自动同步完成",
					zap.Int("total_channels", result.Total),
					zap.Int("new_models", added),
					zap.Int("updated_models", updated),
					zap.Int("errors", errCount))
				return nil
			})
		}

		// 08:00 默认渠道路由巡检：在模型同步之后，确保新模型立即被纳入默认渠道
		// 不依赖 discoverySvc，即使未配置模型同步也应定期重建路由
		{
			select {
			case <-time.After(time.Hour):
			case <-s.stopCh:
				return
			}
			s.safeRunNamed("route_refresh", "默认渠道路由巡检", func(ctx context.Context) error {
				job := channelsvc.NewRouteRefreshJob("cron")
				if err := channelsvc.RefreshDefaultRoutes(ctx, s.db, s.redis, job); err != nil {
					return fmt.Errorf("路由刷新失败: %w", err)
				}
				if job.Summary != nil {
					zap.L().Info("定时路由刷新完成",
						zap.Int("total_models", job.Summary.TotalModels),
						zap.Int("total_routes", job.Summary.TotalRoutes),
						zap.Int("new_aliases", len(job.Summary.NewModels)),
						zap.Int("removed_aliases", len(job.Summary.RemovedModels)),
					)
				}
				return nil
			})
		}
	}
}

// ==================== 每周任务 ====================

// runWeekly 每周日03:00（上海时间）执行价格自动更新
func (s *Scheduler) runWeekly() {
	if s.scraperSvc == nil {
		zap.L().Info("价格爬虫服务未配置，跳过每周价格更新任务")
		return
	}

	for {
		// 计算下一个周日03:00
		now := time.Now().In(shanghaiLoc)
		daysUntilSunday := (7 - int(now.Weekday())) % 7
		if daysUntilSunday == 0 && now.Hour() >= 3 {
			daysUntilSunday = 7 // 今天是周日但03:00已过，等到下周日
		}
		next := time.Date(now.Year(), now.Month(), now.Day()+daysUntilSunday, 3, 0, 0, 0, shanghaiLoc)
		waitDuration := next.Sub(now)

		zap.L().Info("每周价格更新任务等待中", zap.Time("next_run", next), zap.Duration("wait", waitDuration))

		select {
		case <-time.After(waitDuration):
		case <-s.stopCh:
			return
		}

		// 执行价格爬取：遍历所有活跃供应商
		s.safeRunNamed("price_update", "价格自动更新", func(ctx context.Context) error {
			return s.scrapeAllSupplierPrices(ctx)
		})
	}
}

// scrapeAllSupplierPrices 爬取所有活跃供应商的价格并记录差异日志
func (s *Scheduler) scrapeAllSupplierPrices(ctx context.Context) error {
	var suppliers []model.Supplier
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).Find(&suppliers).Error; err != nil {
		return fmt.Errorf("查询活跃供应商失败: %w", err)
	}

	var totalDiffs int
	for _, sup := range suppliers {
		result, err := s.scraperSvc.ScrapeAndPreview(ctx, sup.ID)
		if err != nil {
			zap.L().Warn("供应商价格爬取失败",
				zap.Uint("supplier_id", sup.ID),
				zap.String("supplier_name", sup.Name),
				zap.Error(err))
			continue
		}
		if result != nil && result.ChangedCount > 0 {
			totalDiffs += result.ChangedCount
			zap.L().Info("供应商价格差异",
				zap.Uint("supplier_id", sup.ID),
				zap.String("supplier_name", sup.Name),
				zap.Int("changed_count", result.ChangedCount),
				zap.Int("total_models", result.TotalModels))
		}
	}

	zap.L().Info("每周价格更新完成",
		zap.Int("suppliers_checked", len(suppliers)),
		zap.Int("total_diffs", totalDiffs))
	return nil
}

// ==================== 每月任务 ====================

// runMonthly 每月1号按时间顺序执行各月度任务
// 时间安排：
//   00:00 月消费轮转
//   02:00 会员降级检查
func (s *Scheduler) runMonthly() {
	// 月度任务列表，按执行时间（小时）升序排列
	type monthlyTask struct {
		hour int
		name string
		fn   func(context.Context) error
	}
	tasks := []monthlyTask{
		{0, "月消费轮转", func(ctx context.Context) error { return s.memberSvc.RotateMonthConsume(ctx) }},
		{2, "会员降级检查", func(ctx context.Context) error { return s.memberSvc.CheckAndDegradeAll(ctx) }},
	}

	for {
		// 计算下次每月1号00:00的时间
		now := time.Now().In(shanghaiLoc)
		var nextMonth time.Time
		if now.Day() == 1 {
			// 如果今天就是1号，检查是否还有未执行的任务
			lastTaskHour := tasks[len(tasks)-1].hour
			if now.Hour() <= lastTaskHour {
				// 还有任务未执行，从当前小时继续
				nextMonth = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, shanghaiLoc)
			} else {
				// 所有任务已执行完，等下个月
				nextMonth = time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, shanghaiLoc)
			}
		} else {
			// 非1号，等下个月1号
			nextMonth = time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, shanghaiLoc)
		}

		// 按顺序执行每个月度任务
		for _, task := range tasks {
			taskTime := time.Date(nextMonth.Year(), nextMonth.Month(), 1, task.hour, 0, 0, 0, shanghaiLoc)
			now = time.Now().In(shanghaiLoc)

			if now.After(taskTime) {
				// 已过该任务的执行时间，跳过
				continue
			}

			waitDuration := taskTime.Sub(now)
			zap.L().Info("月度任务等待中", zap.String("task", task.name), zap.Time("next_run", taskTime), zap.Duration("wait", waitDuration))

			select {
			case <-time.After(waitDuration):
			case <-s.stopCh:
				return
			}

			s.safeRun(task.name, task.fn)
		}
	}
}

// ==================== 日志清理任务 ====================

// runLogsCleanup 每日04:00（上海时间）清理7天前的 api_call_logs 与 channel_logs
func (s *Scheduler) runLogsCleanup() {
	for {
		now := time.Now().In(shanghaiLoc)
		next := time.Date(now.Year(), now.Month(), now.Day(), 4, 0, 0, 0, shanghaiLoc)
		if now.After(next) {
			next = next.AddDate(0, 0, 1)
		}
		waitDuration := next.Sub(now)

		zap.L().Info("日志清理任务等待中", zap.Time("next_run", next), zap.Duration("wait", waitDuration))

		select {
		case <-time.After(waitDuration):
		case <-s.stopCh:
			return
		}

		s.safeRunNamed("logs_cleanup", "清理7天前调用日志", func(ctx context.Context) error {
			return s.cleanupOldLogs(ctx)
		})
	}
}

// cleanupOldLogs 分批删除 api_call_logs 与 channel_logs 中 7 天前的记录
// 每批最多 5000 条，循环直到无匹配记录，防止单次大事务锁表
func (s *Scheduler) cleanupOldLogs(ctx context.Context) error {
	cutoff := time.Now().Add(-7 * 24 * time.Hour)

	batchDelete := func(tableName string) (int64, error) {
		var totalDeleted int64
		for {
			select {
			case <-ctx.Done():
				return totalDeleted, ctx.Err()
			default:
			}
			res := s.db.WithContext(ctx).Exec(
				fmt.Sprintf("DELETE FROM %s WHERE created_at < ? LIMIT 5000", tableName), cutoff,
			)
			if res.Error != nil {
				return totalDeleted, res.Error
			}
			totalDeleted += res.RowsAffected
			if res.RowsAffected == 0 {
				return totalDeleted, nil
			}
		}
	}

	apiDeleted, err := batchDelete("api_call_logs")
	if err != nil {
		return fmt.Errorf("清理 api_call_logs 失败（已删 %d 条）: %w", apiDeleted, err)
	}

	channelDeleted, err := batchDelete("channel_logs")
	if err != nil {
		return fmt.Errorf("清理 channel_logs 失败（已删 %d 条）: %w", channelDeleted, err)
	}

	zap.L().Info("日志清理完成",
		zap.Time("cutoff", cutoff),
		zap.Int64("api_call_logs_deleted", apiDeleted),
		zap.Int64("channel_logs_deleted", channelDeleted),
	)
	return nil
}

// ==================== 在途请求计数重置 ====================

// runInflightReset 每5分钟重置渠道在途请求计数器
// 防止 Pod 崩溃后 Redis HINCRBY 计数不归零导致负载路由偏差
func (s *Scheduler) runInflightReset() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.safeRunNamed("inflight_reset", "重置在途请求计数", func(ctx context.Context) error {
				if s.redis == nil {
					return nil
				}
				deleted, err := s.redis.Del(ctx, "channel:inflight", "stream:active:global").Result()
				if err != nil {
					return fmt.Errorf("重置计数失败: %w", err)
				}
				if deleted > 0 {
					zap.L().Debug("在途请求计数已重置", zap.Int64("keys_deleted", deleted))
				}
				return nil
			})
		case <-s.stopCh:
			return
		}
	}
}

// ==================== 业务逻辑 ====================

// settleCommissions 自动结算超过7天的PENDING佣金
// 逻辑：
//   1. 查询所有 status=PENDING 且 created_at < 7天前 的 CommissionRecord
//   2. 批量更新 status 为 SETTLED
//   3. 记录结算数量日志
func (s *Scheduler) settleCommissions(ctx context.Context) error {
	// 7天前的时间点
	sevenDaysAgo := time.Now().Add(-7 * 24 * time.Hour)

	result := s.db.WithContext(ctx).
		Model(&model.CommissionRecord{}).
		Where("status = ? AND created_at < ?", "PENDING", sevenDaysAgo).
		Update("status", "SETTLED")

	if result.Error != nil {
		return fmt.Errorf("佣金结算失败: %w", result.Error)
	}

	if result.RowsAffected > 0 {
		zap.L().Info("佣金自动结算完成", zap.Int64("settled_count", result.RowsAffected))
	}

	return nil
}

// releaseFrozenRecords 释放超时的冻结记录
// 逻辑：
//   1. 查询所有 status=FROZEN 且 created_at < 1小时前 的 FreezeRecord
//   2. 对每条记录：减少 UserBalance.frozen_amount，归还冻结金额到余额
//   3. 更新 FreezeRecord.status 为 RELEASED
//   4. 使用事务确保一致性
func (s *Scheduler) releaseFrozenRecords(ctx context.Context) error {
	// 5分钟前的时间点（与 balance_service.go 中的 freezeExpiry 一致）
	fiveMinutesAgo := time.Now().Add(-5 * time.Minute)

	// 查询所有超时冻结记录
	var records []model.FreezeRecord
	if err := s.db.WithContext(ctx).
		Where("status = ? AND created_at < ?", "FROZEN", fiveMinutesAgo).
		Find(&records).Error; err != nil {
		return fmt.Errorf("查询超时冻结记录失败: %w", err)
	}

	if len(records) == 0 {
		return nil // 无超时记录
	}

	releasedCount := 0
	for _, record := range records {
		// 每条记录独立事务，单条失败不影响其他记录
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// 减少用户冻结金额（积分）
			if err := tx.Model(&model.UserBalance{}).
				Where("user_id = ?", record.UserID).
				UpdateColumn("frozen_amount", gorm.Expr("GREATEST(frozen_amount - ?, 0)", record.FrozenAmount)).
				Error; err != nil {
				return fmt.Errorf("减少冻结金额失败: %w", err)
			}

			// 更新冻结记录状态为 RELEASED
			if err := tx.Model(&record).
				Update("status", "RELEASED").Error; err != nil {
				return fmt.Errorf("更新冻结记录状态失败: %w", err)
			}

			return nil
		})

		if err != nil {
			zap.L().Error("释放冻结记录失败",
				zap.String("freeze_id", record.FreezeID),
				zap.Uint("user_id", record.UserID),
				zap.Error(err),
			)
			continue
		}
		releasedCount++
	}

	if releasedCount > 0 {
		zap.L().Info("冻结超时释放完成", zap.Int("released_count", releasedCount), zap.Int("total_expired", len(records)))
	}

	return nil
}

// reconcileBalances 余额对账检查
// 检查内容：
// 1. 清理过期冻结记录
// 2. 检查余额一致性（不自动修复，仅记录日志）
func (s *Scheduler) reconcileBalances(ctx context.Context) error {
	// 1. 清理过期冻结记录
	if s.balanceSvc != nil {
		cleaned, err := s.balanceSvc.CleanExpiredFreezes(ctx)
		if err != nil {
			zap.L().Error("清理过期冻结记录失败", zap.Error(err))
		} else if cleaned > 0 {
			zap.L().Info("清理过期冻结记录完成", zap.Int("count", cleaned))
		}
	}

	// 2. 检查余额一致性
	// 逻辑：对于每个用户，检查 balance + free_quota - frozen_amount >= 0
	// 以及 total_consumed 是否合理
	var inconsistencies []struct {
		UserID    uint
		Balance   int64
		FreeQuota int64
		Frozen    int64
	}
	
	// 查找余额为负的用户（不应该发生）
	err := s.db.WithContext(ctx).
		Model(&model.UserBalance{}).
		Where("balance < 0 OR free_quota < 0 OR frozen_amount < 0").
		Find(&inconsistencies).Error
	if err != nil {
		return fmt.Errorf("检查余额一致性失败: %w", err)
	}

	if len(inconsistencies) > 0 {
		for _, inc := range inconsistencies {
			zap.L().Warn("余额不一致警告",
				zap.Uint("user_id", inc.UserID),
				zap.Int64("balance", inc.Balance),
				zap.Int64("free_quota", inc.FreeQuota),
				zap.Int64("frozen", inc.Frozen))
		}
	}

	// 3. 统计总充值和总消费，检查平台级一致性
	var totalRecharge int64
	var totalConsumed int64
	
	s.db.WithContext(ctx).Model(&model.BalanceRecord{}).
		Where("type = 'RECHARGE'").
		Select("COALESCE(SUM(amount), 0)").Scan(&totalRecharge)
	
	s.db.WithContext(ctx).Model(&model.BalanceRecord{}).
		Where("type = 'CONSUME'").
		Select("COALESCE(SUM(ABS(amount)), 0)").Scan(&totalConsumed)

	var totalBalance int64
	var totalFreeQuota int64
	var totalFrozen int64
	
	s.db.WithContext(ctx).Model(&model.UserBalance{}).
		Select("COALESCE(SUM(balance), 0)").Scan(&totalBalance)
	s.db.WithContext(ctx).Model(&model.UserBalance{}).
		Select("COALESCE(SUM(free_quota), 0)").Scan(&totalFreeQuota)
	s.db.WithContext(ctx).Model(&model.UserBalance{}).
		Select("COALESCE(SUM(frozen_amount), 0)").Scan(&totalFrozen)

	// 计算：总余额 + 总冻结 + 总赠送 = 总充值 - 总消费 + 初始赠送
	// 这里只是简单记录，不做严格校验
	zap.L().Info("余额对账统计",
		zap.Int64("total_recharge", totalRecharge),
		zap.Int64("total_consumed", totalConsumed),
		zap.Int64("total_balance", totalBalance),
		zap.Int64("total_free_quota", totalFreeQuota),
		zap.Int64("total_frozen", totalFrozen),
		zap.Float64("total_balance_rmb", credits.CreditsToRMB(totalBalance)),
	)

	return nil
}

// ==================== 工具方法 ====================

// safeRun 安全执行定时任务，捕获 panic 防止 goroutine 退出
func (s *Scheduler) safeRun(taskName string, fn func(context.Context) error) {
	s.safeRunNamed("", taskName, fn)
}

// safeRunNamed 安全执行定时任务（带任务ID检查），检查任务是否启用
func (s *Scheduler) safeRunNamed(taskID, taskName string, fn func(context.Context) error) {
	// 检查任务是否启用（如果有 taskID）
	if taskID != "" && !s.IsTaskEnabled(taskID) {
		zap.L().Debug("定时任务已禁用，跳过", zap.String("task", taskName), zap.String("task_id", taskID))
		return
	}

	defer func() {
		if r := recover(); r != nil {
			zap.L().Error("定时任务 panic 已恢复",
				zap.String("task", taskName),
				zap.Any("panic", r),
			)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// 分布式锁：同一时刻只有一个进程/Pod 执行此任务
	// 支持多 Worker 副本部署（K8s 水平扩容安全）
	if s.redis != nil {
		lockKey := "cron:" + taskName
		lock, err := pkgredis.Lock(ctx, lockKey, 10*time.Minute)
		if err != nil {
			// 另一个实例已在执行，静默跳过
			zap.L().Debug("定时任务已被其他实例锁定，跳过",
				zap.String("task", taskName),
			)
			return
		}
		defer lock.Unlock(ctx)
	}

	zap.L().Info("定时任务开始执行", zap.String("task", taskName))
	start := time.Now()

	err := fn(ctx)
	if taskID != "" {
		s.updateTaskRun(taskID, err)
	}

	if err != nil {
		zap.L().Error("定时任务执行失败",
			zap.String("task", taskName),
			zap.Error(err),
			zap.Duration("duration", time.Since(start)),
		)
		return
	}

	zap.L().Info("定时任务执行完成",
		zap.String("task", taskName),
		zap.Duration("duration", time.Since(start)),
	)
}
