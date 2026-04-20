// Package audit 审计日志中间件
//
// 功能：自动捕获管理员后台 + 用户敏感操作的写请求（POST/PUT/PATCH/DELETE），
// 异步写入 audit_logs 表，永不阻塞业务请求。
//
// 数据流：
//
//	请求 → 中间件读 body 备份 → c.Next() handler 执行 →
//	  响应 2xx → 拼装 AuditLog → AuditService.Enqueue → channel → consumer → batch DB write
//
// 字段来源：
//   - operator_id / user_id / tenant_id : auth 中间件已注入 c.Get("userId" / "tenantId")
//   - request_id                        : logger 中间件已注入 c.Get("X-Request-ID")
//   - ip                                : c.ClientIP()
//   - method / path                     : c.Request.Method / c.FullPath()
//   - menu / feature / action / resource: routeMap 查表
//   - new_value                         : 请求 body（敏感字段已脱敏）
//   - old_value                         : handler 通过 audit.SetOldValue(c, ...) 主动塞
//   - resource_id                       : c.Param("id") 解析 / handler audit.SetResourceID()
package audit

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/service/audit"
)

// 请求 body 最大记录长度（防止超长 body 撑爆数据库）
const maxBodyLogSize = 16 * 1024 // 16KB

// 敏感字段名（大小写不敏感匹配），值会被替换为 "***"
var sensitiveFields = []string{
	"password", "old_password", "new_password",
	"secret", "api_key", "apikey", "access_token", "refresh_token",
	"private_key", "client_secret",
}

// AuditLog 中间件构造函数
//
// 用法：
//
//	adminGroup.Use(audit.AuditLog(auditSvc))
//	authGroup.Use(audit.AuditLog(auditSvc))
func AuditLog(svc *audit.AuditService) gin.HandlerFunc {
	if svc == nil {
		// 防御：未注入 service 时退化为 noop，避免 nil panic
		return func(c *gin.Context) { c.Next() }
	}

	return func(c *gin.Context) {
		method := c.Request.Method
		// 1. GET / HEAD / OPTIONS 一律跳过
		if method == "GET" || method == "HEAD" || method == "OPTIONS" {
			c.Next()
			return
		}

		// 2. 路由表未命中则跳过（白名单策略）
		//    注意：Lookup 会查询写+读两张表，审计只记录写操作表命中的条目。
		fullPath := c.FullPath()
		meta, ok := Lookup(method, fullPath)
		if !ok || !IsAuditRelevant(method, fullPath) {
			c.Next()
			return
		}

		// 3. 备份请求 body（限长 + 写回供 handler 复用）
		bodyBytes := readAndRestoreBody(c)

		// 4. 执行 handler
		c.Next()

		// 5. 仅 2xx 响应才记录
		status := c.Writer.Status()
		if status < 200 || status >= 300 {
			return
		}

		// 6. handler 主动跳过
		if isSkipped(c) {
			return
		}

		// 7. 拼装日志
		log := buildLog(c, meta, bodyBytes, status)
		svc.Enqueue(log)
	}
}

// readAndRestoreBody 读取 body 字节用于审计，同时写回 c.Request.Body 供 handler 重新读取
func readAndRestoreBody(c *gin.Context) []byte {
	if c.Request.Body == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			logger.L.Warn("audit body read panic", zap.Any("recover", r))
		}
	}()

	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.L.Warn("audit body read failed", zap.Error(err))
		return nil
	}
	// 写回 body 供 handler ShouldBindJSON 等使用
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	return bodyBytes
}

// buildLog 拼装 AuditLog 实体
func buildLog(c *gin.Context, meta RouteMeta, bodyBytes []byte, status int) *model.AuditLog {
	log := &model.AuditLog{
		Action:     meta.Action,
		Resource:   meta.Resource,
		Menu:       meta.Menu,
		Feature:    meta.Feature,
		Method:     c.Request.Method,
		Path:       c.FullPath(),
		StatusCode: status,
		IP:         c.ClientIP(),
	}

	// operator_id / user_id / tenant_id（来自 auth 中间件）
	if v, ok := c.Get("userId"); ok {
		if uid, ok := v.(uint); ok {
			log.OperatorID = uid
			log.UserID = uid
		}
	}
	if v, ok := c.Get("tenantId"); ok {
		if tid, ok := v.(uint); ok {
			log.TenantID = tid
		}
	}

	// request_id
	if v, ok := c.Get("X-Request-ID"); ok {
		if rid, ok := v.(string); ok {
			log.RequestID = rid
		}
	}

	// resource_id：优先用 handler 显式 SetResourceID，否则解析 :id 路径参数
	if rid, ok := getResourceID(c); ok {
		log.ResourceID = rid
	} else if idStr := c.Param("id"); idStr != "" {
		if id, err := strconv.ParseUint(idStr, 10, 64); err == nil {
			log.ResourceID = uint(id)
		}
	}

	// new_value：请求 body（脱敏 + 限长）
	log.NewValue = sanitizeBody(bodyBytes)

	// old_value：handler 主动塞
	log.OldValue = getOldValue(c)

	// remark
	log.Remark = getRemark(c)

	return log
}

// sanitizeBody 对请求 body 做脱敏 + 限长处理
//   - JSON：递归找到 sensitiveFields 的 key，替换 value 为 "***"
//   - 非 JSON 或解析失败：直接截断
//   - 超过 maxBodyLogSize 仅保留前 N 字节并附 [truncated]
func sanitizeBody(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	// 尝试 JSON 解析
	var raw interface{}
	if err := json.Unmarshal(b, &raw); err == nil {
		redactSensitive(raw)
		out, _ := json.Marshal(raw)
		if len(out) > maxBodyLogSize {
			return string(out[:maxBodyLogSize]) + "...[truncated]"
		}
		return string(out)
	}
	// 非 JSON：直接截断
	if len(b) > maxBodyLogSize {
		return string(b[:maxBodyLogSize]) + "...[truncated]"
	}
	return string(b)
}

// redactSensitive 递归把敏感字段的 value 替换为 "***"
func redactSensitive(v interface{}) {
	switch x := v.(type) {
	case map[string]interface{}:
		for k, val := range x {
			if isSensitiveKey(k) {
				x[k] = "***"
				continue
			}
			redactSensitive(val)
		}
	case []interface{}:
		for _, item := range x {
			redactSensitive(item)
		}
	}
}

func isSensitiveKey(k string) bool {
	lk := strings.ToLower(k)
	for _, s := range sensitiveFields {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return false
}
