package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	pkgredis "tokenhub-server/internal/pkg/redis"
)

// jsonReader 将 JSON 字符串包装为 io.Reader
func jsonReader(s string) io.Reader {
	return strings.NewReader(s)
}

// buildChatURL 根据渠道 endpoint 构建 chat/completions 请求 URL。
//
// 不同供应商的 endpoint 约定不同：
//   - OpenAI: https://api.openai.com          → 需补 /v1/chat/completions
//   - Alibaba: https://dashscope.../v1        → 直接补 /chat/completions
//   - Volcengine: https://ark.../api/v3       → 直接补 /chat/completions
//   - Qianfan V2: https://qianfan.../v2       → 直接补 /chat/completions
//
// 规则：endpoint 尾部已有 /vN 版本段时，直接追加 /chat/completions；
// 否则补全 /v1/chat/completions（兼容原 OpenAI 约定）。
func buildChatURL(endpoint string) string {
	e := strings.TrimRight(endpoint, "/")
	// 检查最后一段是否为版本号（/v 后跟一个或多个数字）
	lastSlash := strings.LastIndex(e, "/")
	if lastSlash >= 0 {
		seg := e[lastSlash+1:]
		if len(seg) >= 2 && seg[0] == 'v' {
			allDigits := true
			for _, c := range seg[1:] {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				return e + "/chat/completions"
			}
		}
	}
	return e + "/v1/chat/completions"
}

