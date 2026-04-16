package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/configaudit"
)

// ConfigAuditHandler 配置变更审计日志查询
// v3.1 新增:统一查询所有 admin 配置变更历史
type ConfigAuditHandler struct {
	svc *configaudit.Service
}

// NewConfigAuditHandler 创建 handler 实例
func NewConfigAuditHandler(svc *configaudit.Service) *ConfigAuditHandler {
	return &ConfigAuditHandler{svc: svc}
}

// Register 注册路由
func (h *ConfigAuditHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/config-audit", h.List)
}

// List 分页查询审计日志
// GET /api/v1/admin/config-audit?table=xxx&id=xxx&action=UPDATE&page=1&page_size=50
func (h *ConfigAuditHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	table := c.Query("table")
	action := c.Query("action")
	var configID uint
	if v := c.Query("id"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			configID = uint(n)
		}
	}

	list, total, err := h.svc.List(c.Request.Context(), table, configID, action, page, pageSize)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}
