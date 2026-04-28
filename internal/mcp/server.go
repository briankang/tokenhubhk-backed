// Package mcp 实现 MCP（Model Context Protocol）协议服务端
// 提供 JSON-RPC 2.0 消息处理，支持 Tools（工具调用）和 Resources（数据资源）
package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// ─── JSON-RPC 2.0 消息结构 ─────────────────────────────────────

// JSONRPCRequest JSON-RPC 2.0 请求消息
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse JSON-RPC 2.0 响应消息
type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      interface{}   `json:"id,omitempty"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

// JSONRPCError JSON-RPC 2.0 错误对象
type JSONRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// ─── MCP 核心类型定义 ─────────────────────────────────────────

// MCPTool MCP 工具定义，每个工具包含名称、描述、输入参数 JSON Schema 和处理函数
type MCPTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"` // JSON Schema 格式
	Handler     func(params map[string]interface{}, userID uint) (interface{}, error)
}

// MCPResource MCP 资源定义，每个资源包含 URI、名称、描述、MIME 类型和读取函数
type MCPResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
	Handler     func(userID uint) (string, error)
}

// MCPServer MCP 协议服务端核心，管理 Tools 和 Resources 注册及 JSON-RPC 消息分发
type MCPServer struct {
	tools     map[string]MCPTool     // 已注册的工具集合
	resources map[string]MCPResource // 已注册的资源集合
	db        *gorm.DB
	redis     *goredis.Client
	mu        sync.RWMutex // 保护并发注册
}

// NewMCPServer 创建 MCP 协议服务端实例
func NewMCPServer(db *gorm.DB, redis *goredis.Client) *MCPServer {
	return &MCPServer{
		tools:     make(map[string]MCPTool),
		resources: make(map[string]MCPResource),
		db:        db,
		redis:     redis,
	}
}

// RegisterTool 注册一个 MCP 工具
func (s *MCPServer) RegisterTool(tool MCPTool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[tool.Name] = tool
}

// RegisterResource 注册一个 MCP 资源
func (s *MCPServer) RegisterResource(res MCPResource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources[res.URI] = res
}

// HandleMessage 处理 JSON-RPC 2.0 消息，根据 method 分发到对应处理器
// 参数 userID 为经过认证的用户 ID
func (s *MCPServer) HandleMessage(raw []byte, userID uint) *JSONRPCResponse {
	var req JSONRPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			Error: &JSONRPCError{
				Code:    -32700,
				Message: "Parse error: invalid JSON",
			},
		}
	}

	if req.JSONRPC != "2.0" {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    -32600,
				Message: "Invalid request: jsonrpc must be '2.0'",
			},
		}
	}

	// 根据 method 分发到对应处理逻辑
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "ping":
		return s.handlePing(req)
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(req, userID)
	case "resources/list":
		return s.handleResourcesList(req)
	case "resources/read":
		return s.handleResourcesRead(req, userID)
	default:
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    -32601,
				Message: fmt.Sprintf("Method not found: %s", req.Method),
			},
		}
	}
}

// handleInitialize 处理 MCP 握手初始化请求，返回服务端信息和支持的能力
func (s *MCPServer) handleInitialize(req JSONRPCRequest) *JSONRPCResponse {
	s.mu.RLock()
	toolCount := len(s.tools)
	resourceCount := len(s.resources)
	s.mu.RUnlock()

	return &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{
					"listChanged": false,
				},
				"resources": map[string]interface{}{
					"subscribe":   false,
					"listChanged": false,
				},
			},
			"serverInfo": map[string]interface{}{
				"name":    "TokenHub MCP Server",
				"version": "1.0.0",
			},
			"meta": map[string]interface{}{
				"toolCount":     toolCount,
				"resourceCount": resourceCount,
			},
		},
	}
}

// handlePing 处理心跳请求
func (s *MCPServer) handlePing(req JSONRPCRequest) *JSONRPCResponse {
	return &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]interface{}{},
	}
}

// handleToolsList 返回所有已注册的工具列表
func (s *MCPServer) handleToolsList(req JSONRPCRequest) *JSONRPCResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tools := make([]map[string]interface{}, 0, len(s.tools))
	for _, t := range s.tools {
		tools = append(tools, map[string]interface{}{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		})
	}

	return &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"tools": tools,
		},
	}
}

