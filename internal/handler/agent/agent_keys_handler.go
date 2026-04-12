package agent

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/permission"
	"tokenhub-server/internal/service/report"
	"gorm.io/gorm"
)

// KeysHandler 代理商API Key管理接口处理器
type KeysHandler struct {
	db        *gorm.DB
	perm      *permission.PermissionService
	reportSvc *report.ReportService
}

// NewKeysHandler 创建代理商Key管理Handler实例
func NewKeysHandler(db *gorm.DB, perm *permission.PermissionService, reportSvc *report.ReportService) *KeysHandler {
	if db == nil {
		panic("agent keys handler: db is nil")
	}
	return &KeysHandler{db: db, perm: perm, reportSvc: reportSvc}
}

// Register 注册代理商Key管理路由到路由组
func (h *KeysHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/sub-tenants", h.ListSubTenants)
	rg.GET("/sub-tenants/:id/keys", h.ListSubTenantKeys)
	rg.GET("/keys/:id/usage", h.KeyUsage)
}

// ListSubTenants 获取子租户列表 GET /api/v1/agent/sub-tenants
func (h *KeysHandler) ListSubTenants(c *gin.Context) {
	tenantID, exists := c.Get("tenantId")
	if !exists {
		response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
		return
	}
	tid, ok := tenantID.(uint)
	if !ok || tid == 0 {
		response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	// Get direct children of the current tenant
	var tenants []model.Tenant
	var total int64

	query := h.db.Where("parent_id = ?", tid)
	query.Model(&model.Tenant{}).Count(&total)

	offset := (page - 1) * pageSize
	if err := query.Offset(offset).Limit(pageSize).Find(&tenants).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, tenants, total, page, pageSize)
}

// ListSubTenantKeys 获取子租户的Key列表 GET /api/v1/agent/sub-tenants/:id/keys
func (h *KeysHandler) ListSubTenantKeys(c *gin.Context) {
	subTenantID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || subTenantID == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// Verify access
	if h.perm != nil {
		ok, err := h.perm.CanAccessTenant(c.Request.Context(), uint(subTenantID))
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
			return
		}
		if !ok {
			response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
			return
		}
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var keys []model.ApiKey
	var total int64

	query := h.db.Where("tenant_id = ?", subTenantID)
	query.Model(&model.ApiKey{}).Count(&total)

	offset := (page - 1) * pageSize
	if err := query.Preload("User", func(db *gorm.DB) *gorm.DB {
		return db.Select("id, name, email")
	}).Offset(offset).Limit(pageSize).Find(&keys).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, keys, total, page, pageSize)
}

// KeyUsage 获取Key用量统计 GET /api/v1/agent/keys/:id/usage
func (h *KeysHandler) KeyUsage(c *gin.Context) {
	keyID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || keyID == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	items, total, err := h.reportSvc.GetKeyUsageDetail(c.Request.Context(), uint(keyID), page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.PageResult(c, items, total, page, pageSize)
}
