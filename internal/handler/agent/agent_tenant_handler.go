package agent

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
)

// AgentTenantHandler 子代理管理接口处理器
type AgentTenantHandler struct {
	db *gorm.DB
}

// NewAgentTenantHandler 创建子代理管理Handler实例
func NewAgentTenantHandler(db *gorm.DB) *AgentTenantHandler {
	return &AgentTenantHandler{db: db}
}

// Register 注册子代理管理路由
func (h *AgentTenantHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/sub-agents", h.List)
	rg.POST("/sub-agents", h.Create)
	rg.GET("/sub-agents/:id", h.GetByID)
	rg.PUT("/sub-agents/:id", h.Update)
	rg.PUT("/sub-agents/:id/status", h.UpdateStatus)
}

// createSubAgentReq is the request body for creating a sub-agent.
type createSubAgentReq struct {
	Name         string `json:"name" binding:"required"`
	ContactEmail string `json:"contact_email" binding:"required,email"`
	ContactPhone string `json:"contact_phone"`
	Domain       string `json:"domain"`
	// Admin user for the new tenant
	AdminEmail    string `json:"admin_email" binding:"required,email"`
	AdminPassword string `json:"admin_password" binding:"required,min=6"`
	AdminName     string `json:"admin_name" binding:"required"`
}

// updateSubAgentReq is the request body for updating a sub-agent.
type updateSubAgentReq struct {
	Name         string `json:"name"`
	ContactEmail string `json:"contact_email"`
	ContactPhone string `json:"contact_phone"`
}

// updateStatusReq is the request body for toggling tenant active status.
type updateStatusReq struct {
	IsActive bool `json:"is_active"`
}

// List 分页获取子代理列表 GET /api/v1/agent/sub-agents
func (h *AgentTenantHandler) List(c *gin.Context) {
	tenantID, role := extractAgentContext(c)
	if tenantID == 0 {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	// L3 agents cannot have sub-agents
	if role == "AGENT_L3" {
		response.PageResult(c, []model.Tenant{}, 0, 1, 20)
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
	offset := (page - 1) * pageSize

	var tenants []model.Tenant
	var total int64

	query := h.db.WithContext(c.Request.Context()).Model(&model.Tenant{}).
		Where("parent_id = ?", tenantID)

	if err := query.Count(&total).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	if err := query.Offset(offset).Limit(pageSize).Order("id DESC").Find(&tenants).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, tenants, total, page, pageSize)
}

// Create 创建子代理 POST /api/v1/agent/sub-agents
func (h *AgentTenantHandler) Create(c *gin.Context) {
	tenantID, role := extractAgentContext(c)
	if tenantID == 0 {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	// Determine current agent level and check if sub-creation is allowed
	currentLevel, err := agentRoleToLevel(role)
	if err != nil || currentLevel >= 3 {
		response.ErrorMsg(c, http.StatusForbidden, errcode.ErrPermissionDenied.Code,
			"L3 agents cannot create sub-agents")
		return
	}

	var req createSubAgentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}

	// Hash admin password
	hashedPwd, err := bcrypt.GenerateFromPassword([]byte(req.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "failed to hash password")
		return
	}

	newLevel := currentLevel + 1
	newRoleStr := fmt.Sprintf("AGENT_L%d", newLevel)

	// Use transaction to create tenant + admin user atomically
	err = h.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		domain := req.Domain
		if domain == "" {
			domain = fmt.Sprintf("t-%d", time.Now().UnixNano())
		}
		tenant := model.Tenant{
			Name:         req.Name,
			ParentID:     &tenantID,
			Level:        newLevel,
			IsActive:     true,
			ContactEmail: req.ContactEmail,
			ContactPhone: req.ContactPhone,
			Domain:       domain,
		}
		if err := tx.Create(&tenant).Error; err != nil {
			return fmt.Errorf("failed to create tenant: %w", err)
		}

		adminUser := model.User{
			TenantID:     tenant.ID,
			Email:        req.AdminEmail,
			PasswordHash: string(hashedPwd),
			Name:         req.AdminName,
			Role:         newRoleStr,
			IsActive:     true,
		}
		if err := tx.Create(&adminUser).Error; err != nil {
			return fmt.Errorf("failed to create admin user: %w", err)
		}

		return nil
	})

	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// Invalidate the parent tenant's subtree cache so new child is visible
	if redis.Client != nil {
		cacheKey := fmt.Sprintf("tenant_subtree:%d", tenantID)
		_ = redis.Client.Del(c.Request.Context(), cacheKey).Err()
	}

	response.Success(c, nil)
}

// GetByID 根据ID获取子代理详情 GET /api/v1/agent/sub-agents/:id
func (h *AgentTenantHandler) GetByID(c *gin.Context) {
	tenantID, _ := extractAgentContext(c)
	if tenantID == 0 {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var tenant model.Tenant
	if err := h.db.WithContext(c.Request.Context()).
		Where("id = ? AND parent_id = ?", id, tenantID).
		First(&tenant).Error; err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrTenantNotFound)
		return
	}

	response.Success(c, tenant)
}

// Update 更新子代理信息 PUT /api/v1/agent/sub-agents/:id
func (h *AgentTenantHandler) Update(c *gin.Context) {
	tenantID, _ := extractAgentContext(c)
	if tenantID == 0 {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req updateSubAgentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// Ensure the sub-agent belongs to the current agent
	result := h.db.WithContext(c.Request.Context()).
		Model(&model.Tenant{}).
		Where("id = ? AND parent_id = ?", id, tenantID).
		Updates(map[string]interface{}{
			"name":          req.Name,
			"contact_email": req.ContactEmail,
			"contact_phone": req.ContactPhone,
		})

	if result.Error != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, result.Error.Error())
		return
	}
	if result.RowsAffected == 0 {
		response.Error(c, http.StatusNotFound, errcode.ErrTenantNotFound)
		return
	}

	response.Success(c, nil)
}

// UpdateStatus 更新子代理状态 PUT /api/v1/agent/sub-agents/:id/status
func (h *AgentTenantHandler) UpdateStatus(c *gin.Context) {
	tenantID, _ := extractAgentContext(c)
	if tenantID == 0 {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req updateStatusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	result := h.db.WithContext(c.Request.Context()).
		Model(&model.Tenant{}).
		Where("id = ? AND parent_id = ?", id, tenantID).
		Update("is_active", req.IsActive)

	if result.Error != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, result.Error.Error())
		return
	}
	if result.RowsAffected == 0 {
		response.Error(c, http.StatusNotFound, errcode.ErrTenantNotFound)
		return
	}

	response.Success(c, nil)
}

// --- helpers ---

// extractAgentContext retrieves tenantId and role from the gin context.
func extractAgentContext(c *gin.Context) (uint, string) {
	tidVal, _ := c.Get("tenantId")
	tid, _ := tidVal.(uint)
	roleVal, _ := c.Get("role")
	role, _ := roleVal.(string)
	return tid, role
}

// agentRoleToLevel converts an agent role string to its numeric level.
func agentRoleToLevel(role string) (int, error) {
	switch role {
	case "AGENT_L1":
		return 1, nil
	case "AGENT_L2":
		return 2, nil
	case "AGENT_L3":
		return 3, nil
	default:
		return 0, fmt.Errorf("unknown agent role: %s", role)
	}
}
