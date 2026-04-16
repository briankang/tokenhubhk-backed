package model

import "time"

// ModelCheckTask 模型批量检测任务记录
// 每次"一键检测"会创建一条任务记录，后台异步运行检测流程
type ModelCheckTask struct {
	BaseModel
	Name           string     `gorm:"type:varchar(100);not null" json:"name"`                          // 任务名称，如"全量检测 2026-04-16 14:30"
	TriggerType    string     `gorm:"type:varchar(20);not null;default:'manual'" json:"trigger_type"` // manual / scheduled
	Status         string     `gorm:"type:varchar(20);not null;default:'pending';index" json:"status"` // pending / running / completed / failed
	Total          int        `json:"total"`
	Available      int        `json:"available"`
	FailedCount    int        `json:"failed_count"`
	DisabledCount  int        `json:"disabled_count"`
	RecoveredCount int        `json:"recovered_count"`
	Progress       int        `gorm:"default:0" json:"progress"`                               // 检测进度百分比 0-100
	ProgressMsg    string     `gorm:"type:varchar(200)" json:"progress_message,omitempty"`      // 进度描述，如"已检测 45/200"
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	ErrorMessage   string     `gorm:"type:text" json:"error_message,omitempty"`
	// ResultJSON 存储完整的 CheckSummaryDetail（含 supplier_groups），前端通过 /check-tasks/:id 获取
	ResultJSON string `gorm:"type:mediumtext" json:"-"`
}

func (ModelCheckTask) TableName() string {
	return "model_check_tasks"
}
