package model

import "time"

// CapabilityTestCase 能力测试用例模板（可复用）
// 用例存库配置，管理员可在后台增删改查，每次新增模型时一键跑一轮全量测试
type CapabilityTestCase struct {
	BaseModel

	Name             string `gorm:"type:varchar(128);uniqueIndex;not null" json:"name"`
	DisplayName      string `gorm:"type:varchar(128)" json:"display_name,omitempty"`
	// 中文显示名称（UI 优先展示）；name 保留英文标识符不变
	Category         string `gorm:"type:varchar(32);index;not null" json:"category"`
	// category: baseline/cache/thinking/web_search/json_mode/function_call/
	//           advanced_params/boundary/async/ux_flow/streaming/multi_turn/performance

	ModelType        string `gorm:"type:varchar(32);index;not null;default:'chat'" json:"model_type"`
	// model_type: chat/vlm/ocr/embedding/rerank/tts/asr/image/video/translation/ux_flow
	Subcategory      string `gorm:"type:varchar(64)" json:"subcategory,omitempty"`

	Capability       string `gorm:"type:varchar(64);index" json:"capability,omitempty"`
	// capability: 映射 AIModel.Features 键，如 supports_thinking/supports_cache/param:top_k

	ProviderFilter   string `gorm:"type:varchar(128)" json:"provider_filter,omitempty"`  // 逗号分隔；空=全部
	ModelTypeFilter  string `gorm:"type:varchar(64)"  json:"model_type_filter,omitempty"` // 空=全部

	EndpointOverride string `gorm:"type:varchar(128)" json:"endpoint_override,omitempty"`
	// 默认 /v1/chat/completions；TTS=/v1/audio/speech，Embedding=/v1/embeddings 等

	RequestTemplate  string `gorm:"type:text;not null" json:"request_template"`
	// JSON: {"path":"/v1/...","body":{...},"timeoutSec":20}
	// 支持 {{.ModelName}} {{.SampleImageBase64}} {{.SampleAudioBase64}} {{.SampleOCRBase64}} {{.vars.xxx}}

	Assertions       string `gorm:"type:text;not null" json:"assertions"`
	// JSON array: [{type,...}]

	FlowSteps        string `gorm:"type:mediumtext" json:"flow_steps,omitempty"`
	// UX 流程多步：[{name,method,endpoint,body_template,assertions,extract_vars:{task_id:"$.id"},sleep_ms}]

	ExpectedOutcome  string `gorm:"type:varchar(16);default:'pass'" json:"expected_outcome"`
	// pass / skip_if_unsupported

	Priority            int  `gorm:"default:100" json:"priority"`
	Enabled             bool `gorm:"default:true;index" json:"enabled"`
	CostEstimateCredits int  `gorm:"default:1" json:"cost_estimate_credits"`

	Notes string `gorm:"type:varchar(512)" json:"notes,omitempty"`
}

func (CapabilityTestCase) TableName() string { return "capability_test_cases" }

// CapabilityTestTask 一次批量运行任务（镜像 ModelCheckTask）
type CapabilityTestTask struct {
	BaseModel

	Status           string `gorm:"type:varchar(16);index;not null;default:'pending'" json:"status"`
	// pending/running/completed/failed
	Progress         int    `gorm:"default:0" json:"progress"`
	ProgressMsg      string `gorm:"type:varchar(255)" json:"progress_message,omitempty"`

	TotalCount       int    `json:"total_count"`
	PassedCount      int    `json:"passed_count"`
	FailedCount      int    `json:"failed_count"`
	SkippedCount     int    `json:"skipped_count"`
	RegressionCount  int    `json:"regression_count"`

	ModelIDs         string `gorm:"type:text" json:"-"` // JSON []uint
	CaseIDs          string `gorm:"type:text" json:"-"` // JSON []uint

	ResultJSON       string `gorm:"type:mediumtext" json:"-"`
	// 最终摘要 + 建议报告

	TriggeredBy      uint       `json:"triggered_by"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	ErrorMessage     string     `gorm:"type:text" json:"error_message,omitempty"`

	// Phase 2 新增：自动化控制标志
	AutoApply       bool `gorm:"default:false" json:"auto_apply"`       // 完成后自动应用高置信度建议
	SystemTriggered bool `gorm:"default:false" json:"system_triggered"` // true=定时/系统自动触发
}

func (CapabilityTestTask) TableName() string { return "capability_test_tasks" }

// CapabilityTestResult 单次 model × case 的运行结果（镜像 ModelCheckLog）
type CapabilityTestResult struct {
	BaseModel

	TaskID   uint `gorm:"index;not null;uniqueIndex:uk_task_model_case,priority:1" json:"task_id"`
	ModelID  uint `gorm:"index;not null;uniqueIndex:uk_task_model_case,priority:2" json:"model_id"`
	CaseID   uint `gorm:"index;not null;uniqueIndex:uk_task_model_case,priority:3" json:"case_id"`
	ChannelID uint `json:"channel_id,omitempty"`

	Status           string `gorm:"type:varchar(16);index;not null" json:"status"`
	// passed/failed/skipped/regression
	ErrorCategory    string `gorm:"type:varchar(32);index" json:"error_category,omitempty"`
	UpstreamStatus   int    `json:"upstream_status"`
	LatencyMS        int    `json:"latency_ms"`

	AssertionResults string `gorm:"type:text"       json:"assertion_results,omitempty"`
	// JSON: [{name,passed,reason}]
	PollTrace        string `gorm:"type:text"       json:"poll_trace,omitempty"`
	// 视频异步轮询：{Attempts:[{ts,status,elapsed}...]}
	FlowTrace        string `gorm:"type:mediumtext" json:"flow_trace,omitempty"`
	// UX 流程：[{step,method,endpoint,status,latency,assertions}]

	RequestSnippet   string `gorm:"type:text" json:"request_snippet,omitempty"`
	ResponseSnippet  string `gorm:"type:text" json:"response_snippet,omitempty"`
	ErrorMessage     string `gorm:"type:varchar(1024)" json:"error_message,omitempty"`

	ModelName string `gorm:"type:varchar(100);index" json:"model_name,omitempty"`
	CaseName  string `gorm:"type:varchar(128);index" json:"case_name,omitempty"`
}

func (CapabilityTestResult) TableName() string { return "capability_test_results" }

// CapabilityTestBaseline 回归基线：某 model × case 的"已确认可用"版本快照
// 下次测试若从 pass 变 fail 或延迟恶化 1.5x → 标 regression
type CapabilityTestBaseline struct {
	BaseModel

	ModelID             uint   `gorm:"not null;uniqueIndex:uk_model_case,priority:1" json:"model_id"`
	CaseID              uint   `gorm:"not null;uniqueIndex:uk_model_case,priority:2" json:"case_id"`
	Outcome             string `gorm:"type:varchar(16);not null" json:"outcome"` // pass/fail
	LatencyMS           int    `json:"latency_ms"`
	ResponseSnippet     string `gorm:"type:text" json:"response_snippet,omitempty"`
	PromotedFromTaskID  uint   `json:"promoted_from_task_id"`
	PromotedByAdminID   uint   `json:"promoted_by_admin_id"`
	PromotedAt          time.Time `json:"promoted_at"`

	ModelName string `gorm:"type:varchar(100)" json:"model_name,omitempty"`
	CaseName  string `gorm:"type:varchar(128)" json:"case_name,omitempty"`
}

func (CapabilityTestBaseline) TableName() string { return "capability_test_baselines" }
