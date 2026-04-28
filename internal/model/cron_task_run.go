package model

import "time"

// CronTaskRun 记录定时任务的每一次执行历史。
type CronTaskRun struct {
	ID            uint       `json:"id" gorm:"primaryKey"`
	TaskName      string     `json:"task_name" gorm:"type:varchar(80);not null;index"`
	TaskLabel     string     `json:"task_label" gorm:"type:varchar(120);not null"`
	Status        string     `json:"status" gorm:"type:varchar(20);not null;index"` // running, completed, failed, skipped
	StartedAt     time.Time  `json:"started_at" gorm:"not null;index"`
	CompletedAt   *time.Time `json:"completed_at"`
	DurationMs    int64      `json:"duration_ms" gorm:"default:0"`
	OutputSummary string     `json:"output_summary" gorm:"type:text"`
	OutputJSON    string     `json:"output_json" gorm:"type:longtext"`
	ErrorMessage  string     `json:"error_message" gorm:"type:text"`
	TriggerType   string     `json:"trigger_type" gorm:"type:varchar(20);not null;default:'schedule'"` // schedule/manual/system
	LockedByOther bool       `json:"locked_by_other" gorm:"default:false"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (CronTaskRun) TableName() string {
	return "cron_task_runs"
}
