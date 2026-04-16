package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/response"
	announcementsvc "tokenhub-server/internal/service/announcement"
)

// ========================================================================
// AnnouncementHandler — 站内公告管理（管理员端）
// 支持增删改查、统计
// ========================================================================

// AnnouncementHandler 公告管理处理器
type AnnouncementHandler struct {
	svc *announcementsvc.Service
}

// NewAnnouncementHandler 创建处理器实例
func NewAnnouncementHandler(db *gorm.DB) *AnnouncementHandler {
	return &AnnouncementHandler{svc: announcementsvc.NewService(db)}
}

// Register 注册路由到管理员组
func (h *AnnouncementHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/announcements/stats", h.Stats)
	rg.GET("/announcements", h.List)
	rg.POST("/announcements", h.Create)
	rg.PUT("/announcements/:id", h.Update)
	rg.DELETE("/announcements/:id", h.Delete)
}

// List GET /admin/announcements?page=1&page_size=20&type=warning&status=active&priority=high
func (h *AnnouncementHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page <= 0 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 20
	}

	list, total, err := h.svc.List(
		c.Request.Context(),
		page, pageSize,
		c.Query("type"),
		c.Query("status"),
		c.Query("priority"),
	)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "查询失败")
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}

// Create POST /admin/announcements
func (h *AnnouncementHandler) Create(c *gin.Context) {
	var req announcementsvc.CreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, err.Error())
		return
	}
	// 校验枚举值
	if !isValidAnnouncementType(req.Type) {
		response.ErrorMsg(c, http.StatusBadRequest, 40002, "无效的公告类型")
		return
	}
	if !isValidPriority(req.Priority) {
		response.ErrorMsg(c, http.StatusBadRequest, 40003, "无效的优先级")
		return
	}
	if !isValidAnnouncementStatus(req.Status) {
		response.ErrorMsg(c, http.StatusBadRequest, 40004, "无效的状态")
		return
	}

	var creatorID uint
	if uid, ok := c.Get("userId"); ok {
		creatorID, _ = uid.(uint)
	}
	ann, err := h.svc.Create(c.Request.Context(), req, creatorID)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50002, "创建失败")
		return
	}
	response.Success(c, ann)
}

// Update PUT /admin/announcements/:id
func (h *AnnouncementHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "无效ID")
		return
	}
	var req announcementsvc.UpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40002, err.Error())
		return
	}
	ann, err := h.svc.Update(c.Request.Context(), uint(id), req)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50003, "更新失败")
		return
	}
	response.Success(c, ann)
}

// Delete DELETE /admin/announcements/:id
func (h *AnnouncementHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "无效ID")
		return
	}
	if err := h.svc.Delete(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50004, "删除失败")
		return
	}
	response.Success(c, nil)
}

// Stats GET /admin/announcements/stats
func (h *AnnouncementHandler) Stats(c *gin.Context) {
	stats, err := h.svc.GetStats(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50005, "统计失败")
		return
	}
	response.Success(c, stats)
}

// ========== 枚举校验辅助 ==========

func isValidAnnouncementType(t string) bool {
	valid := map[string]bool{
		"info": true, "warning": true, "success": true,
		"error": true, "model_deprecation": true, "system": true,
	}
	return valid[t]
}

func isValidPriority(p string) bool {
	valid := map[string]bool{"low": true, "normal": true, "high": true, "urgent": true}
	return valid[p]
}

func isValidAnnouncementStatus(s string) bool {
	valid := map[string]bool{"draft": true, "active": true, "inactive": true}
	return valid[s]
}
