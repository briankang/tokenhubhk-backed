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

// Claims JWT Token 的声明体
// v4.0: 移除 Role 字段 —— 权限来源改为 user_roles 表，由 LoadSubjectPerms 动态解析。
// 兼容：旧 token 含 Role 字段仍可解析（json 反序列化忽略未知字段）。
type Claims struct {
	UserID   uint `json:"user_id"`
	TenantID uint `json:"tenant_id"`
	jwt.RegisteredClaims
}

// Auth JWT 认证中间件：解析 Bearer Token 并注入 userId/tenantId 到 ctx
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

		c.Set("userId", claims.UserID)
		c.Set("tenantId", claims.TenantID)

		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, "userId", claims.UserID)
		ctx = context.WithValue(ctx, "tenantId", claims.TenantID)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
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

// parseToken 解析并验证 JWT Token
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
