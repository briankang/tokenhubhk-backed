package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/response"
	supportsvc "tokenhub-server/internal/service/support"
)

// ================================================================================
// SupportAdminHandler — AI 客服系统管理员后台
// 覆盖：热门问题 CRUD+发布、供应商文档 CRUD、工单全局管理、
//       采纳问答审核、知识库重建、服务状态查询
// ================================================================================

type SupportAdminHandler struct {
	db       *gorm.DB
	services *supportsvc.Services
}

func NewSupportAdminHandler(db *gorm.DB, services *supportsvc.Services) *SupportAdminHandler {
	return &SupportAdminHandler{db: db, services: services}
}

// Register 挂载到 /admin
func (h *SupportAdminHandler) Register(rg *gin.RouterGroup) {
	// --- 状态 ---
	rg.GET("/support/status", h.Status)

	// --- 热门问题 ---
	rg.GET("/support/hot-questions", h.ListHotQuestions)
	rg.POST("/support/hot-questions", h.CreateHotQuestion)
	rg.PUT("/support/hot-questions/:id", h.UpdateHotQuestion)
	rg.POST("/support/hot-questions/:id/publish", h.PublishHotQuestion)
	rg.POST("/support/hot-questions/:id/unpublish", h.UnpublishHotQuestion)
	rg.DELETE("/support/hot-questions/:id", h.DeleteHotQuestion)

	// --- 供应商文档引用 ---
	rg.GET("/support/provider-docs", h.ListProviderDocs)
	rg.POST("/support/provider-docs", h.CreateProviderDoc)
	rg.PUT("/support/provider-docs/:id", h.UpdateProviderDoc)
	rg.DELETE("/support/provider-docs/:id", h.DeleteProviderDoc)

	// --- 工单 ---
	rg.GET("/support/tickets", h.ListTickets)
	rg.GET("/support/tickets/:id", h.GetTicket)
	rg.POST("/support/tickets/:id/reply", h.ReplyTicket)
	rg.PATCH("/support/tickets/:id/status", h.UpdateTicketStatus)
	rg.PATCH("/support/tickets/:id/assign", h.AssignTicket)

	// --- 采纳问答审核 ---
	rg.GET("/support/accepted-answers", h.ListAcceptedAnswers)
	rg.POST("/support/accepted-answers/:id/approve", h.ApproveAcceptedAnswer)
	rg.POST("/support/accepted-answers/:id/reject", h.RejectAcceptedAnswer)

	// --- 知识库 ---
	rg.POST("/support/knowledge/rebuild", h.RebuildKnowledge)
	rg.POST("/support/knowledge/rebuild/:source_type/:id", h.RebuildKnowledgeSource)
	rg.GET("/support/knowledge/stats", h.KnowledgeStats)

	// --- AI 客服模型候选配置（Fallback 链 + 联网搜索开关等）---
	rg.GET("/support/model-profiles", h.ListModelProfiles)
	rg.POST("/support/model-profiles", h.CreateModelProfile)
	rg.PUT("/support/model-profiles/:id", h.UpdateModelProfile)
	rg.DELETE("/support/model-profiles/:id", h.DeleteModelProfile)
	rg.PATCH("/support/model-profiles/:id/toggle", h.ToggleModelProfile)
}

// ================= 状态 =================

