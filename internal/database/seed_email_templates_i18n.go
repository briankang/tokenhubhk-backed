package database

import (
	"encoding/json"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunSeedEmailTemplatesI18n 补充多语言邮件模板（en / zh / zh_TW 变体）
//
// 命名约定：基础 code 加下划线语言后缀：
//   - register_verify        → 默认（英文，fallback 使用）
//   - register_verify_zh     → 简体中文
//   - register_verify_zh_TW  → 繁体中文
//   - password_reset_*       → 同上
//
// 查询顺序（`template_service.GetByCodeWithLang`）：
//  1. {code}_{lang}
//  2. {code}_en
//  3. {code} (legacy fallback)
//
// 幂等：已存在则跳过，不覆盖管理员自定义内容。
func RunSeedEmailTemplatesI18n(db *gorm.DB) error {
	tpls := builtinI18nTemplates()
	created, skipped := 0, 0
	for _, t := range tpls {
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
	logger.L.Info("email i18n templates seeded",
		zap.Int("created", created),
		zap.Int("skipped", skipped),
	)
	return nil
}

func builtinI18nTemplates() []model.EmailTemplate {
	mk := func(defs []map[string]interface{}) []byte {
		b, _ := json.Marshal(defs)
		return b
	}
	codeVar := []map[string]interface{}{
		{"key": "Code", "desc": "6-digit code", "example": "123456", "required": true},
		{"key": "ExpireMinutes", "desc": "Expiry minutes", "example": "10", "required": false},
	}

	list := []model.EmailTemplate{
		// ─── REGISTER VERIFY ───────────────────────────
		{
			Code:     "register_verify_en",
			Name:     "Register Verify (English)",
			Category: model.EmailCategoryTransactional,
			Channel:  model.EmailChannelNotification,
			Subject:  "[TokenHub] Your verification code: {{.Code}}",
			HTMLBody: verifyEnHTML,
			TextBody: "Your TokenHub verification code is {{.Code}}. It expires in {{.ExpireMinutes}} minutes.\nIf you did not request this, please ignore this email.",
			VariablesSchema: mk(codeVar),
			IsSystem: true,
			IsActive: true,
			Remark:   "English registration verification code (default fallback)",
		},
		{
			Code:     "register_verify_zh",
			Name:     "注册验证码（简体中文）",
			Category: model.EmailCategoryTransactional,
			Channel:  model.EmailChannelNotification,
			Subject:  "【TokenHub】您的注册验证码：{{.Code}}",
			HTMLBody: verifyZhHTML,
			TextBody: "您的 TokenHub 注册验证码是 {{.Code}}，{{.ExpireMinutes}} 分钟内有效。\n如非本人操作请忽略本邮件。",
			VariablesSchema: mk(codeVar),
			IsSystem: true,
			IsActive: true,
			Remark:   "简体中文注册验证码",
		},
		{
			Code:     "register_verify_zh_TW",
			Name:     "註冊驗證碼（繁體中文）",
			Category: model.EmailCategoryTransactional,
			Channel:  model.EmailChannelNotification,
			Subject:  "【TokenHub】您的註冊驗證碼：{{.Code}}",
			HTMLBody: verifyZhTWHTML,
			TextBody: "您的 TokenHub 註冊驗證碼是 {{.Code}}，{{.ExpireMinutes}} 分鐘內有效。\n如非本人操作請忽略本郵件。",
			VariablesSchema: mk(codeVar),
			IsSystem: true,
			IsActive: true,
			Remark:   "繁體中文註冊驗證碼",
		},
		// ─── PASSWORD RESET ────────────────────────────
		{
			Code:     "password_reset_en",
			Name:     "Password Reset (English)",
			Category: model.EmailCategoryTransactional,
			Channel:  model.EmailChannelNotification,
			Subject:  "[TokenHub] Password reset code: {{.Code}}",
			HTMLBody: resetEnHTML,
			TextBody: "Your TokenHub password reset code is {{.Code}}. It expires in {{.ExpireMinutes}} minutes.\nIf this was not you, please change your password immediately.",
			VariablesSchema: mk(codeVar),
			IsSystem: true,
			IsActive: true,
			Remark:   "English password reset code",
		},
		{
			Code:     "password_reset_zh",
			Name:     "密码重置验证码（简体中文）",
			Category: model.EmailCategoryTransactional,
			Channel:  model.EmailChannelNotification,
			Subject:  "【TokenHub】密码重置验证码：{{.Code}}",
			HTMLBody: resetZhHTML,
			TextBody: "您的密码重置验证码：{{.Code}}，{{.ExpireMinutes}} 分钟内有效。\n如非本人操作，请立即修改账户密码。",
			VariablesSchema: mk(codeVar),
			IsSystem: true,
			IsActive: true,
			Remark:   "简体中文密码重置",
		},
		{
			Code:     "password_reset_zh_TW",
			Name:     "密碼重置驗證碼（繁體中文）",
			Category: model.EmailCategoryTransactional,
			Channel:  model.EmailChannelNotification,
			Subject:  "【TokenHub】密碼重置驗證碼：{{.Code}}",
			HTMLBody: resetZhTWHTML,
			TextBody: "您的密碼重置驗證碼：{{.Code}}，{{.ExpireMinutes}} 分鐘內有效。\n如非本人操作，請立即修改賬戶密碼。",
			VariablesSchema: mk(codeVar),
			IsSystem: true,
			IsActive: true,
			Remark:   "繁體中文密碼重置",
		},
	}
	return list
}

// ============ HTML 模板（按语言） ============

const verifyEnHTML = `<!DOCTYPE html>
<html><body style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;background:#f5f7fb;padding:40px 20px;margin:0;">
<table role="presentation" style="max-width:560px;margin:0 auto;background:#fff;border-radius:12px;overflow:hidden;box-shadow:0 4px 16px rgba(0,0,0,.06);" cellpadding="0" cellspacing="0">
<tr><td style="background:linear-gradient(135deg,#6366f1,#8b5cf6);padding:32px;text-align:center;color:#fff;">
<h1 style="margin:0;font-size:22px;font-weight:600;">TokenHub Email Verification</h1></td></tr>
<tr><td style="padding:32px;">
<p style="font-size:15px;color:#374151;line-height:1.6;">Hello, your verification code is:</p>
<div style="background:#f3f4f6;border-radius:8px;padding:24px;text-align:center;margin:24px 0;">
<span style="font-size:36px;font-weight:700;letter-spacing:8px;color:#111827;font-family:monospace;">{{.Code}}</span>
</div>
<p style="font-size:14px;color:#6b7280;line-height:1.6;">This code expires in {{.ExpireMinutes}} minutes. Do not share it with anyone.<br/>If this was not you, please ignore this email.</p>
</td></tr>
<tr><td style="padding:20px 32px;background:#f9fafb;color:#9ca3af;font-size:12px;text-align:center;">
This is an automated message — please do not reply.
</td></tr></table></body></html>`

const verifyZhHTML = `<!DOCTYPE html>
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

const verifyZhTWHTML = `<!DOCTYPE html>
<html><body style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;background:#f5f7fb;padding:40px 20px;margin:0;">
<table role="presentation" style="max-width:560px;margin:0 auto;background:#fff;border-radius:12px;overflow:hidden;box-shadow:0 4px 16px rgba(0,0,0,.06);" cellpadding="0" cellspacing="0">
<tr><td style="background:linear-gradient(135deg,#6366f1,#8b5cf6);padding:32px;text-align:center;color:#fff;">
<h1 style="margin:0;font-size:22px;font-weight:600;">TokenHub 郵箱驗證</h1></td></tr>
<tr><td style="padding:32px;">
<p style="font-size:15px;color:#374151;line-height:1.6;">您好，您的註冊驗證碼是：</p>
<div style="background:#f3f4f6;border-radius:8px;padding:24px;text-align:center;margin:24px 0;">
<span style="font-size:36px;font-weight:700;letter-spacing:8px;color:#111827;font-family:monospace;">{{.Code}}</span>
</div>
<p style="font-size:14px;color:#6b7280;line-height:1.6;">該驗證碼 {{.ExpireMinutes}} 分鐘內有效，請勿洩露給他人。<br/>如非本人操作，請忽略此郵件。</p>
</td></tr>
<tr><td style="padding:20px 32px;background:#f9fafb;color:#9ca3af;font-size:12px;text-align:center;">
此郵件由系統自動發送，請勿回覆。
</td></tr></table></body></html>`

const resetEnHTML = `<!DOCTYPE html>
<html><body style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;background:#f5f7fb;padding:40px 20px;margin:0;">
<table role="presentation" style="max-width:560px;margin:0 auto;background:#fff;border-radius:12px;overflow:hidden;box-shadow:0 4px 16px rgba(0,0,0,.06);" cellpadding="0" cellspacing="0">
<tr><td style="background:linear-gradient(135deg,#f59e0b,#ef4444);padding:32px;text-align:center;color:#fff;">
<h1 style="margin:0;font-size:22px;font-weight:600;">Password Reset</h1></td></tr>
<tr><td style="padding:32px;">
<p style="font-size:15px;color:#374151;line-height:1.6;">You are resetting your password. Use the code below:</p>
<div style="background:#fef3c7;border-radius:8px;padding:24px;text-align:center;margin:24px 0;">
<span style="font-size:36px;font-weight:700;letter-spacing:8px;color:#92400e;font-family:monospace;">{{.Code}}</span>
</div>
<p style="font-size:14px;color:#dc2626;line-height:1.6;"><strong>⚠ If this was not you, please change your password immediately.</strong></p>
</td></tr></table></body></html>`

const resetZhHTML = `<!DOCTYPE html>
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

const resetZhTWHTML = `<!DOCTYPE html>
<html><body style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;background:#f5f7fb;padding:40px 20px;margin:0;">
<table role="presentation" style="max-width:560px;margin:0 auto;background:#fff;border-radius:12px;overflow:hidden;box-shadow:0 4px 16px rgba(0,0,0,.06);" cellpadding="0" cellspacing="0">
<tr><td style="background:linear-gradient(135deg,#f59e0b,#ef4444);padding:32px;text-align:center;color:#fff;">
<h1 style="margin:0;font-size:22px;font-weight:600;">密碼重置驗證</h1></td></tr>
<tr><td style="padding:32px;">
<p style="font-size:15px;color:#374151;line-height:1.6;">您正在進行密碼重置操作，請使用以下驗證碼：</p>
<div style="background:#fef3c7;border-radius:8px;padding:24px;text-align:center;margin:24px 0;">
<span style="font-size:36px;font-weight:700;letter-spacing:8px;color:#92400e;font-family:monospace;">{{.Code}}</span>
</div>
<p style="font-size:14px;color:#dc2626;line-height:1.6;"><strong>⚠ 如非本人操作，請立即修改賬戶密碼並聯繫我們。</strong></p>
</td></tr></table></body></html>`