// handleToolsCall 调用指定工具并返回结果
func (s *MCPServer) handleToolsCall(req JSONRPCRequest, userID uint) *JSONRPCResponse {
	// 解析参数
	var params struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    -32602,
				Message: "Invalid params: " + err.Error(),
			},
		}
	}

	s.mu.RLock()
	tool, exists := s.tools[params.Name]
	s.mu.RUnlock()

	if !exists {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    -32602,
				Message: fmt.Sprintf("Tool not found: %s", params.Name),
			},
		}
	}

	// 调用工具处理函数
	result, err := tool.Handler(params.Arguments, userID)
	if err != nil {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": fmt.Sprintf("Error: %s", err.Error()),
					},
				},
				"isError": true,
			},
		}
	}

	// 将结果序列化为文本内容
	var text string
	switch v := result.(type) {
	case string:
		text = v
	default:
		data, _ := json.MarshalIndent(result, "", "  ")
		text = string(data)
	}

	return &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": text,
				},
			},
		},
	}
}

// handleResourcesList 返回所有已注册的资源列表
func (s *MCPServer) handleResourcesList(req JSONRPCRequest) *JSONRPCResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	resources := make([]map[string]interface{}, 0, len(s.resources))
	for _, r := range s.resources {
		resources = append(resources, map[string]interface{}{
			"uri":         r.URI,
			"name":        r.Name,
			"description": r.Description,
			"mimeType":    r.MimeType,
		})
	}

	return &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"resources": resources,
		},
	}
}

// handleResourcesRead 读取指定资源内容
func (s *MCPServer) handleResourcesRead(req JSONRPCRequest, userID uint) *JSONRPCResponse {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    -32602,
				Message: "Invalid params: " + err.Error(),
			},
		}
	}

	s.mu.RLock()
	// 精确匹配
	res, exists := s.resources[params.URI]
	// 模板匹配（如 tokenhub://docs/{slug}）
	if !exists {
		for uri, r := range s.resources {
			if matchResourceURI(uri, params.URI) {
				res = r
				exists = true
				break
			}
		}
	}
	s.mu.RUnlock()

	if !exists {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    -32602,
				Message: fmt.Sprintf("Resource not found: %s", params.URI),
			},
		}
	}

	// 调用资源读取函数
	content, err := res.Handler(userID)
	if err != nil {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    -32603,
				Message: fmt.Sprintf("Resource read error: %s", err.Error()),
			},
		}
	}

	return &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"contents": []map[string]interface{}{
				{
					"uri":      params.URI,
					"mimeType": res.MimeType,
					"text":     content,
				},
			},
		},
	}
}

// matchResourceURI 检查请求 URI 是否匹配资源模板（如 tokenhub://docs/{slug}）
func matchResourceURI(template, actual string) bool {
	// 简单模板匹配：将 {xxx} 视为通配符
	if !strings.Contains(template, "{") {
		return template == actual
	}
	tParts := strings.Split(template, "/")
	aParts := strings.Split(actual, "/")
	if len(tParts) != len(aParts) {
		return false
	}
	for i, tp := range tParts {
		if strings.HasPrefix(tp, "{") && strings.HasSuffix(tp, "}") {
			continue // 通配符，匹配任意值
		}
		if tp != aParts[i] {
			return false
		}
	}
	return true
}

// GetToolsList 获取工具列表（外部访问用，如 manifest 端点）
func (s *MCPServer) GetToolsList() []map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tools := make([]map[string]interface{}, 0, len(s.tools))
	for _, t := range s.tools {
		tools = append(tools, map[string]interface{}{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		})
	}
	return tools
}

// GetResourcesList 获取资源列表（外部访问用，如 manifest 端点）
func (s *MCPServer) GetResourcesList() []map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	resources := make([]map[string]interface{}, 0, len(s.resources))
	for _, r := range s.resources {
		resources = append(resources, map[string]interface{}{
			"uri":         r.URI,
			"name":        r.Name,
			"description": r.Description,
			"mimeType":    r.MimeType,
		})
	}
	return resources
}
