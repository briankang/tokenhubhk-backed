package model

import "time"

// PartnerApplication 合作伙伴线索申请
// 访客通过 /partners 页面提交的合作意向，用于销售/商务团队后续跟进
type PartnerApplication struct {
	BaseModel
	Name            string     `gorm:"type:varchar(100);not null" json:"name"`                      // 联系人姓名
	Email           string     `gorm:"type:varchar(200);not null;index" json:"email"`               // 联系邮箱
	Phone           string     `gorm:"type:varchar(50)" json:"phone,omitempty"`                     // 联系电话
	Company         string     `gorm:"type:varchar(200)" json:"company,omitempty"`                  // 公司名称
	CooperationType string     `gorm:"type:varchar(30);not null" json:"cooperation_type"`           // 合作类型: enterprise / channel / integration / other
	Message         string     `gorm:"type:text" json:"message,omitempty"`                          // 合作说明
	Status          string     `gorm:"type:varchar(20);not null;default:'pending'" json:"status"`   // 状态: pending / contacted / closed
	SourceIP        string     `gorm:"type:varchar(64)" json:"source_ip,omitempty"`                 // 提交 IP（审计用）
	ReadAt          *time.Time `gorm:"index" json:"read_at,omitempty"`                              // 管理员首次阅读时间；NULL=未读
}

// TableName 指定表名
func (PartnerApplication) TableName() string {
	return "partner_applications"
}
