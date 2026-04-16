package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker/v2"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	pkgredis "tokenhub-server/internal/pkg/redis"
)

// ChannelRouter 渠道路由引擎，为模型请求选择最优渠道。
// 基于 CustomChannel 统一路由体系，支持显式路由和自动发现两种模式。
// 集成优先级/权重/最小负载策略，并配合熔断器保护和备份规则
type ChannelRouter struct {
	db       *gorm.DB
	redis    *goredis.Client
	breakers map[uint]*gobreaker.CircuitBreaker[interface{}]
	mu       sync.RWMutex
	logger   *zap.Logger

	// 每个分组的轮询计数器
	rrCounters map[uint]*atomic.Uint64
	rrMu       sync.RWMutex

	// 每个渠道的负载计数器（在途请求数）
	loadCounters map[uint]*atomic.Int64
	loadMu       sync.RWMutex

	groupSvc  *ChannelGroupService
	backupSvc *BackupService
}

// NewChannelRouter 创建渠道路由引擎实例
func NewChannelRouter(db *gorm.DB, redis *goredis.Client, groupSvc *ChannelGroupService, backupSvc *BackupService) *ChannelRouter {
	if db == nil {
		panic("ChannelRouter: db is nil")
	}
	return &ChannelRouter{
		db:           db,
		redis:        redis,
		breakers:     make(map[uint]*gobreaker.CircuitBreaker[interface{}]),
		logger:       logger.L,
		rrCounters:   make(map[uint]*atomic.Uint64),
		loadCounters: make(map[uint]*atomic.Int64),
		groupSvc:     groupSvc,
		backupSvc:    backupSvc,
	}
}

// SelectChannelResult 路由选择结果，包含选中的渠道和实际模型名
// ActualModel 是实际发送给供应商的模型ID（可能与用户请求的模型名不同）
type SelectChannelResult struct {
	Channel     *model.Channel // 选中的渠道
	ActualModel string         // 实际调用的模型名（别名映射后的供应商模型ID）
}

// RouteCandidate 路由候选，统一显式路由和自动发现的结果
type RouteCandidate struct {
	Channel     *model.Channel // 候选渠道
	ActualModel string         // 实际发送给供应商的模型ID
	Weight      int            // 路由权重
	Priority    int            // 路由优先级
}

// SelectChannel 统一路由入口（新版，基于 CustomChannel 体系）
// customChannelID: API Key 关联的自定义渠道（nil 则使用默认渠道）
// userID: 当前用户ID，用于访问控制检查
// 路由流程:
//  1. 加载 CustomChannel（通过 customChannelID 或 is_default=true）
//  2. 检查访问权限（visibility + access_list + userID）
//  3. 从显式路由中查找匹配的 CustomChannelRoute
//  4. 如果无匹配 + auto_route=true → 自动发现（ChannelModel + Supplier 成本排序）
//  5. 按 Strategy 选择最终 Channel + ActualModel
//  6. 返回选中的 Channel + ActualModel
func (r *ChannelRouter) SelectChannel(ctx context.Context, modelName string, customChannelID *uint, userID uint) (*SelectChannelResult, error) {
	return r.SelectChannelWithExcludes(ctx, modelName, customChannelID, userID, nil)
}

