package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/ctxutil"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	emailsvc "tokenhub-server/internal/service/email"
)

// EmailHandler 邮件管理后台 handler
type EmailHandler struct {
	cfgSvc   *emailsvc.ConfigService
	tplSvc   *emailsvc.TemplateService
	mailSvc  *emailsvc.EmailService
}

// NewEmailHandler 构造
func NewEmailHandler(cfg *emailsvc.ConfigService, tpl *emailsvc.TemplateService, mail *emailsvc.EmailService) *EmailHandler {
	return &EmailHandler{cfgSvc: cfg, tplSvc: tpl, mailSvc: mail}
}

// Register 注册路由到 adminGroup
func (h *EmailHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/email")
	{
		// 供应商配置
		g.GET("/providers", h.ListProviders)
		g.PUT("/providers/:channel", h.UpsertProvider)
		g.POST("/providers/:channel/test", h.TestProvider)

		// 模板
		g.GET("/templates", h.ListTemplates)
		g.GET("/templates/:id", h.GetTemplate)
		g.POST("/templates", h.CreateTemplate)
		g.PUT("/templates/:id", h.UpdateTemplate)
		g.DELETE("/templates/:id", h.DeleteTemplate)
		g.POST("/templates/:id/preview", h.PreviewTemplate)
		g.POST("/templates/:id/test-send", h.TestSendTemplate)

		// 发送
		g.POST("/send", h.SendEmail)
		g.POST("/send-batch", h.SendBatch)

		// 日志
		g.GET("/logs", h.ListLogs)
		g.POST("/logs/:id/resend", h.ResendLog)
	}
}

// ============ 供应商配置 ============

type providerDTO struct {
	ID         uint   `json:"id"`
	Channel    string `json:"channel"`
	APIUser    string `json:"api_user"`
	HasAPIKey  bool   `json:"has_api_key"`
	FromEmail  string `json:"from_email"`
	FromName   string `json:"from_name"`
	ReplyTo    string `json:"reply_to"`
	Domain     string `json:"domain"`
	IsActive   bool   `json:"is_active"`
	DailyLimit int    `json:"daily_limit"`
	UpdatedAt  string `json:"updated_at"`
}

func toProviderDTO(c *model.EmailProviderConfig) providerDTO {
	return providerDTO{
		ID:         c.ID,
		Channel:    c.Channel,
		APIUser:    c.APIUser,
		HasAPIKey:  c.APIKeyEncrypted != "",
		FromEmail:  c.FromEmail,
		FromName:   c.FromName,
		ReplyTo:    c.ReplyTo,
		Domain:     c.Domain,
		IsActive:   c.IsActive,
		DailyLimit: c.DailyLimit,
		UpdatedAt:  c.UpdatedAt.Format("2006-01-02 15:04:05"),
	}
}

