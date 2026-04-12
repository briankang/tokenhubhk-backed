// Package mcphandler 提供 MCP 协议的 HTTP 传输层处理器
// 包含 SSE 长连接端点、JSON-RPC 消息端点和 MCP 服务描述端点
package mcphandler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/mcp"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// MCPHandler MCP 协议 HTTP 处理器
type MCPHandler struct {
	server *mcp.MCPServer
	db     *gorm.DB
	logger *zap.Logger
	// SSE 会话管理：sessionID -> SSESession
	sessions sync.Map
}

// SSESession SSE 长连接会话
type SSESession struct {
	ID       string          // 会话唯一标识
	UserID   uint            // 认证用户 ID
	Messages chan []byte     // 消息发送通道
	Done     chan struct{}   // 关闭信号
	Created  time.Time       // 创建时间
}

// NewMCPHandler 创建 MCP HTTP 处理器实例
func NewMCPHandler(server *mcp.MCPServer, db *gorm.DB) *MCPHandler {
	return &MCPHandler{
		server: server,
		db:     db,
		logger: logger.L,
	}
}

// Register 注册 MCP 路由到路由组
func (h *MCPHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/sse", h.SSE)
	rg.POST("/message", h.Message)
	rg.GET("/manifest", h.Manifest)
}

// ─── SSE 传输端点 ──────────────────────────────────────────

// SSE 处理 GET /api/v1/mcp/sse — MCP SSE 长连接端点
// 客户端通过此端点接收服务端推送的 JSON-RPC 响应
// 认证方式：URL 参数 ?token=<api_key> 或 Header Authorization: Bearer <api_key>
func (h *MCPHandler) SSE(c *gin.Context) {
	// 认证：从 URL 参数或 Header 提取 API Key
	userID, err := h.authenticateMCP(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized", "message": err.Error()})
		return
	}

	// 创建 SSE 会话
	sessionID := uuid.New().String()
	session := &SSESession{
		ID:       sessionID,
		UserID:   userID,
		Messages: make(chan []byte, 64),
		Done:     make(chan struct{}),
		Created:  time.Now(),
	}
	h.sessions.Store(sessionID, session)

	// 设置 SSE 响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Header("Access-Control-Allow-Origin", "*")

	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming not supported"})
		return
	}

	// 发送 endpoint 事件，告知客户端消息发送地址
	// MCP SSE 规范：服务端先发送 endpoint 事件
	messageURL := fmt.Sprintf("/api/v1/mcp/message?session_id=%s", sessionID)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", messageURL)
	flusher.Flush()

	if h.logger != nil {
		h.logger.Info("MCP SSE session started",
			zap.String("session_id", sessionID),
			zap.Uint("user_id", userID))
	}

	// 心跳定时器：每 30 秒发送 ping 保持连接
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	ctx := c.Request.Context()

	for {
		select {
		case <-ctx.Done():
			// 客户端断开连接
			h.sessions.Delete(sessionID)
			close(session.Done)
			if h.logger != nil {
				h.logger.Info("MCP SSE session closed (client disconnect)",
					zap.String("session_id", sessionID))
			}
			return

		case msg, ok := <-session.Messages:
			if !ok {
				return
			}
			// 发送 JSON-RPC 响应消息
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(msg))
			flusher.Flush()

		case <-ticker.C:
			// 心跳保活
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()

		case <-session.Done:
			return
		}
	}
}

// ─── JSON-RPC 消息端点 ─────────────────────────────────────

// Message 处理 POST /api/v1/mcp/message — JSON-RPC 消息接收端点
// 客户端通过此端点发送 JSON-RPC 请求，响应通过 SSE 或直接 HTTP 返回
func (h *MCPHandler) Message(c *gin.Context) {
	// 认证：从 Header 或 URL 参数提取 API Key
	userID, err := h.authenticateMCP(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized", "message": err.Error()})
		return
	}

	// 读取请求体
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	// 处理 JSON-RPC 消息
	resp := h.server.HandleMessage(body, userID)

	// 检查是否有关联的 SSE 会话
	sessionID := c.Query("session_id")
	if sessionID != "" {
		if sessionVal, ok := h.sessions.Load(sessionID); ok {
			session := sessionVal.(*SSESession)
			// 通过 SSE 通道发送响应
			data, _ := json.Marshal(resp)
			select {
			case session.Messages <- data:
				// 成功推送到 SSE
				c.JSON(http.StatusAccepted, gin.H{"status": "accepted"})
				return
			default:
				// SSE 通道满，回退到直接返回
			}
		}
	}

	// 直接返回 JSON-RPC 响应（无 SSE 会话或推送失败时）
	c.JSON(http.StatusOK, resp)
}

// ─── Manifest 端点 ─────────────────────────────────────────

// Manifest 处理 GET /api/v1/mcp/manifest — MCP 服务描述端点
// 返回服务端支持的 Tools 和 Resources 列表（无需认证）
func (h *MCPHandler) Manifest(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"name":        "TokenHub MCP Server",
		"version":     "1.0.0",
		"description": "TokenHub AI 平台 MCP 协议服务端，提供模型调用、余额查询、API Key 管理等能力",
		"protocol":    "MCP/2024-11-05",
		"transport": gin.H{
			"sse": gin.H{
				"endpoint": "/api/v1/mcp/sse",
				"message":  "/api/v1/mcp/message",
			},
		},
		"tools":     h.server.GetToolsList(),
		"resources": h.server.GetResourcesList(),
	})
}

// ─── 认证辅助函数 ──────────────────────────────────────────

// authenticateMCP 从请求中提取并验证 API Key，返回用户 ID
// 支持两种方式：
// 1. URL 参数: ?token=sk-xxxx
// 2. Authorization Header: Bearer sk-xxxx
func (h *MCPHandler) authenticateMCP(c *gin.Context) (uint, error) {
	// 优先从 URL 参数获取 token
	rawKey := c.Query("token")

	// 其次从 Authorization Header 获取
	if rawKey == "" {
		auth := c.GetHeader("Authorization")
		if auth != "" {
			parts := strings.SplitN(auth, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
				rawKey = strings.TrimSpace(parts[1])
			}
		}
	}

	if rawKey == "" {
		return 0, fmt.Errorf("missing API key: provide via ?token= parameter or Authorization header")
	}

	// SHA256 哈希查找 API Key
	hash := sha256.Sum256([]byte(rawKey))
	hashStr := hex.EncodeToString(hash[:])

	var apiKey model.ApiKey
	err := h.db.Where("key_hash = ?", hashStr).First(&apiKey).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, fmt.Errorf("invalid API key")
		}
		return 0, fmt.Errorf("authentication error: %w", err)
	}

	// 检查 Key 状态
	if !apiKey.IsActive {
		return 0, fmt.Errorf("API key is revoked")
	}
	if apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now()) {
		return 0, fmt.Errorf("API key has expired")
	}

	// 异步更新最后使用时间
	go func() {
		_ = h.db.Model(&model.ApiKey{}).
			Where("id = ?", apiKey.ID).
			Update("last_used_at", time.Now()).Error
	}()

	return apiKey.UserID, nil
}