// SelectChannelWithExcludes 带排除列表的渠道选择，用于 Failover 重试时排除已失败的渠道
func (r *ChannelRouter) SelectChannelWithExcludes(ctx context.Context, modelName string, customChannelID *uint, userID uint, excludeChannelIDs []uint) (*SelectChannelResult, error) {
	if modelName == "" {
		return nil, fmt.Errorf("model name is required")
	}

	// 构建排除集合
	excludeSet := make(map[uint]bool, len(excludeChannelIDs))
	for _, id := range excludeChannelIDs {
		excludeSet[id] = true
	}

	// ========== 步骤1: 加载 CustomChannel ==========
	cc, err := r.loadCustomChannel(ctx, customChannelID)
	if err != nil {
		// 没有自定义渠道配置，降级到旧版渠道组路由
		r.logger.Debug("无自定义渠道，降级到渠道组路由",
			zap.String("model", modelName), zap.Error(err))
		return r.fallbackToChannelGroup(ctx, modelName)
	}

	// ========== 步骤2: 检查访问权限 ==========
	if !r.checkAccess(ctx, cc, userID) {
		return nil, fmt.Errorf("用户 %d 无权访问自定义渠道 %s", userID, cc.Name)
	}

	// ========== 步骤3: 从显式路由中查找匹配 ==========
	routes, err := r.selectFromCustomChannel(ctx, cc, modelName)
	if err == nil && len(routes) > 0 {
		// 将显式路由转换为统一候选列表
		candidates := r.filterExcluded(r.routesToCandidates(ctx, routes), excludeSet)
		if len(candidates) > 0 {
			result, err := r.applyStrategy(cc.Strategy, candidates, cc.ID)
			if err == nil {
				r.logger.Info("使用自定义渠道显式路由",
					zap.String("model", modelName),
					zap.String("actual_model", result.ActualModel),
					zap.Uint("channel_id", result.Channel.ID),
					zap.String("custom_channel", cc.Name))
				return result, nil
			}
		}
	}

	// ========== 步骤4: 自动发现路由（成本优先）==========
	if cc.AutoRoute {
		channelModels, err := r.autoDiscoverByCost(ctx, modelName)
		if err == nil && len(channelModels) > 0 {
			// 将自动发现结果转换为统一候选列表
			candidates := r.filterExcluded(r.channelModelsToCandidates(channelModels), excludeSet)
			if len(candidates) > 0 {
				result, err := r.applyStrategy(cc.Strategy, candidates, cc.ID)
				if err == nil {
					r.logger.Info("使用自动发现路由（成本优先）",
						zap.String("model", modelName),
						zap.String("actual_model", result.ActualModel),
						zap.Uint("channel_id", result.Channel.ID))
					return result, nil
				}
			}
		}
	}

	// ========== 步骤5: 最终降级到渠道组路由 ==========
	r.logger.Debug("自定义渠道未找到匹配，降级到渠道组路由", zap.String("model", modelName))
	return r.fallbackToChannelGroup(ctx, modelName)
}

