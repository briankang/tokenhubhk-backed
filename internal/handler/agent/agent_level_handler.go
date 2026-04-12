package agent

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	agentsvc "tokenhub-server/internal/service/agent"
)

// AgentLevelHandler 代理等级相关接口处理器
type AgentLevelHandler struct {
	svc *agentsvc.AgentLevelService
}

// NewAgentLevelHandler 创建代理等级 Handler 实例
func NewAgentLevelHandler(svc *agentsvc.AgentLevelService) *AgentLevelHandler {
	return &AgentLevelHandler{svc: svc}
}

// Register 注册代理等级路由到代理路由组
func (h *AgentLevelHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/apply", h.ApplyAgent)
	rg.GET("/profile", h.GetAgentProfile)
	rg.GET("/levels", h.GetAgentLevels)
	rg.GET("/progress", h.GetAgentProgress)
	rg.GET("/team", h.GetTeamTree)
	rg.GET("/team/stats", h.GetTeamStats)
	rg.POST("/withdraw", h.RequestWithdrawal)
	rg.GET("/withdrawals", h.GetWithdrawals)
}

// ApplyAgent 申请成为代理（免费）
// POST /api/v1/agent/apply
func (h *AgentLevelHandler) ApplyAgent(c *gin.Context) {
	userID, _ := c.Get("userId")
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	// 创建代理申请，默认 A0 推广员，状态 PENDING
	profile, err := h.svc.ApplyAgent(c.Request.Context(), uid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, profile)
}

// GetAgentProfile 获取代理档案
// GET /api/v1/agent/profile
func (h *AgentLevelHandler) GetAgentProfile(c *gin.Context) {
	userID, _ := c.Get("userId")
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	// 查询代理档案（含等级、可提现金额、待结算佣金）
	profile, err := h.svc.GetProfile(c.Request.Context(), uid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, profile)
}

// GetAgentLevels 获取所有代理等级列表
// GET /api/v1/agent/levels
func (h *AgentLevelHandler) GetAgentLevels(c *gin.Context) {
	levels, err := h.svc.GetAllLevels(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.Success(c, levels)
}

// GetAgentProgress 获取代理升级进度
// GET /api/v1/agent/progress
func (h *AgentLevelHandler) GetAgentProgress(c *gin.Context) {
	userID, _ := c.Get("userId")
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	// 查询升级进度（月销售额、直推人数 vs 下一级门槛）
	progress, err := h.svc.GetUpgradeProgress(c.Request.Context(), uid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, progress)
}

// GetTeamTree 获取团队树
// GET /api/v1/agent/team
func (h *AgentLevelHandler) GetTeamTree(c *gin.Context) {
	userID, _ := c.Get("userId")
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	// 递归构建团队树结构（最多3层）
	tree, err := h.svc.GetTeamTree(c.Request.Context(), uid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, tree)
}

// GetTeamStats 获取团队统计
// GET /api/v1/agent/team/stats
func (h *AgentLevelHandler) GetTeamStats(c *gin.Context) {
	userID, _ := c.Get("userId")
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	// 汇总团队数据（直推人数、总人数、销售额、收益等）
	stats, err := h.svc.GetTeamStats(c.Request.Context(), uid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, stats)
}

// RequestWithdrawal 申请提现
// POST /api/v1/agent/withdraw
func (h *AgentLevelHandler) RequestWithdrawal(c *gin.Context) {
	userID, _ := c.Get("userId")
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	var req struct {
		Amount   float64 `json:"amount" binding:"required,gt=0"`
		BankInfo string  `json:"bank_info" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 检查可提现金额 → 创建提现申请
	withdrawal, err := h.svc.RequestWithdrawal(c.Request.Context(), uid, req.Amount, req.BankInfo)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, withdrawal)
}

// GetWithdrawals 获取提现记录
// GET /api/v1/agent/withdrawals?page=1&page_size=20
func (h *AgentLevelHandler) GetWithdrawals(c *gin.Context) {
	userID, _ := c.Get("userId")
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	// 解析分页参数
	page := 1
	pageSize := 20
	if p := c.Query("page"); p != "" {
		fmt.Sscanf(p, "%d", &page)
	}
	if ps := c.Query("page_size"); ps != "" {
		fmt.Sscanf(ps, "%d", &pageSize)
	}

	records, total, err := h.svc.GetWithdrawals(c.Request.Context(), uid, page, pageSize)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.PageResult(c, records, total, page, pageSize)
}
