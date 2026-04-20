package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	suppliersvc "tokenhub-server/internal/service/supplier"
)

// SupplierHandler 供应商管理接口处理器
type SupplierHandler struct {
	svc *suppliersvc.SupplierService
}

// NewSupplierHandler creates a new admin SupplierHandler.
func NewSupplierHandler(svc *suppliersvc.SupplierService) *SupplierHandler {
	if svc == nil {
		panic("admin supplier handler: service is nil")
	}
	return &SupplierHandler{svc: svc}
}

// createSupplierReq 创建供应商请求体
type createSupplierReq struct {
	Name            string  `json:"name" binding:"required"`
	Code            string  `json:"code" binding:"required"`
	BaseURL         string  `json:"base_url"`
	Description     string  `json:"description"`
	IsActive        bool    `json:"is_active"`
	SortOrder       int     `json:"sort_order"`
	AccessType      string  `json:"access_type" binding:"required,oneof=api coding_plan"` // 接入点类型: api / coding_plan
	InputPricePerM  float64 `json:"input_price_per_m"`                                    // 输入tokens官网价格（每百万tokens）
	OutputPricePerM float64 `json:"output_price_per_m"`                                   // 输出tokens官网价格（每百万tokens）
	Discount        float64 `json:"discount"`                                             // 折扣比例
	Status          string  `json:"status"`                                               // 状态: active / inactive / maintenance
	PricingURL      string  `json:"pricing_url"`                                          // 官方定价文档 URL（v3.5）
	PricingURLs     []model.PricingURLEntry `json:"pricing_urls,omitempty"`                 // 多页定价配置（v3.5）
}

// updateSupplierReq 更新供应商请求体
type updateSupplierReq struct {
	Name            string  `json:"name"`
	Code            string  `json:"code"`
	BaseURL         string  `json:"base_url"`
	Description     string  `json:"description"`
	IsActive        *bool   `json:"is_active"`
	SortOrder       *int    `json:"sort_order"`
	AccessType      string  `json:"access_type" binding:"omitempty,oneof=api coding_plan"`
	InputPricePerM  *float64 `json:"input_price_per_m"`
	OutputPricePerM *float64 `json:"output_price_per_m"`
	Discount        *float64 `json:"discount"`
	Status          string   `json:"status" binding:"omitempty,oneof=active inactive maintenance"`
	PricingURL      *string                   `json:"pricing_url"`                 // 官方定价文档 URL（v3.5，可置空串清除）
	PricingURLs     []model.PricingURLEntry   `json:"pricing_urls,omitempty"`      // 多页定价配置（v3.5）
}

// List handles GET /api/v1/admin/suppliers
// 支持按 access_type 过滤
func (h *SupplierHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	accessType := c.Query("access_type") // 可选过滤参数: api / coding_plan

	suppliers, total, err := h.svc.List(c.Request.Context(), page, pageSize, accessType)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, suppliers, total, page, pageSize)
}

// Create handles POST /api/v1/admin/suppliers
// 创建供应商，需要提供 access_type
func (h *SupplierHandler) Create(c *gin.Context) {
	var req createSupplierReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 构建供应商模型
	supplier := model.Supplier{
		Name:            req.Name,
		Code:            req.Code,
		BaseURL:         req.BaseURL,
		Description:     req.Description,
		IsActive:        req.IsActive,
		SortOrder:       req.SortOrder,
		AccessType:      req.AccessType,
		InputPricePerM:  req.InputPricePerM,
		OutputPricePerM: req.OutputPricePerM,
		Discount:        req.Discount,
		Status:          req.Status,
		PricingURL:      req.PricingURL,
	}

	// PricingURLs 多页定价配置 → JSON 序列化
	if len(req.PricingURLs) > 0 {
		if b, err := json.Marshal(req.PricingURLs); err == nil {
			supplier.PricingURLs = model.JSON(b)
		}
	}

	// 设置默认值
	if supplier.AccessType == "" {
		supplier.AccessType = "api"
	}
	if supplier.Discount == 0 {
		supplier.Discount = 1.0
	}
	if supplier.Status == "" {
		supplier.Status = "active"
	}

	if err := h.svc.Create(c.Request.Context(), &supplier); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, supplier)
}

// GetByID handles GET /api/v1/admin/suppliers/:id
func (h *SupplierHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	supplier, err := h.svc.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, err.Error())
		return
	}

	response.Success(c, supplier)
}

// Update handles PUT /api/v1/admin/suppliers/:id
func (h *SupplierHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	var req updateSupplierReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 构建更新字段 map
	updates := make(map[string]interface{})
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.Code != "" {
		updates["code"] = req.Code
	}
	if req.BaseURL != "" {
		updates["base_url"] = req.BaseURL
	}
	if req.Description != "" {
		updates["description"] = req.Description
	}
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}
	if req.AccessType != "" {
		updates["access_type"] = req.AccessType
	}
	if req.InputPricePerM != nil {
		updates["input_price_per_m"] = *req.InputPricePerM
	}
	if req.OutputPricePerM != nil {
		updates["output_price_per_m"] = *req.OutputPricePerM
	}
	if req.Discount != nil {
		updates["discount"] = *req.Discount
	}
	if req.Status != "" {
		updates["status"] = req.Status
	}
	if req.PricingURL != nil {
		// 允许置空串清除（不同于省略该字段）
		updates["pricing_url"] = *req.PricingURL
	}
	if req.PricingURLs != nil {
		if b, err := json.Marshal(req.PricingURLs); err == nil {
			updates["pricing_urls"] = model.JSON(b)
		}
	}

	if err := h.svc.Update(c.Request.Context(), uint(id), updates); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "updated"})
}

// Delete handles DELETE /api/v1/admin/suppliers/:id
func (h *SupplierHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.Delete(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "deleted"})
}