// ChannelTestResult 渠道连通性测试结果
type ChannelTestResult struct {
	ChannelID  uint   `json:"channel_id"`
	Success    bool   `json:"success"`
	LatencyMs  int64  `json:"latency_ms"`
	StatusCode int    `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ChannelService 渠道服务，提供渠道的增删改查操作
type ChannelService struct {
	db    *gorm.DB
	redis *goredis.Client
}

// NewChannelService 创建渠道服务实例
func NewChannelService(db *gorm.DB, redis *goredis.Client) *ChannelService {
	if db == nil {
		panic("ChannelService: db is nil")
	}
	return &ChannelService{db: db, redis: redis}
}

// Create 创建新的渠道记录，校验名称、端点和供应商 ID
func (s *ChannelService) Create(ctx context.Context, channel *model.Channel) error {
	if channel == nil {
		return fmt.Errorf("channel is nil")
	}
	if channel.Name == "" {
		return fmt.Errorf("channel name is required")
	}
	if channel.Endpoint == "" {
		return fmt.Errorf("channel endpoint is required")
	}
	if channel.SupplierID == 0 {
		return fmt.Errorf("supplier_id is required")
	}

	// 默认值设置
	if channel.Status == "" {
		channel.Status = "unverified" // 新建渠道默认未验证状态
	}
	channel.Verified = false // 新建渠道默认未验证
	if channel.Weight <= 0 {
		channel.Weight = 1
	}
	if channel.MaxConcurrency <= 0 {
		channel.MaxConcurrency = 100
	}
	if channel.QPM <= 0 {
		channel.QPM = 60
	}

	if err := s.db.WithContext(ctx).Create(channel).Error; err != nil {
		return fmt.Errorf("failed to create channel: %w", err)
	}

	s.invalidateCache(ctx)
	logger.L.Info("channel created", zap.Uint("id", channel.ID), zap.String("name", channel.Name))
	return nil
}

// Update 根据 ID 更新渠道信息，使用分布式锁防止并发冲突
func (s *ChannelService) Update(ctx context.Context, id uint, updates map[string]interface{}) error {
	if id == 0 {
		return fmt.Errorf("channel id is required")
	}
	if len(updates) == 0 {
		return fmt.Errorf("no fields to update")
	}

	// 禁止修改不可变字段
	delete(updates, "id")
	delete(updates, "created_at")

	lock, err := pkgredis.Lock(ctx, fmt.Sprintf("channel:update:%d", id), 10*time.Second)
	if err != nil {
		return fmt.Errorf("failed to acquire lock: %w", err)
	}
	defer lock.Unlock(ctx)

	result := s.db.WithContext(ctx).Model(&model.Channel{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to update channel: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("channel not found")
	}

	s.invalidateCache(ctx)
	logger.L.Info("channel updated", zap.Uint("id", id))
	return nil
}

// Delete 根据 ID 软删除渠道
func (s *ChannelService) Delete(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("channel id is required")
	}

	result := s.db.WithContext(ctx).Delete(&model.Channel{}, id)
	if result.Error != nil {
		return fmt.Errorf("failed to delete channel: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("channel not found")
	}

	s.invalidateCache(ctx)
	logger.L.Info("channel deleted", zap.Uint("id", id))
	return nil
}

// GetByID 根据 ID 查询渠道，预加载标签和供应商信息
func (s *ChannelService) GetByID(ctx context.Context, id uint) (*model.Channel, error) {
	if id == 0 {
		return nil, fmt.Errorf("channel id is required")
	}

	var ch model.Channel
	err := s.db.WithContext(ctx).
		Preload("Tags").
		Preload("Supplier").
		First(&ch, id).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get channel: %w", err)
	}
	return &ch, nil
}

// List 分页查询渠道列表，支持按 status/supplier_id/type/name 过滤
func (s *ChannelService) List(ctx context.Context, page, pageSize int, filters map[string]interface{}) ([]model.Channel, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	query := s.db.WithContext(ctx).Model(&model.Channel{})

	if v, ok := filters["status"]; ok {
		query = query.Where("status = ?", v)
	}
	if v, ok := filters["supplier_id"]; ok {
		query = query.Where("supplier_id = ?", v)
	}
	if v, ok := filters["type"]; ok {
		query = query.Where("type = ?", v)
	}
	if v, ok := filters["name"]; ok {
		query = query.Where("name LIKE ?", fmt.Sprintf("%%%s%%", v))
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count channels: %w", err)
	}

	var channels []model.Channel
	err := query.
		Preload("Tags").
		Preload("Supplier").
		Order("priority DESC, weight DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&channels).Error
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list channels: %w", err)
	}

	return channels, total, nil
}

// SetTags 替换渠道的标签关联
func (s *ChannelService) SetTags(ctx context.Context, channelID uint, tagIDs []uint) error {
	if channelID == 0 {
		return fmt.Errorf("channel id is required")
	}

	var ch model.Channel
	if err := s.db.WithContext(ctx).First(&ch, channelID).Error; err != nil {
		return fmt.Errorf("channel not found: %w", err)
	}

	var tags []model.ChannelTag
	if len(tagIDs) > 0 {
		if err := s.db.WithContext(ctx).Where("id IN ?", tagIDs).Find(&tags).Error; err != nil {
			return fmt.Errorf("failed to find tags: %w", err)
		}
	}

	if err := s.db.WithContext(ctx).Model(&ch).Association("Tags").Replace(tags); err != nil {
		return fmt.Errorf("failed to set tags: %w", err)
	}

	logger.L.Info("channel tags updated", zap.Uint("channel_id", channelID), zap.Int("tag_count", len(tags)))
	return nil
}

// ListByTags 查询拥有指定标签的渠道，matchAll=true 时要求匹配所有标签
func (s *ChannelService) ListByTags(ctx context.Context, tagIDs []uint, matchAll bool) ([]model.Channel, error) {
	if len(tagIDs) == 0 {
		return nil, nil
	}

	query := s.db.WithContext(ctx).
		Joins("JOIN channel_tags_relation ctr ON ctr.channel_id = channels.id").
		Where("ctr.channel_tag_id IN ?", tagIDs).
		Where("channels.status = ?", "active")

	if matchAll {
		query = query.
			Group("channels.id").
			Having("COUNT(DISTINCT ctr.channel_tag_id) = ?", len(tagIDs))
	} else {
		query = query.Group("channels.id")
	}

	var channels []model.Channel
	if err := query.Preload("Tags").Find(&channels).Error; err != nil {
		return nil, fmt.Errorf("failed to list channels by tags: %w", err)
	}

	return channels, nil
}

// TestChannel 对指定渠道执行连通性测试，发送轻量级请求并返回延迟和状态
// 注意：此方法仅测试连通性，不更新渠道状态
func (s *ChannelService) TestChannel(ctx context.Context, id uint) (*ChannelTestResult, error) {
	ch, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	result := &ChannelTestResult{ChannelID: id}
	start := time.Now()

	// 解析模型列表，获取测试用模型名称
	testModel := "gpt-3.5-turbo"
	if ch.Models != nil {
		var models []string
		if json.Unmarshal(ch.Models, &models) == nil && len(models) > 0 {
			testModel = models[0]
		}
	}

	// 构建最小化测试请求
	reqBody := fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`, testModel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, buildChatURL(ch.Endpoint), nil)
	if err != nil {
		result.Error = fmt.Sprintf("failed to build request: %v", err)
		return result, nil
	}
	req.Header.Set("Authorization", "Bearer "+ch.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Body = http.NoBody
	// 用实际请求体替换
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, buildChatURL(ch.Endpoint),
		jsonReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+ch.APIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	elapsed := time.Since(start).Milliseconds()
	result.LatencyMs = elapsed

	if err != nil {
		result.Error = fmt.Sprintf("request failed: %v", err)
		return result, nil
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		result.Success = true
	} else {
		result.Error = fmt.Sprintf("upstream returned status %d", resp.StatusCode)
	}

	return result, nil
}