// ListProviders GET /admin/email/providers
func (h *EmailHandler) ListProviders(c *gin.Context) {
	list, err := h.cfgSvc.List(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	// 确保两个 channel 都返回（未配置时给出空占位）
	existing := map[string]bool{}
	dtos := make([]providerDTO, 0, 2)
	for _, p := range list {
		dtos = append(dtos, toProviderDTO(&p))
		existing[p.Channel] = true
	}
	for _, ch := range []string{model.EmailChannelNotification, model.EmailChannelMarketing} {
		if !existing[ch] {
			dtos = append(dtos, providerDTO{Channel: ch, IsActive: false})
		}
	}
	response.Success(c, dtos)
}

type upsertProviderRequest struct {
	APIUser    string `json:"api_user"`
	APIKey     string `json:"api_key"` // 传 "" 或 "***" 时保留旧值
	FromEmail  string `json:"from_email"`
	FromName   string `json:"from_name"`
	ReplyTo    string `json:"reply_to"`
	Domain     string `json:"domain"`
	IsActive   *bool  `json:"is_active"`
	DailyLimit *int   `json:"daily_limit"`
}

// UpsertProvider PUT /admin/email/providers/:channel
func (h *EmailHandler) UpsertProvider(c *gin.Context) {
	channel := c.Param("channel")
	if channel != model.EmailChannelNotification && channel != model.EmailChannelMarketing {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "invalid channel")
		return
	}
	var req upsertProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	if req.FromEmail != "" && !isValidEmail(req.FromEmail) {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "invalid from_email")
		return
	}
	// 注：from_email 和 domain 的一致性校验由 SendCloud 上游执行；后端不做硬性校验，
	// 以允许使用主域邮箱（如 support@tokenhubhk.com）而 verified 域名是子域（如 info.tokenhubhk.com）的场景。
	_ = strings.HasSuffix // 保留导入

	cfg, err := h.cfgSvc.Upsert(c.Request.Context(), emailsvc.UpsertRequest{
		Channel:    channel,
		APIUser:    req.APIUser,
		APIKey:     req.APIKey,
		FromEmail:  req.FromEmail,
		FromName:   req.FromName,
		ReplyTo:    req.ReplyTo,
		Domain:     req.Domain,
		IsActive:   req.IsActive,
		DailyLimit: req.DailyLimit,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	response.Success(c, toProviderDTO(cfg))
}

type testProviderRequest struct {
	To string `json:"to"`
}

// TestProvider POST /admin/email/providers/:channel/test
func (h *EmailHandler) TestProvider(c *gin.Context) {
	channel := c.Param("channel")
	var req testProviderRequest
	_ = c.ShouldBindJSON(&req)
	to := req.To
	if to == "" || !isValidEmail(to) {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "valid recipient email required")
		return
	}
	operatorID, _ := ctxutil.UserID(c)

	result, err := h.mailSvc.SendByTemplate(c.Request.Context(), emailsvc.SendByTemplateRequest{
		Channel:          channel,
		To:               []string{to},
		OverrideSubject:  "【TokenHub】SendCloud 连通性测试",
		OverrideHTMLBody: `<p>这是一封来自 TokenHub 后台的测试邮件，用于验证 SendCloud 凭证配置是否正确。</p><p>通道：<code>` + channel + `</code></p><p>如果您收到这封邮件，说明配置正确。</p>`,
		OverrideTextBody: "这是一封 TokenHub 测试邮件。通道：" + channel,
		TriggeredBy:      "admin_test",
		OperatorID:       operatorID,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrInternal.Code, fmt.Sprintf("send failed: %s", err.Error()))
		return
	}
	response.Success(c, gin.H{
		"success":      result != nil && result.Success,
		"status_code":  result.StatusCode,
		"message":      result.Message,
		"message_id":   result.MessageID,
	})
}

// ============ 模板 CRUD ============

type templateDTO struct {
	model.EmailTemplate
	VariablesSchemaParsed []emailsvc.VariableDef `json:"variables_schema_parsed,omitempty"`
}

func toTemplateDTO(svc *emailsvc.TemplateService, t *model.EmailTemplate) templateDTO {
	parsed, _ := svc.ParseVariableSchema(t)
	return templateDTO{EmailTemplate: *t, VariablesSchemaParsed: parsed}
}

// ListTemplates GET /admin/email/templates
func (h *EmailHandler) ListTemplates(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	category := c.Query("category")
	channel := c.Query("channel")
	keyword := c.Query("keyword")

	list, total, err := h.tplSvc.List(c.Request.Context(), category, channel, keyword, page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	dtos := make([]templateDTO, 0, len(list))
	for i := range list {
		dtos = append(dtos, toTemplateDTO(h.tplSvc, &list[i]))
	}
	response.PageResult(c, dtos, total, page, pageSize)
}

// GetTemplate GET /admin/email/templates/:id
func (h *EmailHandler) GetTemplate(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	t, err := h.tplSvc.Get(c.Request.Context(), uint(id))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
			return
		}
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, toTemplateDTO(h.tplSvc, t))
}

type templateUpsertRequest struct {
	Code            string                  `json:"code"`
	Name            string                  `json:"name"`
	Category        string                  `json:"category"`
	Channel         string                  `json:"channel"`
	Subject         string                  `json:"subject"`
	HTMLBody        string                  `json:"html_body"`
	TextBody        string                  `json:"text_body"`
	VariablesSchema []emailsvc.VariableDef  `json:"variables_schema"`
	IsActive        *bool                   `json:"is_active"`
	Remark          string                  `json:"remark"`
}

