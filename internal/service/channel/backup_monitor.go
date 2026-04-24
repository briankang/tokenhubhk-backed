package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/safego"
)

// SwitchRuleType 备份切换规则类型
type SwitchRuleType string

const (
	SwitchRuleConsecutiveErrors SwitchRuleType = "consecutive_errors"
	SwitchRuleErrorRate         SwitchRuleType = "error_rate"
	SwitchRuleLatency           SwitchRuleType = "latency"
	SwitchRuleTimeout           SwitchRuleType = "timeout"
	SwitchRuleStatusCode        SwitchRuleType = "status_code"
)

// SwitchRule 单个切换规则配置
type SwitchRule struct {
	Type       SwitchRuleType `json:"type"`
	Threshold  float64        `json:"threshold"`   // threshold value
	WindowSec  int            `json:"window_sec"`  // sliding window in seconds
	StatusCode int            `json:"status_code"`  // for status_code type only
}

// BackupMonitor 备份监控器，后台循环评估切换规则，基于 Redis 滑动窗口统计
type BackupMonitor struct {
	db          *gorm.DB
	redis       *goredis.Client
	logger      *zap.Logger
	backupSvc   *BackupService
}

// NewBackupMonitor 创建备份监控器实例
func NewBackupMonitor(db *gorm.DB, redis *goredis.Client, backupSvc *BackupService) *BackupMonitor {
	return &BackupMonitor{
		db:        db,
		redis:     redis,
		logger:    logger.L,
		backupSvc: backupSvc,
	}
}

// Start 启动后台监控循环，每 10 秒评估一次切换规则
func (m *BackupMonitor) Start(ctx context.Context) {
	m.logger.Info("starting backup monitor")

	safego.Go("backup-monitor", func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				m.logger.Info("backup monitor stopped")
				return
			case <-ticker.C:
				safego.Run("backup-monitor-tick", func() { m.EvaluateRules(ctx) })
			}
		}
	})
}

// RecordMetric 记录请求指标到 Redis 滑动窗口，每次请求完成后调用
func (m *BackupMonitor) RecordMetric(channelGroupID uint, success bool, latencyMs int, statusCode int) {
	ctx := context.Background()
	now := time.Now()
	ts := float64(now.UnixMilli())
	member := fmt.Sprintf("%d:%d:%t:%d", now.UnixNano(), statusCode, success, latencyMs)

	// 1. 错误率: 将所有请求记录到有序集合（score = 时间戳）
	allKey := fmt.Sprintf("backup:metric:all:%d", channelGroupID)
	m.redis.ZAdd(ctx, allKey, goredis.Z{Score: ts, Member: member})
	m.redis.Expire(ctx, allKey, 10*time.Minute) // Keep 10 min of data

	if !success {
		// 错误请求
		errKey := fmt.Sprintf("backup:metric:errors:%d", channelGroupID)
		m.redis.ZAdd(ctx, errKey, goredis.Z{Score: ts, Member: member})
		m.redis.Expire(ctx, errKey, 10*time.Minute)

		// 2. 连续错误: INCR 计数器（成功时重置）
		consKey := fmt.Sprintf("backup:metric:cons_err:%d", channelGroupID)
		m.redis.Incr(ctx, consKey)
		m.redis.Expire(ctx, consKey, 10*time.Minute)
	} else {
		// 成功时重置连续错误计数
		consKey := fmt.Sprintf("backup:metric:cons_err:%d", channelGroupID)
		m.redis.Set(ctx, consKey, "0", 10*time.Minute)
	}

	// 3. 延迟: 存储在列表中（保留最近 200 条）
	latKey := fmt.Sprintf("backup:metric:latency:%d", channelGroupID)
	m.redis.LPush(ctx, latKey, latencyMs)
	m.redis.LTrim(ctx, latKey, 0, 199) // Keep last 200
	m.redis.Expire(ctx, latKey, 10*time.Minute)

	// 4. 超时跟踪
	if statusCode == 408 || latencyMs > 30000 {
		toKey := fmt.Sprintf("backup:metric:timeouts:%d", channelGroupID)
		m.redis.ZAdd(ctx, toKey, goredis.Z{Score: ts, Member: member})
		m.redis.Expire(ctx, toKey, 10*time.Minute)
	}

	// 5. 特定状态码跟踪（如 429）
	scKey := fmt.Sprintf("backup:metric:sc:%d:%d", channelGroupID, statusCode)
	m.redis.ZAdd(ctx, scKey, goredis.Z{Score: ts, Member: member})
	m.redis.Expire(ctx, scKey, 10*time.Minute)
}

