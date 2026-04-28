package sms

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

var mainlandPhoneRe = regexp.MustCompile(`^1[3-9][0-9]{9}$`)
var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_]{4,32}$`)

// NormalizeCNPhone 将中国大陆手机号标准化为 E.164 格式。
func NormalizeCNPhone(raw string) (string, error) {
	phone := strings.TrimSpace(raw)
	phone = strings.ReplaceAll(phone, " ", "")
	phone = strings.ReplaceAll(phone, "-", "")
	phone = strings.ReplaceAll(phone, "(", "")
	phone = strings.ReplaceAll(phone, ")", "")
	if strings.HasPrefix(phone, "+86") {
		phone = phone[3:]
	} else if strings.HasPrefix(phone, "0086") {
		phone = phone[4:]
	} else if strings.HasPrefix(phone, "86") && len(phone) == 13 {
		phone = phone[2:]
	}
	if !mainlandPhoneRe.MatchString(phone) {
		return "", fmt.Errorf("only mainland China mobile numbers are supported")
	}
	return "+86" + phone, nil
}

// LocalCNPhone 返回不含国家码的 11 位手机号。
func LocalCNPhone(e164 string) string {
	return strings.TrimPrefix(e164, "+86")
}

// MaskPhone 脱敏展示手机号。
func MaskPhone(e164 string) string {
	local := LocalCNPhone(e164)
	if len(local) != 11 {
		return e164
	}
	return local[:3] + "****" + local[7:]
}

// ValidateUsername 校验账号名格式。
func ValidateUsername(username string) error {
	u := strings.TrimSpace(username)
	if len(u) < 4 || len(u) > 32 {
		return fmt.Errorf("username length must be 4-32")
	}
	if !usernameRe.MatchString(u) {
		return fmt.Errorf("username must contain only letters, digits, and underscores")
	}
	if strings.HasPrefix(u, "_") || strings.HasSuffix(u, "_") {
		return fmt.Errorf("username cannot start or end with underscore")
	}
	allDigit := true
	for _, ch := range u {
		if ch < '0' || ch > '9' {
			allDigit = false
			break
		}
	}
	if allDigit {
		return fmt.Errorf("username cannot be all digits")
	}
	return nil
}

func InternalPhoneEmail(phoneE164 string) string {
	sum := sha256.Sum256([]byte(phoneE164))
	return "phone_" + hex.EncodeToString(sum[:8]) + "@phone.local.tokenhubhk"
}
