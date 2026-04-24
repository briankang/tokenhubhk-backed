package admin

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/database"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/pkg/safego"
	"tokenhub-server/internal/service/modeldiscovery"
)

// ModelFeatureProbeHandler 快速能力探测 Handler
type ModelFeatureProbeHandler struct {
	db     *gorm.DB
	client *http.Client
}

// NewModelFeatureProbeHandler 创建能力探测 Handler
func NewModelFeatureProbeHandler(db *gorm.DB) *ModelFeatureProbeHandler {
	transport := &http.Transport{
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSNextProto:        make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		TLSHandshakeTimeout: 15 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     60 * time.Second,
	}
	return &ModelFeatureProbeHandler{
		db:     db,
		client: &http.Client{Transport: transport},
	}
}

// featureProbeRequest 请求体
type featureProbeRequest struct {
	ModelIDs       []uint   `json:"model_ids" binding:"required,min=1"`
	FeaturesToProbe []string `json:"features_to_probe"` // 留空表示全部探测
}

// featureProbeItem 单模型单能力探测结果
// nil 表示探测失败/超时，不写 DB
type featureProbeItem struct {
	ModelID    uint              `json:"model_id"`
	ModelName  string            `json:"model_name"`
	Probed     map[string]*bool  `json:"probed"` // key=feature, value=true/false/null
	Error      string            `json:"error,omitempty"`
}

// FeatureProbe POST /admin/models/feature-probe
// 对指定模型执行快速能力探测，探测完成后合并写入 ai_models.features
func (h *ModelFeatureProbeHandler) FeatureProbe(c *gin.Context) {
	var req featureProbeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}

	// 支持的探测类型
	allFeatures := []string{"thinking", "web_search", "json_mode", "function_call"}
	featuresToProbe := req.FeaturesToProbe
	if len(featuresToProbe) == 0 {
		featuresToProbe = allFeatures
	}

	// 加载模型信息
	var models []model.AIModel
	if err := h.db.Where("id IN ?", req.ModelIDs).Find(&models).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrDatabase.Code, err.Error())
		return
	}

	// 为每个模型构建路由信息（复用 model_checker 的 routeMap 逻辑）
	routeMap := h.buildRouteMap(c.Request.Context(), models)

	// 并发探测，全局最多 5 个 goroutine
	sem := make(chan struct{}, 5)
	var mu sync.Mutex
	results := make([]featureProbeItem, 0, len(models))
	var wg sync.WaitGroup

	for _, m := range models {
		m := m
		wg.Add(1)
		safego.Go("feature-probe-"+m.ModelName, func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			item := featureProbeItem{
				ModelID:   m.ID,
				ModelName: m.ModelName,
				Probed:    make(map[string]*bool),
			}

			route, ok := routeMap[m.ModelName]
			if !ok {
				item.Error = "无可用渠道路由"
				mu.Lock()
				results = append(results, item)
				mu.Unlock()
				return
			}

			// 对每个能力逐一探测
			updatedFeats := make(map[string]interface{})
			for _, feat := range featuresToProbe {
				supported, err := h.probeFeature(c.Request.Context(), m, route, feat)
				if err != nil {
					item.Probed[feat] = nil // 探测失败
					logger.L.Debug("feature probe failed", zap.String("model", m.ModelName), zap.String("feature", feat), zap.Error(err))
				} else {
					v := supported
					item.Probed[feat] = &v
					updatedFeats[featureKey(feat)] = supported
				}
			}

			// 合并写入 DB features（不覆盖已有值，仅叠加/更新探测到的字段）
			if len(updatedFeats) > 0 {
				h.mergeFeatures(m.ID, updatedFeats)
			}

			// 探测完成后重算标签
			if len(updatedFeats) > 0 {
				h.retag(m)
			}

			mu.Lock()
			results = append(results, item)
			mu.Unlock()
		})
	}

	wg.Wait()

	// 缓存失效
	invalidatePublicModelsCache()

	updatedCount := 0
	for _, r := range results {
		if r.Error == "" {
			updatedCount++
		}
	}
	response.Success(c, gin.H{
		"results":       results,
		"updated_count": updatedCount,
	})
}

