// Package v1 提供 OpenAI 兼容的 /v1/ 路由处理器
package v1

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// EmbeddingsHandler OpenAI 兼容的向量嵌入接口处理器（占位）
// 处理 POST /v1/embeddings 请求，当前返回 501 Not Implemented
type EmbeddingsHandler struct{}

// NewEmbeddingsHandler 创建 EmbeddingsHandler 实例
func NewEmbeddingsHandler() *EmbeddingsHandler {
	return &EmbeddingsHandler{}
}

// Register 注册路由到 /v1/ 路由组
func (h *EmbeddingsHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/embeddings", h.CreateEmbedding)
}

// CreateEmbedding 处理 POST /v1/embeddings
// 当前返回 501 Not Implemented，预留未来实现向量嵌入功能
func (h *EmbeddingsHandler) CreateEmbedding(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": gin.H{
			"message": "Embeddings endpoint is not yet implemented. Please configure a channel with embedding capability.",
			"type":    "not_implemented",
			"code":    "not_implemented",
		},
	})
}