// VerifyChannelResult 渠道验证结果
// 包含验证状态、更新的模型数量等信息
type VerifyChannelResult struct {
	ChannelID        uint     `json:"channel_id"`         // 渠道ID
	Success           bool     `json:"success"`            // 验证是否成功
	LatencyMs         int64    `json:"latency_ms"`         // 延迟毫秒
	ModelsUpdated     []string `json:"models_updated"`     // 更新的模型列表
	ModelsUpdatedCount int     `json:"models_updated_count"` // 更新的模型数量
	Error             string   `json:"error,omitempty"`    // 错误信息
}

// VerifyChannel 验证渠道API Key是否有效
// 验证成功后：
// 1. 更新渠道状态为active，设置Verified=true
// 2. 更新该渠道关联的所有模型状态为online
// 简化实现：检查API Key非空且格式合理即可通过验证
func (s *ChannelService) VerifyChannel(ctx context.Context, id uint) (*VerifyChannelResult, error) {
	// 获取渠道信息
	ch, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	result := &VerifyChannelResult{
		ChannelID: id,
	}
	start := time.Now()

	// 简化验证：检查API Key非空且格式合理
	if ch.APIKey == "" {
		result.Error = "API Key is empty"
		return result, nil
	}

	// 检查API Key长度（通常至少10个字符）
	if len(ch.APIKey) < 10 {
		result.Error = "API Key format invalid (too short)"
		return result, nil
	}

	// 基本格式检查：常见的API Key格式
	// OpenAI: sk-...
	// Anthropic: sk-ant-...
	// 其他供应商也有类似格式
	keyValid := false
	if strings.HasPrefix(ch.APIKey, "sk-") ||
		strings.HasPrefix(ch.APIKey, "sk-ant-") ||
		strings.HasPrefix(ch.APIKey, "Bearer ") ||
		len(ch.APIKey) >= 20 {
		keyValid = true
	}

	if !keyValid {
		result.Error = "API Key format invalid"
		return result, nil
	}

	// 验证通过，更新渠道状态
	err = s.db.WithContext(ctx).Model(&model.Channel{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":   "active",
		"verified": true,
	}).Error
	if err != nil {
		return nil, fmt.Errorf("failed to update channel status: %w", err)
	}

	// 解析渠道关联的模型列表
	var modelNames []string
	if ch.Models != nil {
		if err := json.Unmarshal(ch.Models, &modelNames); err != nil {
			logger.L.Warn("failed to parse channel models", zap.Uint("channel_id", id), zap.Error(err))
		}
	}

	// 更新关联模型状态为online
	if len(modelNames) > 0 {
		// 根据模型名称更新状态
		err = s.db.WithContext(ctx).Model(&model.AIModel{}).
			Where("model_name IN ?", modelNames).
			Update("status", "online").Error
		if err != nil {
			logger.L.Warn("failed to update model status", zap.Uint("channel_id", id), zap.Error(err))
		}

		// 查询实际更新的模型
		var updatedModels []model.AIModel
		s.db.WithContext(ctx).Select("model_name").
			Where("model_name IN ?", modelNames).
			Find(&updatedModels)
		for _, m := range updatedModels {
			result.ModelsUpdated = append(result.ModelsUpdated, m.ModelName)
		}
		result.ModelsUpdatedCount = len(result.ModelsUpdated)
	}

	result.Success = true
	result.LatencyMs = time.Since(start).Milliseconds()

	s.invalidateCache(ctx)
	logger.L.Info("channel verified",
		zap.Uint("channel_id", id),
		zap.Bool("verified", true),
		zap.Int("models_updated", result.ModelsUpdatedCount))

	return result, nil
}

// invalidateCache 清除 Redis 中缓存的渠道数据
func (s *ChannelService) invalidateCache(ctx context.Context) {
	if s.redis == nil {
		return
	}
	_ = pkgredis.Del(ctx, "channels:list", "channels:active")
}
