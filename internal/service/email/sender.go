package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SendCloud API 端点
const (
	sendCloudSendURL = "https://api.sendcloud.net/apiv2/mail/send"
)

// 单附件大小硬上限：SendCloud 文档约 10MB
const maxAttachmentBytes = 10 * 1024 * 1024

// Attachment 邮件附件
type Attachment struct {
	Filename    string
	Content     []byte
	ContentType string // 可为空，SendCloud 会自行推断
}

// SendPayload 单次请求内容
type SendPayload struct {
	// 单发或批量收件人（≤100）
	To []string
	// 抄送（可选）
	Cc []string
	// 密送（可选）
	Bcc []string
	// 渲染后主题
	Subject string
	// HTML 正文（推荐）
	HTML string
	// 纯文本正文（可选）
	Text string
	// 批量 per-recipient 替换（设置后，form 的 to/cc/bcc 全部失效）
	// 参考 https://www.sendcloud.net/doc/email_v2/send_email/ 的 xsmtpapi
	// 形如 {"to":["a","b"],"sub":{"%name%":["Alice","Bob"]}}
	XSmtpAPI string
	// 附件
	Attachments []Attachment
	// 回信地址（覆盖 channel 默认）
	ReplyTo string
}

// SendResult 发送结果
type SendResult struct {
	Success   bool
	MessageID string // SendCloud 返回的 message id（若有）
	RawResp   string
	// SendCloud statusCode（非 HTTP 状态码，20x 表示成功）
	StatusCode int
	Message    string
}

// sendCloudResponse SendCloud 返回格式
type sendCloudResponse struct {
	Result     bool            `json:"result"`
	StatusCode int             `json:"statusCode"`
	Message    string          `json:"message"`
	Info       json.RawMessage `json:"info,omitempty"`
}

// sendCloudInfo info 内部字段（含 emailIdList）
type sendCloudInfo struct {
	EmailIDList []string `json:"emailIdList,omitempty"`
	MessageID   string   `json:"messageId,omitempty"`
}

// Sender SendCloud HTTP 客户端
type Sender struct {
	httpClient *http.Client
	cfg        *ConfigService
}

// NewSender 构造
func NewSender(cfg *ConfigService) *Sender {
	return &Sender{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		cfg: cfg,
	}
}

// Send 发送一封或多封邮件（不带附件时走 form-urlencoded，带附件走 multipart）
func (s *Sender) Send(ctx context.Context, channel string, payload SendPayload) (*SendResult, error) {
	if s.cfg == nil {
		return nil, errors.New("config service not initialized")
	}
	cfg, apiKey, err := s.cfg.GetDecrypted(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("load channel config: %w", err)
	}
	if !cfg.IsActive {
		return nil, fmt.Errorf("channel %q is disabled", channel)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("channel %q api_key is empty", channel)
	}
	// 附件大小校验
	for _, att := range payload.Attachments {
		if len(att.Content) > maxAttachmentBytes {
			return nil, fmt.Errorf("attachment %q exceeds 10MB limit", att.Filename)
		}
	}

	fields := map[string]string{
		"apiUser":  cfg.APIUser,
		"apiKey":   apiKey,
		"from":     cfg.FromEmail,
		"fromName": cfg.FromName,
		"subject":  payload.Subject,
	}
	if payload.HTML != "" {
		fields["html"] = payload.HTML
	}
	if payload.Text != "" {
		fields["plain"] = payload.Text
	}
	replyTo := payload.ReplyTo
	if replyTo == "" {
		replyTo = cfg.ReplyTo
	}
	if replyTo != "" {
		fields["replyTo"] = replyTo
	}
	if payload.XSmtpAPI != "" {
		fields["xsmtpapi"] = payload.XSmtpAPI
	} else {
		if len(payload.To) == 0 {
			return nil, errors.New("at least one recipient required")
		}
		fields["to"] = strings.Join(payload.To, ";")
		if len(payload.Cc) > 0 {
			fields["cc"] = strings.Join(payload.Cc, ";")
		}
		if len(payload.Bcc) > 0 {
			fields["bcc"] = strings.Join(payload.Bcc, ";")
		}
	}

	if len(payload.Attachments) > 0 {
		return s.postMultipart(ctx, sendCloudSendURL, fields, payload.Attachments)
	}
	return s.postForm(ctx, sendCloudSendURL, fields)
}

// postForm application/x-www-form-urlencoded
func (s *Sender) postForm(ctx context.Context, endpoint string, fields map[string]string) (*SendResult, error) {
	form := url.Values{}
	for k, v := range fields {
		form.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.doRequest(req)
}

// postMultipart multipart/form-data（附件）
func (s *Sender) postMultipart(ctx context.Context, endpoint string, fields map[string]string, atts []Attachment) (*SendResult, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	for _, att := range atts {
		part, err := writer.CreateFormFile("attachments", att.Filename)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(part, bytes.NewReader(att.Content)); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return s.doRequest(req)
}

// doRequest 发送请求并解析响应
func (s *Sender) doRequest(req *http.Request) (*SendResult, error) {
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var parsed sendCloudResponse
	_ = json.Unmarshal(raw, &parsed)

	result := &SendResult{
		Success:    parsed.Result && parsed.StatusCode == 200,
		RawResp:    string(raw),
		StatusCode: parsed.StatusCode,
		Message:    parsed.Message,
	}

	var info sendCloudInfo
	if len(parsed.Info) > 0 {
		_ = json.Unmarshal(parsed.Info, &info)
		if len(info.EmailIDList) > 0 {
			result.MessageID = info.EmailIDList[0]
		} else if info.MessageID != "" {
			result.MessageID = info.MessageID
		}
	}

	if !result.Success {
		// HTTP 层成功但业务失败：仍返回 result（error 为 nil），由调用方根据 Success 判定
		if resp.StatusCode >= 400 {
			return result, fmt.Errorf("sendcloud http %d: %s", resp.StatusCode, truncate(string(raw), 200))
		}
	}
	return result, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
