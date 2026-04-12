package agent

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/referral"
)

// CommissionHandler 代理商佣金接口处理器
type CommissionHandler struct {
	svc *referral.ReferralService
}

// NewCommissionHandler 创建佣金Handler实例
func NewCommissionHandler(svc *referral.ReferralService) *CommissionHandler {
	return &CommissionHandler{svc: svc}
}

// Register 注册佣金路由到代理商路由组
func (h *CommissionHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/commissions", h.ListCommissions)
	rg.GET("/commissions/summary", h.GetSummary)
}

// ListCommissions 获取佣金记录列表 GET /api/v1/agent/commissions
func (h *CommissionHandler) ListCommissions(c *gin.Context) {
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

// GetSummary 获取佣金汇总信息 GET /api/v1/agent/commissions/summary
func (h *CommissionHandler) GetSummary(c *gin.Context) {
	userID, _ := c.Get("userId")
	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	summary, err := h.svc.GetCommissionSummary(c.Request.Context(), uid)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.Success(c, summary)
}
