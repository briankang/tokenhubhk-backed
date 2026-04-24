package model

import "time"

// 用户认证事件类型常量
const (
	AuthEventRegister     = "register"
	AuthEventLoginSuccess = "login_success"
	AuthEventLoginFailed  = "login_failed"
	AuthEventLogout       = "logout"
	AuthEventRefresh      = "refresh"
)

// 登录失败原因枚举（固定集合，禁止记录密码原文）
const (
	AuthFailReasonUserNotFound    = "user_not_found"
	AuthFailReasonWrongPassword   = "wrong_password"
	AuthFailReasonAccountDisabled = "account_disabled"
	AuthFailReasonInvalidRequest  = "invalid_request"
	AuthFailReasonInviteRequired  = "invite_required"
	AuthFailReasonEmailExists     = "email_exists"
	AuthFailReasonTokenInvalid    = "token_invalid"
)

// UserAuthLog 用户认证行为日志（注册/登录成功/登录失败/登出/刷新）
// 字段说明：
//
//	UserID     — 关联用户 ID（登录失败且用户不存在时为 0/NULL）
//	Email      — 请求中的邮箱（即使 UserID 为空也保留）
//	EventType  — register / login_success / login_failed / logout / refresh
//	IP / UA    — 来源 IP 和 User-Agent
//	Country/City — 由 GeoService 异步填充
//	FailReason — 仅 login_failed 使用，取枚举值不记密码原文
//	ExtraJSON  — 附加元数据（如邀请码）
type UserAuthLog struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	UserID     uint      `gorm:"index" json:"user_id"`
	Email      string    `gorm:"size:255;index" json:"email"`
	EventType  string    `gorm:"size:32;index" json:"event_type"`
	IP         string    `gorm:"size:64;index" json:"ip"`
	UserAgent  string    `gorm:"size:512" json:"user_agent"`
	Country    string    `gorm:"size:8" json:"country"`
	City       string    `gorm:"size:64" json:"city"`
	RequestID  string    `gorm:"size:64;index" json:"request_id"`
	FailReason string    `gorm:"size:64" json:"fail_reason,omitempty"`
	ExtraJSON  string    `gorm:"type:text" json:"extra_json,omitempty"`
	CreatedAt  time.Time `gorm:"index" json:"created_at"`
}

// TableName 返回表名
func (UserAuthLog) TableName() string {
	return "user_auth_logs"
}

// UserAuthLogQuery 分页查询条件
type UserAuthLogQuery struct {
	UserID     uint
	Email      string
	EventType  string
	IP         string
	Keyword    string
	RequestID  string
	UserAgent  string
	Country    string
	City       string
	FailReason string
	StartDate  time.Time
	EndDate    time.Time
	Page       int
	PageSize   int
}
