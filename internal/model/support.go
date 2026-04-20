package model

import "time"

// ================= AI 客服 + 工单系统数据模型 =================
// 本文件定义 9 张表，覆盖 AI 客服对话、RAG 知识库、工单、长期记忆、
// 供应商文档引用、热门问题、采纳答案、模型配置
// 详见 CLAUDE.md「AI 客服系统」章节

// -------- 1. 会话 / 消息 --------

// SupportSession AI 客服会话（仅登录用户）
type SupportSession struct {
	BaseModel
	UserID       uint       `gorm:"index;not null" json:"user_id"`
	Title        string     `gorm:"type:varchar(200)" json:"title"`
	Locale       string     `gorm:"type:varchar(10);default:'zh'" json:"locale"`         // 会话界面语言
	OriginalLang string     `gorm:"type:varchar(10)" json:"original_lang,omitempty"`     // 首条消息检测到的语言
	Status       string     `gorm:"type:varchar(20);default:'active';index" json:"status"` // active / escalated / closed
	MsgCount     int        `gorm:"default:0" json:"msg_count"`
	TokenCost    int64      `gorm:"default:0" json:"token_cost"` // 累计 token 消耗
	SourceIP     string     `gorm:"type:varchar(64)" json:"source_ip,omitempty"`
	LastMsgAt    time.Time  `gorm:"index" json:"last_msg_at"`
	ClosedAt     *time.Time `json:"closed_at,omitempty"`
}

func (SupportSession) TableName() string { return "support_sessions" }

// SupportMessage 会话消息（单条，用户或 AI）
type SupportMessage struct {
	BaseModel
	SessionID    uint       `gorm:"index;not null" json:"session_id"`
	Role         string     `gorm:"type:varchar(20);not null" json:"role"` // user / assistant
	Content      string     `gorm:"type:mediumtext" json:"content"`         // 原语言正文
	ContentZh    string     `gorm:"type:mediumtext" json:"content_zh,omitempty"` // 用户问题的中文翻译版（仅 user role 填充，用于检索）
	ModelID      string     `gorm:"type:varchar(100)" json:"model_id,omitempty"` // 实际调用的模型 key
	TokensIn     int        `json:"tokens_in"`
	TokensOut    int        `json:"tokens_out"`
	DocRefs      string     `gorm:"type:varchar(500)" json:"doc_refs,omitempty"`       // 引用 knowledge_chunks.id，逗号分隔
	ExternalUrls string     `gorm:"type:varchar(1000)" json:"external_urls,omitempty"` // 引用供应商 URL，逗号分隔
	NeedHuman    bool       `gorm:"default:false" json:"need_human"`                   // AI 标记需转人工
	IsAccepted   bool       `gorm:"default:false;index" json:"is_accepted"`            // 用户采纳标记
	AcceptedAt   *time.Time `json:"accepted_at,omitempty"`
}

func (SupportMessage) TableName() string { return "support_messages" }

// -------- 2. 知识库（统一表，source_type 区分来源） --------

// KnowledgeChunk RAG 知识切片
// source_type: doc_article (22 篇内置文档) / faq (手工 FAQ) / accepted_qa (用户采纳答案) / hot_question (管理员编辑的热门问题)
type KnowledgeChunk struct {
	BaseModel
	SourceType    string `gorm:"type:varchar(30);not null;index" json:"source_type"`
	SourceID      uint   `gorm:"index" json:"source_id"`                           // 关联原表 id（doc_articles / hot_questions 等）
	SourceSlug    string `gorm:"type:varchar(150);index" json:"source_slug,omitempty"`
	Title         string `gorm:"type:varchar(300)" json:"title"`
	Content       string `gorm:"type:text;not null" json:"content"`                // 400-800 字片段（中文）
	ChunkIndex    int    `json:"chunk_index"`                                      // 同一 source 切出多段时的序号
	Embedding     string `gorm:"type:mediumtext" json:"-"`                         // JSON float32[] 1024 维
	EmbeddingHash string `gorm:"type:varchar(64);index" json:"embedding_hash"`     // md5(content)，内容变化时触发重建
	Tokens        int    `json:"tokens"`
	Priority      int    `gorm:"default:0" json:"priority"`                        // 召回加权：doc=0 / faq=5 / accepted_qa=10 / hot_question=10
	HitCount      int    `gorm:"default:0" json:"hit_count"`                       // 被召回次数（管理员按热度统计用）
	IsActive      bool   `gorm:"default:true;index" json:"is_active"`
}

