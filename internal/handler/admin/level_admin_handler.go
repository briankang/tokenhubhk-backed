package admin

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	agentsvc "tokenhub-server/internal/service/agent"
	membersvc "tokenhub-server/internal/service/member"
)

// LevelAdminHandler 管理员等级配置管理接口处理器
// 统一管理会员等级和代理等级的 CRUD、代理申请审核、提现审核
type LevelAdminHandler struct {
	memberSvc *membersvc.MemberLevelService
	agentSvc  *agentsvc.AgentLevelService
}

// NewLevelAdminHandler 创建等级管理 Handler 实例
func NewLevelAdminHandler(memberSvc *membersvc.MemberLevelService, agentSvc *agentsvc.AgentLevelService) *LevelAdminHandler {
	return &LevelAdminHandler{memberSvc: memberSvc, agentSvc: agentSvc}
}

// Register 注册等级管理路由到管理员路由组
func (h *LevelAdminHandler) Register(rg *gin.RouterGroup) {
	// === 会员等级管理 ===
	rg.GET("/member-levels", h.GetMemberLevels)
	rg.POST("/member-levels", h.CreateMemberLevel)
	rg.PUT("/member-levels/:id", h.UpdateMemberLevel)
	rg.DELETE("/member-levels/:id", h.DeleteMemberLevel)

	// === 代理等级管理 ===
	rg.GET("/agent-levels", h.GetAgentLevels)
	rg.POST("/agent-levels", h.CreateAgentLevel)
	rg.PUT("/agent-levels/:id", h.UpdateAgentLevel)
	rg.DELETE("/agent-levels/:id", h.DeleteAgentLevel)

	// === 代理申请审核 ===
	rg.GET("/agent-applications", h.GetAgentApplications)
	rg.POST("/agent-applications/:id/approve", h.ApproveAgentApplication)
	rg.POST("/agent-applications/:id/reject", h.RejectAgentApplication)

	// === 提现审核 ===
	rg.GET("/withdrawals", h.GetWithdrawalRequests)
	rg.POST("/withdrawals/:id/approve", h.ApproveWithdrawal)
	rg.POST("/withdrawals/:id/reject", h.RejectWithdrawal)
}

// ========== 会员等级管理 ==========

// GetMemberLevels 获取会员等级配置列表
// GET /api/v1/admin/member-levels
func (h *LevelAdminHandler) GetMemberLevels(c *gin.Context) {
	levels, err := h.memberSvc.GetAllLevels(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.Success(c, levels)
}

// CreateMemberLevel 创建会员等级
// POST /api/v1/admin/member-levels
// 前端传入 RMB 字段，后端自动换算积分（1 RMB = 10,000 credits）
func (h *LevelAdminHandler) CreateMemberLevel(c *gin.Context) {
	var level model.MemberLevel
	if err := c.ShouldBindJSON(&level); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	if err := h.memberSvc.CreateLevel(&level); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, level)
}

// DeleteMemberLevel 删除会员等级
// DELETE /api/v1/admin/member-levels/:id
func (h *LevelAdminHandler) DeleteMemberLevel(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "无效的ID")
		return
	}
	if err := h.memberSvc.DeleteLevel(uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "删除成功"})
}

