// Package coding 提供 Coding Plan 代码补全服务
// 负责管理 Coding 类型渠道的路由、负载均衡和 Fallback 策略
package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/provider"
)

// CodingService Coding Plan 代码补全服务
// 管理 Coding 类型渠道的选择、负载均衡和故障转移
type CodingService struct {
	db     *gorm.DB
	logger *zap.Logger

	// 轮询计数器（用于 RoundRobin 策略）
	rrCounter atomic.Uint64
	// 渠道健康状态追踪
	healthMu    sync.RWMutex
	healthState map[uint]*channelHealth
}

// channelHealth 渠道健康状态
type channelHealth struct {
	consecutiveFailures int       // 连续失败次数
	lastFailure         time.Time // 最后失败时间
	isCircuitOpen       bool      // 熔断器是否打开
	circuitOpenTime     time.Time // 熔断器打开时间
}

const (
	// maxConsecutiveFailures 连续失败次数阈值，超过后触发熔断
	maxConsecutiveFailures = 3
	// circuitOpenDuration 熔断器打开持续时间（60秒后自动半开）
	circuitOpenDuration = 60 * time.Second
)

// NewCodingService 创建 Coding Plan 服务实例
func NewCodingService(db *gorm.DB) *CodingService {
	return &CodingService{
		db:          db,
		logger:      logger.L,
		healthState: make(map[uint]*channelHealth),
	}
}

// SelectCodingChannel 从 Coding 类型渠道中选择一个可用渠道
// 支持混合渠道负载均衡（轮询），带 Fallback 策略
func (s *CodingService) SelectCodingChannel(ctx context.Context, modelName string) (*model.Channel, error) {
	// 查询所有 Coding 或 MIXED 类型的活跃渠道
	var channels []model.Channel
	err := s.db.WithContext(ctx).
		Where("status = ? AND (channel_type = ? OR channel_type = ?)", "active", "CODING", "MIXED").
		Order("priority DESC, weight DESC").
		Find(&channels).Error
	if err != nil {
		return nil, fmt.Errorf("查询 Coding 渠道失败: %w", err)
	}

	if len(channels) == 0 {
		return nil, fmt.Errorf("没有可用的 Coding 渠道")
	}

	// 过滤支持指定模型的渠道
	var matched []model.Channel
	for _, ch := range channels {
		if s.channelSupportsModel(ch, modelName) {
			matched = append(matched, ch)
		}
	}

	// 如果没有精确匹配的渠道，尝试使用通配符渠道
	if len(matched) == 0 {
		for _, ch := range channels {
			if s.channelSupportsWildcard(ch) {
				matched = append(matched, ch)
			}
		}
	}

	if len(matched) == 0 {
		return nil, fmt.Errorf("没有支持模型 %s 的 Coding 渠道", modelName)
	}

	// 过滤掉熔断器打开的渠道
	var available []model.Channel
	for _, ch := range matched {
		if !s.isCircuitOpen(ch.ID) {
			available = append(available, ch)
		}
	}

	// 所有渠道都熔断了，尝试使用最早熔断的渠道（半开状态）
	if len(available) == 0 {
		s.logger.Warn("所有 Coding 渠道均已熔断，尝试半开恢复",
			zap.String("model", modelName))
		available = matched // 降级使用所有渠道
	}

	// 使用加权轮询选择渠道
	selected := s.selectByWeightedRoundRobin(available)
	if selected == nil {
		return nil, fmt.Errorf("Coding 渠道选择失败")
	}

	return selected, nil
}

// RecordSuccess 记录渠道请求成功，重置健康状态
func (s *CodingService) RecordSuccess(channelID uint) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()

	// 成功后重置失败计数和熔断器
	if h, ok := s.healthState[channelID]; ok {
		h.consecutiveFailures = 0
		h.isCircuitOpen = false
	}
}

// RecordFailure 记录渠道请求失败，更新健康状态
// 连续失败超过阈值时触发熔断
func (s *CodingService) RecordFailure(channelID uint) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()

	h, ok := s.healthState[channelID]
	if !ok {
		h = &channelHealth{}
		s.healthState[channelID] = h
	}

	h.consecutiveFailures++
	h.lastFailure = time.Now()

	// 连续失败超过阈值，触发熔断
	if h.consecutiveFailures >= maxConsecutiveFailures {
		h.isCircuitOpen = true
		h.circuitOpenTime = time.Now()
		s.logger.Warn("Coding 渠道熔断器打开",
			zap.Uint("channel_id", channelID),
			zap.Int("consecutive_failures", h.consecutiveFailures))
	}
}

// isCircuitOpen 检查渠道的熔断器是否打开
// 熔断器打开后经过指定时间自动进入半开状态
func (s *CodingService) isCircuitOpen(channelID uint) bool {
	s.healthMu.RLock()
	defer s.healthMu.RUnlock()

	h, ok := s.healthState[channelID]
	if !ok {
		return false
	}

	if !h.isCircuitOpen {
		return false
	}

	// 熔断器超时后自动进入半开状态
	if time.Since(h.circuitOpenTime) > circuitOpenDuration {
		return false
	}

	return true
}

