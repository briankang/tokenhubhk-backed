package model

import (
	"time"
)

// AgentApplication 代理申请表
// 存储合作伙伴/代理商的申请信息，包含申请人基本信息和审核状态
type AgentApplication struct {
	BaseModel
	// ─── 申请人信息 ───
	Name       string `gorm:"type:varchar(100);not null" json:"name"`        // 姓名/公司名
	Email      string `gorm:"type:varchar(200);not null" json:"email"`       // 邮箱
	Phone      string `gorm:"type:varchar(50)" json:"phone"`                 // 电话
	WechatID   string `gorm:"type:varchar(100)" json:"wechat_id"`            // 微信号
	Occupation string `gorm:"type:varchar(100)" json:"occupation"`           // 职业/身份
	UseCase    string `gorm:"type:text" json:"use_case"`                     // 使用场景描述
	Source     string `gorm:"type:varchar(200)" json:"source"`               // 从哪了解到平台（多选用逗号分隔）
	Remark     string `gorm:"type:text" json:"remark"`                       // 备注

	// ─── 审核信息 ───
	Status     string     `gorm:"type:varchar(20);default:'pending';index" json:"status"` // pending/approved/rejected
	ReviewerID *uint      `gorm:"index" json:"reviewer_id"`                                // 审核人ID
	ReviewNote string     `gorm:"type:text" json:"review_note"`                            // 审核备注
	ReviewedAt *time.Time `json:"reviewed_at"`                                            // 审核时间
}

// TableName 指定表名
func (AgentApplication) TableName() string {
	return "agent_applications"
}

// ApplicationStatus 申请状态常量
const (
	ApplicationStatusPending  = "pending"  // 待审核
	ApplicationStatusApproved = "approved" // 已通过
	ApplicationStatusRejected = "rejected" // 已拒绝
)

// IsPending 判断是否待审核状态
func (a *AgentApplication) IsPending() bool {
	return a.Status == ApplicationStatusPending
}

// IsApproved 判断是否已通过
func (a *AgentApplication) IsApproved() bool {
	return a.Status == ApplicationStatusApproved
}

// IsRejected 判断是否已拒绝
func (a *AgentApplication) IsRejected() bool {
	return a.Status == ApplicationStatusRejected
}
