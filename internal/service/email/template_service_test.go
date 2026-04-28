package email

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
)

func newEmailTemplateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", name)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.EmailTemplate{}); err != nil {
		t.Fatalf("migrate email templates: %v", err)
	}
	return db
}

func TestEmailTemplateSyntaxRequiredVariablesAndEscaping(t *testing.T) {
	db := newEmailTemplateTestDB(t)
	svc := NewTemplateService(db)
	ctx := context.Background()

	if _, err := svc.Create(ctx, CreateRequest{
		Code:     "bad_syntax",
		Name:     "Bad Syntax",
		Subject:  "Hello {{",
		HTMLBody: "<p>broken</p>",
	}); err == nil || !strings.Contains(err.Error(), "template syntax") {
		t.Fatalf("invalid template syntax error = %v, want template syntax", err)
	}

	tpl, err := svc.Create(ctx, CreateRequest{
		Code:     "welcome",
		Name:     "Welcome",
		Subject:  "Hi {{.Name}}",
		HTMLBody: "<p>{{.Name}}</p>",
		TextBody: "Hi {{.Name}}",
		VariablesSchema: []VariableDef{
			{Key: "Name", Required: true},
		},
	})
	if err != nil {
		t.Fatalf("Create valid template: %v", err)
	}
	if _, err := svc.Render(tpl, map[string]any{}); err == nil || !strings.Contains(err.Error(), "missing required variables") {
		t.Fatalf("missing variable error = %v, want missing required variables", err)
	}
	rendered, err := svc.Render(tpl, map[string]any{"Name": "<script>alert(1)</script>"})
	if err != nil {
		t.Fatalf("Render valid template: %v", err)
	}
	if !strings.Contains(rendered.Subject, "<script>") {
		t.Fatalf("text subject should preserve text value, got %q", rendered.Subject)
	}
	if strings.Contains(rendered.HTMLBody, "<script>") || !strings.Contains(rendered.HTMLBody, "&lt;script&gt;") {
		t.Fatalf("html body should escape script tag, got %q", rendered.HTMLBody)
	}
}

func TestEmailTemplateLanguageFallbackAndSystemDeleteGuard(t *testing.T) {
	db := newEmailTemplateTestDB(t)
	svc := NewTemplateService(db)
	ctx := context.Background()

	if _, err := svc.Create(ctx, CreateRequest{Code: "notice", Name: "Base", Subject: "Base"}); err != nil {
		t.Fatalf("create base template: %v", err)
	}
	en, err := svc.Create(ctx, CreateRequest{Code: "notice_en", Name: "English", Subject: "English"})
	if err != nil {
		t.Fatalf("create english template: %v", err)
	}
	zhTW, err := svc.Create(ctx, CreateRequest{Code: "notice_zh_TW", Name: "Traditional Chinese", Subject: "Traditional"})
	if err != nil {
		t.Fatalf("create zh_TW template: %v", err)
	}

	got, err := svc.GetByCodeWithLang(ctx, "notice", "fr-FR")
	if err != nil {
		t.Fatalf("GetByCodeWithLang fr fallback: %v", err)
	}
	if got.ID != en.ID {
		t.Fatalf("fr fallback id=%d, want english id=%d", got.ID, en.ID)
	}
	got, err = svc.GetByCodeWithLang(ctx, "notice", "zh-HK")
	if err != nil {
		t.Fatalf("GetByCodeWithLang zh-HK fallback: %v", err)
	}
	if got.ID != zhTW.ID {
		t.Fatalf("zh-HK fallback id=%d, want zh_TW id=%d", got.ID, zhTW.ID)
	}

	if err := db.Model(&model.EmailTemplate{}).Where("id = ?", en.ID).Update("is_system", true).Error; err != nil {
		t.Fatalf("mark system template: %v", err)
	}
	if err := svc.Delete(ctx, en.ID); err == nil || !strings.Contains(err.Error(), "system template") {
		t.Fatalf("delete system template error = %v, want system template", err)
	}
}
