package model

import "time"

// Announcement 站内公告/消息通知模型
// 管理员发布，支持多类型、多优先级，可设置滚动展示和时间范围
type Announcement struct {
	BaseModel
	// 公告标题
	Title string `gorm:"type:varchar(255);not null" json:"title"`
	// English announcement title for non-Chinese locales.
	TitleEn string `gorm:"type:varchar(255)" json:"title_en,omitempty"`
	// 公告正文（支持 Markdown）
	Content string `gorm:"type:text" json:"content"`
	// English announcement content for non-Chinese locales.
	ContentEn string `gorm:"type:text" json:"content_en,omitempty"`
	// 公告类型: info | warning | success | error | model_deprecation | system
	Type string `gorm:"type:varchar(30);not null;default:'info'" json:"type"`
	// 优先级: low | normal | high | urgent
	Priority string `gorm:"type:varchar(20);not null;default:'normal'" json:"priority"`
	// 状态: draft | active | inactive
	Status string `gorm:"type:varchar(20);not null;default:'active'" json:"status"`
	// 是否在 dashboard 头部滚动条展示
	ShowBanner bool `gorm:"default:true" json:"show_banner"`
	// 开始展示时间（nil 表示立即生效）
	StartsAt *time.Time `json:"starts_at,omitempty"`
	// 过期时间（nil 表示永不过期）
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// 创建者管理员 UserID
	CreatedBy uint `gorm:"index" json:"created_by"`
	// 相关链接（可选，点击公告可跳转）
	RelatedURL string `gorm:"type:varchar(500)" json:"related_url,omitempty"`
	// 关联的 AI 模型 ID 列表（type=model_deprecation 类公告专用）
	// JSON 数组形式存储，如 [12, 34, 56]
	// 用于一键检测时跳过"已被公告确认下线"的模型，避免重复检测和误报
	ModelIDs JSON `gorm:"type:json" json:"model_ids,omitempty"`
}

func (Announcement) TableName() string { return "announcements" }

// UserAnnouncementRead 用户公告已读记录
// 联合唯一索引 (user_id, announcement_id) 保证每用户每公告仅一条记录
type UserAnnouncementRead struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	UserID         uint      `gorm:"uniqueIndex:idx_user_ann;not null;index" json:"user_id"`
	AnnouncementID uint      `gorm:"uniqueIndex:idx_user_ann;not null" json:"announcement_id"`
	ReadAt         time.Time `json:"read_at"`
}

func (UserAnnouncementRead) TableName() string { return "user_announcement_reads" }
