package public

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/geo"
)

// ========================================================================
// PartnerApplicationHandler — 合作伙伴线索申请接口（公开，无需登录）
// 负责接收 /partners 页面提交的合作意向，写入 partner_applications 表。
// ========================================================================

// PartnerApplicationHandler 合作伙伴线索申请处理器
type PartnerApplicationHandler struct {
	db *gorm.DB
}

// NewPartnerApplicationHandler 创建合作伙伴线索申请处理器实例
func NewPartnerApplicationHandler(db *gorm.DB) *PartnerApplicationHandler {
	return &PartnerApplicationHandler{db: db}
}

// Register 注册路由
// POST /api/v1/public/partner-applications
func (h *PartnerApplicationHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/partner-applications", h.Submit)
}

// partnerApplicationRequest 请求体
type partnerApplicationRequest struct {
	Name            string `json:"name" binding:"required"`
	Email           string `json:"email" binding:"required"`
	Phone           string `json:"phone"`
	Company         string `json:"company"`
	CooperationType string `json:"cooperation_type" binding:"required"`
	Message         string `json:"message"`
}

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// 合作类型白名单
var allowedCooperationTypes = map[string]struct{}{
	"enterprise":  {}, // 企业采购
	"channel":     {}, // 渠道代理
	"integration": {}, // 技术集成
	"other":       {}, // 其他
}

// Submit 提交合作申请
func (h *PartnerApplicationHandler) Submit(c *gin.Context) {
	var req partnerApplicationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "invalid request body: "+err.Error())
		return
	}

	// 长度裁剪 + trim
	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Phone = strings.TrimSpace(req.Phone)
	req.Company = strings.TrimSpace(req.Company)
	req.CooperationType = strings.TrimSpace(strings.ToLower(req.CooperationType))
	req.Message = strings.TrimSpace(req.Message)

	// 字段校验
	if len(req.Name) == 0 || len(req.Name) > 100 {
		response.ErrorMsg(c, http.StatusBadRequest, 40002, "name length must be 1-100")
		return
	}
	if !emailRegex.MatchString(req.Email) || len(req.Email) > 200 {
		response.ErrorMsg(c, http.StatusBadRequest, 40003, "invalid email format")
		return
	}
	if len(req.Phone) > 50 {
		response.ErrorMsg(c, http.StatusBadRequest, 40004, "phone too long")
		return
	}
	if len(req.Company) > 200 {
		response.ErrorMsg(c, http.StatusBadRequest, 40005, "company too long")
		return
	}
	if _, ok := allowedCooperationTypes[req.CooperationType]; !ok {
		response.ErrorMsg(c, http.StatusBadRequest, 40006, "invalid cooperation_type")
		return
	}
	if len(req.Message) > 2000 {
		response.ErrorMsg(c, http.StatusBadRequest, 40007, "message too long (max 2000)")
		return
	}

	// 防刷：同 IP 每 10 分钟最多 5 条
	ip := geo.GetClientIP(c.Request)
	if !h.checkRateLimit(c.Request.Context(), ip) {
		response.ErrorMsg(c, http.StatusTooManyRequests, 42901, "too many submissions, please try again later")
		return
	}

	entry := model.PartnerApplication{
		Name:            req.Name,
		Email:           req.Email,
		Phone:           req.Phone,
		Company:         req.Company,
		CooperationType: req.CooperationType,
		Message:         req.Message,
		Status:          "pending",
		SourceIP:        ip,
	}
	if err := h.db.WithContext(c.Request.Context()).Create(&entry).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "failed to save application")
		return
	}

	response.Success(c, gin.H{"id": entry.ID})
}

// checkRateLimit 基于 Redis 的 IP 级滑动窗口限流（10 分钟 5 次）
// Redis 不可用时 fail-open，不阻断正常提交
func (h *PartnerApplicationHandler) checkRateLimit(ctx context.Context, ip string) bool {
	if redis.Client == nil || ip == "" {
		return true
	}
	key := "partner_app:ip:" + ip
	// INCR；首次 set TTL 10 分钟
	cnt, err := redis.Client.Incr(ctx, key).Result()
	if err != nil {
		return true // fail-open
	}
	if cnt == 1 {
		redis.Client.Expire(ctx, key, 10*time.Minute)
	}
	return cnt <= 5
}
