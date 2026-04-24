package auth

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// disposableEmailDomains 临时邮箱域名黑名单
// 包含常见的一次性邮箱（10分钟邮箱）服务商
var disposableEmailDomains = map[string]bool{
	"10minutemail.com":      true,
	"guerrillamail.com":     true,
	"mailinator.com":        true,
	"temp-mail.org":         true,
	"getairmail.com":        true,
	"dispostable.com":       true,
	"yopmail.com":           true,
	"maildrop.cc":           true,
	"sharklasers.com":       true,
	"guerrillamail.biz":     true,
	"guerrillamail.de":      true,
	"guerrillamail.net":     true,
	"guerrillamail.org":     true,
	"guerrillamailblock.com": true,
	"pokemail.net":          true,
	"spam4.me":              true,
	"grr.la":                true,
	"mailinator.net":        true,
	"sogetthis.com":         true,
	"mailin8r.com":          true,
	"mailinator2.com":       true,
	"spamherelots.com":      true,
	"thisisnotmyrealemail.com": true,
	"tradermail.info":       true,
	"veryrealemail.com":     true,
	"temp-mail.ru":          true,
	"temp-mail.net":         true,
	"temp-mail.org.ru":      true,
	"temp-mail.be":          true,
	"temp-mail.com.ua":      true,
	"temp-mail.eu":          true,
	"temp-mail.info":        true,
	"temp-mail.it":          true,
	"temp-mail.la":          true,
	"temp-mail.li":          true,
	"temp-mail.pl":          true,
	"temp-mail.pw":          true,
	"mail.ru.net":           true,
	"disposable.com":        true,
	"fake-mail.com":         true,
	"dropmail.me":           true,
	"emlpro.com":            true,
	"emlten.com":            true,
	"emltmp.com":            true,
}

// ValidateEmailDomain 校验邮箱域名（黑名单模式）
// 支持同时校验硬编码列表与数据库动态列表
func ValidateEmailDomain(db *gorm.DB, email string) error {
	if email == "" {
		return fmt.Errorf("email is required")
	}

	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 || parts[1] == "" {
		return fmt.Errorf("invalid email format")
	}

	domain := strings.ToLower(strings.TrimSpace(parts[1]))

	// 1. 命中代码内置黑名单（10分钟邮箱）则拦截
	if disposableEmailDomains[domain] {
		return fmt.Errorf("disposable email addresses are not allowed: %s. Please use a permanent email provider (e.g. Gmail, Outlook, QQ Mail)", domain)
	}

	// 2. 命中数据库动态黑名单则拦截
	if db != nil {
		var count int64
		// 假定表名为 disposable_email_domains，由 model.DisposableEmailDomain 映射
		if err := db.Table("disposable_email_domains").
			Where("domain = ? AND is_active = ?", domain, true).
			Count(&count).Error; err == nil && count > 0 {
			return fmt.Errorf("email provider blocked by administrator: %s", domain)
		}
	}

	return nil
}
