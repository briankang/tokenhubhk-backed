package audit

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
)

// Gin context key 名称（前缀 audit. 避免与业务 key 冲突）
const (
	ctxKeyOldValue   = "audit.old_value"
	ctxKeyResourceID = "audit.resource_id"
	ctxKeyRemark     = "audit.remark"
	ctxKeySkip       = "audit.skip"
)

// SetOldValue 由 handler 在「读到旧记录之后、Update 之前」调用，
// 中间件会把该值序列化为 JSON 写入 audit_logs.old_value 字段。
func SetOldValue(c *gin.Context, v interface{}) {
	if v == nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.Set(ctxKeyOldValue, string(b))
}

// SetResourceID 当资源 ID 不在 URL :id 路径参数中时，由 handler 显式塞入
func SetResourceID(c *gin.Context, id uint) {
	c.Set(ctxKeyResourceID, id)
}

// SetRemark 补充备注信息，会写入 audit_logs.remark
func SetRemark(c *gin.Context, s string) {
	c.Set(ctxKeyRemark, s)
}

// Skip 标记本次请求跳过审计（即使路由表命中）
// 用于敏感场景：如登录失败时不想记录密码 body
func Skip(c *gin.Context) {
	c.Set(ctxKeySkip, true)
}

// 内部 helper：从 context 读 old_value
func getOldValue(c *gin.Context) string {
	if v, ok := c.Get(ctxKeyOldValue); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getResourceID(c *gin.Context) (uint, bool) {
	if v, ok := c.Get(ctxKeyResourceID); ok {
		if id, ok := v.(uint); ok {
			return id, true
		}
	}
	return 0, false
}

func getRemark(c *gin.Context) string {
	if v, ok := c.Get(ctxKeyRemark); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func isSkipped(c *gin.Context) bool {
	if v, ok := c.Get(ctxKeySkip); ok {
		if b, ok := v.(bool); ok && b {
			return true
		}
	}
	return false
}
