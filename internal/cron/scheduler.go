package cron

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/service/agent"
	"tokenhub-server/internal/service/balance"
	"tokenhub-server/internal/service/member"
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
	memberSvc  *member.MemberLevelService
	agentSvc   *agent.AgentLevelService
	balanceSvc *balance.BalanceService
	db         *gorm.DB
	redis      *goredis.Client
	stopCh     chan struct{} // 停止信号通道
}

// NewScheduler 创建定时任务调度器实例
func NewScheduler(db *gorm.DB, redis *goredis.Client, memberSvc *member.MemberLevelService, agentSvc *agent.AgentLevelService, balanceSvc *balance.BalanceService) *Scheduler {
	return &Scheduler{
		memberSvc:  memberSvc,
		agentSvc:   agentSvc,
		balanceSvc: balanceSvc,
		db:         db,
		redis:      redis,
		stopCh:     make(chan struct{}),
	}
}

// Start 启动所有定时任务 goroutine
func (s *Scheduler) Start() {
	// 启动每小时任务（冻结超时释放 + 对账）
	go s.runEveryHour()
	// 启动每日任务（佣金自动结算）
	go s.runDaily()
	// 启动每月任务（会员赠送/降级检查/代理升降级/月消费轮转/销售额重置）
	go s.runMonthly()

	zap.L().Info("定时任务调度器已启动",
		zap.String("timezone", shanghaiLoc.String()),
		zap.String("hourly", "每小时整点: 冻结超时释放+对账检查"),
		zap.String("daily", "每日06:00: 佣金自动结算"),
		zap.String("monthly", "每月1号00:00-06:00: 赠送/轮转/降级/升级/销售额重置"),
	)
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
	s.safeRun("冻结超时释放", func(ctx context.Context) error {
		return s.releaseFrozenRecords(ctx)
	})
	s.safeRun("余额对账检查", func(ctx context.Context) error {
		return s.reconcileBalances(ctx)
	})

	// 之后每小时执行
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.safeRun("冻结超时释放", func(ctx context.Context) error {
				return s.releaseFrozenRecords(ctx)
			})
			s.safeRun("余额对账检查", func(ctx context.Context) error {
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
		s.safeRun("佣金自动结算", func(ctx context.Context) error {
			return s.settleCommissions(ctx)
		})
	}
}

// ==================== 每月任务 ====================

// runMonthly 每月1号按时间顺序执行各月度任务
// 时间安排：
//   00:00 会员赠送额度发放
//   01:00 月消费轮转
//   02:00 月度销售额重置（代理 current_month_sales = 0）
//   03:00 会员降级检查
//   04:00 代理升级检查
//   05:00 代理降级检查
func (s *Scheduler) runMonthly() {
	// 月度任务列表，按执行时间（小时）升序排列
	type monthlyTask struct {
		hour int
		name string
		fn   func(context.Context) error
	}
	tasks := []monthlyTask{
		{0, "会员赠送额度发放", func(ctx context.Context) error { return s.memberSvc.GrantMonthlyGifts(ctx) }},
		{1, "月消费轮转", func(ctx context.Context) error { return s.memberSvc.RotateMonthConsume(ctx) }},
		{2, "月度销售额重置", func(ctx context.Context) error { return s.resetAgentMonthlySales(ctx) }},
		{3, "会员降级检查", func(ctx context.Context) error { return s.memberSvc.CheckAndDegradeAll(ctx) }},
		{4, "代理升级检查", func(ctx context.Context) error { return s.agentSvc.CheckAndUpgradeAll(ctx) }},
		{5, "代理降级检查", func(ctx context.Context) error { return s.agentSvc.CheckAndDegradeAll(ctx) }},
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

// resetAgentMonthlySales 重置所有代理的月度销售额
// 在每月1号执行：current_month_sales = 0, last_month_sales = current_month_sales
func (s *Scheduler) resetAgentMonthlySales(ctx context.Context) error {
	// 先将本月销售额保存到上月销售额
	if err := s.db.WithContext(ctx).
		Model(&model.UserAgentProfile{}).
		Where("1 = 1").
		UpdateColumn("last_month_sales", gorm.Expr("current_month_sales")).Error; err != nil {
		return fmt.Errorf("保存上月销售额失败: %w", err)
	}

	// 重置本月销售额为0
	if err := s.db.WithContext(ctx).
		Model(&model.UserAgentProfile{}).
		Where("1 = 1").
		UpdateColumn("current_month_sales", 0).Error; err != nil {
		return fmt.Errorf("重置本月销售额失败: %w", err)
	}

	zap.L().Info("代理月度销售额已重置")
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

	zap.L().Info("定时任务开始执行", zap.String("task", taskName))
	start := time.Now()

	if err := fn(ctx); err != nil {
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
