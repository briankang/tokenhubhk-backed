package public

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
)

// AgentApplicationHandler 代理申请处理器
// 提供公开申请接口和管理员审核接口
type AgentApplicationHandler struct {
	db *gorm.DB
}

// NewAgentApplicationHandler 创建代理申请Handler实例
func NewAgentApplicationHandler(db *gorm.DB) *AgentApplicationHandler {
	return &AgentApplicationHandler{db: db}
}

// RegisterPublic 注册公开路由（无需认证）
// POST /api/v1/public/agent-applications — 提交申请
func (h *AgentApplicationHandler) RegisterPublic(rg *gin.RouterGroup) {
	rg.POST("/agent-applications", h.SubmitApplication)
}

// RegisterAdmin 注册管理员路由（需要ADMIN角色）
// PUT  /api/v1/admin/agent-applications/:id/review — 审核
// 注意: GET /agent-applications 已在 level_admin_handler 中注册，此处不再重复注册
func (h *AgentApplicationHandler) RegisterAdmin(rg *gin.RouterGroup) {
	rg.PUT("/agent-applications/:id/review", h.ReviewApplication)
}

// SubmitApplicationRequest 提交申请请求体
type SubmitApplicationRequest struct {
	Name       string `json:"name" binding:"required"`        // 姓名/公司名（必填）
	Email      string `json:"email" binding:"required,email"` // 邮箱（必填，格式校验）
	Phone      string `json:"phone"`                          // 电话
	WechatID   string `json:"wechat_id" binding:"required"`   // 微信号（必填）
	Occupation string `json:"occupation" binding:"required"`  // 职业/身份（必填）
	UseCase    string `json:"use_case" binding:"required"`    // 使用场景描述（必填）
	Source     string `json:"source"`                         // 从哪了解到平台（多选用逗号分隔）
	Remark     string `json:"remark"`                         // 备注
}

// SubmitApplication 提交代理申请 POST /api/v1/public/agent-applications
// 公开接口，无需登录认证
func (h *AgentApplicationHandler) SubmitApplication(c *gin.Context) {
	var req SubmitApplicationRequest
	// 绑定并验证请求参数
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "请填写所有必填字段")
		return
	}

	// 检查邮箱是否已有待审核或已通过的申请
	var existingCount int64
	h.db.Model(&model.AgentApplication{}).
		Where("email = ? AND status IN ?", req.Email, []string{model.ApplicationStatusPending, model.ApplicationStatusApproved}).
		Count(&existingCount)
	if existingCount > 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrDuplicate.Code, "该邮箱已提交过申请，请等待审核结果")
		return
	}

	// 创建申请记录
	application := &model.AgentApplication{
		Name:       req.Name,
		Email:      req.Email,
		Phone:      req.Phone,
		WechatID:   req.WechatID,
		Occupation: req.Occupation,
		UseCase:    req.UseCase,
		Source:     req.Source,
		Remark:     req.Remark,
		Status:     model.ApplicationStatusPending,
	}

	// 保存到数据库
	if err := h.db.Create(application).Error; err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}

	response.Success(c, gin.H{
		"id":      application.ID,
		"message": "申请已提交，我们将尽快与您联系",
	})
}

// ListApplications 获取申请列表 GET /api/v1/admin/agent-applications
// 支持分页和状态过滤
func (h *AgentApplicationHandler) ListApplications(c *gin.Context) {
	// 解析分页参数
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	// 解析状态过滤参数
	status := c.Query("status") // pending/approved/rejected，空表示全部

	// 构建查询
	query := h.db.Model(&model.AgentApplication{})
	if status != "" {
		query = query.Where("status = ?", status)
	}

	// 统计总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}

	// 查询列表，按创建时间倒序
	var applications []model.AgentApplication
	offset := (page - 1) * pageSize
	if err := query.Order("created_at DESC").Offset(offset).Limit(pageSize).Find(&applications).Error; err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}

	response.PageResult(c, applications, total, page, pageSize)
}

// ReviewApplicationRequest 审核请求体
type ReviewApplicationRequest struct {
	Action     string `json:"action" binding:"required,oneof=approve reject"` // approve 或 reject
	ReviewNote string `json:"review_note"`                                   // 审核备注
}

// ReviewApplication 审核申请 PUT /api/v1/admin/agent-applications/:id/review
// 管理员审核通过或拒绝申请
func (h *AgentApplicationHandler) ReviewApplication(c *gin.Context) {
	// 解析申请ID
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 绑定请求参数
	var req ReviewApplicationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 查询申请记录
	var application model.AgentApplication
	if err := h.db.First(&application, id).Error; err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	// 检查是否已是终态（已审核过）
	if !application.IsPending() {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "该申请已审核，无法重复操作")
		return
	}

	// 获取当前审核人ID（从JWT中获取）
	reviewerID, _ := c.Get("user_id")
	uid, _ := reviewerID.(uint)

	// 更新审核状态
	now := time.Now()
	application.ReviewerID = &uid
	application.ReviewNote = req.ReviewNote
	application.ReviewedAt = &now

	if req.Action == "approve" {
		application.Status = model.ApplicationStatusApproved
	} else {
		application.Status = model.ApplicationStatusRejected
	}

	// 保存更新
	if err := h.db.Save(&application).Error; err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}

	// 如果审核通过，创建代理商档案
	if req.Action == "approve" {
		h.createAgentProfile(&application)
	}

	response.Success(c, application)
}

// createAgentProfile 为审核通过的申请人创建代理商档案
// 自动创建用户账号（如果不存在）并设置代理商角色
func (h *AgentApplicationHandler) createAgentProfile(application *model.AgentApplication) {
	// 查找是否已有用户账号
	var user model.User
	err := h.db.Where("email = ?", application.Email).First(&user).Error

	if err == gorm.ErrRecordNotFound {
		// 用户不存在，创建新用户（默认密码，需要用户重置）
		// 这里暂不自动创建用户，让管理员手动处理
		// 实际生产环境可以发送邮件邀请用户注册
		fmt.Printf("[AgentApplication] 审核通过，申请人邮箱: %s，请手动创建代理商账号\n", application.Email)
		return
	}

	if err != nil {
		// 数据库错误，忽略
		return
	}

	// 用户存在，更新为代理商角色
	// 检查是否已是代理商
	if user.Role == "AGENT_L1" || user.Role == "AGENT_L2" || user.Role == "AGENT_L3" {
		return // 已是代理商，无需重复设置
	}

	// 设置为一级代理商
	user.Role = "AGENT_L1"
	h.db.Save(&user)
}