// featureKey 将 probe feature name 映射到 features JSON key
func featureKey(feature string) string {
	switch feature {
	case "thinking":
		return "supports_thinking"
	case "web_search":
		return "supports_web_search"
	case "json_mode":
		return "supports_json_mode"
	case "function_call":
		return "supports_function_call"
	case "vision":
		return "supports_vision"
	default:
		return "supports_" + feature
	}
}

// probeFeature 对单个模型探测单个能力，返回 (supported, error)
func (h *ModelFeatureProbeHandler) probeFeature(ctx context.Context, m model.AIModel, route channelRouteInfo, feature string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	reqBody := h.buildProbeBody(m.ModelName, feature)
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return false, err
	}

	endpoint := strings.TrimRight(route.Endpoint, "/")
	var url string
	if strings.HasSuffix(endpoint, "/v1") || strings.HasSuffix(endpoint, "/v2") || strings.HasSuffix(endpoint, "/v3") {
		url = endpoint + "/chat/completions"
	} else {
		url = endpoint + "/v1/chat/completions"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+route.APIKey)
	if route.OrgID != "" {
		req.Header.Set("X-Organization-ID", route.OrgID)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	return h.interpretProbeResponse(resp.StatusCode, string(respBody), feature), nil
}

// buildProbeBody 构造最小化探针请求体
func (h *ModelFeatureProbeHandler) buildProbeBody(modelName, feature string) map[string]interface{} {
	body := map[string]interface{}{
		"model": modelName,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "hi"},
		},
		"max_tokens": 20,
	}

	switch feature {
	case "thinking":
		body["enable_thinking"] = true
		body["thinking"] = map[string]interface{}{"type": "enabled", "budget_tokens": 100}
		body["stream"] = true
	case "web_search":
		body["enable_search"] = true
	case "json_mode":
		body["response_format"] = map[string]string{"type": "json_object"}
		body["messages"] = []map[string]interface{}{
			{"role": "user", "content": "Reply with JSON: {\"ok\":true}"},
		}
	case "function_call":
		body["tools"] = []map[string]interface{}{
			{
				"type": "function",
				"function": map[string]interface{}{
					"name":        "get_time",
					"description": "Get current time",
					"parameters":  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
				},
			},
		}
	}
	return body
}

// interpretProbeResponse 解析响应，判断该能力是否被支持
func (h *ModelFeatureProbeHandler) interpretProbeResponse(statusCode int, body string, feature string) bool {
	bodyLower := strings.ToLower(body)

	// 通用失败检测：参数错误提示通常表示不支持
	unsupportedSignals := []string{
		"unknown parameter", "unsupported parameter", "invalid parameter",
		"not support", "not supported", "does not support",
		"unrecognized", "unexpected", "invalid_request_error",
	}

	switch feature {
	case "thinking":
		// 流式响应中出现 reasoning_content 或 thinking block → 支持
		if statusCode == 200 {
			return strings.Contains(bodyLower, "reasoning_content") ||
				strings.Contains(bodyLower, `"thinking"`) ||
				strings.Contains(bodyLower, "budget_tokens")
		}
		return false

	case "web_search":
		if statusCode != 200 {
			return false
		}
		// 响应 200 且无"不支持该参数"错误 → 视为支持
		for _, sig := range unsupportedSignals {
			if strings.Contains(bodyLower, sig) {
				return false
			}
		}
		return true

	case "json_mode":
		if statusCode != 200 {
			return false
		}
		for _, sig := range unsupportedSignals {
			if strings.Contains(bodyLower, sig) {
				return false
			}
		}
		return true

	case "function_call":
		if statusCode != 200 {
			return false
		}
		for _, sig := range unsupportedSignals {
			if strings.Contains(bodyLower, sig) {
				return false
			}
		}
		// 响应包含 tool_calls 字段表示明确支持
		if strings.Contains(bodyLower, "tool_calls") || strings.Contains(bodyLower, "function_call") {
			return true
		}
		return true // 200 且无错误 → 视为支持

	default:
		return statusCode == 200
	}
}

