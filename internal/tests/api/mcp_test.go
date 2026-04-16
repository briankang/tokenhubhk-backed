package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// ─── MCP Test Types ──────────────────────────────────────────

// jsonRPCRequest JSON-RPC 2.0 请求
type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// jsonRPCResponse JSON-RPC 2.0 响应
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// mcpManifestResponse MCP manifest 响应
type mcpManifestResponse struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Protocol    string `json:"protocol"`
	Tools       []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"tools"`
	Resources []struct {
		URI         string `json:"uri"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"resources"`
}

// ─── MCP Helper ──────────────────────────────────────────────

// mcpAPIKey 缓存测试用 API Key
var mcpAPIKey string

// ensureMCPKey 获取或创建测试用 API Key
func ensureMCPKey(t *testing.T) string {
	t.Helper()
	if mcpAPIKey != "" {
		return mcpAPIKey
	}
	requireUser(t)

	name := fmt.Sprintf("mcp_test_%d", time.Now().UnixNano())
	resp, status, err := doPost(baseURL+"/api/v1/user/api-keys", map[string]string{
		"name": name,
	}, userToken)
	if err != nil {
		t.Fatalf("create api key request failed: %v", err)
	}
	if status == http.StatusNotImplemented {
		t.Skip("api key generation not implemented")
	}
	if status != http.StatusOK || resp.Code != 0 {
		t.Fatalf("create api key failed: status=%d, code=%d, msg=%s", status, resp.Code, resp.Message)
	}

	var keyResp struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(resp.Data, &keyResp); err != nil {
		t.Fatalf("parse api key response: %v", err)
	}
	if keyResp.Key == "" {
		t.Fatal("api key is empty")
	}
	mcpAPIKey = keyResp.Key
	return mcpAPIKey
}

// doMCPMessage 发送 JSON-RPC 消息到 MCP message 端点
func doMCPMessage(url string, rpcReq jsonRPCRequest, apiKey string) (*jsonRPCResponse, int, error) {
	data, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("unmarshal (status=%d, body=%s): %w", resp.StatusCode, string(respBody), err)
	}

	return &rpcResp, resp.StatusCode, nil
}

// doMCPGet 发送 GET 请求到 MCP 端点（带 API Key）
func doMCPGet(url, apiKey string) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	return body, resp.StatusCode, nil
}

// ─── Test: Manifest 端点 ─────────────────────────────────────

func TestMCP_Manifest(t *testing.T) {
	body, status, err := doMCPGet(baseURL+"/api/v1/mcp/manifest", "")
	if err != nil {
		t.Fatalf("manifest request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, string(body))
	}

	var manifest mcpManifestResponse
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}

	if manifest.Name == "" {
		t.Error("manifest name is empty")
	}
	if manifest.Version == "" {
		t.Error("manifest version is empty")
	}
	if len(manifest.Tools) == 0 {
		t.Error("manifest tools list is empty")
	}
	if len(manifest.Resources) == 0 {
		t.Error("manifest resources list is empty")
	}

	t.Logf("MCP Manifest: name=%s, version=%s, tools=%d, resources=%d",
		manifest.Name, manifest.Version, len(manifest.Tools), len(manifest.Resources))
}

// ─── Test: Initialize 握手 ─────────────────────────────────────

func TestMCP_Initialize(t *testing.T) {
	apiKey := ensureMCPKey(t)

	rpcReq := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]interface{}{
				"name":    "test-client",
				"version": "1.0.0",
			},
		},
	}

	resp, status, err := doMCPMessage(baseURL+"/api/v1/mcp/message?token="+apiKey, rpcReq, "")
	if err != nil {
		t.Fatalf("initialize request failed: %v", err)
	}
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if resp.Error != nil {
		t.Fatalf("initialize returned error: code=%d, msg=%s", resp.Error.Code, resp.Error.Message)
	}

	// 解析 result
	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("parse initialize result: %v", err)
	}

	if result["protocolVersion"] == nil {
		t.Error("missing protocolVersion in initialize result")
	}
	if result["serverInfo"] == nil {
		t.Error("missing serverInfo in initialize result")
	}
	if result["capabilities"] == nil {
		t.Error("missing capabilities in initialize result")
	}

	t.Logf("Initialize: protocolVersion=%v, serverInfo=%v", result["protocolVersion"], result["serverInfo"])
}

// ─── Test: tools/list ────────────────────────────────────────

func TestMCP_ToolsList(t *testing.T) {
	apiKey := ensureMCPKey(t)

	rpcReq := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
	}

	resp, status, err := doMCPMessage(baseURL+"/api/v1/mcp/message?token="+apiKey, rpcReq, "")
	if err != nil {
		t.Fatalf("tools/list request failed: %v", err)
	}
	skipIfNotFound(t, status)

	if resp.Error != nil {
		t.Fatalf("tools/list error: %s", resp.Error.Message)
	}

	var result struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("parse tools/list: %v", err)
	}

	if len(result.Tools) == 0 {
		t.Fatal("tools/list returned empty list")
	}

	// 验证关键工具存在
	toolNames := make(map[string]bool)
	for _, tool := range result.Tools {
		toolNames[tool.Name] = true
	}

	expectedTools := []string{"list_models", "get_balance", "list_api_keys", "search_docs"}
	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("expected tool '%s' not found in tools/list", name)
		}
	}

	t.Logf("tools/list: %d tools returned", len(result.Tools))
}

// ─── Test: tools/call — get_balance ──────────────────────────

func TestMCP_ToolsCall_GetBalance(t *testing.T) {
	apiKey := ensureMCPKey(t)

	rpcReq := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      3,
		Method:  "tools/call",
		Params: map[string]interface{}{
			"name":      "get_balance",
			"arguments": map[string]interface{}{},
		},
	}

	resp, status, err := doMCPMessage(baseURL+"/api/v1/mcp/message?token="+apiKey, rpcReq, "")
	if err != nil {
		t.Fatalf("tools/call get_balance request failed: %v", err)
	}
	skipIfNotFound(t, status)

	if resp.Error != nil {
		t.Fatalf("tools/call get_balance error: %s", resp.Error.Message)
	}

	// 解析结果：应包含 content 数组
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("parse tools/call result: %v", err)
	}

	if result.IsError {
		t.Fatalf("tools/call returned isError=true: %s", result.Content[0].Text)
	}
	if len(result.Content) == 0 {
		t.Fatal("tools/call returned empty content")
	}

	// 验证余额 JSON 包含预期字段
	var balanceData map[string]interface{}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &balanceData); err != nil {
		t.Fatalf("parse balance data: %v", err)
	}

	if _, ok := balanceData["currency"]; !ok {
		t.Error("balance data missing 'currency' field")
	}

	t.Logf("get_balance: %s", result.Content[0].Text)
}

// ─── Test: tools/call — list_models ──────────────────────────

func TestMCP_ToolsCall_ListModels(t *testing.T) {
	apiKey := ensureMCPKey(t)

	rpcReq := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      4,
		Method:  "tools/call",
		Params: map[string]interface{}{
			"name":      "list_models",
			"arguments": map[string]interface{}{},
		},
	}

	resp, status, err := doMCPMessage(baseURL+"/api/v1/mcp/message?token="+apiKey, rpcReq, "")
	if err != nil {
		t.Fatalf("tools/call list_models failed: %v", err)
	}
	skipIfNotFound(t, status)

	if resp.Error != nil {
		t.Fatalf("tools/call list_models error: %s", resp.Error.Message)
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	if len(result.Content) == 0 {
		t.Fatal("list_models returned empty content")
	}

	t.Logf("list_models: %s", result.Content[0].Text[:min(200, len(result.Content[0].Text))])
}

// ─── Test: resources/list ────────────────────────────────────

func TestMCP_ResourcesList(t *testing.T) {
	apiKey := ensureMCPKey(t)

	rpcReq := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      5,
		Method:  "resources/list",
	}

	resp, status, err := doMCPMessage(baseURL+"/api/v1/mcp/message?token="+apiKey, rpcReq, "")
	if err != nil {
		t.Fatalf("resources/list request failed: %v", err)
	}
	skipIfNotFound(t, status)

	if resp.Error != nil {
		t.Fatalf("resources/list error: %s", resp.Error.Message)
	}

	var result struct {
		Resources []struct {
			URI  string `json:"uri"`
			Name string `json:"name"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("parse resources/list: %v", err)
	}

	if len(result.Resources) == 0 {
		t.Fatal("resources/list returned empty")
	}

	// 验证关键资源存在
	uris := make(map[string]bool)
	for _, r := range result.Resources {
		uris[r.URI] = true
	}

	expectedURIs := []string{"tokenhub://models", "tokenhub://pricing", "tokenhub://balance"}
	for _, uri := range expectedURIs {
		if !uris[uri] {
			t.Errorf("expected resource '%s' not found", uri)
		}
	}

	t.Logf("resources/list: %d resources returned", len(result.Resources))
}

