package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/response"
)

// ========================================================================
// PartnerApplicationAdminHandler — 合作伙伴线索后台管理
// 负责管理员查看 /partners 提交的合作申请，支持分页、未读过滤、
// 单条阅读/已读状态切换、更新处理状态（contacted/closed）。
// ========================================================================

// PartnerApplicationAdminHandler 合作伙伴线索管理处理器
type PartnerApplicationAdminHandler struct {
	db *gorm.DB
}

// NewPartnerApplicationAdminHandler 创建处理器实例
func NewPartnerApplicationAdminHandler(db *gorm.DB) *PartnerApplicationAdminHandler {
	return &PartnerApplicationAdminHandler{db: db}
}

// Register 注册路由到管理员组
func (h *PartnerApplicationAdminHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/partner-applications", h.List)
	rg.GET("/partner-applications/stats", h.Stats)
	rg.GET("/partner-applications/:id", h.Get)
	rg.PATCH("/partner-applications/:id/read", h.MarkRead)
	rg.PATCH("/partner-applications/:id/unread", h.MarkUnread)
	rg.PATCH("/partner-applications/:id/status", h.UpdateStatus)
	rg.DELETE("/partner-applications/:id", h.Delete)
}

// List GET /admin/partner-applications?page=1&page_size=20&status=pending&unread=1&keyword=xxx
func (h *PartnerApplicationAdminHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page <= 0 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 20
	}

	q := h.db.WithContext(c.Request.Context()).Model(&model.PartnerApplication{})

	// 过滤：处理状态
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	// 过滤：合作类型
	if coopType := c.Query("cooperation_type"); coopType != "" {
		q = q.Where("cooperation_type = ?", coopType)
	}
	// 过滤：未读
	if c.Query("unread") == "1" {
		q = q.Where("read_at IS NULL")
	}
	// 关键字搜索：姓名/邮箱/公司
	if kw := c.Query("keyword"); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR email LIKE ? OR company LIKE ?", like, like, like)
	}

	var total int64
	q.Count(&total)

	var list []model.PartnerApplication
	err := q.Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&list).Error
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "query failed: "+err.Error())
		return
	}

	response.PageResult(c, list, total, page, pageSize)
}

// Stats GET /admin/partner-applications/stats
// 返回未读、待处理、总数等统计计数，供导航徽标使用
func (h *PartnerApplicationAdminHandler) Stats(c *gin.Context) {
	db := h.db.WithContext(c.Request.Context())

	var total int64
	var unread int64
	var pending int64
	db.Model(&model.PartnerApplication{}).Count(&total)
	db.Model(&model.PartnerApplication{}).Where("read_at IS NULL").Count(&unread)
	db.Model(&model.PartnerApplication{}).Where("status = ?", "pending").Count(&pending)

	response.Success(c, gin.H{
		"total":   total,
		"unread":  unread,
		"pending": pending,
	})
}

// Get GET /admin/partner-applications/:id
// 查看单条详情（首次打开自动标记为已读）
func (h *PartnerApplicationAdminHandler) Get(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "invalid id")
		return
	}

	var app model.PartnerApplication
	if err := h.db.WithContext(c.Request.Context()).First(&app, id).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, 40401, "application not found")
		return
	}

	// 首次查看自动标记已读
	if app.ReadAt == nil {
		now := time.Now()
		h.db.WithContext(c.Request.Context()).Model(&model.PartnerApplication{}).Where("id = ?", app.ID).Update("read_at", now)
		app.ReadAt = &now
	}

	response.Success(c, app)
}

// MarkRead PATCH /admin/partner-applications/:id/read
func (h *PartnerApplicationAdminHandler) MarkRead(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "invalid id")
		return
	}

	now := time.Now()
	res := h.db.WithContext(c.Request.Context()).Model(&model.PartnerApplication{}).
		Where("id = ? AND read_at IS NULL", id).
		Update("read_at", now)
	if res.Error != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "update failed")
		return
	}
	response.Success(c, gin.H{"affected": res.RowsAffected, "read_at": now})
}

// MarkUnread PATCH /admin/partner-applications/:id/unread
func (h *PartnerApplicationAdminHandler) MarkUnread(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "invalid id")
		return
	}

	res := h.db.WithContext(c.Request.Context()).Model(&model.PartnerApplication{}).
		Where("id = ?", id).
		Update("read_at", nil)
	if res.Error != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "update failed")
		return
	}
	response.Success(c, gin.H{"affected": res.RowsAffected})
}

// allowedStatuses 可赋值的处理状态白名单
var allowedStatuses = map[string]struct{}{
	"pending":   {},
	"contacted": {},
	"closed":    {},
}

// UpdateStatus PATCH /admin/partner-applications/:id/status  body: {"status":"contacted"}
func (h *PartnerApplicationAdminHandler) UpdateStatus(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "invalid id")
		return
	}

	var body struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40002, "invalid body")
		return
	}
	if _, ok := allowedStatuses[body.Status]; !ok {
		response.ErrorMsg(c, http.StatusBadRequest, 40003, "invalid status value")
		return
	}

	res := h.db.WithContext(c.Request.Context()).Model(&model.PartnerApplication{}).
		Where("id = ?", id).
		Update("status", body.Status)
	if res.Error != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "update failed")
		return
	}
	response.Success(c, gin.H{"affected": res.RowsAffected, "status": body.Status})
}

// Delete DELETE /admin/partner-applications/:id
func (h *PartnerApplicationAdminHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "invalid id")
		return
	}

	res := h.db.WithContext(c.Request.Context()).Delete(&model.PartnerApplication{}, id)
	if res.Error != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "delete failed")
		return
	}
	response.Success(c, gin.H{"affected": res.RowsAffected})
}
