package user

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/referral"
)

// ReferralHandler 用户推荐（推广）接口处理器
type ReferralHandler struct {
	svc *referral.ReferralService
}

// NewReferralHandler 创建用户推荐Handler实例
func NewReferralHandler(svc *referral.ReferralService) *ReferralHandler {
	return &ReferralHandler{svc: svc}
}

// Register 注册推荐相关路由到用户路由组
func (h *ReferralHandler) Register(rg *gin.RouterGroup) {
	ref := rg.Group("/referral")
	ref.GET("/link", h.GetLink)
	ref.GET("/stats", h.GetStats)
	ref.GET("/commissions", h.GetCommissions)
	ref.GET("/my-rule", h.GetMyRule)
	ref.GET("/invitees", h.GetInvitees)
}

// GetMyRule 获取用户当前生效的返佣规则 GET /api/v1/user/referral/my-rule
// 合并全局 ReferralConfig 与个人 UserCommissionOverride(若有),用于用户仪表盘展示
func (h *ReferralHandler) GetMyRule(c *gin.Context) {
	userID, _ := c.Get("userId")
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	rule, err := h.svc.GetMyEffectiveRule(c.Request.Context(), uid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, rule)
}

// GetLink 获取用户推荐链接 GET /api/v1/user/referral/link
func (h *ReferralHandler) GetLink(c *gin.Context) {
	userID, _ := c.Get("userId")
	tenantID, _ := c.Get("tenantId")

	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	tid, _ := tenantID.(uint)

	link, err := h.svc.GetOrCreateLink(c.Request.Context(), uid, tid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, gin.H{
		"code":          link.Code,
		"clickCount":    link.ClickCount,
		"registerCount": link.RegisterCount,
		"link":          fmt.Sprintf("/register?ref=%s", link.Code),
	})
}

// GetStats 获取用户推荐统计 GET /api/v1/user/referral/stats
func (h *ReferralHandler) GetStats(c *gin.Context) {
	userID, _ := c.Get("userId")
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	stats, err := h.svc.GetStats(c.Request.Context(), uid)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.Success(c, stats)
}

// GetInvitees 获取用户邀请的被邀用户列表（含消费状态） GET /api/v1/user/referral/invitees
func (h *ReferralHandler) GetInvitees(c *gin.Context) {
	userID, _ := c.Get("userId")
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	page := 1
	pageSize := 20
	if p := c.Query("page"); p != "" {
		fmt.Sscanf(p, "%d", &page)
	}
	if ps := c.Query("page_size"); ps != "" {
		fmt.Sscanf(ps, "%d", &pageSize)
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	list, total, err := h.svc.GetInvitees(c.Request.Context(), uid, page, pageSize)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}

// GetCommissions 获取用户佣金记录列表 GET /api/v1/user/referral/commissions
func (h *ReferralHandler) GetCommissions(c *gin.Context) {
	userID, _ := c.Get("userId")
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	page := 1
	pageSize := 20
	if p := c.Query("page"); p != "" {
		fmt.Sscanf(p, "%d", &page)
	}
	if ps := c.Query("page_size"); ps != "" {
		fmt.Sscanf(ps, "%d", &pageSize)
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	records, total, err := h.svc.GetUserCommissions(c.Request.Context(), uid, page, pageSize)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.PageResult(c, records, total, page, pageSize)
}
