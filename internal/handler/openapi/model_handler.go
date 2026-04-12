package openapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/response"
	svc "tokenhub-server/internal/service/openapi"
)

// ModelHandler 处理模型定价相关的 Open API 请求。
type ModelHandler struct {
	service *svc.OpenAPIService
}

// NewModelHandler 创建模型定价 Handler 实例。
func NewModelHandler(service *svc.OpenAPIService) *ModelHandler {
	return &ModelHandler{service: service}
}

// Register 注册模型相关路由。
func (h *ModelHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/models/pricing", h.Pricing)
}

// Pricing 获取模型定价列表。
// GET /api/v1/open/models/pricing
func (h *ModelHandler) Pricing(c *gin.Context) {
	items, err := h.service.GetModelPricingList(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, items)
}