// EvaluateRules 检查所有活跃的备份规则，触发阈值时执行切换
func (m *BackupMonitor) EvaluateRules(ctx context.Context) {
	var rules []model.BackupRule
	if err := m.db.WithContext(ctx).Where("is_active = ?", true).Find(&rules).Error; err != nil {
		m.logger.Error("failed to fetch backup rules", zap.Error(err))
		return
	}

	for _, rule := range rules {
		// 已切换的规则跳过评估
		switchKey := fmt.Sprintf("backup:active:%d", rule.PrimaryGroupID)
		val, _ := pkgredis.Get(ctx, switchKey)
		if val != "" {
			continue // Already switched, skip evaluation
		}

		triggered, reason := m.evaluateRule(ctx, &rule)
		if triggered {
			m.triggerSwitch(ctx, &rule, reason)
		}
	}
}

// evaluateRule 检查备份规则的所有切换条件是否触发
func (m *BackupMonitor) evaluateRule(ctx context.Context, rule *model.BackupRule) (bool, string) {
	if rule.SwitchRules == nil {
		return false, ""
	}

	var switchRules []SwitchRule
	if err := json.Unmarshal(rule.SwitchRules, &switchRules); err != nil {
		m.logger.Error("failed to parse switch_rules", zap.Uint("rule_id", rule.ID), zap.Error(err))
		return false, ""
	}

	for _, sr := range switchRules {
		triggered, reason := m.checkSwitchRule(ctx, rule.PrimaryGroupID, sr)
		if triggered {
			return true, reason
		}
	}

	return false, ""
}

// checkSwitchRule 评估单个切换规则
func (m *BackupMonitor) checkSwitchRule(ctx context.Context, groupID uint, rule SwitchRule) (bool, string) {
	switch rule.Type {
	case SwitchRuleConsecutiveErrors:
		return m.checkConsecutiveErrors(ctx, groupID, rule)
	case SwitchRuleErrorRate:
		return m.checkErrorRate(ctx, groupID, rule)
	case SwitchRuleLatency:
		return m.checkLatency(ctx, groupID, rule)
	case SwitchRuleTimeout:
		return m.checkTimeout(ctx, groupID, rule)
	case SwitchRuleStatusCode:
		return m.checkStatusCode(ctx, groupID, rule)
	default:
		return false, ""
	}
}

// checkConsecutiveErrors 检查连续错误数是否达到阈值
func (m *BackupMonitor) checkConsecutiveErrors(ctx context.Context, groupID uint, rule SwitchRule) (bool, string) {
	key := fmt.Sprintf("backup:metric:cons_err:%d", groupID)
	val, err := m.redis.Get(ctx, key).Result()
	if err != nil {
		return false, ""
	}

	count, _ := strconv.ParseFloat(val, 64)
	if count >= rule.Threshold {
		return true, fmt.Sprintf("consecutive_errors: %.0f >= %.0f", count, rule.Threshold)
	}
	return false, ""
}

// checkErrorRate 滑动窗口内错误率检查
func (m *BackupMonitor) checkErrorRate(ctx context.Context, groupID uint, rule SwitchRule) (bool, string) {
	windowMs := float64(rule.WindowSec * 1000)
	if windowMs <= 0 {
		windowMs = 60000 // Default 60 seconds
	}
	now := float64(time.Now().UnixMilli())
	min := now - windowMs

	allKey := fmt.Sprintf("backup:metric:all:%d", groupID)
	errKey := fmt.Sprintf("backup:metric:errors:%d", groupID)

	// 清理过期数据
	m.redis.ZRemRangeByScore(ctx, allKey, "-inf", fmt.Sprintf("%f", min))
	m.redis.ZRemRangeByScore(ctx, errKey, "-inf", fmt.Sprintf("%f", min))

	totalCount, _ := m.redis.ZCount(ctx, allKey, fmt.Sprintf("%f", min), "+inf").Result()
	errCount, _ := m.redis.ZCount(ctx, errKey, fmt.Sprintf("%f", min), "+inf").Result()

	if totalCount < 5 {
		return false, "" // Not enough samples
	}

	rate := float64(errCount) / float64(totalCount)
	if rate >= rule.Threshold {
		return true, fmt.Sprintf("error_rate: %.2f%% >= %.2f%% (window: %ds)", rate*100, rule.Threshold*100, rule.WindowSec)
	}
	return false, ""
}

