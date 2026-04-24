// Anthropic Messages API 兼容入站路由：POST /v1/messages
//
// 用户可把 Claude SDK / Claude Code / Cline / Roo 等客户端的 ANTHROPIC_BASE_URL 指向本平台，
// 以 Claude 原生协议调用我们任何后端模型（GPT / Claude / Gemini）。
//
// 协议转换策略：
//   1. 入站请求 Anthropic 格式 → 内部统一为 provider.ChatRequest（OpenAI 形状）
//   2. channel_router 选择渠道，coding_service 创建 Provider
//      - 目标为 Anthropic channel（如网宿-Claude）：WangsuProvider 会把 OpenAI 内部形式再转回 Anthropic 发送（两次转换）
//      - 目标为 OpenAI / Gemini channel：直接按对应协议发送
//   3. 响应（OpenAI ChatResponse 形状）→ 转回 Anthropic Messages 响应
//   4. 流式 SSE：OpenAI `data: {chunk}` → Anthropic `event: content_block_delta\ndata: {...}`
//
// 鉴权：同时支持 `Authorization: Bearer <platform-api-key>` 和 `x-api-key: <platform-api-key>`
// （匹配 Anthropic 客户端默认 header；内部仍使用平台自身的 API Key 体系）
//
// 未覆盖的能力（v1 最小实现）：
//   - tool_use / tool_result content blocks
//   - 多模态 content blocks（image）
//   - thinking 参数（Claude Opus/Sonnet 4 系列）
//   - 精细的 stop_reason 映射
//
// 后续可以按需扩展。
package v1

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/provider"
	"tokenhub-server/internal/service/apikey"
)

// MessagesHandler Anthropic Messages API 兼容处理器
// 复用 CompletionsHandler 的所有依赖
type MessagesHandler struct {
	*CompletionsHandler
}

// NewMessagesHandler 构造 MessagesHandler，复用传入的 CompletionsHandler 的依赖
func NewMessagesHandler(comp *CompletionsHandler) *MessagesHandler {
	return &MessagesHandler{CompletionsHandler: comp}
}

// Register 注册 POST /v1/messages 路由
func (h *MessagesHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/messages", h.Messages)
}

// --- Anthropic Messages API 请求/响应类型 ---

type anthropicInboundRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	Messages      []anthropicInMsg   `json:"messages"`
	System        interface{}        `json:"system,omitempty"` // string 或 []content_block
	Stream        bool               `json:"stream,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	Metadata      map[string]any     `json:"metadata,omitempty"`
}

type anthropicInMsg struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string 或 []{"type":"text","text":"..."}
}

type anthropicOutMessage struct {
	ID           string                `json:"id"`
	Type         string                `json:"type"`
	Role         string                `json:"role"`
	Model        string                `json:"model"`
	Content      []anthropicOutContent `json:"content"`
	StopReason   string                `json:"stop_reason"`
	StopSequence *string               `json:"stop_sequence"`
	Usage        anthropicOutUsage     `json:"usage"`
}

type anthropicOutContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicOutUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Messages 处理 POST /v1/messages
func (h *MessagesHandler) Messages(c *gin.Context) {
	// 1. 鉴权：支持 Authorization 和 x-api-key 两种 header
	keyInfo, err := h.authenticateAnthropicAPIKey(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"type":  "error",
			"error": gin.H{"type": "authentication_error", "message": "invalid x-api-key"},
		})
		return
	}

	// 2. 解析请求
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, anthropicErrorBody("invalid_request_error", "failed to read request body"))
		return
	}
	var req anthropicInboundRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		c.JSON(http.StatusBadRequest, anthropicErrorBody("invalid_request_error", "invalid JSON: "+err.Error()))
		return
	}
	if req.Model == "" {
		c.JSON(http.StatusBadRequest, anthropicErrorBody("invalid_request_error", "field 'model' is required"))
		return
	}
	if len(req.Messages) == 0 {
		c.JSON(http.StatusBadRequest, anthropicErrorBody("invalid_request_error", "'messages' must not be empty"))
		return
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = 4096 // Anthropic 默认约束
	}

	// 3. 提取非标准字段作为 extra（透传到上游）
	var rawMap map[string]json.RawMessage
	_ = json.Unmarshal(rawBody, &rawMap)
	standardFields := map[string]bool{
		"model": true, "max_tokens": true, "messages": true, "system": true,
		"stream": true, "stop_sequences": true, "temperature": true, "top_p": true,
		"metadata": true, "tools": true, "tool_choice": true,
	}
	userExtra := make(map[string]interface{})
	for k, v := range rawMap {
		if !standardFields[k] {
			var val interface{}
			if json.Unmarshal(v, &val) == nil {
				userExtra[k] = val
			}
		}
	}

	// 4. 转换为内部 provider.ChatRequest（OpenAI 形状）
	chatReq := convertAnthropicToProviderRequest(&req)
	chatReq.Extra = userExtra

	// 5. 初始化 ApiCallLog（与 /v1/chat/completions 结构对齐，便于成本分析聚合）
	requestID := "msg-" + uuid.NewString()
	if globalReqID, exists := c.Get("X-Request-ID"); exists {
		if rid, ok := globalReqID.(string); ok && rid != "" {
			requestID = rid
		}
	}
	start := time.Now()
	callLog := &model.ApiCallLog{
		RequestID:    requestID,
		UserID:       keyInfo.UserID,
		TenantID:     keyInfo.TenantID,
		ApiKeyID:     keyInfo.KeyID,
		ClientIP:     c.ClientIP(),
		Endpoint:     "/v1/messages",
		RequestModel: req.Model,
		IsStream:     req.Stream,
		MessageCount: len(req.Messages),
		MaxTokens:    req.MaxTokens,
		Status:       "error",
		RequestBody:  string(rawBody),
	}

	// 6. 选择渠道
	selection, selErr := h.channelRouter.SelectChannel(c.Request.Context(), req.Model, keyInfo.CustomChannelID, keyInfo.UserID)
	if selErr != nil {
		h.logger.Warn("v1 messages: 渠道选择失败",
			zap.String("model", req.Model), zap.Error(selErr))
		callLog.StatusCode = http.StatusServiceUnavailable
		callLog.ErrorMessage = selErr.Error()
		callLog.ErrorType = "channel_selection_failed"
		callLog.TotalLatencyMs = int(time.Since(start).Milliseconds())
		h.recordApiCallLog(callLog)
		c.JSON(http.StatusServiceUnavailable, anthropicErrorBody("api_error", "no available channel for model: "+req.Model))
		return
	}
	ch := selection.Channel
	if selection.ActualModel != "" && selection.ActualModel != req.Model {
		chatReq.Model = selection.ActualModel
	}

	// 7. 创建 provider
	p := h.codingSvc.CreateProviderForChannel(ch)
	if p == nil {
		callLog.StatusCode = http.StatusServiceUnavailable
		callLog.ErrorMessage = "provider initialization failed"
		callLog.ErrorType = "provider_init_failed"
		callLog.TotalLatencyMs = int(time.Since(start).Milliseconds())
		callLog.ChannelID = ch.ID
		callLog.ChannelName = ch.Name
		h.recordApiCallLog(callLog)
		c.JSON(http.StatusServiceUnavailable, anthropicErrorBody("api_error", "provider initialization failed"))
		return
	}

	// 8. 流式 vs 非流式
	if req.Stream {
		h.handleMessagesStream(c, p, chatReq, ch, keyInfo, requestID, start, &req, callLog)
	} else {
		h.handleMessagesNonStream(c, p, chatReq, ch, keyInfo, requestID, start, &req, callLog)
	}
}

// handleMessagesNonStream 非流式路径
func (h *MessagesHandler) handleMessagesNonStream(
	c *gin.Context, p provider.Provider, chatReq *provider.ChatRequest,
	ch *model.Channel, keyInfo *apikey.ApiKeyInfo, requestID string, start time.Time,
	req *anthropicInboundRequest, callLog *model.ApiCallLog,
) {
	resp, err := p.Chat(c.Request.Context(), chatReq)
	latency := time.Since(start).Milliseconds()

	callLog.ChannelID = ch.ID
	callLog.ChannelName = ch.Name
	callLog.ActualModel = chatReq.Model
	callLog.TotalLatencyMs = int(latency)
	callLog.UpstreamLatencyMs = int(latency)

	if err != nil {
		h.recordLog(ch.ID, chatReq.Model, keyInfo, requestID, 0, 0, int(latency), 500, err.Error())
		h.channelRouter.RecordResultWithError(ch.ID, err, 0, int(latency))
		callLog.StatusCode = http.StatusBadGateway
		callLog.Status = "error"
		callLog.ErrorMessage = err.Error()
		callLog.ErrorType = "upstream_error"
		h.recordApiCallLog(callLog)
		c.JSON(http.StatusBadGateway, anthropicErrorBody("api_error", "upstream error: "+err.Error()))
		return
	}

	h.channelRouter.RecordResult(ch.ID, true, int(latency), 200)
	h.recordLog(ch.ID, chatReq.Model, keyInfo, requestID,
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens, int(latency), 200, "")

	// 扣费
	cost, costRMB := h.calculateAndDeductCost(c, req.Model, keyInfo, resp.Usage, requestID)
	if h.commissionCalc != nil && cost > 0 {
		h.commissionCalc.CalculateCommissionsAsyncByModelName(keyInfo.UserID, keyInfo.TenantID, cost, req.Model)
	}

	// 完整填充 ApiCallLog
	callLog.StatusCode = http.StatusOK
	callLog.Status = "success"
	callLog.PromptTokens = resp.Usage.PromptTokens
	callLog.CompletionTokens = resp.Usage.CompletionTokens
	callLog.TotalTokens = resp.Usage.TotalTokens
	callLog.CacheReadTokens = resp.Usage.CacheReadTokens
	callLog.CacheWriteTokens = resp.Usage.CacheWriteTokens
	callLog.CostCredits = cost
	callLog.CostRMB = costRMB
	applyMatchedTierFromCtx(c, callLog)
	h.recordApiCallLog(callLog)

	// 响应转换为 Anthropic Messages 格式
	anthResp := convertProviderResponseToAnthropic(resp, req.Model)
	c.Header("X-Request-ID", requestID)
	c.Header("X-Channel-ID", fmt.Sprintf("%d", ch.ID))
	c.JSON(http.StatusOK, anthResp)
}

// handleMessagesStream 流式路径：把 OpenAI SSE 转成 Anthropic SSE 事件
func (h *MessagesHandler) handleMessagesStream(
	c *gin.Context, p provider.Provider, chatReq *provider.ChatRequest,
	ch *model.Channel, keyInfo *apikey.ApiKeyInfo, requestID string, start time.Time,
	req *anthropicInboundRequest, callLog *model.ApiCallLog,
) {
	channelID := ch.ID
	callLog.ChannelID = channelID
	callLog.ChannelName = ch.Name
	callLog.ActualModel = chatReq.Model

	chatReq.Stream = true
	reader, err := p.StreamChat(c.Request.Context(), chatReq)
	if err != nil {
		latency := time.Since(start).Milliseconds()
		h.recordLog(channelID, chatReq.Model, keyInfo, requestID, 0, 0, int(latency), 500, err.Error())
		h.channelRouter.RecordResultWithError(channelID, err, 0, int(latency))
		callLog.StatusCode = http.StatusBadGateway
		callLog.Status = "error"
		callLog.ErrorMessage = err.Error()
		callLog.ErrorType = "upstream_stream_error"
		callLog.TotalLatencyMs = int(latency)
		h.recordApiCallLog(callLog)
		c.JSON(http.StatusBadGateway, anthropicErrorBody("api_error", "upstream stream error: "+err.Error()))
		return
	}
	defer reader.Close()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Request-ID", requestID)
	c.Header("X-Channel-ID", fmt.Sprintf("%d", channelID))

	// Anthropic SSE 事件序列：
	//   message_start → content_block_start → content_block_delta* → content_block_stop → message_delta → message_stop
	msgID := "msg_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	// 1. message_start
	writeAnthropicEvent(c, "message_start", gin.H{
		"type": "message_start",
		"message": gin.H{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         req.Model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         gin.H{"input_tokens": 0, "output_tokens": 0},
		},
	})
	// 2. content_block_start（单 text 块）
	writeAnthropicEvent(c, "content_block_start", gin.H{
		"type":          "content_block_start",
		"index":         0,
		"content_block": gin.H{"type": "text", "text": ""},
	})

	// 3. 聚合 delta
	promptTokens, completionTokens := 0, 0
	finishReason := "end_turn"
	for {
		chunk, rErr := reader.Read()
		if rErr == io.EOF {
			break
		}
		if rErr != nil {
			h.logger.Warn("v1 messages stream: 读取失败", zap.Error(rErr))
			break
		}
		if chunk == nil {
			continue
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				writeAnthropicEvent(c, "content_block_delta", gin.H{
					"type":  "content_block_delta",
					"index": 0,
					"delta": gin.H{"type": "text_delta", "text": choice.Delta.Content},
				})
				c.Writer.Flush()
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finishReason = mapOpenAIFinishToAnthropic(*choice.FinishReason)
			}
		}
		if chunk.Usage != nil {
			promptTokens = chunk.Usage.PromptTokens
			completionTokens = chunk.Usage.CompletionTokens
		}
	}

	// 4. content_block_stop + message_delta + message_stop
	writeAnthropicEvent(c, "content_block_stop", gin.H{
		"type":  "content_block_stop",
		"index": 0,
	})
	writeAnthropicEvent(c, "message_delta", gin.H{
		"type":  "message_delta",
		"delta": gin.H{"stop_reason": finishReason, "stop_sequence": nil},
		"usage": gin.H{"output_tokens": completionTokens},
	})
	writeAnthropicEvent(c, "message_stop", gin.H{"type": "message_stop"})
	c.Writer.Flush()

	// 5. 日志 + 扣费
	latency := time.Since(start).Milliseconds()
	h.channelRouter.RecordResult(channelID, true, int(latency), 200)
	h.recordLog(channelID, chatReq.Model, keyInfo, requestID,
		promptTokens, completionTokens, int(latency), 200, "")
	usage := provider.Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
	var cost int64
	var costRMB float64
	if promptTokens > 0 || completionTokens > 0 {
		cost, costRMB = h.calculateAndDeductCost(c, req.Model, keyInfo, usage, requestID)
		if h.commissionCalc != nil && cost > 0 {
			h.commissionCalc.CalculateCommissionsAsyncByModelName(keyInfo.UserID, keyInfo.TenantID, cost, req.Model)
		}
	}

	// 6. 完整填充 ApiCallLog
	callLog.StatusCode = http.StatusOK
	callLog.Status = "success"
	callLog.PromptTokens = promptTokens
	callLog.CompletionTokens = completionTokens
	callLog.TotalTokens = promptTokens + completionTokens
	callLog.TotalLatencyMs = int(latency)
	callLog.UpstreamLatencyMs = int(latency)
	callLog.CostCredits = cost
	callLog.CostRMB = costRMB
	applyMatchedTierFromCtx(c, callLog)
	h.recordApiCallLog(callLog)
}

// --- Helpers ---

// authenticateAnthropicAPIKey 同时支持 Authorization: Bearer 和 x-api-key header
func (h *MessagesHandler) authenticateAnthropicAPIKey(c *gin.Context) (*apikey.ApiKeyInfo, error) {
	// 优先 x-api-key（Anthropic SDK 默认）
	if key := strings.TrimSpace(c.GetHeader("x-api-key")); key != "" {
		return h.apiKeySvc.Verify(c.Request.Context(), key)
	}
	// 回退 Authorization: Bearer
	auth := c.GetHeader("Authorization")
	if auth == "" {
		return nil, fmt.Errorf("missing x-api-key or Authorization header")
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return nil, fmt.Errorf("invalid authorization format")
	}
	key := strings.TrimSpace(parts[1])
	if key == "" {
		return nil, fmt.Errorf("empty api key")
	}
	return h.apiKeySvc.Verify(c.Request.Context(), key)
}

// convertAnthropicToProviderRequest Anthropic 请求 → 内部 provider.ChatRequest（OpenAI 形状）
func convertAnthropicToProviderRequest(req *anthropicInboundRequest) *provider.ChatRequest {
	msgs := make([]provider.Message, 0, len(req.Messages)+1)
	// 顶层 system 字段上提为首条 system message
	if req.System != nil {
		if text := extractAnthropicText(req.System); text != "" {
			msgs = append(msgs, provider.Message{Role: "system", Content: text})
		}
	}
	for _, m := range req.Messages {
		text := extractAnthropicText(m.Content)
		msgs = append(msgs, provider.Message{Role: m.Role, Content: text})
	}
	return &provider.ChatRequest{
		Model:       req.Model,
		Messages:    msgs,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSequences,
		Stream:      req.Stream,
	}
}

// extractAnthropicText 从 Anthropic content 中提取文本
// 支持 string 和 []{"type":"text","text":"..."} 两种格式
func extractAnthropicText(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var sb strings.Builder
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if t, _ := m["type"].(string); t == "text" {
					if s, _ := m["text"].(string); s != "" {
						sb.WriteString(s)
					}
				}
			}
		}
		return sb.String()
	}
	return ""
}

// convertProviderResponseToAnthropic 内部响应 → Anthropic Messages 响应
func convertProviderResponseToAnthropic(resp *provider.ChatResponse, originalModel string) anthropicOutMessage {
	text := ""
	finishReason := "end_turn"
	if len(resp.Choices) > 0 {
		if s, ok := resp.Choices[0].Message.Content.(string); ok {
			text = s
		} else {
			b, _ := json.Marshal(resp.Choices[0].Message.Content)
			text = string(b)
		}
		if resp.Choices[0].FinishReason != "" {
			finishReason = mapOpenAIFinishToAnthropic(resp.Choices[0].FinishReason)
		}
	}
	msgID := resp.ID
	if !strings.HasPrefix(msgID, "msg_") {
		msgID = "msg_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	}
	modelOut := resp.Model
	if modelOut == "" {
		modelOut = originalModel
	}
	return anthropicOutMessage{
		ID:         msgID,
		Type:       "message",
		Role:       "assistant",
		Model:      modelOut,
		Content:    []anthropicOutContent{{Type: "text", Text: text}},
		StopReason: finishReason,
		Usage: anthropicOutUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
	}
}

// mapOpenAIFinishToAnthropic OpenAI finish_reason → Anthropic stop_reason
func mapOpenAIFinishToAnthropic(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "stop_sequence"
	default:
		if reason == "" {
			return "end_turn"
		}
		return reason
	}
}

// writeAnthropicEvent 写一条 Anthropic 格式的 SSE 事件
func writeAnthropicEvent(c *gin.Context, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, data)
}

// anthropicErrorBody 构造 Anthropic 格式的错误响应体
func anthropicErrorBody(errType, message string) gin.H {
	return gin.H{
		"type":  "error",
		"error": gin.H{"type": errType, "message": message},
	}
}
