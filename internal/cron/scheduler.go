package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	pkgredis "tokenhub-server/internal/pkg/redis"
	aimodelsvc "tokenhub-server/internal/service/aimodel"
	"tokenhub-server/internal/service/audit"
	"tokenhub-server/internal/service/authlog"
	billingsvc "tokenhub-server/internal/service/billing"
	channelsvc "tokenhub-server/internal/service/channel"
	emailsvc "tokenhub-server/internal/service/email"
	"tokenhub-server/internal/service/member"
	"tokenhub-server/internal/service/modeldiscovery"
	ratelimitsvc "tokenhub-server/internal/service/ratelimit"
	reportSvc "tokenhub-server/internal/service/report"
)

// AuditRetentionDays 审计日志保留天数（30 天前的记录每日 04:00 自动清理）
const AuditRetentionDays = 30

// RateLimitEventRetentionDays 限流 429 事件保留天数（7 天前的记录每日 04:30 自动清理）
const RateLimitEventRetentionDays = 7

// AuthLogRetentionDays 用户认证日志保留天数（90 天前的记录每日 04:45 自动清理）
const AuthLogRetentionDays = 90

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
	memberSvc         *member.MemberLevelService
	discoverySvc      *modeldiscovery.DiscoveryService
	capabilityTester  *aimodelsvc.CapabilityTester   // Phase 2：能力测试自动触发
	auditSvc          *audit.AuditService            // v3.3 审计日志清理
	rateLimitEventSvc *ratelimitsvc.EventRecorder    // 限流 429 事件清理
	authLogSvc        *authlog.Recorder              // 用户认证日志清理
	aggSvc            *reportSvc.UserDailyAggService // 用户调用日表聚合
	db                *gorm.DB
	redis             *goredis.Client
	stopCh            chan struct{} // 停止信号通道

	// 任务状态管理
	tasksMu sync.RWMutex
	tasks   map[string]*TaskInfo
}

// TaskInfo 定时任务信息（对外暴露）
type TaskInfo struct {
	Name           string    `json:"name"`
	Schedule       string    `json:"schedule"`
	Description    string    `json:"description"`
	Enabled        bool      `json:"enabled"`
	LastRun        time.Time `json:"last_run,omitempty"`
	LastErr        string    `json:"last_error,omitempty"`
	LastStatus     string    `json:"last_status,omitempty"`
	LastDurationMs int64     `json:"last_duration_ms,omitempty"`
	LastSummary    string    `json:"last_summary,omitempty"`
}

type taskRunContextKey struct{}

