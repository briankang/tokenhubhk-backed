package openapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/response"
	svc "tokenhub-server/internal/service/openapi"
)

// AccountHandler 处理账户信息/Key 列表/Key 用量相关的 Open API 请求。
type AccountHandler struct {
	service *svc.OpenAPIService
}

// NewAccountHandler 创建账户 Handler 实例。
func NewAccountHandler(service *svc.OpenAPIService) *AccountHandler {
	return &AccountHandler{service: service}
}

// Register 注册账户相关路由。
func (h *AccountHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/account/info", h.Info)
	rg.GET("/keys", h.ListKeys)
	rg.GET("/keys/:id/usage", h.KeyUsage)
}

// Info 获取账户基本信息。
// GET /api/v1/open/account/info
func (h *AccountHandler) Info(c *gin.Context) {
	userID := c.GetUint("userId")

	info, err := h.service.GetAccountInfo(c.Request.Context(), userID)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, info)
}

// ListKeys 获取 API Key 列表。
// GET /api/v1/open/keys
func (h *AccountHandler) ListKeys(c *gin.Context) {
	userID := c.GetUint("userId")

	keys, err := h.service.GetAPIKeys(c.Request.Context(), userID)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, keys)
}

// KeyUsage 获取单个 Key 的用量信息。
// GET /api/v1/open/keys/:id/usage
func (h *AccountHandler) KeyUsage(c *gin.Context) {
	userID := c.GetUint("userId")
	keyID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid key id")
		return
	}

	info, err := h.service.GetKeyUsage(c.Request.Context(), userID, uint(keyID))
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, 20002, err.Error())
		return
	}
	response.Success(c, info)
}
