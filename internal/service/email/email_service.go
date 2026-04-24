package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/dbctx"
	"tokenhub-server/internal/pkg/logger"
)

// SendByTemplateRequest 门面请求
type SendByTemplateRequest struct {
	TemplateCode string
	Channel      string // 可选,空则按模板 Channel 字段;模板为 auto 时按 Category 决定
	To           []string
	Variables    map[string]any
	PerRecipient map[string]map[string]any // 按邮箱覆盖变量(可选)
	Attachments  []Attachment
	TriggeredBy  string
	OperatorID   uint
	// 覆盖：直接传 subject/html/text（若 TemplateCode 为空时可用）
	OverrideSubject  string
	OverrideHTMLBody string
	OverrideTextBody string
	// BatchID 批次号（可选，不传自动生成）
	BatchID string

	// Language 用户语言偏好（en / zh / zh_TW / zh-TW 等），用于按 code_{lang} 选择模板
	// 空字符串 → 默认 "en"
	Language string
}

// EmailService 门面
type EmailService struct {
	db       *gorm.DB
	sender   *Sender
	config   *ConfigService
	template *TemplateService
}

// NewEmailService 构造
func NewEmailService(db *gorm.DB, cfg *ConfigService, tpl *TemplateService, sender *Sender) *EmailService {
	return &EmailService{db: db, config: cfg, template: tpl, sender: sender}
}

// Default 全局单例，由 router.Setup / SetupBackend 初始化；业务模块（auth/invoice）
// 通过 email.Default 调用 SendByTemplate，Default 为 nil 时表示邮件服务未启用，
// 业务代码需优雅降级（记日志但不阻塞主流程）。
var Default *EmailService

// InitDefault 初始化全局 EmailService 单例
func InitDefault(db *gorm.DB) *EmailService {
	cfg := NewConfigService(db)
	tpl := NewTemplateService(db)
	sender := NewSender(cfg)
	Default = NewEmailService(db, cfg, tpl, sender)
	return Default
}

// Config 返回底层 ConfigService
func (s *EmailService) Config() *ConfigService { return s.config }

// Template 返回底层 TemplateService
func (s *EmailService) Template() *TemplateService { return s.template }

// ResolveChannel 根据模板 channel/category 自动选择 channel
func ResolveChannel(t *model.EmailTemplate, override string) string {
	if override != "" && override != model.EmailChannelAuto {
		return override
	}
	if t != nil {
		if t.Channel != "" && t.Channel != model.EmailChannelAuto {
			return t.Channel
		}
		if t.Category == model.EmailCategoryMarketing {
			return model.EmailChannelMarketing
		}
	}
	return model.EmailChannelNotification
}

