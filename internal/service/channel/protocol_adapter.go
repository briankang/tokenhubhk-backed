package channel

import (
	"encoding/json"
	"fmt"
	"strings"

	"tokenhub-server/internal/model"
)

// ProviderRequest 协议适配后的请求配置
// 包含目标URL、鉴权头和是否需要转换请求体
type ProviderRequest struct {
	TargetURL     string            // 完整的请求URL
	Headers       map[string]string // HTTP请求头（包含鉴权信息）
	NeedTransform bool              // 是否需要转换请求体格式（如 OpenAI → Anthropic）
}

// BuildProviderRequest 根据 Channel 的协议配置构建请求
// 支持的协议:
//   - openai_chat: 标准 OpenAI 格式，POST {Endpoint}/chat/completions
//   - openai_responses: OpenAI Responses API，POST {Endpoint}/responses
//   - anthropic: Anthropic Claude API，POST {Endpoint}/v1/messages，x-api-key 鉴权
//   - custom: 自定义协议，POST {Endpoint}/{ApiPath}
//
// 返回: 目标URL、请求头、是否需要转换请求体
func BuildProviderRequest(ch *model.Channel) *ProviderRequest {
	pr := &ProviderRequest{
		Headers: make(map[string]string),
	}

	// ========== 构建 URL ==========
	pr.TargetURL = buildTargetURL(ch)

	// ========== 构建鉴权头 ==========
	buildAuthHeaders(ch, pr.Headers)

	// ========== 判断是否需要转换请求体 ==========
	// anthropic 和 custom 协议需要转换请求体格式
	protocol := ch.ApiProtocol
	if protocol == "" {
		protocol = "openai_chat"
	}
	pr.NeedTransform = (protocol == "anthropic" || protocol == "custom")

	// ========== 处理供应商特定参数 ==========
	// 将 CustomParams 中的自定义 Header 合并到请求头
	mergeCustomHeaders(ch, pr.Headers)

	return pr
}

// buildTargetURL 根据协议配置构建目标请求URL
// 如果 Channel.ApiPath 非空则使用自定义路径，否则按协议默认路径
func buildTargetURL(ch *model.Channel) string {
	endpoint := strings.TrimRight(ch.Endpoint, "/")

	// 如果配置了自定义 API 路径，优先使用
	if ch.ApiPath != "" {
		apiPath := ch.ApiPath
		if !strings.HasPrefix(apiPath, "/") {
			apiPath = "/" + apiPath
		}
		return endpoint + apiPath
	}

	// 按协议类型使用默认路径
	protocol := ch.ApiProtocol
	if protocol == "" {
		protocol = "openai_chat"
	}

	switch protocol {
	case "openai_chat":
		return endpoint + "/chat/completions"
	case "openai_responses":
		return endpoint + "/responses"
	case "anthropic":
		return endpoint + "/v1/messages"
	case "custom":
		// 自定义协议必须配置 ApiPath，否则使用根路径
		return endpoint
	default:
		return endpoint + "/chat/completions"
	}
}

// buildAuthHeaders 根据鉴权方式构建请求头
// 支持的鉴权方式:
//   - bearer: Authorization: Bearer <APIKey>（默认）
//   - x-api-key: x-api-key: <APIKey>（Anthropic 专用）
//   - custom: <AuthHeader>: <APIKey>（自定义 Header 名称）
func buildAuthHeaders(ch *model.Channel, headers map[string]string) {
	authMethod := ch.AuthMethod
	if authMethod == "" {
		authMethod = "bearer"
	}

	switch authMethod {
	case "bearer":
		headers["Authorization"] = "Bearer " + ch.APIKey
	case "x-api-key":
		headers["x-api-key"] = ch.APIKey
	case "custom":
		// 使用自定义 Header 名称
		headerName := ch.AuthHeader
		if headerName == "" {
			headerName = "Authorization"
		}
		headers[headerName] = ch.APIKey
	default:
		// 默认使用 Bearer 鉴权
		headers["Authorization"] = "Bearer " + ch.APIKey
	}

	// Anthropic 协议需要额外的版本头
	protocol := ch.ApiProtocol
	if protocol == "anthropic" {
		headers["anthropic-version"] = "2023-06-01"
		headers["Content-Type"] = "application/json"
	}
}

// mergeCustomHeaders 将 Channel.CustomParams 中的 headers 字段合并到请求头
// CustomParams JSON 格式示例:
//
//	{
//	  "headers": {"X-Custom-Header": "value"},
//	  "extra_body": {"key": "value"}
//	}
func mergeCustomHeaders(ch *model.Channel, headers map[string]string) {
	if ch.CustomParams == nil {
		return
	}

	var params struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(ch.CustomParams, &params); err != nil {
		return
	}

	// 合并自定义 Header（不覆盖鉴权头）
	for k, v := range params.Headers {
		if _, exists := headers[k]; !exists {
			headers[k] = v
		}
	}
}

// GetCustomBodyParams 提取 Channel.CustomParams 中需要合并到请求体的额外参数
// 返回 map[string]interface{} 供调用方合并到请求 JSON 中
func GetCustomBodyParams(ch *model.Channel) map[string]interface{} {
	if ch.CustomParams == nil {
		return nil
	}

	var params map[string]interface{}
	if err := json.Unmarshal(ch.CustomParams, &params); err != nil {
		return nil
	}

	// 移除 headers 字段（已在 BuildProviderRequest 中处理）
	delete(params, "headers")

	if len(params) == 0 {
		return nil
	}

	return params
}

// ProtocolInfo 返回协议的可读描述信息
func ProtocolInfo(protocol string) string {
	switch protocol {
	case "openai_chat":
		return "OpenAI Chat Completions (/chat/completions)"
	case "openai_responses":
		return "OpenAI Responses API (/responses)"
	case "anthropic":
		return "Anthropic Claude (/v1/messages)"
	case "custom":
		return "自定义协议"
	default:
		return fmt.Sprintf("未知协议: %s", protocol)
	}
}
