package support

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	supportsvc "tokenhub-server/internal/service/support"
)

// SessionHandler 会话与消息相关路由
type SessionHandler struct {
	svc *supportsvc.Services
}

func NewSessionHandler(svc *supportsvc.Services) *SessionHandler {
	return &SessionHandler{svc: svc}
}

// Register 挂载到 /api/v1/support
func (h *SessionHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/sessions", h.List)
	rg.GET("/sessions/:id/messages", h.Messages)
	rg.POST("/sessions/:id/close", h.Close)
	rg.POST("/messages/:id/accept", h.Accept)
	rg.GET("/hot-questions", h.HotQuestions)
	rg.GET("/hot-questions/:id", h.HotQuestionDetail)
	rg.GET("/provider-docs", h.ProviderDocs)
}

// List GET /sessions?page=1&page_size=20
func (h *SessionHandler) List(c *gin.Context) {
	userID := c.GetUint("userId")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	list, total, err := h.svc.SessionSvc.List(c.Request.Context(), userID, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50001, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"list": list, "total": total, "page": page, "page_size": pageSize}})
}

// Messages GET /sessions/:id/messages
func (h *SessionHandler) Messages(c *gin.Context) {
	userID := c.GetUint("userId")
	sessionID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if sessionID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40000, "message": "invalid session id"})
		return
	}
	sess, err := h.svc.SessionSvc.GetOrCreate(c.Request.Context(), userID, uint(sessionID), "")
	if err != nil || sess.ID != uint(sessionID) {
		c.JSON(http.StatusNotFound, gin.H{"code": 40400, "message": "session not found"})
		return
	}
	msgs, err := h.svc.MessageSvc.ListMessages(c.Request.Context(), uint(sessionID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50001, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"session": sess, "messages": msgs}})
}

// Close POST /sessions/:id/close
func (h *SessionHandler) Close(c *gin.Context) {
	userID := c.GetUint("userId")
	sessionID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if sessionID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40000, "message": "invalid session id"})
		return
	}
	if err := h.svc.SessionSvc.Close(c.Request.Context(), userID, uint(sessionID)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50001, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"ok": true}})
}

// Accept POST /messages/:id/accept  body: { session_id }
func (h *SessionHandler) Accept(c *gin.Context) {
	userID := c.GetUint("userId")
	msgID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if msgID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40000, "message": "invalid message id"})
		return
	}
	var body struct {
		SessionID uint `json:"session_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40000, "message": err.Error()})
		return
	}
	sess, err := h.svc.SessionSvc.GetOrCreate(c.Request.Context(), userID, body.SessionID, "")
	if err != nil || sess.ID != body.SessionID {
		c.JSON(http.StatusNotFound, gin.H{"code": 40400, "message": "session not found"})
		return
	}
	if err := h.svc.MessageSvc.MarkAccepted(c.Request.Context(), body.SessionID, uint(msgID)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50001, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"ok": true}})
}

// HotQuestions GET /hot-questions?category=billing
func (h *SessionHandler) HotQuestions(c *gin.Context) {
	category := strings.TrimSpace(c.Query("category"))
	items, err := h.svc.HotQuestion.ListPublished(c.Request.Context(), category)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50001, "message": err.Error()})
		return
	}
	type row struct {
		ID       uint   `json:"id"`
		Title    string `json:"title"`
		Category string `json:"category"`
		Priority int    `json:"priority"`
	}
	out := make([]row, 0, len(items))
	for _, it := range items {
		out = append(out, row{ID: it.ID, Title: it.Title, Category: it.Category, Priority: it.Priority})
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": out})
}

// HotQuestionDetail GET /hot-questions/:id
func (h *SessionHandler) HotQuestionDetail(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": 40000, "message": "invalid id"})
		return
	}
	hq, err := h.svc.HotQuestion.Get(c.Request.Context(), uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 40400, "message": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": hq})
}

// ProviderDocs GET /provider-docs?q=...
func (h *SessionHandler) ProviderDocs(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if query != "" {
		refs := h.svc.ProviderSvc.MatchByQuery(c.Request.Context(), query, 10)
		c.JSON(http.StatusOK, gin.H{"code": 0, "data": refs})
		return
	}
	list, err := h.svc.ProviderSvc.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50001, "message": err.Error()})
		return
	}
	// Filter to active-only for user-facing endpoint
	active := make([]model.ProviderDocReference, 0, len(list))
	for _, r := range list {
		if r.IsActive {
			active = append(active, r)
		}
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": active})
}
