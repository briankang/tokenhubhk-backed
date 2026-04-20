package support

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/pkg/logger"
	supportsvc "tokenhub-server/internal/service/support"
)

// ChatHandler AI 客服 SSE 对话端点
type ChatHandler struct {
	svc *supportsvc.Services
}

func NewChatHandler(svc *supportsvc.Services) *ChatHandler {
	return &ChatHandler{svc: svc}
}

// Register 挂载到 /api/v1/support/chat
func (h *ChatHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/chat", h.Chat)
}

// ChatRequestBody SSE 请求体
type ChatRequestBody struct {
	SessionID uint   `json:"session_id"`
	Message   string `json:"message" binding:"required"`
	Locale    string `json:"locale"`
}

// Chat SSE 处理
func (h *ChatHandler) Chat(c *gin.Context) {
	if h.svc == nil || !h.svc.Enabled || h.svc.Orchestrator == nil {
		reason := "support disabled"
		if h.svc != nil && h.svc.Disabled != "" {
			reason = h.svc.Disabled
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": 50301, "message": reason})
		return
	}
	userID := c.GetUint("userId")
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 10005, "message": "unauthorized"})
		return
	}
	var body ChatRequestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40000, "message": "invalid body: " + err.Error()})
		return
	}
	maxLen := config.Global.Support.MaxMsgLen
	if maxLen <= 0 {
		maxLen = 2000
	}
	if len([]rune(body.Message)) > maxLen {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40001, "message": "message too long"})
		return
	}

	userLevel, _ := c.Get("memberLevel")
	levelStr := ""
	if s, ok := userLevel.(string); ok {
		levelStr = s
	}

	// SSE 响应头
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	events := make(chan supportsvc.StreamEvent, 8)
	go h.svc.Orchestrator.Chat(c.Request.Context(), supportsvc.ChatRequest{
		UserID:    userID,
		SessionID: body.SessionID,
		Message:   body.Message,
		Locale:    body.Locale,
		UserLevel: levelStr,
	}, events)

	for e := range events {
		data, err := json.Marshal(e)
		if err != nil {
			logger.L.Warn("marshal SSE event failed", zap.Error(err))
			continue
		}
		if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", data); err != nil {
			return
		}
		c.Writer.Flush()
	}
}