// ─── Test: resources/read — models ───────────────────────────

func TestMCP_ResourcesRead_Models(t *testing.T) {
	apiKey := ensureMCPKey(t)

	rpcReq := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      6,
		Method:  "resources/read",
		Params: map[string]interface{}{
			"uri": "tokenhub://models",
		},
	}

	resp, status, err := doMCPMessage(baseURL+"/api/v1/mcp/message?token="+apiKey, rpcReq, "")
	if err != nil {
		t.Fatalf("resources/read models failed: %v", err)
	}
	skipIfNotFound(t, status)

	if resp.Error != nil {
		t.Fatalf("resources/read models error: %s", resp.Error.Message)
	}

	var result struct {
		Contents []struct {
			URI      string `json:"uri"`
			MimeType string `json:"mimeType"`
			Text     string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("parse resources/read: %v", err)
	}

	if len(result.Contents) == 0 {
		t.Fatal("resources/read returned empty contents")
	}
	if result.Contents[0].MimeType != "application/json" {
		t.Errorf("expected mimeType 'application/json', got '%s'", result.Contents[0].MimeType)
	}

	t.Logf("resources/read models: %d bytes", len(result.Contents[0].Text))
}

// ─── Test: 认证失败 401 ─────────────────────────────────────

func TestMCP_Auth_Failure(t *testing.T) {
	rpcReq := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      7,
		Method:  "tools/list",
	}

	data, err := json.Marshal(rpcReq)
	if err != nil {
		t.Fatal(err)
	}

	// 不提供 token 和 Authorization
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/mcp/message", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		t.Skip("MCP endpoint not registered")
	}

	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 401, got %d: %s", resp.StatusCode, string(body))
	}

	t.Log("Auth failure: correctly returned 401")
}

// ─── Test: Ping 心跳 ──────────────────────────────────────────

func TestMCP_Ping(t *testing.T) {
	apiKey := ensureMCPKey(t)

	rpcReq := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      8,
		Method:  "ping",
	}

	resp, status, err := doMCPMessage(baseURL+"/api/v1/mcp/message?token="+apiKey, rpcReq, "")
	if err != nil {
		t.Fatalf("ping request failed: %v", err)
	}
	skipIfNotFound(t, status)

	if resp.Error != nil {
		t.Fatalf("ping error: %s", resp.Error.Message)
	}

	t.Log("Ping: success")
}

// Go 1.21+ 内置 min 函数，不再需要自定义
