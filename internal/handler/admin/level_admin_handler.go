package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	membersvc "tokenhub-server/internal/service/member"
)

// LevelAdminHandler 管理员等级配置管理接口处理器
// v3.1 仅保留会员等级 CRUD（代理机制已物理删除）
type LevelAdminHandler struct {
	memberSvc *membersvc.MemberLevelService
}

// NewLevelAdminHandler 创建等级管理 Handler 实例
func NewLevelAdminHandler(memberSvc *membersvc.MemberLevelService) *LevelAdminHandler {
	return &LevelAdminHandler{memberSvc: memberSvc}
}

// Register 注册等级管理路由到管理员路由组
func (h *LevelAdminHandler) Register(rg *gin.RouterGroup) {
	// === 会员等级管理 ===
	rg.GET("/member-levels", h.GetMemberLevels)
	rg.POST("/member-levels", h.CreateMemberLevel)
	rg.PUT("/member-levels/:id", h.UpdateMemberLevel)
	rg.DELETE("/member-levels/:id", h.DeleteMemberLevel)

	// === 批量设置用户级 RPM/TPM 覆盖 ===
	rg.POST("/users/batch-rate-limits", h.BatchSetUserRateLimits)
}

// BatchSetUserRateLimitsRequest 批量设置用户限流请求体
// rpm/tpm 任一为 0 表示该字段不修改；user_ids 为目标用户 ID 列表
type BatchSetUserRateLimitsRequest struct {
	UserIDs []uint `json:"user_ids" binding:"required,min=1"`
	RPM     int    `json:"rpm"`
	TPM     int    `json:"tpm"`
}

// BatchSetUserRateLimits 批量为指定用户设置自定义 RPM / TPM 覆盖
// POST /api/v1/admin/users/batch-rate-limits
func (h *LevelAdminHandler) BatchSetUserRateLimits(c *gin.Context) {
	var req BatchSetUserRateLimitsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	if req.RPM <= 0 && req.TPM <= 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "rpm 和 tpm 至少需指定一项 (>0)")
		return
	}
	updated, err := h.memberSvc.BatchSetUserRateLimits(c.Request.Context(), req.UserIDs, req.RPM, req.TPM)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"updated": updated})
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

	// 绑定部分更新字段
	var req struct {
		LevelName          *string  `json:"level_name"`
		MinTotalConsume    *float64 `json:"min_total_consume"`
		MinTotalConsumeRMB *float64 `json:"min_total_consume_rmb"`
		ModelDiscount      *float64 `json:"model_discount"`
		DefaultRPM         *int     `json:"default_rpm"`
		DefaultTPM         *int     `json:"default_tpm"`
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
	if req.DefaultRPM != nil {
		updates["default_rpm"] = *req.DefaultRPM
	}
	if req.DefaultTPM != nil {
		updates["default_tpm"] = *req.DefaultTPM
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