// mergeFeatures 将探测结果合并写入 ai_models.features（仅叠加不覆盖整体）
func (h *ModelFeatureProbeHandler) mergeFeatures(modelID uint, updates map[string]interface{}) {
	var m model.AIModel
	if err := h.db.Select("id, features").First(&m, modelID).Error; err != nil {
		return
	}

	feats := make(map[string]interface{})
	if len(m.Features) > 0 {
		_ = json.Unmarshal(m.Features, &feats)
	}
	for k, v := range updates {
		feats[k] = v
	}

	out, err := json.Marshal(feats)
	if err != nil {
		return
	}
	h.db.Model(&model.AIModel{}).Where("id = ?", modelID).Update("features", out)
}

// retag 重新计算并更新模型标签
func (h *ModelFeatureProbeHandler) retag(m model.AIModel) {
	// 读取最新 features
	var fresh model.AIModel
	if err := h.db.Select("id,model_name,model_type,features,supplier_id").First(&fresh, m.ID).Error; err != nil {
		return
	}
	var supplier model.Supplier
	database.DB.Select("code").First(&supplier, fresh.SupplierID)
	newTags := modeldiscovery.InferModelTagsWithFeatures(fresh.ModelName, supplier.Code, fresh.ModelType, fresh.Features)
	h.db.Model(&model.AIModel{}).Where("id = ?", fresh.ID).Update("tags", newTags)
}

// channelRouteInfo 内部路由信息（对应 model_checker.go 的 channelRoute）
type channelRouteInfo struct {
	ChannelID   uint
	ChannelName string
	ActualModel string
	Endpoint    string
	APIKey      string
	OrgID       string
}

// buildRouteMap 为模型列表构建路由映射（复用 channel_logs 查询策略）
func (h *ModelFeatureProbeHandler) buildRouteMap(ctx context.Context, models []model.AIModel) map[string]channelRouteInfo {
	result := make(map[string]channelRouteInfo, len(models))
	if len(models) == 0 {
		return result
	}

	// 从 custom_channel_routes 查找默认渠道路由
	var routes []struct {
		AliasModel  string
		ActualModel string
		ChannelID   uint
	}
	h.db.WithContext(ctx).
		Table("custom_channel_routes ccr").
		Select("ccr.alias_model, ccr.actual_model, ccr.channel_id").
		Joins("JOIN custom_channels cc ON cc.id = ccr.custom_channel_id AND cc.is_default = true AND cc.is_active = true").
		Where("ccr.is_active = true").
		Scan(&routes)

	routeByModel := make(map[string]struct {
		ActualModel string
		ChannelID   uint
	}, len(routes))
	for _, r := range routes {
		routeByModel[r.AliasModel] = struct {
			ActualModel string
			ChannelID   uint
		}{r.ActualModel, r.ChannelID}
	}

	// 加载渠道信息
	channelIDs := make([]uint, 0)
	for _, r := range routes {
		channelIDs = append(channelIDs, r.ChannelID)
	}

	type channelInfo struct {
		ID       uint
		Name     string
		BaseURL  string
		APIKey   string
		ClientID string
	}
	var channels []channelInfo
	if len(channelIDs) > 0 {
		h.db.WithContext(ctx).
			Table("channels").
			Select("id, name, base_url, api_key, client_id").
			Where("id IN ?", channelIDs).
			Scan(&channels)
	}
	channelMap := make(map[uint]channelInfo, len(channels))
	for _, ch := range channels {
		channelMap[ch.ID] = ch
	}

	for _, m := range models {
		r, ok := routeByModel[m.ModelName]
		if !ok {
			continue
		}
		ch, ok := channelMap[r.ChannelID]
		if !ok {
			continue
		}
		result[m.ModelName] = channelRouteInfo{
			ChannelID:   ch.ID,
			ChannelName: ch.Name,
			ActualModel: r.ActualModel,
			Endpoint:    ch.BaseURL,
			APIKey:      ch.APIKey,
			OrgID:       ch.ClientID,
		}
	}
	return result
}