func (KnowledgeChunk) TableName() string { return "knowledge_chunks" }

// -------- 3. 供应商文档 URL 引用（不爬取内容） --------

// ProviderDocReference 供应商官方文档链接
// AI 回答涉及特定供应商 API / 计费 / SDK 细节时，在答案末尾附上此表的 URL
type ProviderDocReference struct {
	BaseModel
	SupplierID   uint   `gorm:"index;not null" json:"supplier_id"`
	SupplierCode string `gorm:"type:varchar(50);index" json:"supplier_code"`
	DocType      string `gorm:"type:varchar(30);index" json:"doc_type"`  // api_reference / pricing / sdk / error_codes / general
	Title        string `gorm:"type:varchar(200);not null" json:"title"` // 例: 阿里云百炼 API 参考
	URL          string `gorm:"type:varchar(500);not null" json:"url"`
	Description  string `gorm:"type:varchar(500)" json:"description,omitempty"` // 何时引用的提示语
	Keywords     string `gorm:"type:varchar(500)" json:"keywords,omitempty"`    // 逗号分隔，用于匹配用户问题
	Locale       string `gorm:"type:varchar(10);default:'zh'" json:"locale"`
	Priority     int    `gorm:"default:0" json:"priority"`
	IsActive     bool   `gorm:"default:true;index" json:"is_active"`
}

func (ProviderDocReference) TableName() string { return "provider_doc_references" }

// -------- 4. 用户采纳的答案 --------

// AcceptedAnswer 用户对 AI 回答点「采纳」后的记录
// 管理员审核通过后，脱敏的问答对会被发布到 knowledge_chunks（source_type=accepted_qa），共享给全体用户
type AcceptedAnswer struct {
	BaseModel
	UserID       uint       `gorm:"index;not null" json:"user_id"`
	SessionID    uint       `gorm:"index;not null" json:"session_id"`
	MessageID    uint       `gorm:"uniqueIndex" json:"message_id"` // 一条消息只能被采纳一次
	Question     string     `gorm:"type:text;not null" json:"question"` // 脱敏后的中文问题
	Answer       string     `gorm:"type:text;not null" json:"answer"`   // 脱敏后的中文答案
	Status       string     `gorm:"type:varchar(20);default:'pending_review';index" json:"status"`
	ReviewerID   *uint      `gorm:"index" json:"reviewer_id,omitempty"`
	ReviewedAt   *time.Time `json:"reviewed_at,omitempty"`
	RejectReason string     `gorm:"type:varchar(500)" json:"reject_reason,omitempty"`
	ChunkID      *uint      `json:"chunk_id,omitempty"` // approved 后关联的 KnowledgeChunk.id
}

func (AcceptedAnswer) TableName() string { return "accepted_answers" }

// -------- 5. 热门问题（管理员维护的标准答案） --------

// HotQuestion 管理员编辑的热门问题 + 标准答案
// 发布后自动生成 knowledge_chunks（source_type=hot_question, priority=10），参与 RAG 检索
type HotQuestion struct {
	BaseModel
	Title         string `gorm:"type:varchar(300);not null" json:"title"`          // 问题标题
	QuestionBody  string `gorm:"type:text;not null" json:"question_body"`          // 详细描述（多种表达方式，帮助 RAG 召回）
	CuratedAnswer string `gorm:"type:text;not null" json:"curated_answer"`         // 管理员编辑的 Markdown 答案
	Category      string `gorm:"type:varchar(30);index" json:"category"`           // api / billing / channel / account / sdk
	Tags          string `gorm:"type:varchar(300)" json:"tags,omitempty"`          // 逗号分隔
	Priority      int    `gorm:"default:10" json:"priority"`                       // RAG 加权（默认 10，高于 doc_article）
	HitCount      int    `gorm:"default:0" json:"hit_count"`                       // 被 RAG 召回次数
	IsPublished   bool   `gorm:"default:false;index" json:"is_published"`          // 发布后才参与检索
	ChunkID       *uint  `json:"chunk_id,omitempty"`                               // 发布后关联的 chunk id
	AuthorID      uint   `gorm:"index" json:"author_id"`
	LastEditedBy  *uint  `gorm:"index" json:"last_edited_by,omitempty"`
}

func (HotQuestion) TableName() string { return "hot_questions" }

// -------- 6. 长期记忆（个人，不共享） --------

