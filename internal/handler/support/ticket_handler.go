package support

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	supportsvc "tokenhub-server/internal/service/support"
)

// TicketHandler 工单相关路由
type TicketHandler struct {
	svc *supportsvc.Services
}

func NewTicketHandler(svc *supportsvc.Services) *TicketHandler {
	return &TicketHandler{svc: svc}
}

// Register 挂载到 /api/v1/support/tickets
func (h *TicketHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/tickets", h.Create)
	rg.GET("/tickets", h.List)
	rg.GET("/tickets/:no", h.Get)
	rg.POST("/tickets/:id/replies", h.Reply)
	rg.POST("/tickets/:id/resolve", h.Resolve)
	rg.POST("/tickets/:id/reopen", h.Reopen)
}

// CreateRequest 创建工单请求
type CreateRequest struct {
	Title            string `json:"title" binding:"required"`
	Description      string `json:"description" binding:"required"`
	Category         string `json:"category" binding:"required"`
	Priority         string `json:"priority"`
	ContactEmail     string `json:"contact_email"`
	RelatedSessionID uint   `json:"related_session_id"`
}

// Create POST /tickets
func (h *TicketHandler) Create(c *gin.Context) {
	userID := c.GetUint("userId")
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 10005, "message": "unauthorized"})
		return
	}
	var body CreateRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40000, "message": err.Error()})
		return
	}
	if len(body.Title) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40001, "message": "title too long"})
		return
	}
	if len(body.Description) > 10000 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40002, "message": "description too long"})
		return
	}
	allowedCats := map[string]struct{}{
		model.SupportTicketCategoryAPI:                  {},
		model.SupportTicketCategoryAPIInterfaceFeedback: {},
		model.SupportTicketCategoryBilling:              {},
		model.SupportTicketCategoryChannel:              {},
		model.SupportTicketCategoryAccount:              {},
		model.SupportTicketCategoryOther:                {},
	}
	if _, ok := allowedCats[body.Category]; !ok {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40003, "message": "invalid category"})
		return
	}
	if body.Priority == "" {
		body.Priority = "normal"
	}

	t := &model.SupportTicket{
		UserID:       userID,
		ContactEmail: strings.TrimSpace(body.ContactEmail),
		Title:        strings.TrimSpace(body.Title),
		Description:  body.Description,
		Category:     body.Category,
		Priority:     body.Priority,
		SourceIP:     c.ClientIP(),
		DueAt:        time.Now().Add(24 * time.Hour),
	}
	if body.RelatedSessionID > 0 {
		sid := body.RelatedSessionID
		t.RelatedSessionID = &sid
	}
	if err := h.svc.TicketSvc.Create(c.Request.Context(), t); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50001, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"id": t.ID, "ticket_no": t.TicketNo}})
}

// List GET /tickets?status=pending&page=1&page_size=20
func (h *TicketHandler) List(c *gin.Context) {
	userID := c.GetUint("userId")
	status := strings.TrimSpace(c.Query("status"))
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	list, total, err := h.svc.TicketSvc.ListMine(c.Request.Context(), userID, status, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50001, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"list": list, "total": total, "page": page, "page_size": pageSize}})
}

// Get GET /tickets/:no
func (h *TicketHandler) Get(c *gin.Context) {
	userID := c.GetUint("userId")
	no := strings.TrimSpace(c.Param("no"))
	if no == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40000, "message": "invalid ticket no"})
		return
	}
	ticket, replies, err := h.svc.TicketSvc.GetByTicketNo(c.Request.Context(), userID, no)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 40400, "message": "ticket not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"ticket": ticket, "replies": replies}})
}

// Reply POST /tickets/:id/replies
func (h *TicketHandler) Reply(c *gin.Context) {
	userID := c.GetUint("userId")
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40000, "message": "invalid id"})
		return
	}
	var body struct {
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40000, "message": err.Error()})
		return
	}
	if len(body.Content) > 10000 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40001, "message": "content too long"})
		return
	}
	if err := h.svc.TicketSvc.AppendReply(c.Request.Context(), userID, uint(id), body.Content); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50001, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"ok": true}})
}

// Resolve POST /tickets/:id/resolve
func (h *TicketHandler) Resolve(c *gin.Context) {
	userID := c.GetUint("userId")
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40000, "message": "invalid id"})
		return
	}
	if err := h.svc.TicketSvc.ConfirmResolved(c.Request.Context(), userID, uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50001, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"ok": true}})
}

// Reopen POST /tickets/:id/reopen
func (h *TicketHandler) Reopen(c *gin.Context) {
	userID := c.GetUint("userId")
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40000, "message": "invalid id"})
		return
	}
	if err := h.svc.TicketSvc.Reopen(c.Request.Context(), userID, uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50001, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"ok": true}})
}
