package user

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/member"
)

// MemberHandler 会员等级相关接口处理器
type MemberHandler struct {
	svc *member.MemberLevelService
}

// NewMemberHandler 创建会员等级 Handler 实例
func NewMemberHandler(svc *member.MemberLevelService) *MemberHandler {
	return &MemberHandler{svc: svc}
}

// Register 注册会员等级路由到用户路由组
func (h *MemberHandler) Register(rg *gin.RouterGroup) {
	memberGroup := rg.Group("/member")
	{
		memberGroup.GET("/profile", h.GetMemberProfile)
		memberGroup.GET("/levels", h.GetMemberLevels)
		memberGroup.GET("/progress", h.GetMemberProgress)
	}
}

// GetMemberProfile 获取会员档案（等级、权益、升级进度）
// GET /api/v1/user/member/profile
func (h *MemberHandler) GetMemberProfile(c *gin.Context) {
	userID, _ := c.Get("userId")
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	// 查询会员档案（含等级信息和下一级进度）
	profile, err := h.svc.GetProfile(c.Request.Context(), uid)
	if err != nil {
		// 档案不存在时尝试自动初始化
		if initErr := h.svc.InitMemberProfile(c.Request.Context(), uid); initErr != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, initErr.Error())
			return
		}
		// 初始化后重新查询
		profile, err = h.svc.GetProfile(c.Request.Context(), uid)
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
			return
		}
	}

	response.Success(c, profile)
}

// GetMemberLevels 获取所有会员等级列表
// GET /api/v1/user/member/levels
func (h *MemberHandler) GetMemberLevels(c *gin.Context) {
	levels, err := h.svc.GetAllLevels(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.Success(c, levels)
}

// GetMemberProgress 获取升级进度
// GET /api/v1/user/member/progress
func (h *MemberHandler) GetMemberProgress(c *gin.Context) {
	userID, _ := c.Get("userId")
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	// 查询升级进度（当前等级、下一级门槛、进度百分比）
	progress, err := h.svc.GetUpgradeProgress(c.Request.Context(), uid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, progress)
}
