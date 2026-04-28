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

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
	smsauth "tokenhub-server/internal/service/sms"
)

type CheckUsernameHandler struct {
	db *gorm.DB
}

func NewCheckUsernameHandler(db *gorm.DB) *CheckUsernameHandler {
	return &CheckUsernameHandler{db: db}
}

func (h *CheckUsernameHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/check-username", h.CheckUsername)
}

func (h *CheckUsernameHandler) CheckUsername(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "username is required")
		return
	}
	username := strings.ToLower(strings.TrimSpace(req.Username))
	if err := smsauth.ValidateUsername(username); err != nil {
		response.Success(c, gin.H{"exists": false, "valid": false, "reason": err.Error()})
		return
	}
	if redis.Client != nil {
		ip := c.ClientIP()
		if ip != "" {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 800*time.Millisecond)
			defer cancel()
			key := "check_username:ip:" + ip
			n, err := redis.Client.Incr(ctx, key).Result()
			if err == nil {
				if n == 1 {
					_ = redis.Client.Expire(ctx, key, time.Minute).Err()
				}
				if n > 30 {
					response.ErrorMsg(c, http.StatusTooManyRequests, 42901, "too many requests, please retry in a minute")
					return
				}
			} else if !errors.Is(err, goredis.Nil) {
				_ = err
			}
		}
	}
	var count int64
	if err := h.db.WithContext(c.Request.Context()).Model(&model.User{}).Where("username = ?", username).Count(&count).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "failed to check username")
		return
	}
	response.Success(c, gin.H{"exists": count > 0, "valid": true})
}