// Status GET /admin/support/status
// 返回：AI 客服是否启用 + 预算使用情况
func (h *SupportAdminHandler) Status(c *gin.Context) {
	ctx := c.Request.Context()
	data := gin.H{
		"enabled": h.services != nil && h.services.Enabled,
		"reason":  "",
	}
	if h.services != nil && !h.services.Enabled {
		data["reason"] = h.services.Disabled
	}
	if h.services != nil && h.services.Budget != nil {
		used, remaining, total := h.services.Budget.UsedAndRemaining(ctx)
		data["budget"] = gin.H{
			"used":      used,
			"remaining": remaining,
			"total":     total,
			"level":     h.services.Budget.Check(ctx).String(),
		}
	}
	// 知识库统计
	var knowledgeCount int64
	h.db.Model(&model.KnowledgeChunk{}).Where("is_active = ?", true).Count(&knowledgeCount)
	data["knowledge_chunks"] = knowledgeCount

	// 工单计数
	var pendingTickets int64
	h.db.Model(&model.SupportTicket{}).Where("status = ?", "pending").Count(&pendingTickets)
	data["pending_tickets"] = pendingTickets

	// 待审核采纳问答
	var pendingAnswers int64
	h.db.Model(&model.AcceptedAnswer{}).Where("status = ?", "pending_review").Count(&pendingAnswers)
	data["pending_accepted_answers"] = pendingAnswers

	response.Success(c, data)
}

// ================= 热门问题 =================

type hotQuestionRequest struct {
	Title         string `json:"title" binding:"required"`
	QuestionBody  string `json:"question_body" binding:"required"`
	CuratedAnswer string `json:"curated_answer" binding:"required"`
	Category      string `json:"category"`
	Tags          string `json:"tags"`
	Priority      int    `json:"priority"`
}

// ListHotQuestions GET /admin/support/hot-questions?category=&is_published=&page=1&page_size=20
func (h *SupportAdminHandler) ListHotQuestions(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page <= 0 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}

	q := h.db.WithContext(c.Request.Context()).Model(&model.HotQuestion{})
	if cat := c.Query("category"); cat != "" {
		q = q.Where("category = ?", cat)
	}
	if pub := c.Query("is_published"); pub == "true" {
		q = q.Where("is_published = ?", true)
	} else if pub == "false" {
		q = q.Where("is_published = ?", false)
	}
	var total int64
	q.Count(&total)

	var list []model.HotQuestion
	if err := q.Order("priority DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}

// CreateHotQuestion POST /admin/support/hot-questions
func (h *SupportAdminHandler) CreateHotQuestion(c *gin.Context) {
	var body hotQuestionRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if body.Priority == 0 {
		body.Priority = 10
	}
	userID := c.GetUint("userId")
	hq := &model.HotQuestion{
		Title:         strings.TrimSpace(body.Title),
		QuestionBody:  body.QuestionBody,
		CuratedAnswer: body.CuratedAnswer,
		Category:      body.Category,
		Tags:          body.Tags,
		Priority:      body.Priority,
		AuthorID:      userID,
	}
	if err := h.db.WithContext(c.Request.Context()).Create(hq).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, hq)
}

// UpdateHotQuestion PUT /admin/support/hot-questions/:id
func (h *SupportAdminHandler) UpdateHotQuestion(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, "invalid id")
		return
	}
	var body hotQuestionRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	userID := c.GetUint("userId")
	uid := userID
	updates := map[string]any{
		"title":          strings.TrimSpace(body.Title),
		"question_body":  body.QuestionBody,
		"curated_answer": body.CuratedAnswer,
		"category":       body.Category,
		"tags":           body.Tags,
		"priority":       body.Priority,
		"last_edited_by": &uid,
	}
	if err := h.db.WithContext(c.Request.Context()).Model(&model.HotQuestion{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}

	// 若已发布，触发重建
	var hq model.HotQuestion
	if err := h.db.First(&hq, id).Error; err == nil && hq.IsPublished {
		h.triggerRebuild("hot_question", uint(id))
	}
	response.Success(c, gin.H{"ok": true})
}

// PublishHotQuestion POST /admin/support/hot-questions/:id/publish
func (h *SupportAdminHandler) PublishHotQuestion(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, "invalid id")
		return
	}
	if err := h.db.WithContext(c.Request.Context()).Model(&model.HotQuestion{}).Where("id = ?", id).Update("is_published", true).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	h.triggerRebuild("hot_question", uint(id))
	response.Success(c, gin.H{"ok": true})
}

