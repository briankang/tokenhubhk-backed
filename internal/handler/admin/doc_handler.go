package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	docsvc "tokenhub-server/internal/service/doc"
)

// DocHandler 文档管理接口处理器
type DocHandler struct {
	docSvc *docsvc.DocService
	catSvc *docsvc.DocCategoryService
}

// NewDocHandler 创建文档管理Handler实例
func NewDocHandler(docSvc *docsvc.DocService, catSvc *docsvc.DocCategoryService) *DocHandler {
	return &DocHandler{docSvc: docSvc, catSvc: catSvc}
}

// Register 注册文档管理路由到路由组
func (h *DocHandler) Register(rg *gin.RouterGroup) {
	// Doc routes
	rg.GET("/docs", h.ListDocs)
	rg.POST("/docs", h.CreateDoc)
	rg.GET("/docs/:id", h.GetDoc)
	rg.PUT("/docs/:id", h.UpdateDoc)
	rg.DELETE("/docs/:id", h.DeleteDoc)
	rg.POST("/docs/:id/publish", h.PublishDoc)
	rg.POST("/docs/:id/unpublish", h.UnpublishDoc)

	// Category routes
	rg.GET("/doc-categories", h.ListCategories)
	rg.POST("/doc-categories", h.CreateCategory)
	rg.PUT("/doc-categories/:id", h.UpdateCategory)
	rg.DELETE("/doc-categories/:id", h.DeleteCategory)
}

// --- Doc request types ---

type createDocReq struct {
	Title      string `json:"title" binding:"required"`
	Slug       string `json:"slug" binding:"required"`
	Content    string `json:"content"`
	CategoryID *uint  `json:"category_id"`
	SortOrder  int    `json:"sort_order"`
	Author     uint   `json:"author"`
}

type createCategoryReq struct {
	Name      string `json:"name" binding:"required"`
	Slug      string `json:"slug" binding:"required"`
	SortOrder int    `json:"sort_order"`
}

// --- Doc handlers ---

// ListDocs handles GET /admin/docs
func (h *DocHandler) ListDocs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	var categoryID *uint
	if v := c.Query("category_id"); v != "" {
		id, err := strconv.ParseUint(v, 10, 64)
		if err == nil {
			uid := uint(id)
			categoryID = &uid
		}
	}

	var published *bool
	if v := c.Query("published"); v != "" {
		b := v == "true" || v == "1"
		published = &b
	}

	docs, total, err := h.docSvc.List(c.Request.Context(), categoryID, published, page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, docs, total, page, pageSize)
}

// CreateDoc handles POST /admin/docs
func (h *DocHandler) CreateDoc(c *gin.Context) {
	var req createDocReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	doc := &model.Doc{
		Title:      req.Title,
		Slug:       req.Slug,
		Content:    req.Content,
		CategoryID: req.CategoryID,
		SortOrder:  req.SortOrder,
		Author:     req.Author,
	}

	if err := h.docSvc.Create(c.Request.Context(), doc); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, doc)
}

// GetDoc handles GET /admin/docs/:id
func (h *DocHandler) GetDoc(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	doc, err := h.docSvc.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	response.Success(c, doc)
}

// UpdateDoc handles PUT /admin/docs/:id
func (h *DocHandler) UpdateDoc(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	if err := h.docSvc.Update(c.Request.Context(), uint(id), updates); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, nil)
}

// DeleteDoc handles DELETE /admin/docs/:id
func (h *DocHandler) DeleteDoc(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	if err := h.docSvc.Delete(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, nil)
}

// PublishDoc handles POST /admin/docs/:id/publish
func (h *DocHandler) PublishDoc(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	if err := h.docSvc.Publish(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, nil)
}

// UnpublishDoc handles POST /admin/docs/:id/unpublish
func (h *DocHandler) UnpublishDoc(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	if err := h.docSvc.Unpublish(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, nil)
}

// --- Category handlers ---

// ListCategories handles GET /admin/doc-categories
func (h *DocHandler) ListCategories(c *gin.Context) {
	cats, err := h.catSvc.List(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, cats)
}

// CreateCategory handles POST /admin/doc-categories
func (h *DocHandler) CreateCategory(c *gin.Context) {
	var req createCategoryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	cat := &model.DocCategory{
		Name:      req.Name,
		Slug:      req.Slug,
		SortOrder: req.SortOrder,
	}

	if err := h.catSvc.Create(c.Request.Context(), cat); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, cat)
}

// UpdateCategory handles PUT /admin/doc-categories/:id
func (h *DocHandler) UpdateCategory(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	if err := h.catSvc.Update(c.Request.Context(), uint(id), updates); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, nil)
}

// DeleteCategory handles DELETE /admin/doc-categories/:id
func (h *DocHandler) DeleteCategory(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	if err := h.catSvc.Delete(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, nil)
}
