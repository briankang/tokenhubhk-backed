package setup

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/response"
	setupsvc "tokenhub-server/internal/service/setup"
)

// SetupHandler 安装向导 API 处理器
type SetupHandler struct {
	svc *setupsvc.SetupService
}

// NewSetupHandler 创建安装向导处理器实例
func NewSetupHandler(svc *setupsvc.SetupService) *SetupHandler {
	return &SetupHandler{svc: svc}
}

// Register 注册安装向导路由到指定路由组
func (h *SetupHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/status", h.Status)
	rg.POST("/test-db", h.TestDB)
	rg.POST("/test-redis", h.TestRedis)
	rg.POST("/migrate", h.Migrate)
	rg.POST("/init-cache", h.InitCache)
	rg.POST("/create-admin", h.CreateAdmin)
	rg.POST("/import-seed", h.ImportSeed)
	rg.POST("/save-config", h.SaveConfig)
	rg.POST("/finalize", h.Finalize)
}

// Status 获取系统初始化状态
// GET /api/v1/setup/status
func (h *SetupHandler) Status(c *gin.Context) {
	initialized, err := h.svc.CheckInitialized()
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 500, "检查初始化状态失败: "+err.Error())
		return
	}
	response.Success(c, gin.H{
		"initialized": initialized,
	})
}

// TestDB 测试数据库连接
// POST /api/v1/setup/test-db
func (h *SetupHandler) TestDB(c *gin.Context) {
	// 检查是否已初始化，已初始化则拒绝访问
	if h.isInitialized(c) {
		return
	}
	if err := h.svc.TestDatabaseConnection(); err != nil {
		response.ErrorMsg(c, http.StatusServiceUnavailable, 503, "数据库连接失败: "+err.Error())
		return
	}
	response.Success(c, gin.H{"status": "connected"})
}

// TestRedis 测试 Redis 连接
// POST /api/v1/setup/test-redis
func (h *SetupHandler) TestRedis(c *gin.Context) {
	if h.isInitialized(c) {
		return
	}
	if err := h.svc.TestRedisConnection(); err != nil {
		response.ErrorMsg(c, http.StatusServiceUnavailable, 503, "Redis 连接失败: "+err.Error())
		return
	}
	response.Success(c, gin.H{"status": "connected"})
}

// Migrate 执行数据库迁移
// POST /api/v1/setup/migrate
func (h *SetupHandler) Migrate(c *gin.Context) {
	if h.isInitialized(c) {
		return
	}
	if err := h.svc.RunMigrations(); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 500, "数据库迁移失败: "+err.Error())
		return
	}
	response.Success(c, gin.H{"status": "migrated"})
}

// InitCache 初始化 Redis 缓存
// POST /api/v1/setup/init-cache
func (h *SetupHandler) InitCache(c *gin.Context) {
	if h.isInitialized(c) {
		return
	}
	if err := h.svc.InitializeCache(); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 500, "缓存初始化失败: "+err.Error())
		return
	}
	response.Success(c, gin.H{"status": "initialized"})
}

// createAdminReq 创建管理员请求体
type createAdminReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required,min=6"`
	Email    string `json:"email" binding:"required,email"`
}

// CreateAdmin 创建管理员账号
// POST /api/v1/setup/create-admin
func (h *SetupHandler) CreateAdmin(c *gin.Context) {
	if h.isInitialized(c) {
		return
	}
	var req createAdminReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 400, "参数错误: "+err.Error())
		return
	}
	if err := h.svc.CreateAdminAccount(req.Username, req.Password, req.Email); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 500, "创建管理员失败: "+err.Error())
		return
	}
	response.Success(c, gin.H{"status": "created"})
}

// ImportSeed 导入种子数据
// POST /api/v1/setup/import-seed
func (h *SetupHandler) ImportSeed(c *gin.Context) {
	if h.isInitialized(c) {
		return
	}
	if err := h.svc.ImportSeedData(); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 500, "种子数据导入失败: "+err.Error())
		return
	}
	response.Success(c, gin.H{"status": "imported"})
}

// saveConfigReq 保存配置请求体
type saveConfigReq struct {
	SiteName string `json:"site_name" binding:"required"`
}

// SaveConfig 保存基础配置
// POST /api/v1/setup/save-config
func (h *SetupHandler) SaveConfig(c *gin.Context) {
	if h.isInitialized(c) {
		return
	}
	var req saveConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 400, "参数错误: "+err.Error())
		return
	}
	if err := h.svc.SaveBasicConfig(req.SiteName); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 500, "保存配置失败: "+err.Error())
		return
	}
	response.Success(c, gin.H{"status": "saved"})
}

// Finalize 完成安装，标记系统已初始化
// POST /api/v1/setup/finalize
func (h *SetupHandler) Finalize(c *gin.Context) {
	if h.isInitialized(c) {
		return
	}
	if err := h.svc.MarkInitialized(); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 500, "完成安装失败: "+err.Error())
		return
	}
	response.Success(c, gin.H{"status": "completed"})
}

// isInitialized 检查系统是否已初始化，若已初始化则返回 403 并中止请求
func (h *SetupHandler) isInitialized(c *gin.Context) bool {
	initialized, _ := h.svc.CheckInitialized()
	if initialized {
		response.ErrorMsg(c, http.StatusForbidden, 403, "系统已完成初始化，无法重复操作")
		return true
	}
	return false
}