// filterExcluded 从候选列表中过滤掉被排除的渠道ID
func (r *ChannelRouter) filterExcluded(candidates []RouteCandidate, excludeSet map[uint]bool) []RouteCandidate {
	if len(excludeSet) == 0 {
		return candidates
	}
	var filtered []RouteCandidate
	for _, c := range candidates {
		if !excludeSet[c.Channel.ID] {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// filterByCapability 过滤掉不支持指定能力的渠道
// requiredCap 为空时不做过滤（兼容调用方未传类型的场景）
func (r *ChannelRouter) filterByCapability(candidates []RouteCandidate, requiredCap string) []RouteCandidate {
	if requiredCap == "" {
		return candidates
	}
	var filtered []RouteCandidate
	for _, c := range candidates {
		if c.Channel != nil && c.Channel.HasCapability(requiredCap) {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// SelectChannelForCapability 带能力校验的渠道选择
// requiredCap: 模型所需能力（chat/image/video/tts/asr/embedding），空字符串兼容旧调用
func (r *ChannelRouter) SelectChannelForCapability(ctx context.Context, modelName string, customChannelID *uint, userID uint, requiredCap string, excludeChannelIDs []uint) (*SelectChannelResult, error) {
	// 先走原路由流程取候选，然后按能力过滤
	result, err := r.SelectChannelWithExcludes(ctx, modelName, customChannelID, userID, excludeChannelIDs)
	if err != nil {
		return nil, err
	}
	if requiredCap == "" || result == nil || result.Channel == nil {
		return result, nil
	}
	if !result.Channel.HasCapability(requiredCap) {
		return nil, fmt.Errorf("渠道 %s 不支持能力 %s（当前支持: %s）",
			result.Channel.Name, requiredCap, result.Channel.SupportedCapabilities)
	}
	return result, nil
}

// loadCustomChannel 加载自定义渠道配置（带 Redis 缓存，5min TTL）
// 如果指定了 customChannelID 则按 ID 查找，否则查找 is_default=true 的默认渠道
func (r *ChannelRouter) loadCustomChannel(ctx context.Context, customChannelID *uint) (*model.CustomChannel, error) {
	// 尝试从 Redis 缓存读取
	var cacheKey string
	if customChannelID != nil && *customChannelID > 0 {
		cacheKey = fmt.Sprintf("custom_channel:id:%d", *customChannelID)
	} else {
		cacheKey = "custom_channel:default"
	}

	if r.redis != nil {
		var cached model.CustomChannel
		if err := pkgredis.GetJSON(ctx, cacheKey, &cached); err == nil {
			return &cached, nil
		}
	}

	var cc model.CustomChannel

	if customChannelID != nil && *customChannelID > 0 {
		// 按指定 ID 加载
		err := r.db.WithContext(ctx).
			Where("id = ? AND is_active = ?", *customChannelID, true).
			First(&cc).Error
		if err != nil {
			return nil, fmt.Errorf("自定义渠道 %d 不存在或未激活: %w", *customChannelID, err)
		}
	} else {
		// 查找默认自定义渠道
		err := r.db.WithContext(ctx).
			Where("is_default = ? AND is_active = ?", true, true).
			First(&cc).Error
		if err != nil {
			return nil, fmt.Errorf("未找到默认自定义渠道: %w", err)
		}
	}

	// 写入 Redis 缓存
	if r.redis != nil {
		_ = pkgredis.SetJSON(ctx, cacheKey, &cc, 5*time.Minute)
	}

	return &cc, nil
}

// checkAccess 检查用户是否有权访问指定的自定义渠道
// visibility="all" 时所有用户都可访问
// visibility="specific" 时需在 access_list 中查找用户ID
func (r *ChannelRouter) checkAccess(ctx context.Context, cc *model.CustomChannel, userID uint) bool {
	// 所有人可见
	if cc.Visibility == "all" || cc.Visibility == "" {
		return true
	}

	// 仅指定用户可见，查询访问控制列表
	if cc.Visibility == "specific" {
		var count int64
		r.db.WithContext(ctx).
			Model(&model.CustomChannelAccess{}).
			Where("custom_channel_id = ? AND user_id = ?", cc.ID, userID).
			Count(&count)
		return count > 0
	}

	return false
}

// selectFromCustomChannel 从自定义渠道的显式路由中查找匹配（带 Redis 缓存，5min TTL）
// 查找 CustomChannelRoute WHERE custom_channel_id=? AND alias_model=? AND is_active=true
// 返回匹配的路由列表（可能有多条，对应不同渠道的同一模型）
func (r *ChannelRouter) selectFromCustomChannel(ctx context.Context, cc *model.CustomChannel, requestModel string) ([]model.CustomChannelRoute, error) {
	// 尝试从 Redis 缓存读取
	cacheKey := fmt.Sprintf("custom_channel_routes:%d:%s", cc.ID, requestModel)
	if r.redis != nil {
		var cached []model.CustomChannelRoute
		if err := pkgredis.GetJSON(ctx, cacheKey, &cached); err == nil && len(cached) > 0 {
			// Channel.APIKey 带 json:"-" 标签，缓存反序列化后会丢失
			// 重置 Channel 让 routesToCandidates() 从 DB 重新加载完整渠道信息
			for i := range cached {
				cached[i].Channel = model.Channel{}
			}
			return cached, nil
		}
	}

	var routes []model.CustomChannelRoute
	err := r.db.WithContext(ctx).
		Where("custom_channel_id = ? AND alias_model = ? AND is_active = ?", cc.ID, requestModel, true).
		Preload("Channel").
		Find(&routes).Error
	if err != nil {
		return nil, fmt.Errorf("查询自定义渠道路由失败: %w", err)
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("自定义渠道 %s 中没有模型 %s 的路由", cc.Name, requestModel)
	}

	// 写入 Redis 缓存
	if r.redis != nil {
		_ = pkgredis.SetJSON(ctx, cacheKey, &routes, 5*time.Minute)
	}

	return routes, nil
}

// routesToCandidates 将 CustomChannelRoute 列表转换为统一候选列表
// 过滤掉渠道不活跃或已熔断的候选项
func (r *ChannelRouter) routesToCandidates(ctx context.Context, routes []model.CustomChannelRoute) []RouteCandidate {
	var candidates []RouteCandidate
	for i := range routes {
		rt := &routes[i]
		// 确保渠道已预加载且处于活跃状态
		if rt.Channel.ID == 0 {
			// 渠道未预加载，手动查询
			var ch model.Channel
			if err := r.db.WithContext(ctx).Where("id = ? AND status = ?", rt.ChannelID, "active").First(&ch).Error; err != nil {
				continue // 渠道不可用，跳过
			}
			rt.Channel = ch
		} else if rt.Channel.Status != "active" {
			continue // 渠道不活跃，跳过
		}

		candidates = append(candidates, RouteCandidate{
			Channel:     &rt.Channel,
			ActualModel: rt.ActualModel,
			Weight:      rt.Weight,
			Priority:    rt.Priority,
		})
	}
	return candidates
}

// autoDiscoverByCost 自动发现路由（成本优先）
// 当自定义渠道中没有显式路由时，通过 ChannelModel 表自动查找支持该模型的渠道
// 流程:
//  1. 查询 ChannelModel WHERE standard_model_id=requestModel AND is_active=true
//  2. 预加载 Channel + Channel.Supplier
//  3. 计算成本 = (Supplier.InputPricePerM + Supplier.OutputPricePerM) * Supplier.Discount
//  4. 按成本升序排列
//  5. 过滤掉 Channel 不活跃的
//  6. 返回排序后的 ChannelModel 列表
func (r *ChannelRouter) autoDiscoverByCost(ctx context.Context, requestModel string) ([]model.ChannelModel, error) {
	var channelModels []model.ChannelModel
	err := r.db.WithContext(ctx).
		Where("standard_model_id = ? AND is_active = ?", requestModel, true).
		Preload("Channel").
		Preload("Channel.Supplier").
		Find(&channelModels).Error
	if err != nil {
		return nil, fmt.Errorf("自动发现路由查询失败: %w", err)
	}

	if len(channelModels) == 0 {
		return nil, fmt.Errorf("没有找到支持模型 %s 的渠道", requestModel)
	}

	// 过滤掉渠道不活跃的
	var active []model.ChannelModel
	for _, cm := range channelModels {
		if cm.Channel.Status == "active" {
			active = append(active, cm)
		}
	}

	if len(active) == 0 {
		return nil, fmt.Errorf("模型 %s 没有活跃的渠道", requestModel)
	}

	// 按成本升序排列: 成本 = (InputPricePerM + OutputPricePerM) * Discount
	sort.Slice(active, func(i, j int) bool {
		costI := (active[i].Channel.Supplier.InputPricePerM + active[i].Channel.Supplier.OutputPricePerM) * active[i].Channel.Supplier.Discount
		costJ := (active[j].Channel.Supplier.InputPricePerM + active[j].Channel.Supplier.OutputPricePerM) * active[j].Channel.Supplier.Discount
		return costI < costJ
	})

	return active, nil
}

// channelModelsToCandidates 将 ChannelModel 列表转换为统一候选列表
func (r *ChannelRouter) channelModelsToCandidates(channelModels []model.ChannelModel) []RouteCandidate {
	var candidates []RouteCandidate
	for i := range channelModels {
		cm := &channelModels[i]
		ch := cm.Channel
		candidates = append(candidates, RouteCandidate{
			Channel:     &ch,
			ActualModel: cm.VendorModelID, // 使用供应商特定的模型ID
			Weight:      ch.Weight,
			Priority:    ch.Priority,
		})
	}
	return candidates
}

// applyStrategy 按策略从候选列表中选择最终的 Channel
// 支持: weighted/priority/round_robin/least_load/cost_first
// 复用现有的熔断器和负载跟踪机制
func (r *ChannelRouter) applyStrategy(strategy string, candidates []RouteCandidate, groupKey uint) (*SelectChannelResult, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("候选列表为空")
	}

	// 过滤已触发熔断的候选
	var available []RouteCandidate
	for _, c := range candidates {
		breaker := r.getOrCreateBreaker(c.Channel.ID)
		if breaker.State() != gobreaker.StateOpen {
			available = append(available, c)
		}
	}

	if len(available) == 0 {
		return nil, fmt.Errorf("所有候选渠道均已熔断")
	}

	var selected *RouteCandidate
	switch strategy {
	case "weighted":
		selected = r.selectCandidateByWeight(available)
	case "priority":
		selected = r.selectCandidateByPriority(available)
	case "round_robin":
		selected = r.selectCandidateByRoundRobin(available, groupKey)
	case "least_load":
		selected = r.selectCandidateByLeastLoad(available)
	case "cost_first":
		// 成本优先: 候选已按成本排序，直接取第一个
		selected = &available[0]
	default:
		selected = r.selectCandidateByWeight(available)
	}

	if selected == nil {
		return nil, fmt.Errorf("策略 %s 选择失败", strategy)
	}

	// 增加负载计数
	r.IncrementLoad(selected.Channel.ID)

	return &SelectChannelResult{
		Channel:     selected.Channel,
		ActualModel: selected.ActualModel,
	}, nil
}

// ======================== 策略选择方法（基于 RouteCandidate）========================

// selectCandidateByWeight 加权随机选择候选
// 权重越大被选中的概率越高，权重<=0时默认为1
func (r *ChannelRouter) selectCandidateByWeight(candidates []RouteCandidate) *RouteCandidate {
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) == 1 {
		return &candidates[0]
	}

	// 计算总权重
	totalWeight := 0
	for _, c := range candidates {
		w := c.Weight
		if w <= 0 {
			w = 1
		}
		totalWeight += w
	}

	// 加权随机选择
	rnd := rand.Intn(totalWeight)
	cumulative := 0
	for i := range candidates {
		w := candidates[i].Weight
		if w <= 0 {
			w = 1
		}
		cumulative += w
		if rnd < cumulative {
			return &candidates[i]
		}
	}
	return &candidates[0]
}

// selectCandidateByPriority 按优先级选择候选
// 在最高优先级组内使用加权随机
func (r *ChannelRouter) selectCandidateByPriority(candidates []RouteCandidate) *RouteCandidate {
	if len(candidates) == 0 {
		return nil
	}

	// 按优先级降序排列（数字大优先）
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Priority > candidates[j].Priority
	})

	// 提取最高优先级组
	maxPriority := candidates[0].Priority
	var topGroup []RouteCandidate
	for _, c := range candidates {
		if c.Priority == maxPriority {
			topGroup = append(topGroup, c)
		} else {
			break
		}
	}

	// 在最高优先级组内加权选择
	return r.selectCandidateByWeight(topGroup)
}

// selectCandidateByRoundRobin 轮询选择候选
// 使用 groupKey 作为计数器 key，保证同一分组的轮询独立
func (r *ChannelRouter) selectCandidateByRoundRobin(candidates []RouteCandidate, groupKey uint) *RouteCandidate {
	if len(candidates) == 0 {
		return nil
	}

	// 使用 groupKey + 偏移量（避免与渠道组ID冲突）作为轮询计数器key
	rrKey := groupKey + 2000000 // 偏移量避免与旧渠道组/混合渠道ID冲突
	r.rrMu.Lock()
	if _, ok := r.rrCounters[rrKey]; !ok {
		r.rrCounters[rrKey] = &atomic.Uint64{}
	}
	counter := r.rrCounters[rrKey]
	r.rrMu.Unlock()

	idx := counter.Add(1) - 1
	return &candidates[idx%uint64(len(candidates))]
}

// selectCandidateByLeastLoad 选择在途请求最少的候选
// 基于每个候选 Channel 的负载计数器
func (r *ChannelRouter) selectCandidateByLeastLoad(candidates []RouteCandidate) *RouteCandidate {
	if len(candidates) == 0 {
		return nil
	}

	var best *RouteCandidate
	var bestLoad int64 = 1<<63 - 1

	r.loadMu.RLock()
	defer r.loadMu.RUnlock()

	for i := range candidates {
		var load int64
		if counter, ok := r.loadCounters[candidates[i].Channel.ID]; ok {
			load = counter.Load()
		}
		if load < bestLoad {
			bestLoad = load
			best = &candidates[i]
		}
	}

	return best
}

// ======================== 渠道组降级路由（兼容旧数据）========================

// fallbackToChannelGroup 降级到基于 ChannelGroup 的旧版路由
// 当没有可用的 CustomChannel 配置时使用此路径
func (r *ChannelRouter) fallbackToChannelGroup(ctx context.Context, modelName string) (*SelectChannelResult, error) {
	// 步骤1: 查找匹配的渠道组
	groups, err := r.findMatchingGroups(ctx, modelName)
	if err != nil || len(groups) == 0 {
		return nil, fmt.Errorf("no channel group found for model: %s", modelName)
	}

	// 步骤2: 检查备份规则 — 确定当前活跃组
	activeGroup := groups[0]
	if r.backupSvc != nil {
		switched, err := r.checkBackupSwitch(ctx, &activeGroup, modelName)
		if err == nil && switched != nil {
			activeGroup = *switched
		}
	}

	// 步骤3: 根据混合模式解析渠道
	channels, err := r.resolveChannels(ctx, &activeGroup, modelName)
	if err != nil || len(channels) == 0 {
		return nil, fmt.Errorf("no available channels in group %s for model %s", activeGroup.Code, modelName)
	}

	// 步骤4: 使用策略选择渠道 + 熔断器检查
	selected, err := r.selectWithBreaker(ctx, &activeGroup, channels, modelName)
	if err != nil {
		return nil, err
	}

	return &SelectChannelResult{Channel: selected, ActualModel: modelName}, nil
}

// ======================== 负载跟踪 ========================

// RecordResult 记录请求结果，用于熔断器和负载跟踪
func (r *ChannelRouter) RecordResult(channelID uint, success bool, latencyMs int, statusCode int) {
	// 减少负载计数器
	r.loadMu.RLock()
	if counter, ok := r.loadCounters[channelID]; ok {
		counter.Add(-1)
	}
	r.loadMu.RUnlock()

	if !success {
		r.logger.Warn("channel request failed",
			zap.Uint("channel_id", channelID),
			zap.Int("status_code", statusCode),
			zap.Int("latency_ms", latencyMs),
		)
	}
}

// IncrementLoad 增加渠道的在途请求计数
func (r *ChannelRouter) IncrementLoad(channelID uint) {
	r.loadMu.Lock()
	if _, ok := r.loadCounters[channelID]; !ok {
		r.loadCounters[channelID] = &atomic.Int64{}
	}
	r.loadCounters[channelID].Add(1)
	r.loadMu.Unlock()
}

// ======================== 渠道组辅助方法 ========================

// findMatchingGroups 查找包含支持指定模型的渠道组
func (r *ChannelRouter) findMatchingGroups(ctx context.Context, modelName string) ([]model.ChannelGroup, error) {
	var groups []model.ChannelGroup
	err := r.db.WithContext(ctx).
		Where("is_active = ?", true).
		Order("id ASC").
		Find(&groups).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query channel groups: %w", err)
	}

	// 过滤出包含支持该模型的渠道组
	var matched []model.ChannelGroup
	for _, g := range groups {
		channels, err := r.groupSvc.GetChannels(ctx, g.ID)
		if err != nil {
			continue
		}
		for _, ch := range channels {
			if r.channelSupportsModel(ch, modelName) {
				matched = append(matched, g)
				break
			}
		}
	}

	return matched, nil
}

// channelSupportsModel 检查渠道是否支持指定模型
func (r *ChannelRouter) channelSupportsModel(ch model.Channel, modelName string) bool {
	if ch.Models == nil {
		return false
	}
	var models []string
	if err := json.Unmarshal(ch.Models, &models); err != nil {
		return false
	}
	for _, m := range models {
		if m == modelName || m == "*" {
			return true
		}
	}
	return false
}

// checkBackupSwitch 检查是否需要切换到备份渠道组
func (r *ChannelRouter) checkBackupSwitch(ctx context.Context, primary *model.ChannelGroup, modelName string) (*model.ChannelGroup, error) {
	// 从 Redis 检查是否有活跃的备份切换状态
	statusKey := fmt.Sprintf("backup:active:%d", primary.ID)
	val, err := pkgredis.Get(ctx, statusKey)
	if err != nil || val == "" {
		return nil, nil // 没有活跃的备份切换
	}

	var backupGroupID uint
	if err := json.Unmarshal([]byte(val), &backupGroupID); err != nil || backupGroupID == 0 {
		return nil, nil
	}

	backupGroup, err := r.groupSvc.GetByID(ctx, backupGroupID)
	if err != nil {
		return nil, err
	}

	r.logger.Info("备份规则激活，切换到备份渠道组",
		zap.Uint("primary_group_id", primary.ID),
		zap.Uint("backup_group_id", backupGroupID),
	)

	return backupGroup, nil
}

// resolveChannels 根据混合模式解析渠道列表
func (r *ChannelRouter) resolveChannels(ctx context.Context, group *model.ChannelGroup, modelName string) ([]model.Channel, error) {
	switch group.MixMode {
	case "SPLIT_BY_MODEL":
		return r.resolveSplitByModel(ctx, group, modelName)
	default:
		// SINGLE, FALLBACK_CHAIN, TAG_BASED 均解析为渠道列表
		channels, err := r.groupSvc.GetChannels(ctx, group.ID)
		if err != nil {
			return nil, err
		}
		// 过滤支持该模型的渠道
		var filtered []model.Channel
		for _, ch := range channels {
			if r.channelSupportsModel(ch, modelName) {
				filtered = append(filtered, ch)
			}
		}
		return filtered, nil
	}
}

// resolveSplitByModel 使用 MixConfig 查找模型对应的特定渠道
func (r *ChannelRouter) resolveSplitByModel(ctx context.Context, group *model.ChannelGroup, modelName string) ([]model.Channel, error) {
	if group.MixConfig == nil {
		return nil, fmt.Errorf("mix_config is required for SPLIT_BY_MODEL mode")
	}

	// MixConfig 格式: {"model_mapping": {"gpt-4": 1, "gpt-3.5-turbo": 2}}
	var cfg struct {
		ModelMapping map[string]uint `json:"model_mapping"`
	}
	if err := json.Unmarshal(group.MixConfig, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse mix_config: %w", err)
	}

	channelID, ok := cfg.ModelMapping[modelName]
	if !ok {
		return nil, fmt.Errorf("no channel mapping for model %s in SPLIT_BY_MODEL", modelName)
	}

	var ch model.Channel
	if err := r.db.WithContext(ctx).Where("id = ? AND status = ?", channelID, "active").Preload("Tags").First(&ch).Error; err != nil {
		return nil, fmt.Errorf("mapped channel %d not available: %w", channelID, err)
	}

	return []model.Channel{ch}, nil
}

// selectWithBreaker 使用分组策略选择渠道，同时检查熔断器状态
func (r *ChannelRouter) selectWithBreaker(ctx context.Context, group *model.ChannelGroup, channels []model.Channel, modelName string) (*model.Channel, error) {
	if group.MixMode == "FALLBACK_CHAIN" {
		return r.selectFallbackChain(ctx, channels)
	}

	// 过滤已触发熔断的渠道
	var available []model.Channel
	for _, ch := range channels {
		breaker := r.getOrCreateBreaker(ch.ID)
		state := breaker.State()
		if state != gobreaker.StateOpen {
			available = append(available, ch)
		}
	}

	if len(available) == 0 {
		return nil, fmt.Errorf("all channels are circuit-broken for model %s", modelName)
	}

	var selected *model.Channel
	switch group.Strategy {
	case "Priority":
		selected = r.selectByPriority(available)
	case "Weight":
		selected = r.selectByWeight(available)
	case "RoundRobin":
		selected = r.selectByRoundRobin(available, group.ID)
	case "LeastLoad":
		selected = r.selectByLeastLoad(available)
	case "CostFirst":
		selected = r.selectByCostFirst(available, modelName)
	default:
		selected = r.selectByPriority(available)
	}

	if selected == nil {
		return nil, fmt.Errorf("failed to select channel")
	}

	r.IncrementLoad(selected.ID)
	return selected, nil
}

// selectFallbackChain 按顺序尝试渠道，跳过已熔断的
func (r *ChannelRouter) selectFallbackChain(ctx context.Context, channels []model.Channel) (*model.Channel, error) {
	for i := range channels {
		breaker := r.getOrCreateBreaker(channels[i].ID)
		if breaker.State() != gobreaker.StateOpen {
			r.IncrementLoad(channels[i].ID)
			return &channels[i], nil
		}
	}
	return nil, fmt.Errorf("all channels in fallback chain are circuit-broken")
}

// selectByPriority 按优先级分组，在最高优先级组内按权重选择
func (r *ChannelRouter) selectByPriority(channels []model.Channel) *model.Channel {
	if len(channels) == 0 {
		return nil
	}

	// 按优先级降序排列
	sort.Slice(channels, func(i, j int) bool {
		return channels[i].Priority > channels[j].Priority
	})

	// 获取最高优先级的所有渠道
	maxPriority := channels[0].Priority
	var topGroup []model.Channel
	for _, ch := range channels {
		if ch.Priority == maxPriority {
			topGroup = append(topGroup, ch)
		} else {
			break
		}
	}

	// 在最高优先级组内按权重选择
	return r.selectByWeight(topGroup)
}

// selectByWeight 加权随机选择渠道
func (r *ChannelRouter) selectByWeight(channels []model.Channel) *model.Channel {
	if len(channels) == 0 {
		return nil
	}
	if len(channels) == 1 {
		return &channels[0]
	}

	totalWeight := 0
	for _, ch := range channels {
		w := ch.Weight
		if w <= 0 {
			w = 1
		}
		totalWeight += w
	}

	rnd := rand.Intn(totalWeight)
	cumulative := 0
	for i := range channels {
		w := channels[i].Weight
		if w <= 0 {
			w = 1
		}
		cumulative += w
		if rnd < cumulative {
			return &channels[i]
		}
	}

	return &channels[0]
}

// selectByRoundRobin 按分组轮询选择渠道
func (r *ChannelRouter) selectByRoundRobin(channels []model.Channel, groupID uint) *model.Channel {
	if len(channels) == 0 {
		return nil
	}

	r.rrMu.Lock()
	if _, ok := r.rrCounters[groupID]; !ok {
		r.rrCounters[groupID] = &atomic.Uint64{}
	}
	counter := r.rrCounters[groupID]
	r.rrMu.Unlock()

	idx := counter.Add(1) - 1
	return &channels[idx%uint64(len(channels))]
}

// selectByLeastLoad 选择在途请求最少的渠道
func (r *ChannelRouter) selectByLeastLoad(channels []model.Channel) *model.Channel {
	if len(channels) == 0 {
		return nil
	}

	var best *model.Channel
	var bestLoad int64 = 1<<63 - 1

	r.loadMu.RLock()
	defer r.loadMu.RUnlock()

	for i := range channels {
		var load int64
		if counter, ok := r.loadCounters[channels[i].ID]; ok {
			load = counter.Load()
		}
		if load < bestLoad {
			bestLoad = load
			best = &channels[i]
		}
	}

	return best
}

// selectByCostFirst 优先选择成本最低的渠道
func (r *ChannelRouter) selectByCostFirst(channels []model.Channel, modelName string) *model.Channel {
	if len(channels) == 0 {
		return nil
	}
	// 成本优先策略：当前回退为优先级选择
	return r.selectByPriority(channels)
}

// ======================== 熔断器 ========================

// getOrCreateBreaker 获取或创建渠道的熔断器实例
func (r *ChannelRouter) getOrCreateBreaker(channelID uint) *gobreaker.CircuitBreaker[interface{}] {
	r.mu.RLock()
	if cb, ok := r.breakers[channelID]; ok {
		r.mu.RUnlock()
		return cb
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	// 双重检查锁定
	if cb, ok := r.breakers[channelID]; ok {
		return cb
	}

	settings := gobreaker.Settings{
		Name:        fmt.Sprintf("channel-%d", channelID),
		MaxRequests: 3,                // 半开状态允许 3 个请求
		Interval:    30 * time.Second, // 关闭状态每 30s 重置计数
		Timeout:     60 * time.Second, // 开启状态保持 60s 后进入半开
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			// 失败率 > 50%（至少 5 个请求），或连续失败 >= 5 次时触发熔断
			if counts.ConsecutiveFailures >= 5 {
				return true
			}
			if counts.Requests >= 5 {
				failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
				return failureRatio > 0.5
			}
			return false
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			r.logger.Warn("熔断器状态变更",
				zap.String("name", name),
				zap.String("from", from.String()),
				zap.String("to", to.String()),
			)
			// 将熔断器状态同步到 Redis 实现分布式可见性
			if r.redis != nil {
				ctx := context.Background()
				key := fmt.Sprintf("breaker:state:%d", channelID)
				_ = pkgredis.Set(ctx, key, to.String(), 5*time.Minute)
			}
		},
	}

	cb := gobreaker.NewCircuitBreaker[interface{}](settings)
	r.breakers[channelID] = cb
	return cb
}

// Execute 将函数调用包装在熔断器保护中执行
func (r *ChannelRouter) Execute(channelID uint, fn func() (interface{}, error)) (interface{}, error) {
	cb := r.getOrCreateBreaker(channelID)
	return cb.Execute(fn)
}