// UnpublishHotQuestion POST /admin/support/hot-questions/:id/unpublish
func (h *SupportAdminHandler) UnpublishHotQuestion(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, "invalid id")
		return
	}
	if err := h.db.WithContext(c.Request.Context()).Model(&model.HotQuestion{}).Where("id = ?", id).Update("is_published", false).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	h.triggerRebuild("hot_question", uint(id)) // rebuild 会自动清理已下架的
	response.Success(c, gin.H{"ok": true})
}

// DeleteHotQuestion DELETE /admin/support/hot-questions/:id
func (h *SupportAdminHandler) DeleteHotQuestion(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, "invalid id")
		return
	}
	if err := h.db.WithContext(c.Request.Context()).Delete(&model.HotQuestion{}, id).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	// 清理关联的 knowledge_chunks
	h.db.Where("source_type = ? AND source_id = ?", "hot_question", id).Delete(&model.KnowledgeChunk{})
	response.Success(c, gin.H{"ok": true})
}

// ================= 供应商文档引用 =================

type providerDocRequest struct {
	SupplierID   uint   `json:"supplier_id" binding:"required"`
	SupplierCode string `json:"supplier_code"`
	DocType      string `json:"doc_type"`
	Title        string `json:"title" binding:"required"`
	URL          string `json:"url" binding:"required"`
	Description  string `json:"description"`
	Keywords     string `json:"keywords"`
	Locale       string `json:"locale"`
	Priority     int    `json:"priority"`
	IsActive     *bool  `json:"is_active"`
}

// ListProviderDocs GET /admin/support/provider-docs
func (h *SupportAdminHandler) ListProviderDocs(c *gin.Context) {
	list, err := h.services.ProviderSvc.List(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, list)
}

// CreateProviderDoc POST /admin/support/provider-docs
func (h *SupportAdminHandler) CreateProviderDoc(c *gin.Context) {
	var body providerDocRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	active := true
	if body.IsActive != nil {
		active = *body.IsActive
	}
	if body.Locale == "" {
		body.Locale = "zh"
	}
	r := &model.ProviderDocReference{
		SupplierID:   body.SupplierID,
		SupplierCode: body.SupplierCode,
		DocType:      body.DocType,
		Title:        body.Title,
		URL:          body.URL,
		Description:  body.Description,
		Keywords:     body.Keywords,
		Locale:       body.Locale,
		Priority:     body.Priority,
		IsActive:     active,
	}
	if err := h.services.ProviderSvc.Create(c.Request.Context(), r); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, r)
}

// UpdateProviderDoc PUT /admin/support/provider-docs/:id
func (h *SupportAdminHandler) UpdateProviderDoc(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, "invalid id")
		return
	}
	var body providerDocRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	updates := map[string]any{
		"supplier_id":   body.SupplierID,
		"supplier_code": body.SupplierCode,
		"doc_type":      body.DocType,
		"title":         body.Title,
		"url":           body.URL,
		"description":   body.Description,
		"keywords":      body.Keywords,
		"locale":        body.Locale,
		"priority":      body.Priority,
	}
	if body.IsActive != nil {
		updates["is_active"] = *body.IsActive
	}
	if err := h.db.WithContext(c.Request.Context()).Model(&model.ProviderDocReference{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"ok": true})
}

// DeleteProviderDoc DELETE /admin/support/provider-docs/:id
func (h *SupportAdminHandler) DeleteProviderDoc(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, "invalid id")
		return
	}
	if err := h.services.ProviderSvc.Delete(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"ok": true})
}

// ================= 工单 =================

// ListTickets GET /admin/support/tickets?status=pending&category=&page=1&page_size=20
func (h *SupportAdminHandler) ListTickets(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page <= 0 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	q := h.db.WithContext(c.Request.Context()).Model(&model.SupportTicket{})
	if s := c.Query("status"); s != "" {
		q = q.Where("status = ?", s)
	}
	if cat := c.Query("category"); cat != "" {
		q = q.Where("category = ?", cat)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("title LIKE ? OR ticket_no LIKE ?", like, like)
	}
	var total int64
	q.Count(&total)
	var list []model.SupportTicket
	if err := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}

