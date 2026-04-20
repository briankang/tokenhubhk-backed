// Package middleware - permission gate
//
// LoadSubjectPerms 在 Auth 之后运行，一次性把用户的 SubjectPerms
// （权限码集合 + 数据范围）注入到 gin.Context，供后续中间件和 handler 读取。
//
// PermissionGate 依托 audit.Lookup(method, fullPath) 查路由元数据，
// 未命中直接 403（白名单策略：未纳入权限目录的 /admin 路径不允许通过）；
// 命中则校验 SubjectPerms.Has(meta.Action)。
package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tokenhub-server/internal/middleware/audit"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/response"
	permissionsvc "tokenhub-server/internal/service/permission"
)

// Context key（字符串类型，与现有 Auth 一致）
const (
	ctxKeySubjectPerms     = "subjectPerms"
	ctxKeyEffectiveScope   = "effectiveDataScope"
)

// LoadSubjectPerms 在 Auth 后加载主体权限到 ctx
// 若 resolver 为 nil 或 userId 不存在，直接放行（/public/* 等场景）
func LoadSubjectPerms(resolver *permissionsvc.Resolver) gin.HandlerFunc {
	if resolver == nil {
		resolver = permissionsvc.Default
	}
	return func(c *gin.Context) {
		if resolver == nil {
			c.Next()
			return
		}
		uidAny, ok := c.Get("userId")
		if !ok {
			c.Next()
			return
		}
		uid, ok := uidAny.(uint)
		if !ok || uid == 0 {
			c.Next()
			return
		}
		perms, err := resolver.Resolve(c.Request.Context(), uid)
		if err != nil {
			if logger.L != nil {
				logger.L.Warn("LoadSubjectPerms: resolve failed",
					zap.Uint("user_id", uid),
					zap.Error(err),
				)
			}
			c.Next()
			return
		}
		c.Set(ctxKeySubjectPerms, perms)
		c.Set(ctxKeyEffectiveScope, perms.DataScope)
		c.Next()
	}
}

// GetSubjectPerms 从 ctx 读取 SubjectPerms；未加载则返回 nil
func GetSubjectPerms(c *gin.Context) *permissionsvc.SubjectPerms {
	v, ok := c.Get(ctxKeySubjectPerms)
	if !ok {
		return nil
	}
	p, _ := v.(*permissionsvc.SubjectPerms)
	return p
}

// PermissionGate 基于 audit.Lookup 查路由元数据，比对 SubjectPerms 强制授权
// 使用方式：
//
//	adminGroup.Use(Auth(), LoadSubjectPerms(resolver), PermissionGate())
func PermissionGate() gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method
		fullPath := c.FullPath()

		meta, ok := audit.Lookup(method, fullPath)
		if !ok {
			// 路由未在 audit.routeMap / readRouteMap 中声明 → 拒绝
			// 防止新加的 admin 端点漏网；修复方式：补齐 route_map.go
			if logger.L != nil {
				logger.L.Warn("PermissionGate: unmapped admin route rejected",
					zap.String("method", method),
					zap.String("path", fullPath),
					zap.String("client_ip", c.ClientIP()),
				)
			}
			response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
			c.Abort()
			return
		}

		perms := GetSubjectPerms(c)
		if perms == nil {
			response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
			c.Abort()
			return
		}

		if !perms.Has(meta.Action) {
			if logger.L != nil {
				logger.L.Info("PermissionGate: denied",
					zap.Uint("user_id", perms.UserID),
					zap.Strings("roles", perms.RoleCodes),
					zap.String("required_action", meta.Action),
					zap.String("path", fullPath),
				)
			}
			response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
			c.Abort()
			return
		}

		c.Next()
	}
}

// RequirePermission 手动声明型权限网关（特殊场景用）
// 适用于路由未在 audit.routeMap 中登记或需要额外检查的端点
func RequirePermission(codes ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		perms := GetSubjectPerms(c)
		if perms == nil || !perms.HasAny(codes...) {
			response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
			c.Abort()
			return
		}
		c.Next()
	}
}
