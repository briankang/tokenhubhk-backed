package model

// LabelDictionary 标签字典表（v3.5）
//
// 设计目标：统一管理"热卖 / 优惠 / 新品 / 免费"等标签的多语言名称、颜色、图标、排序权重。
// 前端直接用服务端返回的 `name`（按 Accept-Language 选字段）渲染，不再硬编码任何语言。
//
// 与 ModelLabel 的关系：
//   - LabelDictionary.Key 是业务键（英文小写，如 `hot`、`promo`）
//   - ModelLabel.LabelKey 引用 LabelDictionary.Key（不走外键约束，避免级联删除风险）
//   - 渲染标签时 JOIN 两表读元数据
//
// i18n 语言字段：覆盖前端 22 语言中使用率最高的 10 种；其他语言通过 fallback 到 NameEn 降级。
type LabelDictionary struct {
	BaseModel
	Key        string `gorm:"type:varchar(50);uniqueIndex;not null"                    json:"key"`
	NameZhCN   string `gorm:"type:varchar(100);not null"                               json:"name_zh_cn"`
	NameZhTW   string `gorm:"type:varchar(100)"                                         json:"name_zh_tw,omitempty"`
	NameEn     string `gorm:"type:varchar(100);not null"                               json:"name_en"`
	NameJa     string `gorm:"type:varchar(100)"                                         json:"name_ja,omitempty"`
	NameKo     string `gorm:"type:varchar(100)"                                         json:"name_ko,omitempty"`
	NameEs     string `gorm:"type:varchar(100)"                                         json:"name_es,omitempty"`
	NameFr     string `gorm:"type:varchar(100)"                                         json:"name_fr,omitempty"`
	NameDe     string `gorm:"type:varchar(100)"                                         json:"name_de,omitempty"`
	NameRu     string `gorm:"type:varchar(100)"                                         json:"name_ru,omitempty"`
	NameAr     string `gorm:"type:varchar(100)"                                         json:"name_ar,omitempty"`
	NamePt     string `gorm:"type:varchar(100)"                                         json:"name_pt,omitempty"`
	NameVi     string `gorm:"type:varchar(100)"                                         json:"name_vi,omitempty"`
	NameTh     string `gorm:"type:varchar(100)"                                         json:"name_th,omitempty"`
	NameId     string `gorm:"type:varchar(100)"                                         json:"name_id,omitempty"`
	NameHi     string `gorm:"type:varchar(100)"                                         json:"name_hi,omitempty"`
	NameIt     string `gorm:"type:varchar(100)"                                         json:"name_it,omitempty"`
	NameNl     string `gorm:"type:varchar(100)"                                         json:"name_nl,omitempty"`
	NamePl     string `gorm:"type:varchar(100)"                                         json:"name_pl,omitempty"`
	NameTr     string `gorm:"type:varchar(100)"                                         json:"name_tr,omitempty"`
	NameMs     string `gorm:"type:varchar(100)"                                         json:"name_ms,omitempty"`
	NameFil    string `gorm:"type:varchar(100)"                                         json:"name_fil,omitempty"`
	NameHe     string `gorm:"type:varchar(100)"                                         json:"name_he,omitempty"`
	NameFa     string `gorm:"type:varchar(100)"                                         json:"name_fa,omitempty"`

	// --- 视觉样式 ---
	Color string `gorm:"type:varchar(20);default:'gray'" json:"color"` // Tailwind 色板值（red/amber/emerald/blue/purple/gray）
	Icon  string `gorm:"type:varchar(50)"                 json:"icon,omitempty"` // Lucide 图标名（flame/sparkles/gift 等）

	// --- 分类与排序 ---
	Category  string `gorm:"type:varchar(20);default:'user';index" json:"category"`  // user(用户可见) / pricing(价格相关) / brand(供应商品牌) / system(内部)
	Priority  int    `gorm:"default:0;index"                       json:"priority"`  // 排序优先级（热卖=100 > 优惠=80 > 新品=70 > 免费=60）
	IsActive  bool   `gorm:"default:true"                          json:"is_active"`
	SortOrder int    `gorm:"default:0"                             json:"sort_order"`
}

