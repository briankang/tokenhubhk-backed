package public

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
	smsauth "tokenhub-server/internal/service/sms"
)

// CheckPhoneHandler 处理注册前手机号唯一性预检。
type CheckPhoneHandler struct {
	db *gorm.DB
}

func NewCheckPhoneHandler(db *gorm.DB) *CheckPhoneHandler {
	if db == nil {
		panic("check phone handler: db is nil")
	}
	return &CheckPhoneHandler{db: db}
}

func (h *CheckPhoneHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/check-phone", h.CheckPhone)
}

func (h *CheckPhoneHandler) CheckPhone(c *gin.Context) {
	var req struct {
		Phone string `json:"phone" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "phone is required")
		return
	}
	phone, err := smsauth.NormalizeCNPhone(req.Phone)
	if err != nil {
		response.Success(c, gin.H{"exists": false, "valid": false, "reason": err.Error()})
		return
	}
	if redis.Client != nil {
		ip := c.ClientIP()
		if ip != "" {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 800*time.Millisecond)
			defer cancel()
			key := "check_phone:ip:" + ip
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
	if err := h.db.WithContext(c.Request.Context()).Model(&model.User{}).Where("phone_e164 = ?", phone).Count(&count).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "failed to check phone")
		return
	}
	response.Success(c, gin.H{"exists": count > 0, "valid": true, "masked_phone": smsauth.MaskPhone(phone)})
}