// CreateTemplate POST /admin/email/templates
func (h *EmailHandler) CreateTemplate(c *gin.Context) {
	var req templateUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	operatorID, _ := ctxutil.UserID(c)
	t, err := h.tplSvc.Create(c.Request.Context(), emailsvc.CreateRequest{
		Code:            req.Code,
		Name:            req.Name,
		Category:        req.Category,
		Channel:         req.Channel,
		Subject:         req.Subject,
		HTMLBody:        req.HTMLBody,
		TextBody:        req.TextBody,
		VariablesSchema: req.VariablesSchema,
		OperatorID:      operatorID,
		Remark:          req.Remark,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	response.Success(c, toTemplateDTO(h.tplSvc, t))
}

// UpdateTemplate PUT /admin/email/templates/:id
func (h *EmailHandler) UpdateTemplate(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req templateUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	operatorID, _ := ctxutil.UserID(c)

	update := emailsvc.UpdateRequest{OperatorID: operatorID}
	if req.Name != "" {
		update.Name = &req.Name
	}
	if req.Category != "" {
		update.Category = &req.Category
	}
	if req.Channel != "" {
		update.Channel = &req.Channel
	}
	if req.Subject != "" {
		update.Subject = &req.Subject
	}
	update.HTMLBody = strPtrOrNil(req.HTMLBody)
	update.TextBody = strPtrOrNil(req.TextBody)
	if req.VariablesSchema != nil {
		update.VariablesSchema = &req.VariablesSchema
	}
	if req.IsActive != nil {
		update.IsActive = req.IsActive
	}
	update.Remark = strPtrOrNil(req.Remark)

	t, err := h.tplSvc.Update(c.Request.Context(), uint(id), update)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	response.Success(c, toTemplateDTO(h.tplSvc, t))
}

// DeleteTemplate DELETE /admin/email/templates/:id
func (h *EmailHandler) DeleteTemplate(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.tplSvc.Delete(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

type previewRequest struct {
	Variables map[string]any `json:"variables"`
}

// PreviewTemplate POST /admin/email/templates/:id/preview
func (h *EmailHandler) PreviewTemplate(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req previewRequest
	_ = c.ShouldBindJSON(&req)
	t, err := h.tplSvc.Get(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, err.Error())
		return
	}
	rendered, err := h.tplSvc.Render(t, req.Variables)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	response.Success(c, gin.H{
		"subject":   rendered.Subject,
		"html_body": rendered.HTMLBody,
		"text_body": rendered.TextBody,
	})
}

type testSendRequest struct {
	To        string         `json:"to"`
	Variables map[string]any `json:"variables"`
}

// TestSendTemplate POST /admin/email/templates/:id/test-send
func (h *EmailHandler) TestSendTemplate(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req testSendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	if !isValidEmail(req.To) {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "invalid recipient")
		return
	}
	t, err := h.tplSvc.Get(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, err.Error())
		return
	}
	operatorID, _ := ctxutil.UserID(c)

	result, err := h.mailSvc.SendByTemplate(c.Request.Context(), emailsvc.SendByTemplateRequest{
		TemplateCode: t.Code,
		To:           []string{req.To},
		Variables:    req.Variables,
		TriggeredBy:  "admin_test",
		OperatorID:   operatorID,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{
		"success":    result != nil && result.Success,
		"message_id": result.MessageID,
		"message":    result.Message,
	})
}

// ============ 发送 ============

type sendRequest struct {
	TemplateCode     string                       `json:"template_code"`
	Channel          string                       `json:"channel"`
	To               []string                     `json:"to"`
	Variables        map[string]any               `json:"variables"`
	PerRecipient     map[string]map[string]any    `json:"per_recipient"`
	Subject          string                       `json:"subject"`
	HTMLBody         string                       `json:"html_body"`
	TextBody         string                       `json:"text_body"`
}

// SendEmail POST /admin/email/send — 单发或小批量（≤100）
func (h *EmailHandler) SendEmail(c *gin.Context) {
	var req sendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	// 收件人校验
	cleanTo := make([]string, 0, len(req.To))
	for _, t := range req.To {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !isValidEmail(t) {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "invalid recipient: "+t)
			return
		}
		cleanTo = append(cleanTo, t)
	}
	if len(cleanTo) == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "no recipients")
		return
	}
	if len(cleanTo) > 100 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "single send limited to 100 recipients (use send-batch)")
		return
	}
	if req.TemplateCode == "" && req.Subject == "" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "either template_code or subject required")
		return
	}
	operatorID, _ := ctxutil.UserID(c)

	result, err := h.mailSvc.SendByTemplate(c.Request.Context(), emailsvc.SendByTemplateRequest{
		TemplateCode:     req.TemplateCode,
		Channel:          req.Channel,
		To:               cleanTo,
		Variables:        req.Variables,
		PerRecipient:     req.PerRecipient,
		OverrideSubject:  req.Subject,
		OverrideHTMLBody: req.HTMLBody,
		OverrideTextBody: req.TextBody,
		TriggeredBy:      "admin",
		OperatorID:       operatorID,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{
		"success":      result != nil && result.Success,
		"recipients":   len(cleanTo),
		"message":      result.Message,
		"status_code":  result.StatusCode,
	})
}