// TableName 返回表名
func (LabelDictionary) TableName() string { return "label_dictionary" }

// LocalizedName 返回指定 locale 的名称，缺失时降级到英文
// locale 示例：zh / zh-CN / zh-TW / en / ja / ko / es / fr / de / ru / ar 等
func (l LabelDictionary) LocalizedName(locale string) string {
	// 规范化 locale
	switch locale {
	case "zh", "zh-CN", "zh-Hans", "cn":
		if l.NameZhCN != "" {
			return l.NameZhCN
		}
	case "zh-TW", "zh-Hant", "tw":
		if l.NameZhTW != "" {
			return l.NameZhTW
		}
		if l.NameZhCN != "" {
			return l.NameZhCN
		}
	case "ja":
		if l.NameJa != "" {
			return l.NameJa
		}
	case "ko":
		if l.NameKo != "" {
			return l.NameKo
		}
	case "es":
		if l.NameEs != "" {
			return l.NameEs
		}
	case "fr":
		if l.NameFr != "" {
			return l.NameFr
		}
	case "de":
		if l.NameDe != "" {
			return l.NameDe
		}
	case "ru":
		if l.NameRu != "" {
			return l.NameRu
		}
	case "ar":
		if l.NameAr != "" {
			return l.NameAr
		}
	case "pt":
		if l.NamePt != "" {
			return l.NamePt
		}
	case "vi":
		if l.NameVi != "" {
			return l.NameVi
		}
	case "th":
		if l.NameTh != "" {
			return l.NameTh
		}
	case "id":
		if l.NameId != "" {
			return l.NameId
		}
	case "hi":
		if l.NameHi != "" {
			return l.NameHi
		}
	case "it":
		if l.NameIt != "" {
			return l.NameIt
		}
	case "nl":
		if l.NameNl != "" {
			return l.NameNl
		}
	case "pl":
		if l.NamePl != "" {
			return l.NamePl
		}
	case "tr":
		if l.NameTr != "" {
			return l.NameTr
		}
	case "ms":
		if l.NameMs != "" {
			return l.NameMs
		}
	case "fil":
		if l.NameFil != "" {
			return l.NameFil
		}
	case "he":
		if l.NameHe != "" {
			return l.NameHe
		}
	case "fa":
		if l.NameFa != "" {
			return l.NameFa
		}
	}
	return l.NameEn
}

// PickNameColumn 返回 GORM SELECT 时按 locale 选择的列表达式
// 用于 JOIN label_dictionary 时的 SQL SELECT 子句生成
//
// 示例：
//   col := PickNameColumn("zh-CN") // "name_zh_cn"
//   "SELECT ld." + col + " AS name ..."
func PickNameColumn(locale string) string {
	switch locale {
	case "zh", "zh-CN", "zh-Hans", "cn":
		return "name_zh_cn"
	case "zh-TW", "zh-Hant", "tw":
		return "name_zh_tw"
	case "ja":
		return "name_ja"
	case "ko":
		return "name_ko"
	case "es":
		return "name_es"
	case "fr":
		return "name_fr"
	case "de":
		return "name_de"
	case "ru":
		return "name_ru"
	case "ar":
		return "name_ar"
	case "pt":
		return "name_pt"
	case "vi":
		return "name_vi"
	case "th":
		return "name_th"
	case "id":
		return "name_id"
	case "hi":
		return "name_hi"
	case "it":
		return "name_it"
	case "nl":
		return "name_nl"
	case "pl":
		return "name_pl"
	case "tr":
		return "name_tr"
	case "ms":
		return "name_ms"
	case "fil":
		return "name_fil"
	case "he":
		return "name_he"
	case "fa":
		return "name_fa"
	}
	return "name_en"
}
