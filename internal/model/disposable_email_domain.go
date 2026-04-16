package model

// DisposableEmailDomain 一次性邮箱域名黑名单
// 管理员可在后台增删,GuardService 注册时检查 email 的域名是否在此表
// 初始化种子包含常见一次性邮箱服务(如 tempmail.com, 10minutemail.com 等)
type DisposableEmailDomain struct {
	BaseModel
	Domain    string `gorm:"size:120;uniqueIndex;not null" json:"domain"` // 域名(小写,如 tempmail.com)
	Source    string `gorm:"size:30;default:'MANUAL'" json:"source"`     // 来源: MANUAL / IMPORT / API_FEED
	IsActive  bool   `gorm:"default:true;index" json:"is_active"`
	Note      string `gorm:"size:200" json:"note"`                       // 备注
}

// TableName 指定表名
func (DisposableEmailDomain) TableName() string {
	return "disposable_email_domains"
}
