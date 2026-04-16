package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/parammapping"
)

// ParamMappingHandler 平台参数映射管理 Handler
type ParamMappingHandler struct {
	svc *parammapping.ParamMappingService
}

// NewParamMappingHandler 创建 Handler 实例
func NewParamMappingHandler(svc *parammapping.ParamMappingService) *ParamMappingHandler {
	return &ParamMappingHandler{svc: svc}
}

// ListParams 获取所有平台参数定义（含供应商映射） GET /admin/param-mappings
func (h *ParamMappingHandler) ListParams(c *gin.Context) {
	params, err := h.svc.ListParams(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, params)
}

// GetParam 获取单个参数详情 GET /admin/param-mappings/:id
func (h *ParamMappingHandler) GetParam(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	param, err := h.svc.GetParam(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, param)
}

// CreateParam 创建平台参数 POST /admin/param-mappings
func (h *ParamMappingHandler) CreateParam(c *gin.Context) {
	var param model.PlatformParam
	if err := c.ShouldBindJSON(&param); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.CreateParam(c.Request.Context(), &param); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, param)
}

// UpdateParam 更新平台参数 PUT /admin/param-mappings/:id
func (h *ParamMappingHandler) UpdateParam(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.UpdateParam(c.Request.Context(), uint(id), updates); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "updated"})
}

// DeleteParam 删除平台参数 DELETE /admin/param-mappings/:id
func (h *ParamMappingHandler) DeleteParam(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.DeleteParam(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "deleted"})
}

// UpsertMapping 创建或更新供应商映射 POST /admin/param-mappings/:id/mappings
func (h *ParamMappingHandler) UpsertMapping(c *gin.Context) {
	paramID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || paramID == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	var mapping model.SupplierParamMapping
	if err := c.ShouldBindJSON(&mapping); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}
	mapping.PlatformParamID = uint(paramID)

	if err := h.svc.UpsertMapping(c.Request.Context(), &mapping); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, mapping)
}

// DeleteMapping 删除供应商映射 DELETE /admin/param-mappings/mappings/:mappingId
func (h *ParamMappingHandler) DeleteMapping(c *gin.Context) {
	mappingID, err := strconv.ParseUint(c.Param("mappingId"), 10, 64)
	if err != nil || mappingID == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.DeleteMapping(c.Request.Context(), uint(mappingID)); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "deleted"})
}

// BatchUpdateMappings 批量更新某供应商的映射 PUT /admin/param-mappings/supplier/:code
func (h *ParamMappingHandler) BatchUpdateMappings(c *gin.Context) {
	supplierCode := c.Param("code")
	if supplierCode == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	var mappings []model.SupplierParamMapping
	if err := c.ShouldBindJSON(&mappings); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.BatchUpdateMappings(c.Request.Context(), supplierCode, mappings); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "updated"})
}

// GetMappingsBySupplier 获取某供应商的所有映射 GET /admin/param-mappings/supplier/:code
func (h *ParamMappingHandler) GetMappingsBySupplier(c *gin.Context) {
	supplierCode := c.Param("code")
	if supplierCode == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	mappings, err := h.svc.GetMappingsBySupplier(c.Request.Context(), supplierCode)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, mappings)
}
