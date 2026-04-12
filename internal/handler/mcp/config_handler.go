// Package mcphandler - MCP 配置生成器
// 为不同的 AI 客户端工具生成 MCP 接入配置片段
package mcphandler

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

// ConfigHandler MCP 配置生成器处理器
type ConfigHandler struct{}

// NewConfigHandler 创建配置生成器处理器实例
func NewConfigHandler() *ConfigHandler {
	return &ConfigHandler{}
}

// Register 注册配置生成器路由
func (h *ConfigHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/config", h.GenerateConfig)
}

// GenerateConfig 处理 GET /api/v1/mcp/config?tool=cursor&token=sk-xxx
// 根据指定工具生成对应的 MCP 配置片段
// 支持的工具: claude_desktop, cursor, continue, cline, windsurf, generic
func (h *ConfigHandler) GenerateConfig(c *gin.Context) {
	tool := c.DefaultQuery("tool", "generic")
	apiKey := c.DefaultQuery("token", "sk-your-api-key")

	// 获取服务器基础 URL
	baseURL := getBaseURL()
	sseURL := fmt.Sprintf("%s/api/v1/mcp/sse?token=%s", baseURL, apiKey)

	var config interface{}
	var filename string

	switch tool {
	case "claude_desktop", "claude":
		// Claude Desktop 配置格式（claude_desktop_config.json）
		config = map[string]interface{}{
			"mcpServers": map[string]interface{}{
				"tokenhub": map[string]interface{}{
					"url":       sseURL,
					"transport": "sse",
				},
			},
		}
		filename = "claude_desktop_config.json"

	case "cursor":
		// Cursor 配置格式（.cursor/mcp.json）
		config = map[string]interface{}{
			"mcpServers": map[string]interface{}{
				"tokenhub": map[string]interface{}{
					"url":       sseURL,
					"transport": "sse",
				},
			},
		}
		filename = ".cursor/mcp.json"

	case "continue":
		// Continue 配置格式（.continue/config.yaml 中的 MCP 部分）
		config = map[string]interface{}{
			"mcpServers": []map[string]interface{}{
				{
					"name":      "tokenhub",
					"url":       sseURL,
					"transport": "sse",
				},
			},
		}
		filename = ".continue/config.yaml (mcpServers section)"

	case "cline":
		// Cline 配置格式
		config = map[string]interface{}{
			"mcpServers": map[string]interface{}{
				"tokenhub": map[string]interface{}{
					"url":       sseURL,
					"transport": "sse",
					"disabled":  false,
				},
			},
		}
		filename = "cline_mcp_settings.json"

	case "windsurf":
		// Windsurf 配置格式
		config = map[string]interface{}{
			"mcpServers": map[string]interface{}{
				"tokenhub": map[string]interface{}{
					"serverUrl": sseURL,
				},
			},
		}
		filename = "~/.codeium/windsurf/mcp_config.json"

	default:
		// 通用配置格式
		config = map[string]interface{}{
			"mcpServers": map[string]interface{}{
				"tokenhub": map[string]interface{}{
					"url":       sseURL,
					"transport": "sse",
				},
			},
		}
		filename = "mcp.json"
	}

	c.JSON(http.StatusOK, gin.H{
		"tool":     tool,
		"filename": filename,
		"config":   config,
		"instructions": getInstructions(tool),
		"endpoints": map[string]string{
			"sse":      fmt.Sprintf("%s/api/v1/mcp/sse", baseURL),
			"message":  fmt.Sprintf("%s/api/v1/mcp/message", baseURL),
			"manifest": fmt.Sprintf("%s/api/v1/mcp/manifest", baseURL),
		},
	})
}

// getBaseURL 获取服务器基础 URL
func getBaseURL() string {
	// 从配置中获取，如果未配置则使用默认值
	domain := viper.GetString("server.platform_domain")
	if domain != "" {
		return "https://" + domain
	}
	port := viper.GetInt("server.port")
	if port == 0 {
		port = 8090
	}
	return fmt.Sprintf("http://localhost:%d", port)
}

// getInstructions 获取不同工具的配置说明
func getInstructions(tool string) string {
	switch tool {
	case "claude_desktop", "claude":
		return "将以下配置添加到 Claude Desktop 的 claude_desktop_config.json 文件中。" +
			"\nmacOS: ~/Library/Application Support/Claude/claude_desktop_config.json" +
			"\nWindows: %APPDATA%\\Claude\\claude_desktop_config.json" +
			"\n配置完成后重启 Claude Desktop 即可使用 TokenHub 的所有工具。"

	case "cursor":
		return "将以下配置保存到项目根目录的 .cursor/mcp.json 文件中。" +
			"\n或全局配置: ~/.cursor/mcp.json" +
			"\n配置完成后重启 Cursor 即可使用 TokenHub 的所有工具。"

	case "continue":
		return "将以下 mcpServers 配置添加到 .continue/config.yaml 文件中。" +
			"\n或通过 Continue 的设置 UI 添加 MCP Server。" +
			"\n配置完成后在 Continue 面板中即可看到 TokenHub 的工具。"

	case "cline":
		return "在 Cline 的 MCP 设置中添加以下配置。" +
			"\n打开 VSCode → Cline 扩展 → 设置 → MCP Servers → 添加。" +
			"\n配置完成后在 Cline 对话中即可调用 TokenHub 的工具。"

	case "windsurf":
		return "将以下配置添加到 Windsurf 的 MCP 配置文件中。" +
			"\n路径: ~/.codeium/windsurf/mcp_config.json" +
			"\n配置完成后重启 Windsurf 即可使用。"

	default:
		return "将以下 MCP 配置添加到你的 AI 工具中。" +
			"\n大多数支持 MCP 的工具都使用类似的配置格式。" +
			"\n确保你的 API Key 有效，然后将 SSE URL 配置到工具中即可。"
	}
}
