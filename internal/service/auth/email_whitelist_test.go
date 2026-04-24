package auth

import (
	"testing"
)

func TestValidateEmailDomain(t *testing.T) {
	tests := []struct {
		name    string
		email   string
		wantErr bool
	}{
		{"valid gmail", "user@gmail.com", false},
		{"valid outlook", "test@outlook.com", false},
		{"valid qq", "12345@qq.com", false},
		{"invalid 10minutemail", "spam@10minutemail.com", true},
		{"invalid temp-mail.org", "test@temp-mail.org", true},
		{"invalid guerrillamail.com", "hey@guerrillamail.com", true},
		{"invalid mailinator.com", "user@mailinator.com", true},
		{"empty email", "", true},
		{"invalid format", "not-an-email", true},
		{"case insensitive", "USER@10MINUTEMAIL.COM", true},
		{"valid with subdomain", "user@mail.google.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 传 nil DB：仅测试硬编码黑名单，跳过数据库动态黑名单查询
			err := ValidateEmailDomain(nil, tt.email)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateEmailDomain(%s) error = %v, wantErr %v", tt.email, err, tt.wantErr)
			}
		})
	}
}
