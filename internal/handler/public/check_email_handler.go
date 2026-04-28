package public

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/user"
)

// ========================================================================
// CheckEmailHandler — 注册前邮箱预检接口(公开,无需登录)
//
// 用途:用户在注册页填完邮箱失焦时调用,判断该邮箱是否已被占用,
// 立即给出"该邮箱已注册"提示,避免用户填完整张表单提交才得到 400。
//
// 设计要点:
//   - 仅返回 {exists: bool},不暴露用户 ID/姓名/状态等任何额外信息
//   - 软删除的 tombstone 用户(deleted_*@deleted.tombstone)默认被 GORM 排除,不会误命中
//   - IP 级限流:每分钟 30 次/IP,防止枚举注册邮箱(Redis 不可用时 fail-open)
// ========================================================================

// CheckEmailHandler 邮箱预检处理器
type CheckEmailHandler struct {
	db      *gorm.DB
	userSvc *user.UserService
}

// NewCheckEmailHandler 构造邮箱预检处理器
func NewCheckEmailHandler(db *gorm.DB) *CheckEmailHandler {
	if db == nil {
		panic("check email handler: db is nil")
	}
	return &CheckEmailHandler{
		db:      db,
		userSvc: user.NewUserService(db),
	}
}

// Register 注册路由
// POST /api/v1/public/check-email
func (h *CheckEmailHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/check-email", h.CheckEmail)
}

// checkEmailRequest 请求体
type checkEmailRequest struct {
	Email string `json:"email" binding:"required"`
}

// checkEmailResponse 响应体
type checkEmailResponse struct {
	Exists bool `json:"exists"`
}

// CheckEmail 检查邮箱是否已被注册
//
// 错误码:
//   40001 - email 字段缺失或格式不合法
//   42901 - IP 限流(1 分钟超过 30 次)
//   50001 - DB 异常
func (h *CheckEmailHandler) CheckEmail(c *gin.Context) {
	var req checkEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "email is required")
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !emailRegex.MatchString(email) {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "invalid email format")
		return
	}

	// IP 限流(Redis 不可用 fail-open)
	if redis.Client != nil {
		ip := c.ClientIP()
		if ip != "" {
			key := "check_email:ip:" + ip
			ctx, cancel := context.WithTimeout(c.Request.Context(), 800*time.Millisecond)
			defer cancel()
			n, err := redis.Client.Incr(ctx, key).Result()
			if err == nil {
				if n == 1 {
					_ = redis.Client.Expire(ctx, key, 1*time.Minute).Err()
				}
				if n > 30 {
					response.ErrorMsg(c, http.StatusTooManyRequests, 42901, "too many requests, please retry in a minute")
					return
				}
			} else if !errors.Is(err, goredis.Nil) {
				// Redis 异常时不阻塞用户,继续走 DB 查询
				_ = err
			}
		}
	}

	exists, err := h.userSvc.EmailExists(c.Request.Context(), email)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "failed to check email")
		return
	}

	response.Success(c, checkEmailResponse{Exists: exists})
}
