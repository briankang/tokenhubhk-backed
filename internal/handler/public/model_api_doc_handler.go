package public

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

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
	rg.GET("/model-api-docs/:slug", h.GetBySlug)
}

func (h *ModelAPIDocHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	docs, total, err := h.svc.ListPublic(c.Request.Context(), modelapidoc.ListOptions{
		SupplierCode: c.Query("supplier"),
		ModelName:    c.Query("model"),
		Keyword:      c.Query("q"),
		Locale:       c.Query("locale"),
		Page:         page,
		PageSize:     pageSize,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.PageResult(c, docs, total, page, pageSize)
}

func (h *ModelAPIDocHandler) GetBySlug(c *gin.Context) {
	doc, err := h.svc.GetPublicBySlugLocale(c.Request.Context(), c.Param("slug"), c.Query("locale"))
	if err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}
	response.Success(c, doc)
}
