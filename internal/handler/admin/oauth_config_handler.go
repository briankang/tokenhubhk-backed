package admin

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	authsvc "tokenhub-server/internal/service/auth"
)

// OAuthConfigHandler 管理 Google/GitHub 登录配置。
type OAuthConfigHandler struct {
	svc *authsvc.OAuthService
}

// NewOAuthConfigHandler 创建 OAuth 配置 handler。
func NewOAuthConfigHandler(svc *authsvc.OAuthService) *OAuthConfigHandler {
	return &OAuthConfigHandler{svc: svc}
}

// Register 注册 OAuth 配置管理路由。
func (h *OAuthConfigHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/oauth")
	g.GET("/providers", h.ListProviders)
	g.PUT("/providers/:provider", h.UpsertProvider)
}

// ListProviders GET /api/v1/admin/oauth/providers
func (h *OAuthConfigHandler) ListProviders(c *gin.Context) {
	list, err := h.svc.ListProviderConfigs(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, list)
}

// UpsertProvider PUT /api/v1/admin/oauth/providers/:provider
func (h *OAuthConfigHandler) UpsertProvider(c *gin.Context) {
	var req authsvc.OAuthProviderUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	// 派生当前请求 origin（scheme://host），供 service 层在 redirect_url 缺失时兜底派生 callback URL。
	// 优先使用反向代理头部（ALB / nginx 注入的 X-Forwarded-*），否则回退到 Request.TLS + Request.Host。
	scheme := "https"
	if c.Request.TLS == nil && !strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "http"
	}
	host := c.GetHeader("X-Forwarded-Host")
	if host == "" {
		host = c.Request.Host
	}
	origin := ""
	if host != "" {
		origin = scheme + "://" + host
	}
	dto, err := h.svc.UpsertProviderConfig(c.Request.Context(), c.Param("provider"), req, origin)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	response.Success(c, dto)
}
