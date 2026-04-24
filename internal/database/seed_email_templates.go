package database

import (
	"encoding/json"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunSeedEmailTemplates 幂等种子系统预设邮件模板
// 规则：
//   - 以 code 为唯一键；code 已存在则跳过（不覆盖管理员的编辑）
//   - 仅首次安装或新增 code 时生效
func RunSeedEmailTemplates(db *gorm.DB) error {
	templates := builtinEmailTemplates()
	var created, skipped int
	for _, t := range templates {
		var existing model.EmailTemplate
		err := db.Where("code = ?", t.Code).First(&existing).Error
		if err == nil {
			skipped++
			continue
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		if err := db.Create(&t).Error; err != nil {
			return err
		}
		created++
	}
	logger.L.Info("email templates seeded",
		zap.Int("created", created),
		zap.Int("skipped", skipped),
	)
	return nil
}

// builtinEmailTemplates 返回 6 条系统预设模板（使用 Go html/template 语法）
func builtinEmailTemplates() []model.EmailTemplate {
	type varSchema []map[string]interface{}
	mk := func(v varSchema) []byte {
		b, _ := json.Marshal(v)
		return b
	}

	return []model.EmailTemplate{
		{
			Code:     "register_verify",
			Name:     "注册邮箱验证码",
			Category: model.EmailCategoryTransactional,
			Channel:  model.EmailChannelNotification,
			Subject:  "【TokenHub】您的注册验证码：{{.Code}}",
			HTMLBody: registerVerifyHTML,
			TextBody: "您的 TokenHub 注册验证码是 {{.Code}}，10 分钟内有效。\n如非本人操作请忽略本邮件。",
			VariablesSchema: mk(varSchema{
				{"key": "Code", "desc": "6 位验证码", "example": "123456", "required": true},
				{"key": "ExpireMinutes", "desc": "过期分钟数", "example": "10", "required": false},
			}),
			IsSystem: true,
			IsActive: true,
			Remark:   "新用户注册时发送到目标邮箱，验证邮箱有效性",
		},
		{
			Code:     "welcome",
			Name:     "注册成功欢迎邮件",
			Category: model.EmailCategoryTransactional,
			Channel:  model.EmailChannelNotification,
			Subject:  "欢迎加入 TokenHub！",
			HTMLBody: welcomeHTML,
			TextBody: "您好 {{.Name}}，\n\n欢迎加入 TokenHub！\n\n您可以立即登录控制台开始使用：{{.DashboardURL}}\n\n祝好,\nTokenHub 团队",
			VariablesSchema: mk(varSchema{
				{"key": "Name", "desc": "用户昵称", "example": "Alice", "required": false},
				{"key": "DashboardURL", "desc": "控制台地址", "example": "https://www.tokenhubhk.com/dashboard", "required": false},
			}),
			IsSystem: true,
			IsActive: true,
			Remark:   "注册完成后异步发送",
		},
		{
			Code:     "password_reset",
			Name:     "密码重置验证码",
			Category: model.EmailCategoryTransactional,
			Channel:  model.EmailChannelNotification,
			Subject:  "【TokenHub】密码重置验证码：{{.Code}}",
			HTMLBody: passwordResetHTML,
			TextBody: "您的密码重置验证码：{{.Code}}，10 分钟内有效。\n如非本人操作，请立即修改账户密码。",
			VariablesSchema: mk(varSchema{
				{"key": "Code", "desc": "6 位验证码", "example": "123456", "required": true},
			}),
			IsSystem: true,
			IsActive: true,
			Remark:   "用户发起密码重置时触发",
		},
		{
			Code:     "invoice_issued",
			Name:     "发票开具通知",
			Category: model.EmailCategoryTransactional,
			Channel:  model.EmailChannelNotification,
			Subject:  "【TokenHub】您的发票已开具（单号 {{.InvoiceNo}}）",
			HTMLBody: invoiceIssuedHTML,
			TextBody: "您好 {{.Name}}，\n\n您申请的发票已开具，发票信息如下：\n- 发票抬头：{{.TitleName}}\n- 金额：{{.Amount}} 元\n- 发票号：{{.InvoiceNo}}\n\n下载地址：{{.DownloadURL}}\n\nTokenHub 团队",
			VariablesSchema: mk(varSchema{
				{"key": "Name", "desc": "用户名", "example": "Alice", "required": false},
				{"key": "TitleName", "desc": "发票抬头", "example": "ABC 有限公司", "required": true},
				{"key": "Amount", "desc": "金额（人民币元）", "example": "1000.00", "required": true},
				{"key": "InvoiceNo", "desc": "发票号", "example": "INV202604210001", "required": true},
				{"key": "DownloadURL", "desc": "PDF 下载地址", "example": "https://oss.../invoice.pdf", "required": true},
			}),
			IsSystem: true,
			IsActive: true,
			Remark:   "管理员上传发票 PDF 后自动发送，邮件内含下载链接",
		},
		{
			Code:     "withdrawal_approved",
			Name:     "提现审批通过通知",
			Category: model.EmailCategoryTransactional,
			Channel:  model.EmailChannelNotification,
			Subject:  "【TokenHub】您的提现申请已通过",
			HTMLBody: withdrawalApprovedHTML,
			TextBody: "您好 {{.Name}}，\n\n您申请的提现 {{.Amount}} 元已审批通过，资金将在 1-3 个工作日内到账。\n\n如有疑问请联系客服。\n\nTokenHub 团队",
			VariablesSchema: mk(varSchema{
				{"key": "Name", "desc": "用户名", "example": "Alice", "required": false},
				{"key": "Amount", "desc": "提现金额", "example": "500.00", "required": true},
			}),
			IsSystem: true,
			IsActive: true,
			Remark:   "管理员通过提现申请时发送",
		},
		{
			Code:     "withdrawal_rejected",
			Name:     "提现审批拒绝通知",
			Category: model.EmailCategoryTransactional,
			Channel:  model.EmailChannelNotification,
			Subject:  "【TokenHub】关于您的提现申请",
			HTMLBody: withdrawalRejectedHTML,
			TextBody: "您好 {{.Name}}，\n\n很抱歉，您的提现申请（{{.Amount}} 元）未能通过审核。\n\n原因：{{.Reason}}\n\n如需协助请联系客服。\n\nTokenHub 团队",
			VariablesSchema: mk(varSchema{
				{"key": "Name", "desc": "用户名", "example": "Alice", "required": false},
				{"key": "Amount", "desc": "提现金额", "example": "500.00", "required": true},
				{"key": "Reason", "desc": "拒绝原因", "example": "账户信息不完整", "required": false},
			}),
			IsSystem: true,
			IsActive: true,
			Remark:   "管理员驳回提现申请时发送",
		},
	}
}

// ============ HTML 模板常量 ============

const registerVerifyHTML = `<!DOCTYPE html>
<html><body style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;background:#f5f7fb;padding:40px 20px;margin:0;">
<table role="presentation" style="max-width:560px;margin:0 auto;background:#fff;border-radius:12px;overflow:hidden;box-shadow:0 4px 16px rgba(0,0,0,.06);" cellpadding="0" cellspacing="0">
<tr><td style="background:linear-gradient(135deg,#6366f1,#8b5cf6);padding:32px;text-align:center;color:#fff;">
<h1 style="margin:0;font-size:22px;font-weight:600;">TokenHub 邮箱验证</h1></td></tr>
<tr><td style="padding:32px;">
<p style="font-size:15px;color:#374151;line-height:1.6;">您好，您的注册验证码是：</p>
<div style="background:#f3f4f6;border-radius:8px;padding:24px;text-align:center;margin:24px 0;">
<span style="font-size:36px;font-weight:700;letter-spacing:8px;color:#111827;font-family:monospace;">{{.Code}}</span>
</div>
<p style="font-size:14px;color:#6b7280;line-height:1.6;">该验证码 {{.ExpireMinutes}} 分钟内有效，请勿泄露给他人。<br/>如非本人操作，请忽略此邮件。</p>
</td></tr>
<tr><td style="padding:20px 32px;background:#f9fafb;color:#9ca3af;font-size:12px;text-align:center;">
此邮件由系统自动发送，请勿回复。
</td></tr></table></body></html>`

const welcomeHTML = `<!DOCTYPE html>
<html><body style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;background:#f5f7fb;padding:40px 20px;margin:0;">
<table role="presentation" style="max-width:560px;margin:0 auto;background:#fff;border-radius:12px;overflow:hidden;box-shadow:0 4px 16px rgba(0,0,0,.06);" cellpadding="0" cellspacing="0">
<tr><td style="background:linear-gradient(135deg,#10b981,#059669);padding:40px;text-align:center;color:#fff;">
<h1 style="margin:0 0 8px;font-size:26px;font-weight:700;">欢迎加入 TokenHub</h1>
<p style="margin:0;font-size:14px;opacity:.9;">Hello{{with .Name}} {{.}}{{end}}，感谢您的注册</p></td></tr>
<tr><td style="padding:32px;">
<p style="font-size:15px;color:#374151;line-height:1.7;">TokenHub 为您提供统一的 AI 模型 API 接入，覆盖 OpenAI / Anthropic / Gemini / DeepSeek / 百炼 等 10+ 主流供应商。</p>
<p style="font-size:15px;color:#374151;line-height:1.7;">您已获得 <strong>¥0.3 注册体验额度</strong>，可立即在控制台创建 API Key 开始使用。</p>
<div style="margin:32px 0;text-align:center;">
<a href="{{.DashboardURL}}" style="display:inline-block;background:#6366f1;color:#fff;padding:12px 32px;border-radius:8px;text-decoration:none;font-weight:600;font-size:15px;">前往控制台</a>
</div>
</td></tr>
<tr><td style="padding:20px 32px;background:#f9fafb;color:#9ca3af;font-size:12px;text-align:center;">
TokenHub 团队 · 如有疑问请随时联系我们
</td></tr></table></body></html>`

const passwordResetHTML = `<!DOCTYPE html>
<html><body style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;background:#f5f7fb;padding:40px 20px;margin:0;">
<table role="presentation" style="max-width:560px;margin:0 auto;background:#fff;border-radius:12px;overflow:hidden;box-shadow:0 4px 16px rgba(0,0,0,.06);" cellpadding="0" cellspacing="0">
<tr><td style="background:linear-gradient(135deg,#f59e0b,#ef4444);padding:32px;text-align:center;color:#fff;">
<h1 style="margin:0;font-size:22px;font-weight:600;">密码重置验证</h1></td></tr>
<tr><td style="padding:32px;">
<p style="font-size:15px;color:#374151;line-height:1.6;">您正在进行密码重置操作，请使用以下验证码：</p>
<div style="background:#fef3c7;border-radius:8px;padding:24px;text-align:center;margin:24px 0;">
<span style="font-size:36px;font-weight:700;letter-spacing:8px;color:#92400e;font-family:monospace;">{{.Code}}</span>
</div>
<p style="font-size:14px;color:#dc2626;line-height:1.6;"><strong>⚠ 如非本人操作，请立即修改账户密码并联系我们。</strong></p>
</td></tr></table></body></html>`

const invoiceIssuedHTML = `<!DOCTYPE html>
<html><body style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;background:#f5f7fb;padding:40px 20px;margin:0;">
<table role="presentation" style="max-width:560px;margin:0 auto;background:#fff;border-radius:12px;overflow:hidden;box-shadow:0 4px 16px rgba(0,0,0,.06);" cellpadding="0" cellspacing="0">
<tr><td style="background:linear-gradient(135deg,#0ea5e9,#0284c7);padding:32px;text-align:center;color:#fff;">
<h1 style="margin:0;font-size:22px;font-weight:600;">发票已开具</h1></td></tr>
<tr><td style="padding:32px;">
<p style="font-size:15px;color:#374151;line-height:1.6;">您好{{with .Name}} {{.}}{{end}}，您申请的发票已开具，详情如下：</p>
<table style="width:100%;border-collapse:collapse;margin:24px 0;">
<tr><td style="padding:10px 0;color:#6b7280;font-size:14px;">发票抬头</td><td style="padding:10px 0;text-align:right;color:#111827;font-size:14px;font-weight:500;">{{.TitleName}}</td></tr>
<tr style="border-top:1px solid #e5e7eb;"><td style="padding:10px 0;color:#6b7280;font-size:14px;">金额</td><td style="padding:10px 0;text-align:right;color:#111827;font-size:14px;font-weight:500;">¥{{.Amount}}</td></tr>
<tr style="border-top:1px solid #e5e7eb;"><td style="padding:10px 0;color:#6b7280;font-size:14px;">发票号</td><td style="padding:10px 0;text-align:right;color:#111827;font-size:14px;font-family:monospace;">{{.InvoiceNo}}</td></tr>
</table>
<div style="margin:32px 0;text-align:center;"><a href="{{.DownloadURL}}" style="display:inline-block;background:#0ea5e9;color:#fff;padding:12px 32px;border-radius:8px;text-decoration:none;font-weight:600;font-size:15px;">下载 PDF 发票</a></div>
<p style="font-size:12px;color:#9ca3af;line-height:1.6;word-break:break-all;">如按钮无法点击，请复制链接：{{.DownloadURL}}</p>
</td></tr></table></body></html>`

const withdrawalApprovedHTML = `<!DOCTYPE html>
<html><body style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;background:#f5f7fb;padding:40px 20px;margin:0;">
<table role="presentation" style="max-width:560px;margin:0 auto;background:#fff;border-radius:12px;overflow:hidden;" cellpadding="0" cellspacing="0">
<tr><td style="background:linear-gradient(135deg,#10b981,#059669);padding:32px;text-align:center;color:#fff;">
<h1 style="margin:0;font-size:22px;font-weight:600;">✓ 提现申请已通过</h1></td></tr>
<tr><td style="padding:32px;">
<p style="font-size:15px;color:#374151;line-height:1.7;">您好{{with .Name}} {{.}}{{end}}，您申请的提现 <strong style="color:#059669;">¥{{.Amount}}</strong> 已审批通过。</p>
<p style="font-size:14px;color:#6b7280;line-height:1.6;">资金将在 1-3 个工作日内到账，请留意您的收款账户。</p>
</td></tr></table></body></html>`

const withdrawalRejectedHTML = `<!DOCTYPE html>
<html><body style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;background:#f5f7fb;padding:40px 20px;margin:0;">
<table role="presentation" style="max-width:560px;margin:0 auto;background:#fff;border-radius:12px;overflow:hidden;" cellpadding="0" cellspacing="0">
<tr><td style="background:#ef4444;padding:32px;text-align:center;color:#fff;">
<h1 style="margin:0;font-size:22px;font-weight:600;">关于您的提现申请</h1></td></tr>
<tr><td style="padding:32px;">
<p style="font-size:15px;color:#374151;line-height:1.7;">您好{{with .Name}} {{.}}{{end}}，很抱歉您申请的提现 ¥{{.Amount}} 未能通过审核。</p>
<div style="background:#fef2f2;border-left:4px solid #ef4444;padding:16px;margin:20px 0;border-radius:4px;">
<p style="margin:0;font-size:14px;color:#991b1b;"><strong>原因：</strong>{{.Reason}}</p>
</div>
<p style="font-size:14px;color:#6b7280;line-height:1.6;">如需协助，请联系客服或在控制台重新提交申请。</p>
</td></tr></table></body></html>`
