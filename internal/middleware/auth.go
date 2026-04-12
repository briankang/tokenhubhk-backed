package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
)

// Claims JWT Token 的声明体，包含用户 ID、租户 ID 和角色
type Claims struct {
	UserID   uint   `json:"user_id"`
	TenantID uint   `json:"tenant_id"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// Auth 返回 JWT 认证中间件
// 从 Authorization 头提取 Bearer Token，验证签名并将用户信息注入上下文
func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		if token == "" {
			response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
			c.Abort()
			return
		}

		claims, err := parseToken(token)
		if err != nil {
			response.Error(c, http.StatusUnauthorized, errcode.ErrTokenInvalid)
			c.Abort()
			return
		}

		// 将用户信息写入 Gin 上下文
		c.Set("userId", claims.UserID)
		c.Set("tenantId", claims.TenantID)
		c.Set("role", claims.Role)

		// 同时写入 request context，便于 Service 层通过 ctx.Value() 读取
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, "userId", claims.UserID)
		ctx = context.WithValue(ctx, "tenantId", claims.TenantID)
		ctx = context.WithValue(ctx, "role", claims.Role)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

// RequireRole 返回角色校验中间件，检查用户是否拥有指定角色之一
// 参数 roles: 允许的角色列表 (如 "ADMIN", "AGENT_L1")
func RequireRole(roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get("role")
		if !exists {
			response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
			c.Abort()
			return
		}
		roleStr, ok := role.(string)
		if !ok {
			response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
			c.Abort()
			return
		}
		for _, r := range roles {
			if r == roleStr {
				c.Next()
				return
			}
		}
		response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
		c.Abort()
	}
}

// extractToken 从请求头提取 Bearer Token
func extractToken(c *gin.Context) string {
	auth := c.GetHeader("Authorization")
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// parseToken 解析并验证 JWT Token，返回 Claims
func parseToken(tokenStr string) (*Claims, error) {
	secret := config.Global.JWT.Secret
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, jwt.ErrSignatureInvalid
	}
	return claims, nil
}
