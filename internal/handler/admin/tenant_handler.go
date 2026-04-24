package admin

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
	"tokenhub-server/internal/pkg/response"
)

// AdminTenantHandler 租户管理接口处理器
type AdminTenantHandler struct {
	db *gorm.DB
}

// NewAdminTenantHandler 创建租户管理Handler实例
func NewAdminTenantHandler(db *gorm.DB) *AdminTenantHandler {
	return &AdminTenantHandler{db: db}
}

type createTenantReq struct {
	Name          string `json:"name" binding:"required"`
	ContactEmail  string `json:"contact_email" binding:"required,email"`
	ContactPhone  string `json:"contact_phone"`
	Domain        string `json:"domain"`
	AdminEmail    string `json:"admin_email" binding:"required,email"`
	AdminPassword string `json:"admin_password" binding:"required,min=6"`
	AdminName     string `json:"admin_name" binding:"required"`
}

// List 分页获取租户列表 GET /api/v1/admin/tenants
func (h *AdminTenantHandler) List(c *gin.Context) {
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

	query := h.db.WithContext(c.Request.Context()).Model(&model.Tenant{})
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

// Create 创建一级租户及管理员用户 POST /api/v1/admin/tenants
func (h *AdminTenantHandler) Create(c *gin.Context) {
	var req createTenantReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}

	hashedPwd, err := bcrypt.GenerateFromPassword([]byte(req.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "failed to hash password")
		return
	}

	var tenant model.Tenant
	err = h.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		tenant = model.Tenant{
			Name:         req.Name,
			Level:        1,
			IsActive:     true,
			ContactEmail: req.ContactEmail,
			ContactPhone: req.ContactPhone,
			Domain:       req.Domain,
		}
		if tenant.Domain == "" {
			tenant.Domain = fmt.Sprintf("t-%d", time.Now().UnixNano())
		}
		if err := tx.Create(&tenant).Error; err != nil {
			return fmt.Errorf("failed to create tenant: %w", err)
		}

		// v4.0: 代理角色(AGENT_L1)已移除；此处创建的用户默认为普通 USER，
		// 权限由 user_roles 表控制（不再通过 users.role 字段）
		adminUser := model.User{
			TenantID:     tenant.ID,
			Email:        req.AdminEmail,
			PasswordHash: string(hashedPwd),
			Name:         req.AdminName,
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

	response.Success(c, tenant)
}
