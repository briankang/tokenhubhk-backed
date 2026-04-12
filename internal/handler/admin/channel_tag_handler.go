package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	channelsvc "tokenhub-server/internal/service/channel"
)

// ChannelTagHandler 渠道标签管理接口处理器
type ChannelTagHandler struct {
	svc *channelsvc.ChannelTagService
}

// NewChannelTagHandler creates a new ChannelTagHandler.
func NewChannelTagHandler(svc *channelsvc.ChannelTagService) *ChannelTagHandler {
	return &ChannelTagHandler{svc: svc}
}

// Register registers channel tag routes on the given router group.
func (h *ChannelTagHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/channel-tags", h.List)
	rg.POST("/channel-tags", h.Create)
	rg.PUT("/channel-tags/:id", h.Update)
	rg.DELETE("/channel-tags/:id", h.Delete)
	rg.GET("/channel-tags/:id/stats", h.GetStats)
}

// createTagReq is the request body for creating a tag.
type createTagReq struct {
	Name      string `json:"name" binding:"required"`
	Color     string `json:"color"`
	SortOrder int    `json:"sort_order"`
}

// Create handles POST /channel-tags
func (h *ChannelTagHandler) Create(c *gin.Context) {
	var req createTagReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	tag := &model.ChannelTag{
		Name:      req.Name,
		Color:     req.Color,
		SortOrder: req.SortOrder,
	}

	if err := h.svc.Create(c.Request.Context(), tag); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, tag)
}

// List handles GET /channel-tags
func (h *ChannelTagHandler) List(c *gin.Context) {
	tags, err := h.svc.List(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, tags)
}

// Update handles PUT /channel-tags/:id
func (h *ChannelTagHandler) Update(c *gin.Context) {
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

	if err := h.svc.Update(c.Request.Context(), uint(id), updates); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, nil)
}

// Delete handles DELETE /channel-tags/:id
func (h *ChannelTagHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	if err := h.svc.Delete(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, nil)
}

// GetStats handles GET /channel-tags/:id/stats
func (h *ChannelTagHandler) GetStats(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	stats, err := h.svc.GetStats(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, stats)
}
