package user

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/database"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/referral"
)

// ReferralHandler 用户推荐（推广）接口处理器
type ReferralHandler struct {
	svc     *referral.ReferralService
	ruleSvc *referral.CommissionRuleService
}

// NewReferralHandler 创建用户推荐Handler实例
func NewReferralHandler(svc *referral.ReferralService) *ReferralHandler {
	return &ReferralHandler{svc: svc}
}

// WithRuleService 注入特殊返佣规则 service（可选）
func (h *ReferralHandler) WithRuleService(s *referral.CommissionRuleService) *ReferralHandler {
	h.ruleSvc = s
	return h
}

// Register 注册推荐相关路由到用户路由组
func (h *ReferralHandler) Register(rg *gin.RouterGroup) {
	ref := rg.Group("/referral")
	ref.GET("/link", h.GetLink)
	ref.GET("/stats", h.GetStats)
	ref.GET("/commissions", h.GetCommissions)
	ref.GET("/my-rule", h.GetMyRule)
	ref.GET("/invitees", h.GetInvitees)
	ref.GET("/special-rules", h.GetSpecialRules)
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

// SpecialRuleItem 返给前端的单条特殊规则明细
type SpecialRuleItem struct {
	ModelID      uint    `json:"model_id"`
	ModelName    string  `json:"model_name"`
	DisplayName  string  `json:"display_name"`
	Rate         float64 `json:"rate"`          // 用户在此规则下的返佣比例
	PlatformRate float64 `json:"platform_rate"` // 平台默认比例
	RuleName     string  `json:"rule_name"`
	Note         string  `json:"note"`
}

// GetSpecialRules 返回当前用户作为邀请人时与平台默认不一致的特殊返佣规则列表
// GET /api/v1/user/referral/special-rules
func (h *ReferralHandler) GetSpecialRules(c *gin.Context) {
	userID, _ := c.Get("userId")
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	if h.ruleSvc == nil {
		response.Success(c, gin.H{"list": []SpecialRuleItem{}, "platform_rate": 0})
		return
	}

	platformRule, _ := h.svc.GetMyEffectiveRule(c.Request.Context(), uid)
	platformRate := float64(0)
	if platformRule != nil {
		platformRate = platformRule.CommissionRate
	}

	rules, err := h.ruleSvc.ListForInviter(c.Request.Context(), uid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 聚合到 model_id 维度（同一模型多条规则按 priority ASC 已返回，取第一条）
	seen := make(map[uint]struct{})
	items := make([]SpecialRuleItem, 0)
	db := database.DB
	for _, r := range rules {
		for _, mid := range r.ModelIDs {
			if _, ok := seen[mid]; ok {
				continue
			}
			// 只列出与平台默认不一致的
			if platformRate > 0 && r.CommissionRate == platformRate {
				continue
			}
			seen[mid] = struct{}{}
			var m model.AIModel
			if db != nil {
				_ = db.WithContext(c.Request.Context()).Select("id, model_name, display_name").
					First(&m, mid).Error
			}
			items = append(items, SpecialRuleItem{
				ModelID:      mid,
				ModelName:    m.ModelName,
				DisplayName:  m.DisplayName,
				Rate:         r.CommissionRate,
				PlatformRate: platformRate,
				RuleName:     r.Name,
				Note:         r.Note,
			})
		}
	}

	response.Success(c, gin.H{"list": items, "platform_rate": platformRate})
}
