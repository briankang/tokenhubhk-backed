package user

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/response"
	announcementsvc "tokenhub-server/internal/service/announcement"
)

// ========================================================================
// NotificationHandler — 用户端站内通知（已读状态管理）
// 依赖 AnnouncementService，通过 user_announcement_reads 表记录已读状态
// ========================================================================

// NotificationHandler 用户通知处理器
type NotificationHandler struct {
	svc *announcementsvc.Service
}

// NewNotificationHandler 创建处理器实例
func NewNotificationHandler(db *gorm.DB) *NotificationHandler {
	return &NotificationHandler{svc: announcementsvc.NewService(db)}
}

// Register 注册路由到用户组
func (h *NotificationHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/notifications", h.List)
	rg.GET("/notifications/unread-count", h.UnreadCount)
	rg.POST("/notifications/read-all", h.MarkAllRead)
	rg.POST("/notifications/:id/read", h.MarkRead)
}

// List GET /user/notifications?page=1&page_size=20&unread=1
func (h *NotificationHandler) List(c *gin.Context) {
	userID := h.getUserID(c)
	if userID == 0 {
		response.ErrorMsg(c, http.StatusUnauthorized, 40101, "未认证")
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page <= 0 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	unreadOnly := c.Query("unread") == "1"
	readOnly := c.Query("read") == "1"

	list, total, err := h.svc.GetUserNotifications(c.Request.Context(), userID, page, pageSize, unreadOnly, readOnly)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "查询失败")
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}

// UnreadCount GET /user/notifications/unread-count
func (h *NotificationHandler) UnreadCount(c *gin.Context) {
	userID := h.getUserID(c)
	if userID == 0 {
		response.ErrorMsg(c, http.StatusUnauthorized, 40101, "未认证")
		return
	}
	count, err := h.svc.GetUnreadCount(c.Request.Context(), userID)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50002, "统计失败")
		return
	}
	response.Success(c, gin.H{"count": count})
}

// MarkRead POST /user/notifications/:id/read
func (h *NotificationHandler) MarkRead(c *gin.Context) {
	userID := h.getUserID(c)
	if userID == 0 {
		response.ErrorMsg(c, http.StatusUnauthorized, 40101, "未认证")
		return
	}
	annID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "无效ID")
		return
	}
	if err := h.svc.MarkAsRead(c.Request.Context(), userID, uint(annID)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50003, "标记已读失败")
		return
	}
	response.Success(c, nil)
}

// MarkAllRead POST /user/notifications/read-all
func (h *NotificationHandler) MarkAllRead(c *gin.Context) {
	userID := h.getUserID(c)
	if userID == 0 {
		response.ErrorMsg(c, http.StatusUnauthorized, 40101, "未认证")
		return
	}
	if err := h.svc.MarkAllAsRead(c.Request.Context(), userID); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50004, "操作失败")
		return
	}
	response.Success(c, nil)
}

func (h *NotificationHandler) getUserID(c *gin.Context) uint {
	uid, ok := c.Get("userId")
	if !ok {
		return 0
	}
	id, _ := uid.(uint)
	return id
}
