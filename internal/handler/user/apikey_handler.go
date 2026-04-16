package user

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	apikeysvc "tokenhub-server/internal/service/apikey"
)

// ApiKeyHandler 用户API Key管理接口处理器
type ApiKeyHandler struct {
	svc *apikeysvc.ApiKeyService
}

// NewApiKeyHandler 创建API Key管理Handler实例
func NewApiKeyHandler(svc *apikeysvc.ApiKeyService) *ApiKeyHandler {
	if svc == nil {
		panic("apikey handler: service is nil")
	}
	return &ApiKeyHandler{svc: svc}
}

// List 获取用户API Key列表 GET /api/v1/user/api-keys
func (h *ApiKeyHandler) List(c *gin.Context) {
	userID, ok := c.Get("userId")
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	keys, total, err := h.svc.List(c.Request.Context(), uid, page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, keys, total, page, pageSize)
}

// Generate 生成新的API Key POST /api/v1/user/api-keys
func (h *ApiKeyHandler) Generate(c *gin.Context) {
	userID, ok := c.Get("userId")
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	tenantID, ok := c.Get("tenantId")
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	tid, ok := tenantID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	// 支持新字段的请求体
	var req struct {
		Name            string `json:"name" binding:"required"`
		CustomChannelID *uint  `json:"custom_channel_id"`   // 关联的自定义渠道ID
		CreditLimit     int64  `json:"credit_limit"`        // 积分限额，0=无限
		AllowedModels   string `json:"allowed_models"`      // JSON数组字符串，限制可调用模型
		RateLimitRPM    int    `json:"rate_limit_rpm"`      // 每分钟请求数，0=系统默认
		RateLimitTPM    int    `json:"rate_limit_tpm"`      // 每分钟Token数，0=系统默认
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	opts := apikeysvc.CreateKeyOptions{
		Name:            req.Name,
		CustomChannelID: req.CustomChannelID,
		CreditLimit:     req.CreditLimit,
		AllowedModels:   req.AllowedModels,
		RateLimitRPM:    req.RateLimitRPM,
		RateLimitTPM:    req.RateLimitTPM,
	}

	result, err := h.svc.GenerateWithOptions(c.Request.Context(), uid, tid, opts)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, result)
}

// Revoke 删除（软删除）指定API Key DELETE /api/v1/user/api-keys/:id
// 该接口现等价于软删除（设置 deleted_at），用户与管理员均不可见
func (h *ApiKeyHandler) Revoke(c *gin.Context) {
	uid, ok := getApiKeyUserID(c)
	if !ok {
		return
	}
	id, ok := parseKeyID(c)
	if !ok {
		return
	}
	if err := h.svc.SoftDelete(c.Request.Context(), id, uid); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "api key deleted"})
}

// Disable 禁用指定 API Key PUT /api/v1/user/api-keys/:id/disable
func (h *ApiKeyHandler) Disable(c *gin.Context) {
	uid, ok := getApiKeyUserID(c)
	if !ok {
		return
	}
	id, ok := parseKeyID(c)
	if !ok {
		return
	}
	if err := h.svc.Disable(c.Request.Context(), id, uid); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "api key disabled"})
}

// Enable 启用指定 API Key PUT /api/v1/user/api-keys/:id/enable
func (h *ApiKeyHandler) Enable(c *gin.Context) {
	uid, ok := getApiKeyUserID(c)
	if !ok {
		return
	}
	id, ok := parseKeyID(c)
	if !ok {
		return
	}
	if err := h.svc.Enable(c.Request.Context(), id, uid); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "api key enabled"})
}

// getApiKeyUserID 从上下文提取已登录用户ID，失败时已写入 401 响应
func getApiKeyUserID(c *gin.Context) (uint, bool) {
	userID, exists := c.Get("userId")
	if !exists {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return 0, false
	}
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return 0, false
	}
	return uid, true
}

// parseKeyID 解析路由 :id 参数；失败时已写入 400 响应
func parseKeyID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return 0, false
	}
	return uint(id), true
}

// Reveal 解密并返回完整API Key明文 GET /api/v1/user/api-keys/:id/reveal
func (h *ApiKeyHandler) Reveal(c *gin.Context) {
	userID, ok := c.Get("userId")
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	plainKey, err := h.svc.RevealKey(c.Request.Context(), uint(id), uid)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"key": plainKey})
}

// Update 更新指定API Key配置 PUT /api/v1/user/api-keys/:id
func (h *ApiKeyHandler) Update(c *gin.Context) {
	userID, ok := c.Get("userId")
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 请求体字段为指针类型，支持部分更新
	var req struct {
		Name            *string `json:"name"`
		CustomChannelID *uint   `json:"custom_channel_id"`
		CreditLimit     *int64  `json:"credit_limit"`
		AllowedModels   *string `json:"allowed_models"`
		RateLimitRPM    *int    `json:"rate_limit_rpm"`
		RateLimitTPM    *int    `json:"rate_limit_tpm"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	opts := apikeysvc.UpdateKeyOptions{
		Name:            req.Name,
		CustomChannelID: req.CustomChannelID,
		CreditLimit:     req.CreditLimit,
		AllowedModels:   req.AllowedModels,
		RateLimitRPM:    req.RateLimitRPM,
		RateLimitTPM:    req.RateLimitTPM,
	}

	if err := h.svc.Update(c.Request.Context(), uint(id), uid, opts); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "api key updated"})
}
