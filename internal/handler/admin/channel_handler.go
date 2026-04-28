package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	auditmw "tokenhub-server/internal/middleware/audit"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	channelsvc "tokenhub-server/internal/service/channel"
)

// ChannelHandler 渠道管理接口处理器
type ChannelHandler struct {
	svc *channelsvc.ChannelService
}

// NewChannelHandler 创建渠道管理Handler实例
func NewChannelHandler(svc *channelsvc.ChannelService) *ChannelHandler {
	return &ChannelHandler{svc: svc}
}

// Register 注册渠道管理路由到路由组
func (h *ChannelHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/channels", h.List)
	rg.GET("/channels/custom-params/schema", h.CustomParamSchema)
	rg.POST("/channels", h.Create)
	rg.GET("/channels/:id", h.GetByID)
	rg.PUT("/channels/:id", h.Update)
	rg.DELETE("/channels/:id", h.Delete)
	rg.PUT("/channels/:id/tags", h.SetTags)
	rg.POST("/channels/:id/test", h.TestChannel)
	rg.POST("/channels/:id/verify", h.VerifyChannel) // 验证渠道Key
}

// createChannelReq is the request body for creating a channel.
type createChannelReq struct {
	Name                  string          `json:"name" binding:"required"`
	SupplierID            uint            `json:"supplier_id" binding:"required"`
	Type                  string          `json:"type" binding:"required"`
	Endpoint              string          `json:"endpoint" binding:"required"`
	APIKey                string          `json:"api_key" binding:"required"`
	Models                []byte          `json:"models,omitempty"`
	Weight                int             `json:"weight"`
	Priority              int             `json:"priority"`
	Status                string          `json:"status"`
	MaxConcurrency        int             `json:"max_concurrency"`
	QPM                   int             `json:"qpm"`
	PreferenceTag         string          `json:"preference_tag"`         // 偏好标签: availability/cost/speed
	SupportedCapabilities string          `json:"supported_capabilities"` // 支持能力，逗号分隔: chat,image,video,tts,asr,embedding
	ChannelType           string          `json:"channel_type"`
	ApiProtocol           string          `json:"api_protocol"`
	ApiPath               string          `json:"api_path"`
	AuthMethod            string          `json:"auth_method"`
	AuthHeader            string          `json:"custom_auth_header"`
	ContextLength         int             `json:"context_length"`
	CustomParams          json.RawMessage `json:"custom_params,omitempty"`
}

// channelFromCreateReq converts a create request to a model.Channel.
func channelFromCreateReq(req *createChannelReq) *model.Channel {
	caps := req.SupportedCapabilities
	if caps == "" {
		caps = "chat"
	}
	return &model.Channel{
		Name:                  req.Name,
		SupplierID:            req.SupplierID,
		Type:                  req.Type,
		Endpoint:              req.Endpoint,
		APIKey:                req.APIKey,
		Models:                req.Models,
		Weight:                req.Weight,
		Priority:              req.Priority,
		Status:                req.Status,
		MaxConcurrency:        req.MaxConcurrency,
		QPM:                   req.QPM,
		PreferenceTag:         req.PreferenceTag,
		SupportedCapabilities: caps,
		ChannelType:           req.ChannelType,
		ApiProtocol:           req.ApiProtocol,
		ApiPath:               req.ApiPath,
		AuthMethod:            req.AuthMethod,
		AuthHeader:            req.AuthHeader,
		ContextLength:         req.ContextLength,
		CustomParams:          req.CustomParams,
	}
}

// Create handles POST /channels
func (h *ChannelHandler) Create(c *gin.Context) {
	var req createChannelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	ch := channelFromCreateReq(&req)
	if err := h.svc.Create(c.Request.Context(), ch); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, ch)
}

// List handles GET /channels
func (h *ChannelHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	filters := make(map[string]interface{})
	if v := c.Query("status"); v != "" {
		filters["status"] = v
	}
	if v := c.Query("supplier_id"); v != "" {
		filters["supplier_id"] = v
	}
	if v := c.Query("type"); v != "" {
		filters["type"] = v
	}
	if v := c.Query("name"); v != "" {
		filters["name"] = v
	}

	channels, total, err := h.svc.List(c.Request.Context(), page, pageSize, filters)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, channels, total, page, pageSize)
}

// CustomParamSchema returns the custom_params schema for the selected supplier.
func (h *ChannelHandler) CustomParamSchema(c *gin.Context) {
	code := c.Query("supplier_code")
	response.Success(c, channelsvc.GetCustomParamSchema(code))
}

// GetByID handles GET /channels/:id
func (h *ChannelHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	ch, err := h.svc.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	response.Success(c, ch)
}

// Update handles PUT /channels/:id
func (h *ChannelHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 审计：记录修改前的渠道快照
	if old, gerr := h.svc.GetByID(c.Request.Context(), uint(id)); gerr == nil && old != nil {
		auditmw.SetOldValue(c, old)
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	// 空值保护：APIKey 在 GET 响应中被 json:"-" 过滤，前端无法回显；
	// 若 payload 中 api_key 为空字符串，视为"保留已有 Key"，不覆盖数据库
	if v, ok := updates["api_key"]; ok {
		if s, isStr := v.(string); isStr && s == "" {
			delete(updates, "api_key")
		}
	}
	if v, ok := updates["custom_auth_header"]; ok {
		updates["auth_header"] = v
		delete(updates, "custom_auth_header")
	}

	if err := h.svc.Update(c.Request.Context(), uint(id), updates); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, nil)
}

// Delete handles DELETE /channels/:id
func (h *ChannelHandler) Delete(c *gin.Context) {
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

// setTagsReq is the request body for setting channel tags.
type setTagsReq struct {
	TagIDs []uint `json:"tag_ids" binding:"required"`
}

// SetTags handles PUT /channels/:id/tags
func (h *ChannelHandler) SetTags(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req setTagsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	if err := h.svc.SetTags(c.Request.Context(), uint(id), req.TagIDs); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, nil)
}

// TestChannel handles POST /channels/:id/test
// 测试渠道连通性（不更新状态）
func (h *ChannelHandler) TestChannel(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	result, err := h.svc.TestChannel(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, result)
}

// VerifyChannel handles POST /channels/:id/verify
// 验证渠道API Key，验证通过后更新渠道状态为active并更新关联模型状态为online
func (h *ChannelHandler) VerifyChannel(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	result, err := h.svc.VerifyChannel(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, result)
}
