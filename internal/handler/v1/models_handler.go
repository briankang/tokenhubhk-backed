// Package v1 提供 OpenAI 兼容的 /v1/ 路由处理器
package v1

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// ModelsHandler OpenAI 兼容的模型列表接口处理器
// 处理 GET /v1/models 请求，返回标准 OpenAI 格式的模型列表
type ModelsHandler struct {
	db *gorm.DB
}

// NewModelsHandler 创建 ModelsHandler 实例
func NewModelsHandler(db *gorm.DB) *ModelsHandler {
	return &ModelsHandler{db: db}
}

// Register 注册路由到 /v1/ 路由组
func (h *ModelsHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/models", h.ListModels)
}

// openAIModel OpenAI 格式的模型信息结构体
type openAIModel struct {
	ID      string `json:"id"`       // 模型标识符（即 model_name）
	Object  string `json:"object"`   // 固定值 "model"
	Created int64  `json:"created"`  // 创建时间戳
	OwnedBy string `json:"owned_by"` // 所属供应商
}

// openAIModelList OpenAI 格式的模型列表响应
type openAIModelList struct {
	Object string        `json:"object"` // 固定值 "list"
	Data   []openAIModel `json:"data"`   // 模型列表
}

// ListModels 处理 GET /v1/models
// 返回 OpenAI 兼容格式的模型列表，包含所有已激活的 AI 模型
func (h *ModelsHandler) ListModels(c *gin.Context) {
	// 从数据库查询所有已激活的 AI 模型，并预加载供应商信息
	var models []model.AIModel
	if err := h.db.Where("is_active = ?", true).
		Preload("Supplier").
		Order("model_name ASC").
		Find(&models).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"message": "Failed to fetch models",
				"type":    "server_error",
			},
		})
		return
	}

	// 格式化为 OpenAI 兼容的模型列表
	data := make([]openAIModel, 0, len(models))
	for _, m := range models {
		ownedBy := "tokenhub"
		if m.Supplier.Code != "" {
			ownedBy = m.Supplier.Code
		}
		data = append(data, openAIModel{
			ID:      m.ModelName,
			Object:  "model",
			Created: m.CreatedAt.Unix(),
			OwnedBy: ownedBy,
		})
	}

	c.JSON(http.StatusOK, openAIModelList{
		Object: "list",
		Data:   data,
	})
}
