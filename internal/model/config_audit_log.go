package model

// ConfigAuditLog 配置变更审计日志
// 所有管理后台的配置写操作前后做 diff,逐字段记录 old/new 值
// 支持按配置表、记录 ID、时间筛选查询;管理页"变更历史"按钮展示近 50 条 diff
type ConfigAuditLog struct {
	BaseModel
	AdminID     uint   `gorm:"index;not null" json:"admin_id"`            // 操作管理员ID
	AdminEmail  string `gorm:"size:255" json:"admin_email"`               // 冗余存储邮箱便于查询
	ConfigTable string `gorm:"size:60;index;not null" json:"config_table"` // 配置表名(如 referral_configs / registration_guards)
	ConfigID    uint   `gorm:"index;default:0" json:"config_id"`           // 被改记录的 ID(CREATE 时为 0)
	Action      string `gorm:"size:20;index;not null" json:"action"`       // CREATE / UPDATE / DELETE / TOGGLE
	FieldName   string `gorm:"size:80" json:"field_name"`                  // 被改字段名(UPDATE 用,CREATE/DELETE 为空)
	OldValue    string `gorm:"type:text" json:"old_value"`                 // 旧值 JSON 字符串
	NewValue    string `gorm:"type:text" json:"new_value"`                 // 新值 JSON 字符串
	IP          string `gorm:"size:45" json:"ip"`
	UserAgent   string `gorm:"size:500" json:"user_agent"`
}

// TableName 指定表名
func (ConfigAuditLog) TableName() string {
	return "config_audit_logs"
}