// UpdateMemberLevel 修改会员等级配置
// PUT /api/v1/admin/member-levels/:id
// 支持 RMB 字段，后端自动换算积分（1 RMB = 10,000 credits）
func (h *LevelAdminHandler) UpdateMemberLevel(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 绑定部分更新字段（同时支持积分字段和 RMB 字段）
	var req struct {
		LevelName          *string  `json:"level_name"`
		MinTotalConsume    *float64 `json:"min_total_consume"`
		MinTotalConsumeRMB *float64 `json:"min_total_consume_rmb"`
		ModelDiscount      *float64 `json:"model_discount"`
		MonthlyGift        *float64 `json:"monthly_gift"`
		MonthlyGiftRMB     *float64 `json:"monthly_gift_rmb"`
		MaxTokensPerReq    *int     `json:"max_tokens_per_req"`
		DailyLimit         *float64 `json:"daily_limit"`
		DailyLimitRMB      *float64 `json:"daily_limit_rmb"`
		DegradeMonths      *int     `json:"degrade_months"`
		IsActive           *bool    `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 构建更新字段 map（仅更新非 nil 字段）
	updates := make(map[string]interface{})
	if req.LevelName != nil {
		updates["level_name"] = *req.LevelName
	}
	if req.MinTotalConsume != nil {
		updates["min_total_consume"] = *req.MinTotalConsume
	}
	// RMB 字段：同时写入 RMB 值，Service 层自动换算积分
	if req.MinTotalConsumeRMB != nil {
		updates["min_total_consume_rmb"] = *req.MinTotalConsumeRMB
	}
	if req.ModelDiscount != nil {
		updates["model_discount"] = *req.ModelDiscount
	}
	if req.MonthlyGift != nil {
		updates["monthly_gift"] = *req.MonthlyGift
	}
	if req.MonthlyGiftRMB != nil {
		updates["monthly_gift_rmb"] = *req.MonthlyGiftRMB
	}
	if req.MaxTokensPerReq != nil {
		updates["max_tokens_per_req"] = *req.MaxTokensPerReq
	}
	if req.DailyLimit != nil {
		updates["daily_limit"] = *req.DailyLimit
	}
	if req.DailyLimitRMB != nil {
		updates["daily_limit_rmb"] = *req.DailyLimitRMB
	}
	if req.DegradeMonths != nil {
		updates["degrade_months"] = *req.DegradeMonths
	}
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}

	if len(updates) == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "无更新字段")
		return
	}

	level, err := h.memberSvc.UpdateLevel(c.Request.Context(), uint(id), updates)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, level)
}

// ========== 代理等级管理 ==========

// GetAgentLevels 获取代理等级配置列表
// GET /api/v1/admin/agent-levels
func (h *LevelAdminHandler) GetAgentLevels(c *gin.Context) {
	levels, err := h.agentSvc.GetAllLevels(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.Success(c, levels)
}

// CreateAgentLevel 创建代理等级
// POST /api/v1/admin/agent-levels
// 前端传入 RMB 字段，后端自动换算积分（1 RMB = 10,000 credits）
func (h *LevelAdminHandler) CreateAgentLevel(c *gin.Context) {
	var level model.AgentLevel
	if err := c.ShouldBindJSON(&level); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	if err := h.agentSvc.CreateLevel(&level); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, level)
}

// DeleteAgentLevel 删除代理等级
// DELETE /api/v1/admin/agent-levels/:id
func (h *LevelAdminHandler) DeleteAgentLevel(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "无效的ID")
		return
	}
	if err := h.agentSvc.DeleteLevel(uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "删除成功"})
}

// UpdateAgentLevel 修改代理等级配置
// PUT /api/v1/admin/agent-levels/:id
// 支持 RMB 字段，后端自动换算积分（1 RMB = 10,000 credits）
func (h *LevelAdminHandler) UpdateAgentLevel(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 绑定部分更新字段（同时支持积分字段和 RMB 字段）
	var req struct {
		LevelName          *string  `json:"level_name"`
		MinMonthlySales    *float64 `json:"min_monthly_sales"`
		MinMonthlySalesRMB *float64 `json:"min_monthly_sales_rmb"`
		MinDirectSubs      *int     `json:"min_direct_subs"`
		DirectCommission   *float64 `json:"direct_commission"`
		L2Commission       *float64 `json:"l2_commission"`
		L3Commission       *float64 `json:"l3_commission"`
		Benefits           *string  `json:"benefits"`
		DegradeMonths      *int     `json:"degrade_months"`
		IsActive           *bool    `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 构建更新字段 map
	updates := make(map[string]interface{})
	if req.LevelName != nil {
		updates["level_name"] = *req.LevelName
	}
	if req.MinMonthlySales != nil {
		updates["min_monthly_sales"] = *req.MinMonthlySales
	}
	// RMB 字段：同时写入 RMB 值，Service 层自动换算积分
	if req.MinMonthlySalesRMB != nil {
		updates["min_monthly_sales_rmb"] = *req.MinMonthlySalesRMB
	}
	if req.MinDirectSubs != nil {
		updates["min_direct_subs"] = *req.MinDirectSubs
	}
	if req.DirectCommission != nil {
		updates["direct_commission"] = *req.DirectCommission
	}
	if req.L2Commission != nil {
		updates["l2_commission"] = *req.L2Commission
	}
	if req.L3Commission != nil {
		updates["l3_commission"] = *req.L3Commission
	}
	if req.Benefits != nil {
		updates["benefits"] = *req.Benefits
	}
	if req.DegradeMonths != nil {
		updates["degrade_months"] = *req.DegradeMonths
	}
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}

	if len(updates) == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "无更新字段")
		return
	}

	level, err := h.agentSvc.UpdateLevel(c.Request.Context(), uint(id), updates)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, level)
}