// checkLatency 检查平均延迟是否达到阈值
func (m *BackupMonitor) checkLatency(ctx context.Context, groupID uint, rule SwitchRule) (bool, string) {
	key := fmt.Sprintf("backup:metric:latency:%d", groupID)

	// 获取最近 N 个值（使用 window_sec 作为样本数，默认 50）
	sampleCount := rule.WindowSec
	if sampleCount <= 0 || sampleCount > 200 {
		sampleCount = 50
	}

	vals, err := m.redis.LRange(ctx, key, 0, int64(sampleCount-1)).Result()
	if err != nil || len(vals) < 5 {
		return false, "" // Not enough samples
	}

	var total float64
	for _, v := range vals {
		f, _ := strconv.ParseFloat(v, 64)
		total += f
	}
	avg := total / float64(len(vals))

	if avg >= rule.Threshold {
		return true, fmt.Sprintf("latency: avg %.0fms >= %.0fms", avg, rule.Threshold)
	}
	return false, ""
}

// checkTimeout 滑动窗口内超时率检查
func (m *BackupMonitor) checkTimeout(ctx context.Context, groupID uint, rule SwitchRule) (bool, string) {
	windowMs := float64(rule.WindowSec * 1000)
	if windowMs <= 0 {
		windowMs = 60000
	}
	now := float64(time.Now().UnixMilli())
	min := now - windowMs

	toKey := fmt.Sprintf("backup:metric:timeouts:%d", groupID)
	allKey := fmt.Sprintf("backup:metric:all:%d", groupID)

	m.redis.ZRemRangeByScore(ctx, toKey, "-inf", fmt.Sprintf("%f", min))

	toCount, _ := m.redis.ZCount(ctx, toKey, fmt.Sprintf("%f", min), "+inf").Result()
	totalCount, _ := m.redis.ZCount(ctx, allKey, fmt.Sprintf("%f", min), "+inf").Result()

	if totalCount < 5 {
		return false, ""
	}

	rate := float64(toCount) / float64(totalCount)
	if rate >= rule.Threshold {
		return true, fmt.Sprintf("timeout_rate: %.2f%% >= %.2f%% (window: %ds)", rate*100, rule.Threshold*100, rule.WindowSec)
	}
	return false, ""
}

// checkStatusCode 滑动窗口内特定状态码计数检查
func (m *BackupMonitor) checkStatusCode(ctx context.Context, groupID uint, rule SwitchRule) (bool, string) {
	if rule.StatusCode == 0 {
		return false, ""
	}

	windowMs := float64(rule.WindowSec * 1000)
	if windowMs <= 0 {
		windowMs = 60000
	}
	now := float64(time.Now().UnixMilli())
	min := now - windowMs

	scKey := fmt.Sprintf("backup:metric:sc:%d:%d", groupID, rule.StatusCode)
	m.redis.ZRemRangeByScore(ctx, scKey, "-inf", fmt.Sprintf("%f", min))

	count, _ := m.redis.ZCount(ctx, scKey, fmt.Sprintf("%f", min), "+inf").Result()

	if float64(count) >= rule.Threshold {
		return true, fmt.Sprintf("status_code_%d: %d >= %.0f (window: %ds)", rule.StatusCode, count, rule.Threshold, rule.WindowSec)
	}
	return false, ""
}

// triggerSwitch 执行实际的备份组切换
func (m *BackupMonitor) triggerSwitch(ctx context.Context, rule *model.BackupRule, reason string) {
	backupIDs, err := m.parseBackupGroupIDs(rule)
	if err != nil || len(backupIDs) == 0 {
		m.logger.Error("no backup groups for auto-switch", zap.Uint("rule_id", rule.ID))
		return
	}

	lock, err := pkgredis.Lock(ctx, fmt.Sprintf("backup:switch:%d", rule.ID), 10*time.Second)
	if err != nil {
		return // Another instance is handling the switch
	}
	defer lock.Unlock(ctx)

	// 双重检查确保尚未切换
	switchKey := fmt.Sprintf("backup:active:%d", rule.PrimaryGroupID)
	val, _ := pkgredis.Get(ctx, switchKey)
	if val != "" {
		return
	}

	targetGroupID := backupIDs[0]

	m.backupSvc.setSwitchState(ctx, rule.PrimaryGroupID, targetGroupID, reason)
	m.backupSvc.recordEvent(ctx, rule.ID, "switch", rule.PrimaryGroupID, targetGroupID, reason)

	m.logger.Warn("backup auto-switch triggered",
		zap.Uint("rule_id", rule.ID),
		zap.Uint("from_group", rule.PrimaryGroupID),
		zap.Uint("to_group", targetGroupID),
		zap.String("reason", reason),
	)
}

// parseBackupGroupIDs 解析备份组 ID 列表
func (m *BackupMonitor) parseBackupGroupIDs(rule *model.BackupRule) ([]uint, error) {
	if rule.BackupGroupIDs == nil {
		return nil, nil
	}
	var ids []uint
	if err := json.Unmarshal(rule.BackupGroupIDs, &ids); err != nil {
		return nil, fmt.Errorf("failed to parse backup_group_ids: %w", err)
	}
	return ids, nil
}
