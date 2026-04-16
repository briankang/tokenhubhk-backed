package public

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/parammapping"
)

// ParamSupportHandler 参数支持情况查询处理器
type ParamSupportHandler struct {
	svc *parammapping.ParamMappingService
}

// NewParamSupportHandler 创建参数支持处理器
func NewParamSupportHandler(svc *parammapping.ParamMappingService) *ParamSupportHandler {
	return &ParamSupportHandler{svc: svc}
}

// GetParamSupport GET /api/v1/public/param-support?supplier={supplier_code}
func (h *ParamSupportHandler) GetParamSupport(c *gin.Context) {
	supplierCode := c.Query("supplier")
	if supplierCode == "" {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "supplier is required")
		return
	}

	params, err := h.svc.GetSupplierParamSupport(c.Request.Context(), supplierCode)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}

	response.Success(c, gin.H{
		"supplier_code": supplierCode,
		"params":        params,
	})
}
