// Package ctxutil 提供从 gin.Context 安全提取用户身份信息的 helper。
//
// 设计目标：消除 handler 中大量重复的 c.Get + 不安全类型断言模式，
// 把所有 `userID.(uint)` 这类可能 panic 的写法收敛到一处。
package ctxutil

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
)

// UserID 从 ctx 安全读取 Auth 中间件注入的 userId。
// 未登录 / 类型异常 / 零值均返回 (0, false)，调用方应据此返回 401。
func UserID(c *gin.Context) (uint, bool) {
	v, exists := c.Get("userId")
	if !exists || v == nil {
		return 0, false
	}
	id, ok := v.(uint)
	if !ok || id == 0 {
		return 0, false
	}
	return id, true
}

// TenantID 从 ctx 安全读取 tenantId。
// 未设置 / 类型不符时返回 0（平台租户 / 默认租户），不视作错误。
func TenantID(c *gin.Context) uint {
	v, exists := c.Get("tenantId")
	if !exists || v == nil {
		return 0
	}
	id, _ := v.(uint)
	return id
}

// MustUserID 是 UserID 的便捷封装：提取失败时自动写 401 响应并 Abort。
// handler 只需：
//
//	uid, ok := ctxutil.MustUserID(c)
//	if !ok { return }
func MustUserID(c *gin.Context) (uint, bool) {
	id, ok := UserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		c.Abort()
		return 0, false
	}
	return id, true
}

// UserID64 等同于 UserID，但返回 uint64（服务层常用该类型）。
func UserID64(c *gin.Context) (uint64, bool) {
	id, ok := UserID(c)
	if !ok {
		return 0, false
	}
	return uint64(id), true
}
