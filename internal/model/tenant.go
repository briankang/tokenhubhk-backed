package model

// Tenant 多租户/代理商组织模型
// 支持树形层级结构 (ParentID)，最多 3 级代理
type Tenant struct {
	BaseModel
	Name         string  `gorm:"type:varchar(100);not null" json:"name"`                // 租户名称
	Domain       string  `gorm:"type:varchar(255);uniqueIndex" json:"domain,omitempty"` // 自定义域名（唯一）
	LogoURL      string  `gorm:"type:varchar(500)" json:"logo_url,omitempty"`           // Logo 地址
	ParentID     *uint   `gorm:"index" json:"parent_id,omitempty"`                      // 父级租户 ID
	Level        int     `gorm:"default:1;not null" json:"level"`                       // 层级 1-3
	IsActive     bool    `gorm:"default:true" json:"is_active"`                         // 是否启用
	ContactEmail string  `gorm:"type:varchar(255)" json:"contact_email,omitempty"`      // 联系邮箱
	ContactPhone string  `gorm:"type:varchar(50)" json:"contact_phone,omitempty"`       // 联系电话

	Children []Tenant `gorm:"foreignKey:ParentID" json:"children,omitempty"` // 子租户列表
	Parent   *Tenant  `gorm:"foreignKey:ParentID" json:"parent,omitempty"`   // 父租户关联
}

// TableName 指定租户表名
func (Tenant) TableName() string {
	return "tenants"
}
