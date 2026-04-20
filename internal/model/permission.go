package model

import "time"

// Permission 权限目录 - 单个可授权的操作
// 从 audit.routeMap 一次性种子化，管理员创建自定义角色时从本表选权限。
type Permission struct {
	BaseModel
	Code       string `gorm:"type:varchar(80);uniqueIndex;not null" json:"code"` // = RouteMeta.Action, e.g. "user_update"
	Menu       string `gorm:"type:varchar(50);index;not null" json:"menu"`       // 菜单名, e.g. "用户管理"
	Feature    string `gorm:"type:varchar(100);not null" json:"feature"`         // 功能名, e.g. "更新用户"
	Resource   string `gorm:"type:varchar(50);index" json:"resource"`            // 资源类型
	HTTPMethod string `gorm:"type:varchar(10);index:idx_perm_method_path" json:"http_method"`
	HTTPPath   string `gorm:"type:varchar(200);index:idx_perm_method_path" json:"http_path"`
	IsRead     bool   `gorm:"default:false;index" json:"is_read"`   // true = GET 读权限(不写审计日志)
	IsSystem   bool   `gorm:"default:true;index" json:"is_system"`  // true = seed 数据, 不可删
}

func (Permission) TableName() string { return "permissions" }

// Role 角色定义 - 功能权限 + 数据权限
// is_system=true 的内置角色不可删但可克隆；自定义角色由管理员在 UI 创建。
type Role struct {
	BaseModel
	Code        string `gorm:"type:varchar(50);uniqueIndex;not null" json:"code"` // e.g. "SUPER_ADMIN" / "custom_xxx"
	Name        string `gorm:"type:varchar(100);not null" json:"name"`            // 展示名
	Description string `gorm:"type:varchar(500)" json:"description"`
	IsSystem    bool   `gorm:"default:false;index" json:"is_system"` // 内置角色
	DataScope   JSON   `gorm:"type:json" json:"data_scope"`          // {"type":"all|own_tenant|custom_tenants|own_only","tenant_ids":[...]}
}

func (Role) TableName() string { return "roles" }

// RolePermission 角色-权限关联表
type RolePermission struct {
	RoleID       uint      `gorm:"primaryKey;autoIncrement:false" json:"role_id"`
	PermissionID uint      `gorm:"primaryKey;autoIncrement:false;index" json:"permission_id"`
	CreatedAt    time.Time `json:"created_at"`
}

func (RolePermission) TableName() string { return "role_permissions" }

// UserRole 用户-角色关联表（支持一个用户多个角色）
type UserRole struct {
	UserID    uint      `gorm:"primaryKey;autoIncrement:false" json:"user_id"`
	RoleID    uint      `gorm:"primaryKey;autoIncrement:false;index" json:"role_id"`
	GrantedBy uint      `gorm:"not null" json:"granted_by"` // 授予者 user_id (0 = 系统回填)
	GrantedAt time.Time `gorm:"not null" json:"granted_at"`
}

func (UserRole) TableName() string { return "user_roles" }
