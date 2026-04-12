package model

import "time"

// AuditLog 审计日志，记录管理员的重要操作行为
type AuditLog struct {
	BaseModel
	TenantID   uint   `gorm:"index;not null" json:"tenant_id"`               // 租户 ID
	UserID     uint   `gorm:"index;not null" json:"user_id"`                 // 操作用户 ID
	Action     string `gorm:"type:varchar(50);not null;index" json:"action"` // 操作类型: balance_adjust, commission_settle, withdrawal_approve, exchange_rate_update, etc.
	Resource   string `gorm:"type:varchar(50);not null" json:"resource"`     // 资源类型
	ResourceID uint   `gorm:"index" json:"resource_id"`                      // 资源 ID
	OldValue   string `gorm:"type:text" json:"old_value,omitempty"`          // 旧值 (JSON)
	NewValue   string `gorm:"type:text" json:"new_value,omitempty"`          // 新值 (JSON)
	Details    JSON   `gorm:"type:json" json:"details,omitempty"`            // 详细信息 (JSON)
	IP         string `gorm:"type:varchar(45)" json:"ip"`                    // 客户端 IP
	RequestID  string `gorm:"type:varchar(64);index" json:"request_id"`      // 请求 ID
	OperatorID uint   `gorm:"index" json:"operator_id"`                      // 操作人ID（管理员）
	Remark     string `gorm:"type:text" json:"remark"`                       // 备注
}

// TableName 指定审计日志表名
func (AuditLog) TableName() string {
	return "audit_logs"
}

// AuditLogQuery 审计日志查询参数
type AuditLogQuery struct {
	Action     string
	OperatorID uint
	StartDate  time.Time
	EndDate    time.Time
	Page       int
	PageSize   int
}