// selectByWeightedRoundRobin 加权轮询选择渠道
func (s *CodingService) selectByWeightedRoundRobin(channels []model.Channel) *model.Channel {
	if len(channels) == 0 {
		return nil
	}
	if len(channels) == 1 {
		return &channels[0]
	}

	// 计算总权重
	totalWeight := 0
	for _, ch := range channels {
		w := ch.Weight
		if w <= 0 {
			w = 1
		}
		totalWeight += w
	}

	// 使用轮询计数器 + 权重选择
	idx := s.rrCounter.Add(1) - 1
	target := int(idx) % totalWeight

	cumulative := 0
	for i := range channels {
		w := channels[i].Weight
		if w <= 0 {
			w = 1
		}
		cumulative += w
		if target < cumulative {
			return &channels[i]
		}
	}

	return &channels[0]
}

// SelectWithFallback 带 Fallback 的渠道选择和请求执行
// 主渠道失败时自动切换到备用渠道
func (s *CodingService) SelectWithFallback(
	ctx context.Context,
	modelName string,
	execFn func(ch *model.Channel) error,
) (*model.Channel, error) {
	// 查询所有 Coding 渠道
	var channels []model.Channel
	err := s.db.WithContext(ctx).
		Where("status = ? AND (channel_type = ? OR channel_type = ?)", "active", "CODING", "MIXED").
		Order("priority DESC, weight DESC").
		Find(&channels).Error
	if err != nil {
		return nil, fmt.Errorf("查询 Coding 渠道失败: %w", err)
	}

	// 过滤支持该模型的渠道
	var matched []model.Channel
	for _, ch := range channels {
		if s.channelSupportsModel(ch, modelName) || s.channelSupportsWildcard(ch) {
			matched = append(matched, ch)
		}
	}

	if len(matched) == 0 {
		return nil, fmt.Errorf("没有支持模型 %s 的 Coding 渠道", modelName)
	}

	// 打乱非首选渠道的顺序（保持优先级最高的在前面，其余随机）
	if len(matched) > 1 {
		rest := matched[1:]
		rand.Shuffle(len(rest), func(i, j int) {
			rest[i], rest[j] = rest[j], rest[i]
		})
	}

	// 依次尝试每个渠道
	var lastErr error
	for _, ch := range matched {
		if s.isCircuitOpen(ch.ID) {
			continue
		}

		err := execFn(&ch)
		if err == nil {
			s.RecordSuccess(ch.ID)
			return &ch, nil
		}

		s.logger.Warn("Coding 渠道请求失败，尝试 Fallback",
			zap.Uint("channel_id", ch.ID),
			zap.String("channel_name", ch.Name),
			zap.Error(err))
		s.RecordFailure(ch.ID)
		lastErr = err
	}

	return nil, fmt.Errorf("所有 Coding 渠道均失败: %w", lastErr)
}

// ListCodingChannels 列出所有 Coding 类型渠道
func (s *CodingService) ListCodingChannels(ctx context.Context) ([]model.Channel, error) {
	var channels []model.Channel
	err := s.db.WithContext(ctx).
		Where("channel_type IN ?", []string{"CODING", "MIXED"}).
		Preload("Supplier").
		Order("priority DESC, id ASC").
		Find(&channels).Error
	if err != nil {
		return nil, fmt.Errorf("查询 Coding 渠道列表失败: %w", err)
	}
	return channels, nil
}

// UpdateCodingChannel 更新 Coding 渠道配置
func (s *CodingService) UpdateCodingChannel(ctx context.Context, id uint, updates map[string]interface{}) error {
	return s.db.WithContext(ctx).Model(&model.Channel{}).Where("id = ?", id).Updates(updates).Error
}

// channelSupportsModel 检查渠道是否支持指定模型
func (s *CodingService) channelSupportsModel(ch model.Channel, modelName string) bool {
	if ch.Models == nil {
		return false
	}
	var models []string
	if err := json.Unmarshal(ch.Models, &models); err != nil {
		return false
	}
	for _, m := range models {
		if m == modelName {
			return true
		}
	}
	return false
}

// channelSupportsWildcard 检查渠道是否配置了通配符模型
func (s *CodingService) channelSupportsWildcard(ch model.Channel) bool {
	if ch.Models == nil {
		return false
	}
	var models []string
	if err := json.Unmarshal(ch.Models, &models); err != nil {
		return false
	}
	for _, m := range models {
		if m == "*" {
			return true
		}
	}
	return false
}

// CreateProviderForChannel 根据渠道配置创建对应的 Provider 实例
// 根据渠道 Type 字段动态匹配适配器
func (s *CodingService) CreateProviderForChannel(ch *model.Channel) provider.Provider {
	cfg := provider.ProviderConfig{
		APIKey:  ch.APIKey,
		BaseURL: ch.Endpoint,
		Timeout: 120,
	}

	// 根据渠道端点特征自动识别提供商类型
	switch {
	case containsStr(ch.Endpoint, "dashscope.aliyuncs.com"):
		return provider.NewCodingAlibabaProvider(cfg)
	case containsStr(ch.Endpoint, "volces.com") || containsStr(ch.Endpoint, "volcengine"):
		return provider.NewCodingVolcengineProvider(cfg)
	case containsStr(ch.Endpoint, "deepseek.com"):
		return provider.NewCodingDeepSeekProvider(cfg)
	default:
		// 默认使用 OpenAI 兼容协议
		return provider.NewQwenProvider(cfg)
	}
}

// containsStr 检查字符串是否包含子串
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && contains(s, substr))
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
