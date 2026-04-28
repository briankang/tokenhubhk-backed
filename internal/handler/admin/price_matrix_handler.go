package admin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/middleware"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/pricing"
)

// PriceMatrixHandler 管理 PriceMatrix 读写(v3 引入)。
type PriceMatrixHandler struct {
	svc *pricing.PriceMatrixService
}

// NewPriceMatrixHandler 构造 handler。
func NewPriceMatrixHandler(db *gorm.DB) *PriceMatrixHandler {
	return &PriceMatrixHandler{svc: pricing.NewPriceMatrixService(db)}
}

// matrixResponse 是 GET /price-matrix 的统一响应结构。
type matrixResponse struct {
	ModelID     uint               `json:"model_id"`
	ModelName   string             `json:"model_name"`
	ModelType   string             `json:"model_type"`
	PricingUnit string             `json:"pricing_unit"`
	Matrix      *model.PriceMatrix `json:"matrix"`
	IsDefault   bool               `json:"is_default"` // true = 当前矩阵是按模板生成的占位,尚未保存
}

// GetPriceMatrix 读取模型当前 PriceMatrix。
//
//	GET /api/v1/admin/ai-models/:id/price-matrix
//
// 若模型尚未保存矩阵,返回按 ModelType 默认模板生成的占位矩阵(空 cells),
// 同时 is_default = true,前端据此提示「尚未配置」。
func (h *PriceMatrixHandler) GetPriceMatrix(c *gin.Context) {
	id, ok := h.parseModelID(c)
	if !ok {
		return
	}
	matrix, aiModel, isDefault, err := h.svc.GetMatrix(c.Request.Context(), uint(id))
	if err != nil {
		if errors.Is(err, pricing.ErrPriceMatrixModelNotFound) {
			response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "模型不存在")
			return
		}
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	// isDefault 由 service 层根据「是否从 ModelPricing.PriceMatrix 加载」直接判定,
	// 与 cell 价格内容解耦:默认模板即使被预填了价格,徽标也应显示「尚未保存」,
	// 直到管理员真正点击保存按钮把矩阵写入 DB。
	response.Success(c, matrixResponse{
		ModelID:     aiModel.ID,
		ModelName:   aiModel.ModelName,
		ModelType:   aiModel.ModelType,
		PricingUnit: aiModel.PricingUnit,
		Matrix:      matrix,
		IsDefault:   isDefault,
	})
}

// updateMatrixRequest 是 PUT /price-matrix 的请求体。
type updateMatrixRequest struct {
	Matrix *model.PriceMatrix `json:"matrix" binding:"required"`
}

// UpdatePriceMatrix 整体覆盖保存矩阵。
//
//	PUT /api/v1/admin/ai-models/:id/price-matrix
//	body: { matrix: { schema_version, currency, unit, dimensions, cells } }
//
// 保存后:
//   - 触发公开模型缓存失效(立即对用户可见)
//   - 同步 selling_input/output 顶层字段(向后兼容)
func (h *PriceMatrixHandler) UpdatePriceMatrix(c *gin.Context) {
	id, ok := h.parseModelID(c)
	if !ok {
		return
	}
	var req updateMatrixRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	if req.Matrix == nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "matrix 必填")
		return
	}
	if err := h.svc.UpdateMatrix(c.Request.Context(), uint(id), req.Matrix); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	middleware.CacheInvalidate("cache:/api/v1/public/models*")
	response.Success(c, gin.H{
		"message":  "price matrix updated",
		"model_id": id,
		"cells":    len(req.Matrix.Cells),
	})
}

func (h *PriceMatrixHandler) parseModelID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return 0, false
	}
	return id, true
}