// GetTicket GET /admin/support/tickets/:id
func (h *SupportAdminHandler) GetTicket(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, "invalid id")
		return
	}
	var ticket model.SupportTicket
	if err := h.db.First(&ticket, id).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, 40400, "ticket not found")
		return
	}
	var replies []model.SupportTicketReply
	h.db.Where("ticket_id = ?", id).Order("id ASC").Find(&replies)

	// 自动标记已读
	if ticket.ReadByAdminAt == nil {
		now := time.Now()
		h.db.Model(&model.SupportTicket{}).Where("id = ?", id).Update("read_by_admin_at", &now)
	}
	response.Success(c, gin.H{"ticket": ticket, "replies": replies})
}

// ReplyTicket POST /admin/support/tickets/:id/reply
func (h *SupportAdminHandler) ReplyTicket(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, "invalid id")
		return
	}
	var body struct {
		Content    string `json:"content" binding:"required"`
		IsInternal bool   `json:"is_internal"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	adminID := c.GetUint("userId")
	reply := &model.SupportTicketReply{
		TicketID:   uint(id),
		AuthorID:   adminID,
		AuthorType: "admin",
		Content:    body.Content,
		IsInternal: body.IsInternal,
	}
	if err := h.db.WithContext(c.Request.Context()).Create(reply).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	// 非内部回复 → 状态改为 replied + unread_by_user=true
	if !body.IsInternal {
		h.db.Model(&model.SupportTicket{}).Where("id = ?", id).Updates(map[string]any{
			"status":         "replied",
			"unread_by_user": true,
		})
	}
	response.Success(c, reply)
}

// UpdateTicketStatus PATCH /admin/support/tickets/:id/status  body: {"status":"closed"}
func (h *SupportAdminHandler) UpdateTicketStatus(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, "invalid id")
		return
	}
	var body struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	allowed := map[string]struct{}{"pending": {}, "assigned": {}, "replied": {}, "awaiting_user": {}, "resolved": {}, "closed": {}, "reopened": {}}
	if _, ok := allowed[body.Status]; !ok {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "invalid status")
		return
	}
	updates := map[string]any{"status": body.Status}
	if body.Status == "resolved" || body.Status == "closed" {
		now := time.Now()
		updates["resolved_at"] = &now
	}
	if err := h.db.WithContext(c.Request.Context()).Model(&model.SupportTicket{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"ok": true})
}

// AssignTicket PATCH /admin/support/tickets/:id/assign  body: {"assignee_id": 1}
func (h *SupportAdminHandler) AssignTicket(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, "invalid id")
		return
	}
	var body struct {
		AssigneeID uint `json:"assignee_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	aid := body.AssigneeID
	if err := h.db.WithContext(c.Request.Context()).Model(&model.SupportTicket{}).Where("id = ?", id).Updates(map[string]any{
		"assignee_id": &aid,
		"status":      "assigned",
	}).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"ok": true})
}

// ================= 采纳问答审核 =================

