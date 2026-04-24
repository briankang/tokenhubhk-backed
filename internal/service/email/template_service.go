package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"strings"
	texttemplate "text/template"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/dbctx"
)

// VariableDef 模板变量元数据
type VariableDef struct {
	Key      string `json:"key"`
	Desc     string `json:"desc"`
	Example  string `json:"example"`
	Required bool   `json:"required"`
}

// TemplateService 模板管理
type TemplateService struct {
	db *gorm.DB
}

// NewTemplateService 构造
func NewTemplateService(db *gorm.DB) *TemplateService {
	return &TemplateService{db: db}
}

// List 列出模板，支持按 category / channel / keyword 过滤
func (s *TemplateService) List(ctx context.Context, category, channel, keyword string, page, pageSize int) ([]model.EmailTemplate, int64, error) {
	cctx, cancel := dbctx.Medium(ctx)
	defer cancel()
	tx := s.db.WithContext(cctx).Model(&model.EmailTemplate{})
	if category != "" {
		tx = tx.Where("category = ?", category)
	}
	if channel != "" {
		tx = tx.Where("channel = ?", channel)
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		tx = tx.Where("code LIKE ? OR name LIKE ?", like, like)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	var list []model.EmailTemplate
	if err := tx.Order("is_system DESC, id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// Get 根据 ID 取模板
func (s *TemplateService) Get(ctx context.Context, id uint) (*model.EmailTemplate, error) {
	cctx, cancel := dbctx.Short(ctx)
	defer cancel()
	var t model.EmailTemplate
	if err := s.db.WithContext(cctx).First(&t, id).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// GetByCode 按 code 取模板（业务代码使用）
func (s *TemplateService) GetByCode(ctx context.Context, code string) (*model.EmailTemplate, error) {
	cctx, cancel := dbctx.Short(ctx)
	defer cancel()
	var t model.EmailTemplate
	if err := s.db.WithContext(cctx).Where("code = ? AND is_active = ?", code, true).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// NormalizeLang 规范化语言标识 → 与模板 code 后缀对齐
//
//	en / en-US / en-GB            → "en"
//	zh-TW / zh-HK / zh-MO / zh-Hant → "zh_TW"
//	zh / zh-CN / zh-Hans           → "zh"
//	其他（ja/ko/fr/...）            → "en"  (默认英文)
func NormalizeLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	l = strings.ReplaceAll(l, "_", "-")
	if l == "" {
		return "en"
	}
	if strings.HasPrefix(l, "zh") {
		if strings.Contains(l, "tw") || strings.Contains(l, "hk") || strings.Contains(l, "mo") || strings.Contains(l, "hant") {
			return "zh_TW"
		}
		return "zh"
	}
	if strings.HasPrefix(l, "en") {
		return "en"
	}
	return "en" // 默认英文
}

// GetByCodeWithLang 按 code + lang 查模板，fallback 链：
//
//	{code}_{lang} → {code}_en → {code}
func (s *TemplateService) GetByCodeWithLang(ctx context.Context, code, lang string) (*model.EmailTemplate, error) {
	normalized := NormalizeLang(lang)
	candidates := []string{}
	if normalized != "" {
		candidates = append(candidates, code+"_"+normalized)
	}
	if normalized != "en" {
		candidates = append(candidates, code+"_en")
	}
	candidates = append(candidates, code)

	for _, c := range candidates {
		t, err := s.GetByCode(ctx, c)
		if err == nil {
			return t, nil
		}
	}
	// 回退都失败：返回最后一次 GetByCode 错误信息给调用方
	return s.GetByCode(ctx, code)
}

// CreateRequest 创建请求
type CreateRequest struct {
	Code            string
	Name            string
	Category        string
	Channel         string
	Subject         string
	HTMLBody        string
	TextBody        string
	VariablesSchema []VariableDef
	OperatorID      uint
	Remark          string
}

// Create 创建模板
func (s *TemplateService) Create(ctx context.Context, req CreateRequest) (*model.EmailTemplate, error) {
	if req.Code == "" || req.Name == "" || req.Subject == "" {
		return nil, errors.New("code/name/subject required")
	}
	if err := validateTemplateSyntax(req.Subject, req.HTMLBody, req.TextBody); err != nil {
		return nil, fmt.Errorf("template syntax error: %w", err)
	}

	varsJSON, _ := json.Marshal(req.VariablesSchema)
	if req.Category == "" {
		req.Category = model.EmailCategoryTransactional
	}
	if req.Channel == "" {
		req.Channel = model.EmailChannelAuto
	}

	t := &model.EmailTemplate{
		Code:            req.Code,
		Name:            req.Name,
		Category:        req.Category,
		Channel:         req.Channel,
		Subject:         req.Subject,
		HTMLBody:        req.HTMLBody,
		TextBody:        req.TextBody,
		VariablesSchema: varsJSON,
		IsActive:        true,
		CreatedBy:       req.OperatorID,
		UpdatedBy:       req.OperatorID,
		Remark:          req.Remark,
	}

	cctx, cancel := dbctx.Short(ctx)
	defer cancel()
	if err := s.db.WithContext(cctx).Create(t).Error; err != nil {
		return nil, err
	}
	return t, nil
}

// UpdateRequest 更新请求
type UpdateRequest struct {
	Name            *string
	Category        *string
	Channel         *string
	Subject         *string
	HTMLBody        *string
	TextBody        *string
	VariablesSchema *[]VariableDef
	IsActive        *bool
	Remark          *string
	OperatorID      uint
}

// Update 更新模板（系统预设模板：Code 不可改）
func (s *TemplateService) Update(ctx context.Context, id uint, req UpdateRequest) (*model.EmailTemplate, error) {
	t, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// 先拿候选值做语法校验
	sub := t.Subject
	html := t.HTMLBody
	text := t.TextBody
	if req.Subject != nil {
		sub = *req.Subject
	}
	if req.HTMLBody != nil {
		html = *req.HTMLBody
	}
	if req.TextBody != nil {
		text = *req.TextBody
	}
	if err := validateTemplateSyntax(sub, html, text); err != nil {
		return nil, fmt.Errorf("template syntax error: %w", err)
	}

	updates := map[string]interface{}{
		"updated_by": req.OperatorID,
	}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Category != nil {
		updates["category"] = *req.Category
	}
	if req.Channel != nil {
		updates["channel"] = *req.Channel
	}
	if req.Subject != nil {
		updates["subject"] = *req.Subject
	}
	if req.HTMLBody != nil {
		updates["html_body"] = *req.HTMLBody
	}
	if req.TextBody != nil {
		updates["text_body"] = *req.TextBody
	}
	if req.VariablesSchema != nil {
		b, _ := json.Marshal(*req.VariablesSchema)
		updates["variables_schema"] = b
	}
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}
	if req.Remark != nil {
		updates["remark"] = *req.Remark
	}

	cctx, cancel := dbctx.Short(ctx)
	defer cancel()
	if err := s.db.WithContext(cctx).Model(&model.EmailTemplate{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Delete 删除模板（系统预设模板禁止删除）
func (s *TemplateService) Delete(ctx context.Context, id uint) error {
	t, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if t.IsSystem {
		return errors.New("system template cannot be deleted")
	}
	cctx, cancel := dbctx.Short(ctx)
	defer cancel()
	return s.db.WithContext(cctx).Delete(&model.EmailTemplate{}, id).Error
}

// RenderResult 渲染结果
type RenderResult struct {
	Subject  string
	HTMLBody string
	TextBody string
}

// Render 使用给定变量渲染模板
func (s *TemplateService) Render(t *model.EmailTemplate, vars map[string]any) (*RenderResult, error) {
	if vars == nil {
		vars = map[string]any{}
	}
	// 必填变量校验
	if err := s.validateRequiredVars(t, vars); err != nil {
		return nil, err
	}
	subject, err := renderText(t.Subject, vars)
	if err != nil {
		return nil, fmt.Errorf("render subject: %w", err)
	}
	htmlBody, err := renderHTML(t.HTMLBody, vars)
	if err != nil {
		return nil, fmt.Errorf("render html: %w", err)
	}
	textBody := ""
	if strings.TrimSpace(t.TextBody) != "" {
		textBody, err = renderText(t.TextBody, vars)
		if err != nil {
			return nil, fmt.Errorf("render text: %w", err)
		}
	}
	return &RenderResult{Subject: subject, HTMLBody: htmlBody, TextBody: textBody}, nil
}

// ParseVariableSchema 解析存储的变量元数据
func (s *TemplateService) ParseVariableSchema(t *model.EmailTemplate) ([]VariableDef, error) {
	if len(t.VariablesSchema) == 0 {
		return nil, nil
	}
	var defs []VariableDef
	if err := json.Unmarshal(t.VariablesSchema, &defs); err != nil {
		return nil, err
	}
	return defs, nil
}

// validateRequiredVars 校验必填变量
func (s *TemplateService) validateRequiredVars(t *model.EmailTemplate, vars map[string]any) error {
	defs, err := s.ParseVariableSchema(t)
	if err != nil {
		return nil // schema 解析失败不阻塞渲染
	}
	var missing []string
	for _, d := range defs {
		if !d.Required {
			continue
		}
		v, ok := vars[d.Key]
		if !ok || v == nil || fmt.Sprintf("%v", v) == "" {
			missing = append(missing, d.Key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required variables: %s", strings.Join(missing, ", "))
	}
	return nil
}

// validateTemplateSyntax 用 text/template 解析主题，html/template 解析 HTML，text/template 解析纯文本
func validateTemplateSyntax(subject, html, text string) error {
	if _, err := texttemplate.New("subject").Parse(subject); err != nil {
		return fmt.Errorf("subject: %w", err)
	}
	if html != "" {
		if _, err := template.New("html").Parse(html); err != nil {
			return fmt.Errorf("html: %w", err)
		}
	}
	if text != "" {
		if _, err := texttemplate.New("text").Parse(text); err != nil {
			return fmt.Errorf("text: %w", err)
		}
	}
	return nil
}

// renderText 渲染纯文本（不转义）
func renderText(tpl string, vars map[string]any) (string, error) {
	t, err := texttemplate.New("t").Option("missingkey=zero").Parse(tpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, vars); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// renderHTML 渲染 HTML（自动 XSS 转义）
func renderHTML(tpl string, vars map[string]any) (string, error) {
	t, err := template.New("h").Option("missingkey=zero").Parse(tpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, vars); err != nil {
		return "", err
	}
	return buf.String(), nil
}
