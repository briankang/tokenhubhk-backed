package agent

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
)

// AgentUserHandler 代理商租户范围内的用户管理接口处理器
type AgentUserHandler struct {
	db *gorm.DB
}

// NewAgentUserHandler 创建代理商用户管理Handler实例
func NewAgentUserHandler(db *gorm.DB) *AgentUserHandler {
	return &AgentUserHandler{db: db}
}

// Register 注册代理商用户管理路由
func (h *AgentUserHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/users", h.List)
	rg.POST("/users", h.Create)
	rg.PUT("/users/:id/status", h.UpdateStatus)
}

// createUserReq is the request body for creating a user under the agent's tenant.
type createUserReq struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
	Name     string `json:"name" binding:"required"`
	Language string `json:"language"`
}

// userStatusReq is the request body for toggling user status.
type userStatusReq struct {
	IsActive bool `json:"is_active"`
}

// List 分页获取代理商下的用户列表 GET /api/v1/agent/users
func (h *AgentUserHandler) List(c *gin.Context) {
	tenantID, _ := extractAgentContext(c)
	if tenantID == 0 {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
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

	var users []model.User
	var total int64

	query := h.db.WithContext(c.Request.Context()).Model(&model.User{}).
		Where("tenant_id = ? AND role = ?", tenantID, "USER")

	if err := query.Count(&total).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	if err := query.Offset(offset).Limit(pageSize).Order("id DESC").Find(&users).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, users, total, page, pageSize)
}

// Create 创建代理商下的用户 POST /api/v1/agent/users
func (h *AgentUserHandler) Create(c *gin.Context) {
	tenantID, _ := extractAgentContext(c)
	if tenantID == 0 {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	var req createUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}

	// Check for duplicate email
	var count int64
	if err := h.db.WithContext(c.Request.Context()).Model(&model.User{}).
		Where("email = ?", req.Email).Count(&count).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	if count > 0 {
		response.ErrorMsg(c, http.StatusConflict, errcode.ErrDuplicate.Code, "email already exists")
		return
	}

	hashedPwd, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "failed to hash password")
		return
	}

	lang := req.Language
	if lang == "" {
		lang = "en"
	}

	user := model.User{
		TenantID:     tenantID,
		Email:        req.Email,
		PasswordHash: string(hashedPwd),
		Name:         req.Name,
		Role:         "USER",
		IsActive:     true,
		Language:     lang,
	}

	if err := h.db.WithContext(c.Request.Context()).Create(&user).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"id": user.ID, "email": user.Email, "name": user.Name})
}

// UpdateStatus 更新用户状态 PUT /api/v1/agent/users/:id/status
func (h *AgentUserHandler) UpdateStatus(c *gin.Context) {
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

	var req userStatusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// Only allow updating users that belong to the agent's own tenant
	result := h.db.WithContext(c.Request.Context()).
		Model(&model.User{}).
		Where("id = ? AND tenant_id = ? AND role = ?", id, tenantID, "USER").
		Update("is_active", req.IsActive)

	if result.Error != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, result.Error.Error())
		return
	}
	if result.RowsAffected == 0 {
		response.Error(c, http.StatusNotFound, errcode.ErrUserNotFound)
		return
	}

	response.Success(c, nil)
}
