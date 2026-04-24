// Package admin 用户认证行为日志查询
package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/authlog"
)

// AuthLogHandler 用户认证日志管理接口
type AuthLogHandler struct {
	recorder *authlog.Recorder
}

// NewAuthLogHandler 构造
func NewAuthLogHandler(recorder *authlog.Recorder) *AuthLogHandler {
	return &AuthLogHandler{recorder: recorder}
}

// Register 注册路由
func (h *AuthLogHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/auth-logs", h.List)
	rg.GET("/auth-logs/stats", h.Stats)
}

// List GET /admin/auth-logs
// 支持参数：user_id / email / event_type / ip / start / end / page / page_size
func (h *AuthLogHandler) List(c *gin.Context) {
	if h.recorder == nil {
		response.Error(c, http.StatusServiceUnavailable, errcode.ErrInternal)
		return
	}
	q := &model.UserAuthLogQuery{
		Email:      c.Query("email"),
		EventType:  c.Query("event_type"),
		IP:         c.Query("ip"),
		Keyword:    c.Query("keyword"),
		RequestID:  c.Query("request_id"),
		UserAgent:  c.Query("user_agent"),
		Country:    c.Query("country"),
		City:       c.Query("city"),
		FailReason: c.Query("fail_reason"),
	}
	if v := c.Query("user_id"); v != "" {
		if uid, err := strconv.ParseUint(v, 10, 64); err == nil {
			q.UserID = uint(uid)
		}
	}
	if v := c.Query("start"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q.StartDate = t
		} else if t, err := time.Parse("2006-01-02", v); err == nil {
			q.StartDate = t
		}
	}
	if v := c.Query("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q.EndDate = t
		} else if t, err := time.Parse("2006-01-02", v); err == nil {
			q.EndDate = t.Add(24 * time.Hour)
		}
	}
	q.Page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	q.PageSize, _ = strconv.Atoi(c.DefaultQuery("page_size", "50"))

	list, total, err := h.recorder.List(c.Request.Context(), q)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{
		"list":      list,
		"total":     total,
		"page":      q.Page,
		"page_size": q.PageSize,
	})
}

// Stats GET /admin/auth-logs/stats?days=7
func (h *AuthLogHandler) Stats(c *gin.Context) {
	if h.recorder == nil {
		response.Error(c, http.StatusServiceUnavailable, errcode.ErrInternal)
		return
	}
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	if days <= 0 {
		days = 7
	}
	if days > 365 {
		days = 365
	}
	st, err := h.recorder.GetStats(c.Request.Context(), days)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, st)
}