type taskRunOutput struct {
	Summary string                 `json:"summary"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

// SetTaskRunOutput 为当前定时任务写入可展示的运行摘要和数据汇总。
func SetTaskRunOutput(ctx context.Context, summary string, data map[string]interface{}) {
	if out, ok := ctx.Value(taskRunContextKey{}).(*taskRunOutput); ok && out != nil {
		out.Summary = summary
		out.Data = data
	}
}

// NewScheduler 创建定时任务调度器实例
func NewScheduler(db *gorm.DB, redis *goredis.Client, memberSvc *member.MemberLevelService, opts ...SchedulerOption) *Scheduler {
	s := &Scheduler{
		memberSvc: memberSvc,
		db:        db,
		redis:     redis,
		stopCh:    make(chan struct{}),
		tasks:     make(map[string]*TaskInfo),
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

// WithCapabilityTester 注入能力测试服务（Phase 2：新模型同步后自动触发测试）
func WithCapabilityTester(tester *aimodelsvc.CapabilityTester) SchedulerOption {
	return func(s *Scheduler) { s.capabilityTester = tester }
}

// WithAuditService 注入审计日志服务（v3.3：每日清理 30 天前数据）
func WithAuditService(svc *audit.AuditService) SchedulerOption {
	return func(s *Scheduler) { s.auditSvc = svc }
}

// WithRateLimitEventRecorder 注入限流事件记录器（每日清理 7 天前数据）
func WithRateLimitEventRecorder(svc *ratelimitsvc.EventRecorder) SchedulerOption {
	return func(s *Scheduler) { s.rateLimitEventSvc = svc }
}

// WithAuthLogRecorder 注入用户认证日志记录器（每日清理 90 天前数据）
func WithAuthLogRecorder(svc *authlog.Recorder) SchedulerOption {
	return func(s *Scheduler) { s.authLogSvc = svc }
}

// WithUserDailyAggService 注入用户调用日表聚合服务
func WithUserDailyAggService(svc *reportSvc.UserDailyAggService) SchedulerOption {
	return func(s *Scheduler) { s.aggSvc = svc }
}

// Start 启动所有定时任务 goroutine
func (s *Scheduler) Start() {
	// 注册所有任务到状态表（含详细描述，前端直接展示）
	s.registerTask("frozen_release", "每小时: 冻结超时释放",
		"扫描5分钟前仍为FROZEN状态的支付记录，自动归还冻结金额至用户余额，防止支付超时导致资金永久冻结", true)
	s.registerTask("commission_settle", "每日06:00: 佣金自动结算",
		"将创建时间超过7天的PENDING佣金记录批量转为SETTLED（可提现状态），完成邀请返佣结算周期", true)
	s.registerTask("model_sync", "每日07:00: 模型自动同步",
		"调用各供应商API拉取最新模型列表并增量写库；同步后停用无售价模型，有新模型时自动触发能力测试", s.discoverySvc != nil)
	s.registerTask("route_refresh", "每日08:00: 默认渠道路由巡检",
		"按成本倒数重算所有默认渠道的路由权重，更新channel_routes表，确保价格变动后路由自动生效", true)
	s.registerTask("consume_rotate", "每月1号: 月消费轮转",
		"滚动3个月消费字段：month_consume_3←2←1←0，为会员降级任务提供滑动窗口数据", true)
	s.registerTask("member_degrade", "每月1号: 会员降级检查",
		"遍历V1+会员，检查连续不达标月数，达到DegradeMonths阈值时自动降一级", true)
	s.registerTask("cache_ratio_audit_reminder", "每月1号09:00: 缓存系数审核提醒",
		"写入审计日志提醒管理员核验各供应商缓存系数与官网当前规则是否一致", s.auditSvc != nil)
	s.registerTask("logs_cleanup", "每日04:00: 清理7天前调用日志",
		"分批删除7天前的api_call_logs和channel_logs，每批5000条+50ms间隔，避免锁表影响业务", true)
	s.registerTask("audit_cleanup", fmt.Sprintf("每日04:00: 清理%d天前审计日志", AuditRetentionDays),
		fmt.Sprintf("分批删除%d天前的audit_logs审计记录，每批1000条，控制审计表数据量", AuditRetentionDays), true)
	s.registerTask("rate_limit_event_cleanup", fmt.Sprintf("每日04:00: 清理%d天前限流事件", RateLimitEventRetentionDays),
		fmt.Sprintf("分批删除%d天前的rate_limit_events 429事件记录", RateLimitEventRetentionDays), true)
	s.registerTask("auth_log_cleanup", fmt.Sprintf("每日04:00: 清理%d天前认证日志", AuthLogRetentionDays),
		fmt.Sprintf("分批删除%d天前的user_auth_logs登录/注册/登出事件记录", AuthLogRetentionDays), true)
	s.registerTask("email_log_cleanup", "每日04:00: 清理30天前邮件发送日志",
		"分批删除30天前的email_send_logs记录，控制邮件日志表数据量", true)
	s.registerTask("inflight_reset", "每5分钟: 重置渠道在途请求计数",
		"删除Redis中channel:inflight和stream:active:global计数键，防止容器崩溃后残留计数导致负载路由偏差", true)
	s.registerTask("billing_anomaly_check", "每小时整点: 计费异常巡检",
		"按 5 条 SQL 规则检查近 1 小时 api_call_logs（定价缺失/缓存反常/usage 缺失/单价突变/缓存节省漏算），命中即写 audit_logs 告警", true)
	s.registerTask("daily_user_agg", "每日01:00: 用户调用日表聚合",
		"从api_call_logs聚合前一天数据，按用户×模型×日期写入user_daily_stats，在7天清理前完成持久化", s.aggSvc != nil)
	s.registerTask("daily_billing_reconciliation", "每日01:10: 扣费对账快照",
		"按日汇总api_call_logs的实扣收入、平台成本、少收、扣费失败和估算usage等指标，保存到日对账快照表", true)

	// 启动每小时任务（冻结超时释放）
	go s.runEveryHour()
	// 启动每日任务（佣金自动结算 + 模型自动同步 + 路由巡检）
	go s.runDaily()
	// 启动每月任务（月消费轮转/会员降级检查）
	go s.runMonthly()
	// 启动日志清理任务（每日04:00）
	go s.runLogsCleanup()
	// 启动在途请求计数重置（每5分钟，防进程崩溃残留）
	go s.runInflightReset()
	// 启动用户调用日表聚合任务（每日01:00）
	if s.aggSvc != nil {
		go s.runDailyAgg()
	}
	// 启动扣费对账快照任务（每日01:10），不依赖用户日表聚合服务。
	go s.runDailyBillingReconciliation()
	zap.L().Info("定时任务调度器已启动",
		zap.String("timezone", shanghaiLoc.String()),
		zap.String("hourly", "每小时整点: 冻结超时释放"),
		zap.String("daily", "01:00: 用户日表聚合, 01:10: 扣费对账快照, 06:00: 佣金结算, 07:00: 模型同步, 08:00: 路由巡检"),
		zap.String("weekly", "每周日03:00: 价格自动更新"),
		zap.String("monthly", "每月1号: 消费轮转+会员降级"),
	)
}

// registerTask 注册定时任务到状态表
func (s *Scheduler) registerTask(name, schedule string, args ...interface{}) {
	description := ""
	enabled := true
	if len(args) == 1 {
		if v, ok := args[0].(bool); ok {
			enabled = v
		}
	} else if len(args) >= 2 {
		if v, ok := args[0].(string); ok {
			description = v
		}
		if v, ok := args[1].(bool); ok {
			enabled = v
		}
	}
	s.tasksMu.Lock()
	defer s.tasksMu.Unlock()
	s.tasks[name] = &TaskInfo{Name: name, Schedule: schedule, Description: description, Enabled: enabled}
}

// GetTasks 获取所有定时任务的状态列表
func (s *Scheduler) GetTasks() []TaskInfo {
	s.tasksMu.RLock()
	defer s.tasksMu.RUnlock()
	result := make([]TaskInfo, 0, len(s.tasks))
	for _, t := range s.tasks {
		result = append(result, *t)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// ListTasks 分页查询定时任务列表，支持按名称、执行频率和描述搜索。
func (s *Scheduler) ListTasks(search string, page, pageSize int) ([]TaskInfo, int64) {
	all := s.GetTasks()
	search = strings.ToLower(strings.TrimSpace(search))
	filtered := make([]TaskInfo, 0, len(all))
	for _, task := range all {
		if search == "" ||
			strings.Contains(strings.ToLower(task.Name), search) ||
			strings.Contains(strings.ToLower(task.Schedule), search) ||
			strings.Contains(strings.ToLower(task.Description), search) {
			filtered = append(filtered, task)
		}
	}
	total := int64(len(filtered))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	start := (page - 1) * pageSize
	if start >= len(filtered) {
		return []TaskInfo{}, total
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[start:end], total
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

// ListTaskRuns 分页查询定时任务运行历史；taskName 为空时返回全部任务历史。
func (s *Scheduler) ListTaskRuns(taskName, status, search string, page, pageSize int) ([]model.CronTaskRun, int64, error) {
	if s.db == nil {
		return []model.CronTaskRun{}, 0, nil
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	query := s.db.Model(&model.CronTaskRun{})
	if taskName != "" {
		query = query.Where("task_name = ?", taskName)
	}
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if search != "" {
		like := "%" + search + "%"
		query = query.Where(
			"task_name LIKE ? OR task_label LIKE ? OR output_summary LIKE ? OR error_message LIKE ?",
			like, like, like, like,
		)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var runs []model.CronTaskRun
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&runs).Error
	return runs, total, err
}

// updateTaskRun 更新任务最后执行时间、状态、耗时和摘要。
func (s *Scheduler) updateTaskRun(name string, err error, duration time.Duration, summary string) {
	s.tasksMu.Lock()
	defer s.tasksMu.Unlock()
	if t, ok := s.tasks[name]; ok {
		t.LastRun = time.Now()
		t.LastDurationMs = duration.Milliseconds()
		t.LastSummary = summary
		if err != nil {
			t.LastStatus = "failed"
			t.LastErr = err.Error()
		} else {
			t.LastStatus = "completed"
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
	s.safeRunNamed("billing_anomaly_check", "计费异常巡检", func(ctx context.Context) error {
		return s.runBillingAnomalyCheck(ctx)
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
			s.safeRunNamed("billing_anomaly_check", "计费异常巡检", func(ctx context.Context) error {
				return s.runBillingAnomalyCheck(ctx)
			})
		case <-s.stopCh:
			return
		}
	}
}

// runBillingAnomalyCheck 计费异常巡检（每小时整点）
func (s *Scheduler) runBillingAnomalyCheck(ctx context.Context) error {
	if s.db == nil {
		return nil
	}
	checker := billingsvc.NewAnomalyChecker(s.db, s.auditSvc)
	res, err := checker.Run(ctx)
	if err != nil {
		return err
	}
	SetTaskRunOutput(ctx, fmt.Sprintf("巡检完成，命中告警 %d 条", res.TotalAlerts), map[string]interface{}{
		"window_start":          res.WindowStart,
		"window_end":            res.WindowEnd,
		"pricing_missing":       res.PricingMissing,
		"cache_field_invalid":   res.CacheFieldInvalid,
		"stream_usage_missing":  res.StreamUsageMissing,
		"unit_price_shift":      res.UnitPriceShift,
		"cache_savings_missing": res.CacheSavingsMissing,
	})
	return nil
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
				// 汇总统计 + 收集新增模型 ID
				var added, updated, errCount int
				var newModelIDs []uint
				for _, r := range result.Results {
					added += r.ModelsAdded
					updated += r.ModelsUpdated
					errCount += len(r.Errors)
					newModelIDs = append(newModelIDs, r.NewModelIDs...)
				}
				zap.L().Info("模型自动同步完成",
					zap.Int("total_channels", result.Total),
					zap.Int("new_models", added),
					zap.Int("updated_models", updated),
					zap.Int("errors", errCount))
				SetTaskRunOutput(ctx, fmt.Sprintf("同步 %d 个渠道，新增 %d 个模型，更新 %d 个模型，错误 %d 个", result.Total, added, updated, errCount), map[string]interface{}{
					"total_channels": result.Total,
					"new_models":     added,
					"updated_models": updated,
					"errors":         errCount,
					"new_model_ids":  newModelIDs,
				})

				// Phase 2：有新模型时自动触发能力测试
				if len(newModelIDs) > 0 && s.capabilityTester != nil {
					go s.runCapabilityTestForModels(context.Background(), newModelIDs, "新模型自动测试")
				}
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
					SetTaskRunOutput(ctx, fmt.Sprintf("刷新默认路由：模型 %d 个，路由 %d 条，新增别名 %d 个，移除别名 %d 个",
						job.Summary.TotalModels, job.Summary.TotalRoutes, len(job.Summary.NewModels), len(job.Summary.RemovedModels)), map[string]interface{}{
						"total_models":    job.Summary.TotalModels,
						"total_routes":    job.Summary.TotalRoutes,
						"new_aliases":     len(job.Summary.NewModels),
						"removed_aliases": len(job.Summary.RemovedModels),
					})
				} else {
					SetTaskRunOutput(ctx, "默认渠道路由刷新完成", map[string]interface{}{})
				}
				return nil
			})
		}
	}
}

// ==================== 每月任务 ====================

// runMonthly 每月1号按时间顺序执行各月度任务
// 时间安排：
//
//	00:00 月消费轮转
//	02:00 会员降级检查
func (s *Scheduler) runMonthly() {
	// 月度任务列表，按执行时间（小时）升序排列
	type monthlyTask struct {
		hour int
		id   string
		name string
		fn   func(context.Context) error
	}
	tasks := []monthlyTask{
		{0, "consume_rotate", "月消费轮转", func(ctx context.Context) error {
			if err := s.memberSvc.RotateMonthConsume(ctx); err != nil {
				return err
			}
			SetTaskRunOutput(ctx, "月消费字段轮转完成", map[string]interface{}{"month": time.Now().In(shanghaiLoc).Format("2006-01")})
			return nil
		}},
		{2, "member_degrade", "会员降级检查", func(ctx context.Context) error {
			if err := s.memberSvc.CheckAndDegradeAll(ctx); err != nil {
				return err
			}
			SetTaskRunOutput(ctx, "会员降级检查完成", map[string]interface{}{"month": time.Now().In(shanghaiLoc).Format("2006-01")})
			return nil
		}},
		{9, "cache_ratio_audit_reminder", "缓存系数审核提醒", func(ctx context.Context) error {
			if s.auditSvc == nil {
				return nil
			}
			month := time.Now().In(shanghaiLoc).Format("2006-01")
			s.auditSvc.Enqueue(&model.AuditLog{
				Action:   "cache_ratio_audit_reminder",
				Resource: "cache_pricing",
				Menu:     "缓存系数审核",
				Feature:  "月度提醒",
				Method:   "CRON",
				Path:     "cron://cache_ratio_audit_reminder",
				Remark: "请管理员前往「模型管理」核验各供应商缓存系数与官网当前规则一致性。" +
					"参考：OpenAI 0.5（auto）/ Anthropic 0.1+1.25（explicit）/ DeepSeek 0.1 / " +
					"Moonshot 0.25 / Gemini 0.75 / 阿里云 0.2+0.1+1.25（both）/ " +
					"百度千帆 0.2+0.1+1.25 / 火山豆包 0.4 / 智谱 0.2",
			})
			SetTaskRunOutput(ctx, "缓存系数审核提醒已写入审计日志", map[string]interface{}{"month": month})
			return nil
		}},
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

			s.safeRunNamed(task.id, task.name, task.fn)
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

		// v3.3: 同时清理审计日志（30 天前）
		s.safeRunNamed("audit_cleanup", "清理30天前审计日志", func(ctx context.Context) error {
			return s.cleanupOldAuditLogs(ctx)
		})

		// 清理 7 天前限流事件
		s.safeRunNamed("rate_limit_event_cleanup", "清理7天前限流事件", func(ctx context.Context) error {
			return s.cleanupOldRateLimitEvents(ctx)
		})

		// 清理 90 天前用户认证日志
		s.safeRunNamed("auth_log_cleanup", "清理90天前认证日志", func(ctx context.Context) error {
			return s.cleanupOldAuthLogs(ctx)
		})

		// 清理 30 天前邮件发送日志
		s.safeRunNamed("email_log_cleanup", "清理30天前邮件发送日志", func(ctx context.Context) error {
			return s.cleanupOldEmailLogs(ctx)
		})
	}
}

// cleanupOldEmailLogs 清理 email_send_logs 表 30 天前记录
func (s *Scheduler) cleanupOldEmailLogs(ctx context.Context) error {
	if emailsvc.Default == nil {
		SetTaskRunOutput(ctx, "邮件服务未初始化，跳过清理", map[string]interface{}{"skipped": true})
		return nil
	}
	before := time.Now().AddDate(0, 0, -30)
	deleted, err := emailsvc.Default.CleanupLogsBefore(ctx, before)
	if err == nil {
		SetTaskRunOutput(ctx, fmt.Sprintf("清理 30 天前邮件日志 %d 条", deleted), map[string]interface{}{
			"retention_days": 30,
			"deleted":        deleted,
			"before":         before.Format(time.RFC3339),
		})
	}
	return err
}

// cleanupOldAuthLogs 清理 user_auth_logs 表中 AuthLogRetentionDays 天前的记录
func (s *Scheduler) cleanupOldAuthLogs(ctx context.Context) error {
	svc := s.authLogSvc
	if svc == nil {
		svc = authlog.Default
	}
	if svc == nil {
		zap.L().Warn("auth_log cleanup skipped: recorder not available")
		SetTaskRunOutput(ctx, "认证日志服务未初始化，跳过清理", map[string]interface{}{"skipped": true})
		return nil
	}
	start := time.Now()
	deleted, err := svc.DeleteOlderThan(ctx, AuthLogRetentionDays)
	if err != nil {
		return fmt.Errorf("清理认证日志失败（已删 %d 条）: %w", deleted, err)
	}
	zap.L().Info("认证日志清理完成",
		zap.Int("retention_days", AuthLogRetentionDays),
		zap.Int64("deleted", deleted),
		zap.Duration("duration", time.Since(start)),
	)
	SetTaskRunOutput(ctx, fmt.Sprintf("清理 %d 天前认证日志 %d 条", AuthLogRetentionDays, deleted), map[string]interface{}{
		"retention_days": AuthLogRetentionDays,
		"deleted":        deleted,
		"duration_ms":    time.Since(start).Milliseconds(),
	})
	return nil
}

// cleanupOldRateLimitEvents 清理 rate_limit_events 表中 RateLimitEventRetentionDays 天前记录
func (s *Scheduler) cleanupOldRateLimitEvents(ctx context.Context) error {
	svc := s.rateLimitEventSvc
	if svc == nil {
		svc = ratelimitsvc.Default
	}
	if svc == nil {
		zap.L().Warn("rate_limit_event cleanup skipped: recorder not available")
		SetTaskRunOutput(ctx, "限流事件服务未初始化，跳过清理", map[string]interface{}{"skipped": true})
		return nil
	}
	start := time.Now()
	deleted, err := svc.DeleteOlderThan(ctx, RateLimitEventRetentionDays)
	if err != nil {
		return fmt.Errorf("清理限流事件失败（已删 %d 条）: %w", deleted, err)
	}
	zap.L().Info("限流事件清理完成",
		zap.Int("retention_days", RateLimitEventRetentionDays),
		zap.Int64("deleted", deleted),
		zap.Duration("duration", time.Since(start)),
	)
	SetTaskRunOutput(ctx, fmt.Sprintf("清理 %d 天前限流事件 %d 条", RateLimitEventRetentionDays, deleted), map[string]interface{}{
		"retention_days": RateLimitEventRetentionDays,
		"deleted":        deleted,
		"duration_ms":    time.Since(start).Milliseconds(),
	})
	return nil
}

// cleanupOldAuditLogs 清理 audit_logs 表中 AuditRetentionDays 天前的记录
// 复用 audit.AuditService.DeleteOlderThan，分批删除避免锁表
func (s *Scheduler) cleanupOldAuditLogs(ctx context.Context) error {
	svc := s.auditSvc
	if svc == nil {
		// 未注入则使用全局单例（router 已初始化）
		svc = audit.Default
	}
	if svc == nil {
		zap.L().Warn("audit cleanup skipped: audit service not available")
		SetTaskRunOutput(ctx, "审计日志服务未初始化，跳过清理", map[string]interface{}{"skipped": true})
		return nil
	}

	start := time.Now()
	deleted, err := svc.DeleteOlderThan(ctx, AuditRetentionDays)
	if err != nil {
		return fmt.Errorf("清理审计日志失败（已删 %d 条）: %w", deleted, err)
	}
	zap.L().Info("审计日志清理完成",
		zap.Int("retention_days", AuditRetentionDays),
		zap.Int64("deleted", deleted),
		zap.Duration("duration", time.Since(start)),
	)
	SetTaskRunOutput(ctx, fmt.Sprintf("清理 %d 天前审计日志 %d 条", AuditRetentionDays, deleted), map[string]interface{}{
		"retention_days": AuditRetentionDays,
		"deleted":        deleted,
		"duration_ms":    time.Since(start).Milliseconds(),
	})
	return nil
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
	SetTaskRunOutput(ctx, fmt.Sprintf("清理调用日志 %d 条、渠道日志 %d 条", apiDeleted, channelDeleted), map[string]interface{}{
		"cutoff":                cutoff.Format(time.RFC3339),
		"api_call_logs_deleted": apiDeleted,
		"channel_logs_deleted":  channelDeleted,
		"total_deleted":         apiDeleted + channelDeleted,
	})
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
					SetTaskRunOutput(ctx, "Redis 未初始化，跳过在途计数重置", map[string]interface{}{"skipped": true})
					return nil
				}
				deleted, err := s.redis.Del(ctx, "channel:inflight", "stream:active:global").Result()
				if err != nil {
					return fmt.Errorf("重置计数失败: %w", err)
				}
				if deleted > 0 {
					zap.L().Debug("在途请求计数已重置", zap.Int64("keys_deleted", deleted))
				}
				SetTaskRunOutput(ctx, fmt.Sprintf("重置在途请求计数，删除 Redis Key %d 个", deleted), map[string]interface{}{
					"keys_deleted": deleted,
					"keys":         []string{"channel:inflight", "stream:active:global"},
				})
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
//  1. 查询所有 status=PENDING 且 created_at < 7天前 的 CommissionRecord
//  2. 批量更新 status 为 SETTLED
//  3. 记录结算数量日志
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
	SetTaskRunOutput(ctx, fmt.Sprintf("结算 PENDING 佣金 %d 条", result.RowsAffected), map[string]interface{}{
		"settled_count": result.RowsAffected,
		"before":        sevenDaysAgo.Format(time.RFC3339),
	})

	return nil
}

// releaseFrozenRecords 释放超时的冻结记录
// 逻辑：
//  1. 查询所有 status=FROZEN 且 created_at < 1小时前 的 FreezeRecord
//  2. 对每条记录：减少 UserBalance.frozen_amount，归还冻结金额到余额
//  3. 更新 FreezeRecord.status 为 RELEASED
//  4. 使用事务确保一致性
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
		SetTaskRunOutput(ctx, "无超时冻结记录需要释放", map[string]interface{}{
			"expired_count":  0,
			"released_count": 0,
		})
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
	SetTaskRunOutput(ctx, fmt.Sprintf("释放超时冻结记录 %d/%d 条", releasedCount, len(records)), map[string]interface{}{
		"expired_count":  len(records),
		"released_count": releasedCount,
		"failed_count":   len(records) - releasedCount,
		"before":         fiveMinutesAgo.Format(time.RFC3339),
	})

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
	runName := taskID
	if runName == "" {
		runName = taskName
	}
	run := model.CronTaskRun{
		TaskName:    runName,
		TaskLabel:   taskName,
		Status:      "running",
		StartedAt:   start,
		TriggerType: "schedule",
	}
	if s.db != nil {
		_ = s.db.Create(&run).Error
	}

	output := &taskRunOutput{}
	ctx = context.WithValue(ctx, taskRunContextKey{}, output)

	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("任务执行异常: %v", r)
				zap.L().Error("定时任务 panic 已恢复",
					zap.String("task", taskName),
					zap.Any("panic", r),
				)
			}
		}()
		err = fn(ctx)
	}()

	completedAt := time.Now()
	duration := completedAt.Sub(start)
	summary := output.Summary
	if summary == "" {
		if err != nil {
			summary = "任务执行失败"
		} else {
			summary = "任务执行成功"
		}
	}
	if taskID != "" {
		s.updateTaskRun(taskID, err, duration, summary)
	}

	status := "completed"
	errMsg := ""
	if err != nil {
		status = "failed"
		errMsg = err.Error()
	}
	outputJSON := ""
	if output.Data != nil {
		if b, marshalErr := json.Marshal(output.Data); marshalErr == nil {
			outputJSON = string(b)
		}
	}
	if s.db != nil && run.ID > 0 {
		_ = s.db.Model(&model.CronTaskRun{}).Where("id = ?", run.ID).Updates(map[string]interface{}{
			"status":         status,
			"completed_at":   completedAt,
			"duration_ms":    duration.Milliseconds(),
			"output_summary": summary,
			"output_json":    outputJSON,
			"error_message":  errMsg,
		}).Error
	}

	if err != nil {
		zap.L().Error("定时任务执行失败",
			zap.String("task", taskName),
			zap.Error(err),
			zap.Duration("duration", duration),
		)
		return
	}

	zap.L().Info("定时任务执行完成",
		zap.String("task", taskName),
		zap.Duration("duration", duration),
	)
}

// runDailyAgg 每日 01:00 执行用户调用日表聚合
func (s *Scheduler) runDailyAgg() {
	for {
		now := time.Now().In(shanghaiLoc)
		next := time.Date(now.Year(), now.Month(), now.Day(), 1, 0, 0, 0, shanghaiLoc)
		if now.After(next) {
			next = next.AddDate(0, 0, 1)
		}
		waitDuration := next.Sub(now)
		zap.L().Info("用户日表聚合任务等待中", zap.Time("next_run", next), zap.Duration("wait", waitDuration))
		select {
		case <-time.After(waitDuration):
		case <-s.stopCh:
			return
		}
		s.safeRunNamed("daily_user_agg", "用户调用日表聚合", func(ctx context.Context) error {
			n, err := s.aggSvc.AggregateYesterday(ctx)
			if err != nil {
				return err
			}
			zap.L().Info("用户调用日表聚合完成", zap.Int64("rows", n))
			SetTaskRunOutput(ctx, fmt.Sprintf("聚合昨日用户调用日表 %d 行", n), map[string]interface{}{
				"rows": n,
				"date": time.Now().In(shanghaiLoc).AddDate(0, 0, -1).Format("2006-01-02"),
			})
			return nil
		})
	}
}

// runDailyBillingReconciliation 每日 01:10 执行扣费对账快照，独立于用户日表聚合。
func (s *Scheduler) runDailyBillingReconciliation() {
	for {
		now := time.Now().In(shanghaiLoc)
		next := time.Date(now.Year(), now.Month(), now.Day(), 1, 10, 0, 0, shanghaiLoc)
		if now.After(next) {
			next = next.AddDate(0, 0, 1)
		}
		waitDuration := next.Sub(now)
		zap.L().Info("扣费对账快照任务等待中", zap.Time("next_run", next), zap.Duration("wait", waitDuration))
		select {
		case <-time.After(waitDuration):
		case <-s.stopCh:
			return
		}
		s.safeRunNamed("daily_billing_reconciliation", "扣费对账快照", func(ctx context.Context) error {
			date := time.Now().In(shanghaiLoc).AddDate(0, 0, -1).Format("2006-01-02")
			return s.runBillingReconciliationForDate(ctx, date)
		})
	}
}

func (s *Scheduler) runBillingReconciliationForDate(ctx context.Context, date string) error {
	svc := reportSvc.NewBillingReconciliationService(s.db)
	snap, err := svc.UpsertDate(ctx, date)
	if err != nil {
		return err
	}
	zap.L().Info("扣费对账快照完成",
		zap.String("date", date),
		zap.String("health", snap.ReconciliationHealth),
		zap.Int64("requests", snap.TotalRequests),
	)
	SetTaskRunOutput(ctx, fmt.Sprintf("生成 %s 扣费对账快照：%s，请求 %d 次", date, snap.ReconciliationHealth, snap.TotalRequests), map[string]interface{}{
		"date":           date,
		"health":         snap.ReconciliationHealth,
		"requests":       snap.TotalRequests,
		"revenue":        snap.ActualRevenueRMB,
		"cost":           snap.PlatformCostRMB,
		"variance_units": snap.EstimateVarianceUnits,
	})
	return nil
}

// ==================== 热卖模型抽样能力检测 ====================

// SelectHotModelSample 按 model_type 随机抽取热卖模型（每种类型1个），供 Handler 调用
// 查询范围：tags LIKE '%热卖%'，不强制 is_active（能力测试本身决定是否可用）
func SelectHotModelSample(db *gorm.DB) (map[string][]model.AIModel, []uint, error) {
	var models []model.AIModel
	if err := db.Where("tags LIKE ? AND deleted_at IS NULL", "%热卖%").
		Order("RAND()").
		Find(&models).Error; err != nil {
		return nil, nil, fmt.Errorf("查询热卖模型失败: %w", err)
	}

	// 按 model_type 分组，每组随机保留第一个（已 ORDER BY RAND()）
	grouped := make(map[string][]model.AIModel)
	seen := make(map[string]bool)
	var selected []uint
	for _, m := range models {
		mt := m.ModelType
		if mt == "" {
			mt = "LLM"
		}
		grouped[mt] = append(grouped[mt], m)
		if !seen[mt] {
			seen[mt] = true
			selected = append(selected, m.ID)
		}
	}
	return grouped, selected, nil
}

// ==================== Phase 2：能力测试自动触发 ====================

// runCapabilityTestForModels 为指定模型创建并运行能力测试任务（后台 goroutine 调用）
// 用于：新模型同步后自动触发全量能力发现
func (s *Scheduler) runCapabilityTestForModels(ctx context.Context, modelIDs []uint, name string) {
	if s.capabilityTester == nil {
		return
	}
	modelIDsJSON, _ := json.Marshal(modelIDs)
	task := model.CapabilityTestTask{
		Status:          "pending",
		ModelIDs:        string(modelIDsJSON),
		CaseIDs:         "[]", // 空=使用所有 enabled 用例
		TriggeredBy:     0,    // 系统触发
		AutoApply:       true,
		SystemTriggered: true,
	}
	if err := s.db.WithContext(ctx).Create(&task).Error; err != nil {
		zap.L().Error("创建能力测试任务失败", zap.String("name", name), zap.Error(err))
		return
	}
	zap.L().Info(name,
		zap.Uint("task_id", task.ID),
		zap.Int("model_count", len(modelIDs)))

	// 执行测试（全量能力发现：skipKnownDisabled=false；完成后自动应用建议）
	if err := s.capabilityTester.RunTests(ctx, aimodelsvc.RunTestsInput{
		TaskID:               task.ID,
		ModelIDs:             modelIDs,
		AutoApplySuggestions: true,
		SkipKnownDisabled:    false,
	}, nil); err != nil {
		zap.L().Error("能力测试运行失败",
			zap.String("name", name),
			zap.Uint("task_id", task.ID),
			zap.Error(err))
	}
}