// ========== 代理申请审核 ==========

// GetAgentApplications 获取待审核代理申请列表
// GET /api/v1/admin/agent-applications?status=PENDING&page=1&page_size=20
func (h *LevelAdminHandler) GetAgentApplications(c *gin.Context) {
	status := c.Query("status")
	page := 1
	pageSize := 20
	if p := c.Query("page"); p != "" {
		fmt.Sscanf(p, "%d", &page)
	}
	if ps := c.Query("page_size"); ps != "" {
		fmt.Sscanf(ps, "%d", &pageSize)
	}

	applications, total, err := h.agentSvc.GetApplications(c.Request.Context(), status, page, pageSize)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.PageResult(c, applications, total, page, pageSize)
}

// ApproveAgentApplication 审核通过代理申请
// POST /api/v1/admin/agent-applications/:id/approve
func (h *LevelAdminHandler) ApproveAgentApplication(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 获取当前管理员ID
	adminID, _ := c.Get("userId")
	aid, _ := adminID.(uint)

	if err := h.agentSvc.ApproveAgent(c.Request.Context(), uint(id), aid); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "审核通过"})
}

// RejectAgentApplication 拒绝代理申请
// POST /api/v1/admin/agent-applications/:id/reject
func (h *LevelAdminHandler) RejectAgentApplication(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&req)

	adminID, _ := c.Get("userId")
	aid, _ := adminID.(uint)

	if err := h.agentSvc.RejectAgent(c.Request.Context(), uint(id), aid, req.Reason); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "已拒绝"})
}

// ========== 提现审核 ==========

// GetWithdrawalRequests 获取提现申请列表
// GET /api/v1/admin/withdrawals?status=PENDING&page=1&page_size=20
func (h *LevelAdminHandler) GetWithdrawalRequests(c *gin.Context) {
	status := c.Query("status")
	page := 1
	pageSize := 20
	if p := c.Query("page"); p != "" {
		fmt.Sscanf(p, "%d", &page)
	}
	if ps := c.Query("page_size"); ps != "" {
		fmt.Sscanf(ps, "%d", &pageSize)
	}

	records, total, err := h.agentSvc.GetAllWithdrawals(c.Request.Context(), status, page, pageSize)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.PageResult(c, records, total, page, pageSize)
}

// ApproveWithdrawal 审核提现
// POST /api/v1/admin/withdrawals/:id/approve
func (h *LevelAdminHandler) ApproveWithdrawal(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	adminID, _ := c.Get("userId")
	aid, _ := adminID.(uint)

	if err := h.agentSvc.ApproveWithdrawal(c.Request.Context(), uint(id), aid); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "提现已批准"})
}

// RejectWithdrawal 拒绝提现
// POST /api/v1/admin/withdrawals/:id/reject
func (h *LevelAdminHandler) RejectWithdrawal(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&req)

	adminID, _ := c.Get("userId")
	aid, _ := adminID.(uint)

	if err := h.agentSvc.RejectWithdrawal(c.Request.Context(), uint(id), aid, req.Reason); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "提现已拒绝"})
}
