package model

import "time"

// 邮件通道常量
const (
	EmailChannelNotification = "notification" // 通知邮件（事务类）
	EmailChannelMarketing    = "marketing"    // 营销邮件
	EmailChannelAuto         = "auto"         // 由模板 category 自动选择
)

// 邮件发送状态
const (
	EmailStatusPending   = "pending"
	EmailStatusSent      = "sent"
	EmailStatusFailed    = "failed"
	EmailStatusDelivered = "delivered"
	EmailStatusOpened    = "opened"
	EmailStatusClicked   = "clicked"
)

// 邮件模板分类
const (
	EmailCategorySystem        = "system"
	EmailCategoryTransactional = "transactional"
	EmailCategoryMarketing     = "marketing"
)

// EmailProviderConfig SendCloud 凭证配置（apiKey 使用 AES-256-GCM 加密存储）
type EmailProviderConfig struct {
	BaseModel
	// 通道: notification | marketing
	Channel string `gorm:"type:varchar(32);uniqueIndex;not null" json:"channel"`
	// SendCloud apiUser（明文存储，非敏感）
	APIUser string `gorm:"type:varchar(100);not null" json:"api_user"`
	// SendCloud apiKey 加密后的密文（base64），对外返回时脱敏为 "***"
	APIKeyEncrypted string `gorm:"type:text" json:"-"`
	// 发件人邮箱（必须是已验证域名下的地址）
	FromEmail string `gorm:"type:varchar(200);not null" json:"from_email"`
	// 发件人显示名
	FromName string `gorm:"type:varchar(100)" json:"from_name"`
	// 回信地址（可选）
	ReplyTo string `gorm:"type:varchar(200)" json:"reply_to,omitempty"`
	// 已验证域名
	Domain string `gorm:"type:varchar(200)" json:"domain"`
	// 启用开关
	IsActive bool `gorm:"default:true" json:"is_active"`
	// 每日发送上限（0 = 无限）
	DailyLimit int `gorm:"default:0" json:"daily_limit"`
}

func (EmailProviderConfig) TableName() string { return "email_provider_configs" }

// EmailTemplate 本地邮件模板（使用 Go html/template 渲染）
type EmailTemplate struct {
	BaseModel
	// 业务调用键，全局唯一，如 register_verify / invoice_issued
	Code string `gorm:"type:varchar(64);uniqueIndex;not null" json:"code"`
	// 后台展示名
	Name string `gorm:"type:varchar(100);not null" json:"name"`
	// 分类: system | transactional | marketing
	Category string `gorm:"type:varchar(32);default:'transactional'" json:"category"`
	// 强制通道: notification | marketing | auto
	Channel string `gorm:"type:varchar(32);default:'auto'" json:"channel"`
	// 主题模板（支持 {{.Name}}）
	Subject string `gorm:"type:varchar(500);not null" json:"subject"`
	// HTML 正文模板
	HTMLBody string `gorm:"type:longtext" json:"html_body"`
	// 纯文本 fallback（可选）
	TextBody string `gorm:"type:longtext" json:"text_body,omitempty"`
	// 变量元数据 JSON: [{"key":"name","desc":"用户名","example":"Alice","required":true}]
	VariablesSchema JSON `gorm:"type:json" json:"variables_schema,omitempty"`
	// 系统预设模板：不可删除，仅可编辑内容
	IsSystem bool `gorm:"default:false" json:"is_system"`
	// 启用状态
	IsActive bool `gorm:"default:true" json:"is_active"`
	// 审计
	CreatedBy uint `gorm:"index" json:"created_by,omitempty"`
	UpdatedBy uint `json:"updated_by,omitempty"`
	// 备注
	Remark string `gorm:"type:varchar(500)" json:"remark,omitempty"`
}

func (EmailTemplate) TableName() string { return "email_templates" }

// EmailSendLog 发送记录
type EmailSendLog struct {
	BaseModel
	// 批次号（同一次批量操作共享）
	BatchID string `gorm:"type:varchar(64);index" json:"batch_id,omitempty"`
	// 通道
	Channel string `gorm:"type:varchar(32);index" json:"channel"`
	// 使用的模板 code（可为空，直接发送时）
	TemplateCode string `gorm:"type:varchar(64);index" json:"template_code,omitempty"`
	// 收件人
	ToEmail string `gorm:"type:varchar(200);index;not null" json:"to_email"`
	// 实际发送主题（渲染后）
	Subject string `gorm:"type:varchar(500)" json:"subject"`
	// 渲染变量（脱敏后）
	Variables JSON `gorm:"type:json" json:"variables,omitempty"`
	// SendCloud 返回的 message id
	ProviderMessageID string `gorm:"type:varchar(100)" json:"provider_message_id,omitempty"`
	// 状态
	Status string `gorm:"type:varchar(20);index;default:'pending'" json:"status"`
	// 错误码
	ErrorCode string `gorm:"type:varchar(20)" json:"error_code,omitempty"`
	// 错误信息
	ErrorMessage string `gorm:"type:text" json:"error_message,omitempty"`
	// 重试次数
	AttemptCount int `gorm:"default:0" json:"attempt_count"`
	// 触发来源: admin | system | user_register | invoice | withdrawal
	TriggeredBy string `gorm:"type:varchar(32);index" json:"triggered_by"`
	// 操作员（管理员触发时）
	OperatorID uint `gorm:"index" json:"operator_id,omitempty"`
	// 发送时间
	SentAt *time.Time `json:"sent_at,omitempty"`
	// 送达时间（依赖 webhook，当前未实现）
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
}

func (EmailSendLog) TableName() string { return "email_send_logs" }
