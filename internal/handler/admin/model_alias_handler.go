package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	modelaliassvc "tokenhub-server/internal/service/modelalias"
)

type ModelAliasHandler struct {
	svc *modelaliassvc.Service
}

func NewModelAliasHandler(svc *modelaliassvc.Service) *ModelAliasHandler {
	return &ModelAliasHandler{svc: svc}
}

func (h *ModelAliasHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/model-aliases", h.List)
	rg.POST("/model-aliases", h.Create)
	rg.PUT("/model-aliases/:id", h.Update)
	rg.DELETE("/model-aliases/:id", h.Delete)
	rg.POST("/model-aliases/infer", h.Infer)
	rg.GET("/model-aliases/resolve", h.Resolve)
}

func (h *ModelAliasHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	supplierID, _ := strconv.ParseUint(c.Query("supplier_id"), 10, 64)
	result, err := h.svc.List(modelaliassvc.ListOptions{
		Page:        page,
		PageSize:    pageSize,
		Search:      strings.TrimSpace(c.Query("search")),
		Model:       strings.TrimSpace(c.Query("model")),
		SupplierID:  uint(supplierID),
		ActiveOnly:  boolQuery(c, "active_only"),
		PublicOnly:  boolQuery(c, "public_only"),
		Source:      strings.TrimSpace(c.Query("source")),
		AliasType:   strings.TrimSpace(c.Query("alias_type")),
		IncludeMeta: true,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, result)
}

func (h *ModelAliasHandler) Create(c *gin.Context) {
	var req model.ModelAlias
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	created, err := h.svc.Create(&req)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, created)
}

func (h *ModelAliasHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	var patch map[string]interface{}
	if err := c.ShouldBindJSON(&patch); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	updated, err := h.svc.Update(uint(id), patch)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, updated)
}

func (h *ModelAliasHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	if err := h.svc.Delete(uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"id": id})
}

func (h *ModelAliasHandler) Infer(c *gin.Context) {
	var req struct {
		SupplierID uint `json:"supplier_id"`
		Apply      bool `json:"apply"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	items, err := h.svc.SuggestAliases(modelaliassvc.SuggestOptions{SupplierID: req.SupplierID, Apply: req.Apply})
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"list": items, "total": len(items), "applied": req.Apply})
}

func (h *ModelAliasHandler) Resolve(c *gin.Context) {
	modelName := strings.TrimSpace(c.Query("model"))
	result, err := h.svc.Resolve(modelName)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, result)
}

func boolQuery(c *gin.Context, key string) bool {
	value := strings.ToLower(strings.TrimSpace(c.Query(key)))
	return value == "1" || value == "true" || value == "yes"
}
