package user

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
)

// AvailableModelsHandler 用户可用模型列表处理器
type AvailableModelsHandler struct {
	db *gorm.DB
}

// NewAvailableModelsHandler 创建可用模型列表处理器
func NewAvailableModelsHandler(db *gorm.DB) *AvailableModelsHandler {
	return &AvailableModelsHandler{db: db}
}

// Register 注册路由
func (h *AvailableModelsHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/available-models", h.List)
}

// ModelInfo 模型信息（用于返回给用户）
type ModelInfo struct {
	ID           uint   `json:"id"`
	ModelName    string `json:"model_name"`
	DisplayName  string `json:"display_name"`
	CategoryID   uint   `json:"category_id"`   // 分类 ID
	Description  string `json:"description"`
	IsAlias      bool   `json:"is_alias"`      // 是否为别名模型
	ActualModels string `json:"actual_models"` // 如果是别名，显示实际映射的模型列表
}

// List 返回用户可用的模型列表（含别名模型）
// GET /api/v1/user/available-models
// 只返回 is_active=true 且 status=online 的模型
func (h *AvailableModelsHandler) List(c *gin.Context) {
	// 1. 获取普通 AI 模型列表（只返回已验证上线的模型）
	var aiModels []model.AIModel
	if err := h.db.WithContext(c.Request.Context()).
		Where("is_active = ? AND status = ?", true, "online").
		Order("model_name ASC").
		Find(&aiModels).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 2. 获取自定义渠道路由的别名模型列表（替代旧的 ModelAlias）
	var routes []model.CustomChannelRoute
	if err := h.db.WithContext(c.Request.Context()).
		Where("is_active = ?", true).
		Order("alias_model ASC").
		Find(&routes).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 3. 组装结果
	result := make([]ModelInfo, 0, len(aiModels)+len(routes))

	// 添加普通模型
	for _, m := range aiModels {
		result = append(result, ModelInfo{
			ID:          m.ID,
			ModelName:   m.ModelName,
			DisplayName: m.DisplayName,
			CategoryID:  m.CategoryID,
			Description: m.Description,
			IsAlias:     false,
		})
	}

	// 添加别名模型（去重，从自定义渠道路由中聚合）
	aliasMap := make(map[string]*ModelInfo)
	for _, r := range routes {
		if existing, ok := aliasMap[r.AliasModel]; ok {
			// 已存在，追加实际模型
			if existing.ActualModels != "" {
				existing.ActualModels += ", "
			}
			existing.ActualModels += r.ActualModel
		} else {
			// 新增
			aliasMap[r.AliasModel] = &ModelInfo{
				ModelName:    r.AliasModel,
				DisplayName:  r.AliasModel,
				IsAlias:      true,
				ActualModels: r.ActualModel,
			}
		}
	}

	// 将别名模型添加到结果
	for _, info := range aliasMap {
		result = append(result, *info)
	}

	response.Success(c, result)
}
