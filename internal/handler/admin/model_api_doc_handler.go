package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	modelapidoc "tokenhub-server/internal/service/modelapidoc"
)

type ModelAPIDocHandler struct {
	svc *modelapidoc.Service
}

func NewModelAPIDocHandler(svc *modelapidoc.Service) *ModelAPIDocHandler {
	return &ModelAPIDocHandler{svc: svc}
}

func (h *ModelAPIDocHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/model-api-docs", h.List)
	rg.GET("/model-api-docs/:id", h.Get)
	rg.POST("/model-api-docs", h.Create)
	rg.PUT("/model-api-docs/:id", h.Update)
	rg.POST("/model-api-docs/:id/sources", h.CreateSource)
	rg.POST("/model-api-docs/:id/param-verifications", h.UpsertParamVerification)
}

func (h *ModelAPIDocHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	docs, total, err := h.svc.ListAdmin(c.Request.Context(), modelapidoc.ListOptions{
		SupplierCode: c.Query("supplier"),
		ModelName:    c.Query("model"),
		Keyword:      c.Query("q"),
		Page:         page,
		PageSize:     pageSize,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.PageResult(c, docs, total, page, pageSize)
}

func (h *ModelAPIDocHandler) Get(c *gin.Context) {
	id, err := parseModelAPIDocUintParam(c, "id")
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}
	doc, err := h.svc.GetAdminByID(c.Request.Context(), id)
	if err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}
	response.Success(c, doc)
}

func (h *ModelAPIDocHandler) Create(c *gin.Context) {
	var doc model.ModelAPIDoc
	if err := c.ShouldBindJSON(&doc); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}
	if err := h.svc.Create(c.Request.Context(), &doc); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, doc)
}

func (h *ModelAPIDocHandler) Update(c *gin.Context) {
	id, err := parseModelAPIDocUintParam(c, "id")
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}
	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}
	if err := h.svc.Update(c.Request.Context(), id, updates); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "updated"})
}

func (h *ModelAPIDocHandler) CreateSource(c *gin.Context) {
	id, err := parseModelAPIDocUintParam(c, "id")
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}
	var source model.ModelAPIDocSource
	if err := c.ShouldBindJSON(&source); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}
	source.DocID = id
	if err := h.svc.UpsertSource(c.Request.Context(), &source); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, source)
}

func (h *ModelAPIDocHandler) UpsertParamVerification(c *gin.Context) {
	id, err := parseModelAPIDocUintParam(c, "id")
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}
	var item model.ModelAPIParamVerification
	if err := c.ShouldBindJSON(&item); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}
	item.DocID = id
	if err := h.svc.UpsertParamVerification(c.Request.Context(), &item); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, item)
}

func parseModelAPIDocUintParam(c *gin.Context, name string) (uint, error) {
	id, err := strconv.ParseUint(c.Param(name), 10, 64)
	return uint(id), err
}