// UserSupportMemory 用户个人记忆
// 会话结束后 LLM 从对话中提取用户偏好 / 事实 / 历史摘要（禁止记录 API Key / 余额 / 订单号）
// 下次对话注入 system prompt 的「用户背景」段
type UserSupportMemory struct {
	BaseModel
	UserID          uint       `gorm:"index;not null" json:"user_id"`
	MemoryType      string     `gorm:"type:varchar(30);index" json:"memory_type"` // preference / fact / history_summary
	Content         string     `gorm:"type:text;not null" json:"content"`
	SourceSessionID *uint      `gorm:"index" json:"source_session_id,omitempty"`
	Relevance       float32    `gorm:"default:1.0" json:"relevance"` // 0-1，衰减
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	IsActive        bool       `gorm:"default:true;index" json:"is_active"`
}

func (UserSupportMemory) TableName() string { return "user_support_memories" }

// -------- 7. 模型配置（多候选 + 优先级 + 预算分级） --------

// SupportModelProfile 客服模型候选配置
// 对话时按 Priority 降序 + BudgetLevel 匹配挑选可用模型，形成 Fallback 链
type SupportModelProfile struct {
	BaseModel
	ModelKey       string  `gorm:"type:varchar(100);uniqueIndex;not null" json:"model_key"` // 对应 ai_models.name，如 glm-4
	DisplayName    string  `gorm:"type:varchar(200)" json:"display_name"`
	Priority       int     `gorm:"default:100;index" json:"priority"` // 降序优先使用
	IsActive       bool    `gorm:"default:true;index" json:"is_active"`
	MaxTokens      int     `gorm:"default:1024" json:"max_tokens"`
	Temperature    float32 `gorm:"default:0.3" json:"temperature"`
	EnableSearch   bool    `gorm:"default:true" json:"enable_search"`
	EnableThinking bool    `gorm:"default:false" json:"enable_thinking"`
	BudgetLevel    string  `gorm:"type:varchar(20);default:'normal'" json:"budget_level"` // normal / economy / emergency
	Notes          string  `gorm:"type:varchar(500)" json:"notes,omitempty"`
}

func (SupportModelProfile) TableName() string { return "support_model_profiles" }

// -------- 8. 工单 --------

// SupportTicket 用户工单
// SLA：DueAt = CreatedAt + 24h，超期未响应触发告警
type SupportTicket struct {
	BaseModel
	TicketNo         string     `gorm:"type:varchar(32);uniqueIndex;not null" json:"ticket_no"` // T20260420001
	UserID           uint       `gorm:"index;not null" json:"user_id"`
	ContactEmail     string     `gorm:"type:varchar(200);index" json:"contact_email"`
	Title            string     `gorm:"type:varchar(200);not null" json:"title"`
	Description      string     `gorm:"type:text;not null" json:"description"`
	Category         string     `gorm:"type:varchar(30);not null;index" json:"category"`        // api / billing / channel / account / other
	Priority         string     `gorm:"type:varchar(20);default:'normal'" json:"priority"`      // low / normal / high / urgent
	Status           string     `gorm:"type:varchar(20);default:'pending';index" json:"status"` // pending / assigned / replied / awaiting_user / resolved / closed / reopened
	AssigneeID       *uint      `gorm:"index" json:"assignee_id,omitempty"`
	RelatedSessionID *uint      `gorm:"index" json:"related_session_id,omitempty"` // 关联的 AI 会话 id，管理员可查完整上下文
	SourceIP         string     `gorm:"type:varchar(64)" json:"source_ip,omitempty"`
	ReadByAdminAt    *time.Time `gorm:"index" json:"read_by_admin_at,omitempty"`
	UnreadByUser     bool       `gorm:"default:false;index" json:"unread_by_user"` // 有管理员新回复用户未读
	DueAt            time.Time  `gorm:"index" json:"due_at"`                       // SLA 截止时间
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
}

func (SupportTicket) TableName() string { return "support_tickets" }

// SupportTicketReply 工单回复
type SupportTicketReply struct {
	BaseModel
	TicketID   uint   `gorm:"index;not null" json:"ticket_id"`
	AuthorID   uint   `gorm:"index;not null" json:"author_id"`
	AuthorType string `gorm:"type:varchar(20);not null" json:"author_type"` // user / admin
	Content    string `gorm:"type:text;not null" json:"content"`
	IsInternal bool   `gorm:"default:false" json:"is_internal"` // 管理员仅内部备注，用户不可见
}

func (SupportTicketReply) TableName() string { return "support_ticket_replies" }