// SendBatch POST /admin/email/send-batch — 大批量（自动切 100/批顺序发送）
func (h *EmailHandler) SendBatch(c *gin.Context) {
	var req sendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	cleanTo := make([]string, 0, len(req.To))
	seen := map[string]bool{}
	for _, t := range req.To {
		t = strings.TrimSpace(strings.ToLower(t))
		if t == "" || seen[t] {
			continue
		}
		if !isValidEmail(t) {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "invalid recipient: "+t)
			return
		}
		seen[t] = true
		cleanTo = append(cleanTo, t)
	}
	if len(cleanTo) == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "no recipients")
		return
	}
	if len(cleanTo) > 10000 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "batch exceeds 10000 recipients")
		return
	}
	operatorID, _ := ctxutil.UserID(c)

	// 分片为 100/批，每批同步发送（失败不中断）
	const chunkSize = 100
	totalSent := 0
	totalFailed := 0
	for i := 0; i < len(cleanTo); i += chunkSize {
		end := i + chunkSize
		if end > len(cleanTo) {
			end = len(cleanTo)
		}
		chunk := cleanTo[i:end]
		_, err := h.mailSvc.SendByTemplate(c.Request.Context(), emailsvc.SendByTemplateRequest{
			TemplateCode:     req.TemplateCode,
			Channel:          req.Channel,
			To:               chunk,
			Variables:        req.Variables,
			PerRecipient:     req.PerRecipient,
			OverrideSubject:  req.Subject,
			OverrideHTMLBody: req.HTMLBody,
			OverrideTextBody: req.TextBody,
			TriggeredBy:      "admin_batch",
			OperatorID:       operatorID,
		})
		if err != nil {
			totalFailed += len(chunk)
		} else {
			totalSent += len(chunk)
		}
	}
	response.Success(c, gin.H{
		"total":       len(cleanTo),
		"sent":        totalSent,
		"failed":      totalFailed,
	})
}

// ============ 日志 ============

// ListLogs GET /admin/email/logs
func (h *EmailHandler) ListLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	channel := c.Query("channel")
	status := c.Query("status")
	templateCode := c.Query("template_code")
	keyword := c.Query("keyword")

	list, total, err := h.mailSvc.ListLogs(c.Request.Context(), channel, status, templateCode, keyword, page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}

// ResendLog POST /admin/email/logs/:id/resend — 重发失败记录
func (h *EmailHandler) ResendLog(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	log, err := h.mailSvc.GetLog(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, err.Error())
		return
	}
	// 解码原始变量
	var vars map[string]any
	if len(log.Variables) > 0 {
		_ = json.Unmarshal(log.Variables, &vars)
	}
	operatorID, _ := ctxutil.UserID(c)

	result, err := h.mailSvc.SendByTemplate(c.Request.Context(), emailsvc.SendByTemplateRequest{
		TemplateCode: log.TemplateCode,
		Channel:      log.Channel,
		To:           []string{log.ToEmail},
		Variables:    vars,
		TriggeredBy:  "admin_resend",
		OperatorID:   operatorID,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{
		"success":      result != nil && result.Success,
		"message":      result.Message,
	})
}

// ============ helpers ============

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

func isValidEmail(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) > 0 && len(s) <= 254 && emailRegex.MatchString(s)
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
