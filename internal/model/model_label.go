package model

// ModelLabel k:v 标签，用于灵活标注模型属性
// 设计参考阿里云 ECS 资源标签，支持任意自定义 k:v 对
//
// 系统内置语义标签（前端会渲染为彩色 chip）：
//   - tag:hot           → 热卖（橙色）
//   - tag:discount      → 优惠（蓝色）
//   - license:open-source → 开源（绿色）
//
// 其他自定义标签以 "key:value" 形式展示为灰色 chip
type ModelLabel struct {
	BaseModel
	ModelID    uint   `gorm:"uniqueIndex:uidx_model_label;not null"                     json:"model_id"`
	LabelKey   string `gorm:"type:varchar(50);uniqueIndex:uidx_model_label;not null"    json:"label_key"`
	LabelValue string `gorm:"type:varchar(100);uniqueIndex:uidx_model_label;not null"   json:"label_value"`
}

// TableName 指定表名
func (ModelLabel) TableName() string { return "model_labels" }
