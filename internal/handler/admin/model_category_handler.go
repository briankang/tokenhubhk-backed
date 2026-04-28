package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	modelcatsvc "tokenhub-server/internal/service/modelcategory"
)

// ModelCategoryHandler 模型分类管理接口处理器
type ModelCategoryHandler struct {
	svc *modelcatsvc.ModelCategoryService
}

// NewModelCategoryHandler creates a new admin ModelCategoryHandler.
func NewModelCategoryHandler(svc *modelcatsvc.ModelCategoryService) *ModelCategoryHandler {
	if svc == nil {
		panic("admin model category handler: service is nil")
	}
	return &ModelCategoryHandler{svc: svc}
}

// List handles GET /api/v1/admin/model-categories
func (h *ModelCategoryHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	supplierID64, _ := strconv.ParseUint(c.DefaultQuery("supplier_id", "0"), 10, 64)

	cats, total, err := h.svc.List(c.Request.Context(), page, pageSize, uint(supplierID64))
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, cats, total, page, pageSize)
}

// Create handles POST /api/v1/admin/model-categories
func (h *ModelCategoryHandler) Create(c *gin.Context) {
	var cat model.ModelCategory
	if err := c.ShouldBindJSON(&cat); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.Create(c.Request.Context(), &cat); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, cat)
}

// GetByID handles GET /api/v1/admin/model-categories/:id
func (h *ModelCategoryHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	cat, err := h.svc.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, err.Error())
		return
	}

	response.Success(c, cat)
}

// Update handles PUT /api/v1/admin/model-categories/:id
func (h *ModelCategoryHandler) Update(c *gin.Context) {
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

	if err := h.svc.Update(c.Request.Context(), uint(id), updates); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "updated"})
}

// Delete handles DELETE /api/v1/admin/model-categories/:id
func (h *ModelCategoryHandler) Delete(c *gin.Context) {
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