// SendByTemplate 核心发送入口
//
// 行为：
//   - 解析模板 + 渲染（per-recipient 变量合并到全局变量之上）
//   - 调用 Sender.Send
//   - 写 email_send_logs
//
// 注意：To 超过 100 的调用方应提前分片。此方法不拆包。
func (s *EmailService) SendByTemplate(ctx context.Context, req SendByTemplateRequest) (*SendResult, error) {
	if len(req.To) == 0 {
		return nil, errors.New("no recipients")
	}
	if len(req.To) > 100 {
		return nil, errors.New("too many recipients (max 100 per call)")
	}

	var tpl *model.EmailTemplate
	subject := req.OverrideSubject
	htmlBody := req.OverrideHTMLBody
	textBody := req.OverrideTextBody

	if req.TemplateCode != "" {
		t, err := s.template.GetByCodeWithLang(ctx, req.TemplateCode, req.Language)
		if err != nil {
			return nil, fmt.Errorf("load template %q (lang=%s): %w", req.TemplateCode, req.Language, err)
		}
		tpl = t
	}

	channel := ResolveChannel(tpl, req.Channel)

	// 分两种场景：单收件人 vs 多收件人
	// 单：直接渲染一次发一封
	// 多：逐个渲染（因为 per-recipient 变量可能不同）后，若变量完全一致则合并一次 send 走 multi-to，否则循环多次 send
	batchID := req.BatchID
	if batchID == "" {
		batchID = uuid.NewString()
	}

	// 准备日志基础字段
	triggeredBy := req.TriggeredBy
	if triggeredBy == "" {
		triggeredBy = "system"
	}

	// 单收件人路径（最常见）
	if len(req.To) == 1 {
		to := req.To[0]
		vars := mergeVars(req.Variables, req.PerRecipient[to])
		if tpl != nil {
			rendered, err := s.template.Render(tpl, vars)
			if err != nil {
				s.writeFailedLog(ctx, batchID, channel, req.TemplateCode, to, subject, vars, "RENDER", err.Error(), triggeredBy, req.OperatorID)
				return nil, err
			}
			subject = rendered.Subject
			htmlBody = rendered.HTMLBody
			textBody = rendered.TextBody
		}
		return s.sendOneAndLog(ctx, batchID, channel, tpl, to, subject, htmlBody, textBody, vars, req.Attachments, triggeredBy, req.OperatorID)
	}

	// 多收件人：判断变量是否可合并
	// 若所有 PerRecipient 均为空（或相同）→ 一次批量 send（multi-to）
	// 否则：逐个发送（保证 per-recipient 定制）
	if len(req.PerRecipient) == 0 {
		vars := req.Variables
		if tpl != nil {
			rendered, err := s.template.Render(tpl, vars)
			if err != nil {
				return nil, err
			}
			subject = rendered.Subject
			htmlBody = rendered.HTMLBody
			textBody = rendered.TextBody
		}
		payload := SendPayload{
			To:          req.To,
			Subject:     subject,
			HTML:        htmlBody,
			Text:        textBody,
			Attachments: req.Attachments,
		}
		result, err := s.sender.Send(ctx, channel, payload)
		// 为每个收件人写一条日志
		for _, to := range req.To {
			s.writeLogFromResult(ctx, batchID, channel, req.TemplateCode, to, subject, vars, result, err, triggeredBy, req.OperatorID)
		}
		if err != nil {
			return result, err
		}
		return result, nil
	}

	// 逐个发送
	var lastResult *SendResult
	var lastErr error
	for _, to := range req.To {
		vars := mergeVars(req.Variables, req.PerRecipient[to])
		sub, html, text := subject, htmlBody, textBody
		if tpl != nil {
			rendered, err := s.template.Render(tpl, vars)
			if err != nil {
				s.writeFailedLog(ctx, batchID, channel, req.TemplateCode, to, subject, vars, "RENDER", err.Error(), triggeredBy, req.OperatorID)
				lastErr = err
				continue
			}
			sub = rendered.Subject
			html = rendered.HTMLBody
			text = rendered.TextBody
		}
		r, err := s.sendOneAndLog(ctx, batchID, channel, tpl, to, sub, html, text, vars, req.Attachments, triggeredBy, req.OperatorID)
		if err != nil {
			lastErr = err
		}
		lastResult = r
	}
	return lastResult, lastErr
}

// sendOneAndLog 发送单封 + 落日志
func (s *EmailService) sendOneAndLog(
	ctx context.Context,
	batchID, channel string,
	tpl *model.EmailTemplate,
	to, subject, html, text string,
	vars map[string]any,
	attachments []Attachment,
	triggeredBy string,
	operatorID uint,
) (*SendResult, error) {
	payload := SendPayload{
		To:          []string{to},
		Subject:     subject,
		HTML:        html,
		Text:        text,
		Attachments: attachments,
	}
	result, err := s.sender.Send(ctx, channel, payload)
	tplCode := ""
	if tpl != nil {
		tplCode = tpl.Code
	}
	s.writeLogFromResult(ctx, batchID, channel, tplCode, to, subject, vars, result, err, triggeredBy, operatorID)
	return result, err
}

// writeLogFromResult 根据 result/err 写日志
func (s *EmailService) writeLogFromResult(
	ctx context.Context,
	batchID, channel, templateCode, to, subject string,
	vars map[string]any,
	result *SendResult,
	err error,
	triggeredBy string,
	operatorID uint,
) {
	varsJSON, _ := json.Marshal(sanitizeVars(vars))
	now := time.Now()
	logRow := &model.EmailSendLog{
		BatchID:      batchID,
		Channel:      channel,
		TemplateCode: templateCode,
		ToEmail:      to,
		Subject:      subject,
		Variables:    varsJSON,
		TriggeredBy:  triggeredBy,
		OperatorID:   operatorID,
		AttemptCount: 1,
	}
	if result != nil {
		logRow.ProviderMessageID = result.MessageID
		if result.Success {
			logRow.Status = model.EmailStatusSent
			logRow.SentAt = &now
		} else {
			logRow.Status = model.EmailStatusFailed
			logRow.ErrorCode = fmt.Sprintf("%d", result.StatusCode)
			logRow.ErrorMessage = truncate(result.Message, 1000)
		}
	}
	if err != nil {
		logRow.Status = model.EmailStatusFailed
		logRow.ErrorMessage = truncate(err.Error(), 1000)
		if result != nil && logRow.ErrorCode == "" {
			logRow.ErrorCode = fmt.Sprintf("%d", result.StatusCode)
		}
	}
	s.persistLog(ctx, logRow)
}