// ListAcceptedAnswers GET /admin/support/accepted-answers?status=pending_review&page=1&page_size=20
func (h *SupportAdminHandler) ListAcceptedAnswers(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page <= 0 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	status := c.DefaultQuery("status", "pending_review")
	q := h.db.WithContext(c.Request.Context()).Model(&model.AcceptedAnswer{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	q.Count(&total)
	var list []model.AcceptedAnswer
	if err := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}

// ApproveAcceptedAnswer POST /admin/support/accepted-answers/:id/approve
//
// body（可选）: {"question":"脱敏后版本","answer":"脱敏后版本"} - 覆盖原文
func (h *SupportAdminHandler) ApproveAcceptedAnswer(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, "invalid id")
		return
	}
	var body struct {
		Question string `json:"question"`
		Answer   string `json:"answer"`
	}
	_ = c.ShouldBindJSON(&body)

	adminID := c.GetUint("userId")
	updates := map[string]any{
		"status":       "approved",
		"reviewer_id":  &adminID,
		"reviewed_at":  time.Now(),
	}
	if body.Question != "" {
		updates["question"] = body.Question
	}
	if body.Answer != "" {
		updates["answer"] = body.Answer
	}
	if err := h.db.WithContext(c.Request.Context()).Model(&model.AcceptedAnswer{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	// 触发重建（异步）
	h.triggerRebuild("accepted_qa", uint(id))
	response.Success(c, gin.H{"ok": true})
}

// RejectAcceptedAnswer POST /admin/support/accepted-answers/:id/reject
func (h *SupportAdminHandler) RejectAcceptedAnswer(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, "invalid id")
		return
	}
	var body struct {
		RejectReason string `json:"reject_reason"`
	}
	_ = c.ShouldBindJSON(&body)
	adminID := c.GetUint("userId")
	if err := h.db.WithContext(c.Request.Context()).Model(&model.AcceptedAnswer{}).Where("id = ?", id).Updates(map[string]any{
		"status":        "rejected",
		"reviewer_id":   &adminID,
		"reviewed_at":   time.Now(),
		"reject_reason": body.RejectReason,
	}).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, gin.H{"ok": true})
}

// ================= 知识库重建 =================

// RebuildKnowledge POST /admin/support/knowledge/rebuild
func (h *SupportAdminHandler) RebuildKnowledge(c *gin.Context) {
	if h.services == nil || h.services.Rebuilder == nil {
		response.ErrorMsg(c, http.StatusServiceUnavailable, 50301, "rebuilder not available (internal API key missing?)")
		return
	}
	stats, err := h.services.Rebuilder.RebuildFull(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	// 重建后失效检索器缓存
	if h.services.Retriever != nil {
		h.services.Retriever.InvalidateCache()
	}
	response.Success(c, stats)
}

// RebuildKnowledgeSource POST /admin/support/knowledge/rebuild/:source_type/:id
func (h *SupportAdminHandler) RebuildKnowledgeSource(c *gin.Context) {
	if h.services == nil || h.services.Rebuilder == nil {
		response.ErrorMsg(c, http.StatusServiceUnavailable, 50301, "rebuilder not available")
		return
	}
	sourceType := c.Param("source_type")
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40000, "invalid id")
		return
	}
	stats, err := h.services.Rebuilder.RebuildSource(c.Request.Context(), sourceType, uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	if h.services.Retriever != nil {
		h.services.Retriever.InvalidateCache()
	}
	response.Success(c, stats)
}

// KnowledgeStats GET /admin/support/knowledge/stats
func (h *SupportAdminHandler) KnowledgeStats(c *gin.Context) {
	type row struct {
		SourceType string `json:"source_type"`
		Count      int64  `json:"count"`
	}
	var rows []row
	h.db.WithContext(c.Request.Context()).Model(&model.KnowledgeChunk{}).
		Select("source_type, COUNT(*) AS count").
		Where("is_active = ?", true).
		Group("source_type").
		Scan(&rows)
	var total int64
	h.db.Model(&model.KnowledgeChunk{}).Where("is_active = ?", true).Count(&total)
	response.Success(c, gin.H{"by_source": rows, "total": total})
}

// triggerRebuild 后台异步重建单源（不阻塞请求）
// rebuilder 为 nil 时静默跳过
func (h *SupportAdminHandler) triggerRebuild(sourceType string, sourceID uint) {
	if h.services == nil || h.services.Rebuilder == nil {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		ctx := context.Background()
		_, _ = h.services.Rebuilder.RebuildSource(ctx, sourceType, sourceID)
		if h.services.Retriever != nil {
			h.services.Retriever.InvalidateCache()
		}
	}()
}
