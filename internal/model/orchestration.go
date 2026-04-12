package model

// Orchestration 多模型编排工作流模型
// 支持 PIPELINE(串行)/ROUTER(条件路由)/FALLBACK(降级) 三种模式
type Orchestration struct {
	BaseModel
	Name        string `gorm:"type:varchar(100);not null" json:"name"`                             // 编排名称
	Code        string `gorm:"type:varchar(50);uniqueIndex;not null" json:"code"`                   // 唯一编码
	Description string `gorm:"type:text" json:"description,omitempty"`                             // 描述
	Mode        string `gorm:"type:varchar(20);not null;default:'PIPELINE'" json:"mode"`            // 模式: PIPELINE/ROUTER/FALLBACK
	Steps       JSON   `gorm:"type:json" json:"steps,omitempty"`                                   // 步骤配置 (JSON)
	IsActive    bool   `gorm:"default:true" json:"is_active"`                                      // 是否启用
	IsPublic    bool   `gorm:"default:false" json:"is_public"`                                     // 是否公开
}

// TableName 指定编排表名
func (Orchestration) TableName() string {
	return "orchestrations"
}