// writeFailedLog 渲染或前置校验失败时落日志
func (s *EmailService) writeFailedLog(
	ctx context.Context,
	batchID, channel, templateCode, to, subject string,
	vars map[string]any,
	errCode, errMsg, triggeredBy string,
	operatorID uint,
) {
	varsJSON, _ := json.Marshal(sanitizeVars(vars))
	logRow := &model.EmailSendLog{
		BatchID:      batchID,
		Channel:      channel,
		TemplateCode: templateCode,
		ToEmail:      to,
		Subject:      subject,
		Variables:    varsJSON,
		Status:       model.EmailStatusFailed,
		ErrorCode:    errCode,
		ErrorMessage: truncate(errMsg, 1000),
		TriggeredBy:  triggeredBy,
		OperatorID:   operatorID,
		AttemptCount: 1,
	}
	s.persistLog(ctx, logRow)
}

// persistLog 写入 DB（失败仅 warn，不阻塞发送流程）
func (s *EmailService) persistLog(ctx context.Context, row *model.EmailSendLog) {
	cctx, cancel := dbctx.Short(ctx)
	defer cancel()
	if err := s.db.WithContext(cctx).Create(row).Error; err != nil {
		logger.L.Warn("write email send log failed", zap.Error(err), zap.String("to", row.ToEmail))
	}
}

// ListLogs 查询发送记录（分页）
func (s *EmailService) ListLogs(ctx context.Context, channel, status, templateCode, keyword string, page, pageSize int) ([]model.EmailSendLog, int64, error) {
	cctx, cancel := dbctx.Medium(ctx)
	defer cancel()
	tx := s.db.WithContext(cctx).Model(&model.EmailSendLog{})
	if channel != "" {
		tx = tx.Where("channel = ?", channel)
	}
	if status != "" {
		tx = tx.Where("status = ?", status)
	}
	if templateCode != "" {
		tx = tx.Where("template_code = ?", templateCode)
	}
	if keyword != "" {
		tx = tx.Where("to_email LIKE ? OR subject LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 500 {
		pageSize = 50
	}
	var list []model.EmailSendLog
	if err := tx.Order("id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// GetLog 取单条日志
func (s *EmailService) GetLog(ctx context.Context, id uint) (*model.EmailSendLog, error) {
	cctx, cancel := dbctx.Short(ctx)
	defer cancel()
	var log model.EmailSendLog
	if err := s.db.WithContext(cctx).First(&log, id).Error; err != nil {
		return nil, err
	}
	return &log, nil
}

// CleanupLogsBefore 删除指定时间前的日志
func (s *EmailService) CleanupLogsBefore(ctx context.Context, before time.Time) (int64, error) {
	cctx, cancel := dbctx.Long(ctx)
	defer cancel()
	r := s.db.WithContext(cctx).Where("created_at < ?", before).Delete(&model.EmailSendLog{})
	return r.RowsAffected, r.Error
}

// mergeVars 合并全局变量 + per-recipient 变量（后者优先）
func mergeVars(global, override map[string]any) map[string]any {
	out := make(map[string]any, len(global)+len(override))
	for k, v := range global {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

// sanitizeVars 脱敏敏感变量（password / code / secret / token）
func sanitizeVars(vars map[string]any) map[string]any {
	if len(vars) == 0 {
		return vars
	}
	sanitized := make(map[string]any, len(vars))
	for k, v := range vars {
		lk := lowercase(k)
		if contains(lk, "password") || contains(lk, "secret") || contains(lk, "token") || contains(lk, "api_key") {
			sanitized[k] = "***"
			continue
		}
		// code / otp 字段保留前后各一位
		if contains(lk, "code") || contains(lk, "otp") {
			if s, ok := v.(string); ok && len(s) > 2 {
				sanitized[k] = s[:1] + "***" + s[len(s)-1:]
				continue
			}
		}
		sanitized[k] = v
	}
	return sanitized
}

func lowercase(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
