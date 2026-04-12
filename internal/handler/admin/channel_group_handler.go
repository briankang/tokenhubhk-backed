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

// ChannelGroupHandler 渠道分组管理接口处理器
type ChannelGroupHandler struct {
	svc *channelsvc.ChannelGroupService
}

// NewChannelGroupHandler creates a new ChannelGroupHandler.
func NewChannelGroupHandler(svc *channelsvc.ChannelGroupService) *ChannelGroupHandler {
	return &ChannelGroupHandler{svc: svc}
}

// Register registers channel group routes on the given router group.
func (h *ChannelGroupHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/channel-groups", h.List)
	rg.POST("/channel-groups", h.Create)
	rg.GET("/channel-groups/:id", h.GetByID)
	rg.PUT("/channel-groups/:id", h.Update)
	rg.DELETE("/channel-groups/:id", h.Delete)
	rg.GET("/channel-groups/:id/channels", h.GetChannels)
}

// createGroupReq is the request body for creating a channel group.
type createGroupReq struct {
	Name       string `json:"name" binding:"required"`
	Code       string `json:"code" binding:"required"`
	Strategy   string `json:"strategy"`
	ChannelIDs []byte `json:"channel_ids,omitempty"`
	MixMode    string `json:"mix_mode"`
	MixConfig  []byte `json:"mix_config,omitempty"`
	TagFilter  []byte `json:"tag_filter,omitempty"`
	IsActive   *bool  `json:"is_active,omitempty"`
}

// Create handles POST /channel-groups
func (h *ChannelGroupHandler) Create(c *gin.Context) {
	var req createGroupReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	group := &model.ChannelGroup{
		Name:       req.Name,
		Code:       req.Code,
		Strategy:   req.Strategy,
		ChannelIDs: req.ChannelIDs,
		MixMode:    req.MixMode,
		MixConfig:  req.MixConfig,
		TagFilter:  req.TagFilter,
	}
	if req.IsActive != nil {
		group.IsActive = *req.IsActive
	} else {
		group.IsActive = true
	}

	if err := h.svc.Create(c.Request.Context(), group); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, group)
}

// List handles GET /channel-groups
func (h *ChannelGroupHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	groups, total, err := h.svc.List(c.Request.Context(), page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, groups, total, page, pageSize)
}

// GetByID handles GET /channel-groups/:id
func (h *ChannelGroupHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	group, err := h.svc.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	response.Success(c, group)
}

// Update handles PUT /channel-groups/:id
func (h *ChannelGroupHandler) Update(c *gin.Context) {
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

// Delete handles DELETE /channel-groups/:id
func (h *ChannelGroupHandler) Delete(c *gin.Context) {
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

// GetChannels handles GET /channel-groups/:id/channels
func (h *ChannelGroupHandler) GetChannels(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	channels, err := h.svc.GetChannels(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, channels)
}
