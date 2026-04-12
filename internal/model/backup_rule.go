package model

// BackupRule 备用路由规则，定义主渠道组失败时的 fallback 策略
type BackupRule struct {
	BaseModel
	Name           string `gorm:"type:varchar(100);not null" json:"name"`               // 规则名称
	ModelPattern   string `gorm:"type:varchar(200);not null" json:"model_pattern"`      // 模型匹配模式
	PrimaryGroupID uint   `gorm:"index;not null" json:"primary_group_id"`              // 主渠道组 ID
	BackupGroupIDs JSON   `gorm:"type:json" json:"backup_group_ids,omitempty"`          // 备用渠道组 ID 列表
	SwitchRules    JSON   `gorm:"type:json" json:"switch_rules,omitempty"`              // 切换规则 (JSON)
	IsActive       bool   `gorm:"default:true" json:"is_active"`                        // 是否启用

	PrimaryGroup ChannelGroup `gorm:"foreignKey:PrimaryGroupID" json:"primary_group,omitempty"` // 主渠道组
}

// TableName 指定备用规则表名
func (BackupRule) TableName() string {
	return "backup_rules"
}
