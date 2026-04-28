package model

// DocArticle 文档文章模型，存储 Markdown 格式的文档内容
type DocArticle struct {
	BaseModel
	CategoryID  uint   `gorm:"index;not null" json:"category_id"`                                                    // 所属分类 ID
	Title       string `gorm:"type:varchar(200);not null" json:"title"`                                              // 文档标题
	Slug        string `gorm:"type:varchar(200);uniqueIndex:uidx_doc_article_slug_locale" json:"slug"`               // URL 标识（唯一）
	Content     string `gorm:"type:longtext" json:"content"`                                                         // Markdown 正文
	Summary     string `gorm:"type:varchar(500)" json:"summary"`                                                     // 摘要
	Tags        string `gorm:"type:varchar(200)" json:"tags"`                                                        // 标签（逗号分隔）
	Locale      string `gorm:"type:varchar(10);default:'zh';uniqueIndex:uidx_doc_article_slug_locale" json:"locale"` // 语言标识
	SortOrder   int    `gorm:"default:0" json:"sort_order"`                                                          // 排序权重
	IsPublished bool   `gorm:"default:true" json:"is_published"`                                                     // 是否已发布

	Category *DocCategory `gorm:"foreignKey:CategoryID" json:"category,omitempty"` // 所属分类（关联查询）
}

// TableName 指定文档文章表名
func (DocArticle) TableName() string {
	return "doc_articles"
}

// Doc 文档页面模型（保留兼容旧数据，新文档使用 DocArticle）
type Doc struct {
	BaseModel
	TenantID    uint   `gorm:"index" json:"tenant_id"`
	Slug        string `gorm:"type:varchar(200);uniqueIndex;not null" json:"slug"`
	Title       string `gorm:"type:varchar(300);not null" json:"title"`
	Content     string `gorm:"type:longtext" json:"content"`
	CategoryID  *uint  `gorm:"index" json:"category_id,omitempty"`
	SortOrder   int    `gorm:"default:0" json:"sort_order"`
	IsPublished bool   `gorm:"default:false" json:"is_published"`
	Author      uint   `gorm:"index" json:"author"`

	Category *DocCategory `gorm:"foreignKey:CategoryID" json:"category,omitempty"`
}

// TableName 指定文档表名
func (Doc) TableName() string {
	return "docs"
}

// DocCategory 文档分类模型，支持二级分类（ParentID 为空表示一级分类）
type DocCategory struct {
	BaseModel
	Name        string `gorm:"type:varchar(100);not null" json:"name"`    // 分类名称
	Slug        string `gorm:"type:varchar(100);uniqueIndex" json:"slug"` // URL 标识
	Icon        string `gorm:"type:varchar(50)" json:"icon"`              // 图标标识（如 rocket, book 等）
	Description string `gorm:"type:varchar(500)" json:"description"`      // 分类描述
	ParentID    *uint  `gorm:"index" json:"parent_id"`                    // 父分类 ID（空=一级分类）
	SortOrder   int    `gorm:"default:0" json:"sort_order"`               // 排序权重

	Children []DocCategory `gorm:"foreignKey:ParentID" json:"children,omitempty"`   // 子分类列表
	Articles []DocArticle  `gorm:"foreignKey:CategoryID" json:"articles,omitempty"` // 文档列表
	Docs     []Doc         `gorm:"foreignKey:CategoryID" json:"docs,omitempty"`     // 旧文档列表（兼容）
}

// TableName 指定文档分类表名
func (DocCategory) TableName() string {
	return "doc_categories"
}
