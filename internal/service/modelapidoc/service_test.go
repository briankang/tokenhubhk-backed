package modelapidoc

import (
	"context"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func newTestService(t *testing.T) (*Service, *gorm.DB) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Supplier{}, &model.ModelCategory{}, &model.AIModel{}, &model.ModelAPIDoc{}, &model.ModelAPIDocSource{}, &model.ModelAPIParamVerification{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(db), db
}

func seedDoc(t *testing.T, db *gorm.DB) model.ModelAPIDoc {
	t.Helper()
	sup := model.Supplier{Name: "OpenAI", Code: "openai", IsActive: true, AccessType: "api"}
	if err := db.Create(&sup).Error; err != nil {
		t.Fatalf("create supplier: %v", err)
	}
	cat := model.ModelCategory{Name: "Chat", Code: "chat", SupplierID: sup.ID}
	if err := db.Create(&cat).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	m := model.AIModel{SupplierID: sup.ID, CategoryID: cat.ID, ModelName: "gpt-4o-mini", DisplayName: "GPT-4o Mini", IsActive: true, Status: "online", ModelType: "LLM"}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("create model: %v", err)
	}
	now := time.Now().UTC()
	doc := model.ModelAPIDoc{
		SupplierID:     sup.ID,
		ModelID:        &m.ID,
		Slug:           "openai-gpt-4o-mini",
		Locale:         "zh",
		Title:          "GPT-4o Mini API 鏂囨。",
		Summary:        "TokenHubHK developer doc",
		ModelName:      m.ModelName,
		ModelType:      "LLM",
		Status:         model.ModelAPIDocStatusPublished,
		IsPublished:    true,
		EndpointPath:   "/v1/chat/completions",
		TokenHubAuth:   "Authorization: Bearer sk-your-tokenhubhk-key",
		PublicOverview: "Use TokenHubHK auth only.",
		DeveloperGuide: "Use TokenHubHK credentials only.",
		FAQs:           model.JSON(`[{"question":"How do I report a problem?","answer":"Use API interface feedback."}]`),
		VerifiedAt:     &now,
	}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	source := model.ModelAPIDocSource{DocID: doc.ID, ProviderName: "OpenAI", SourceTitle: "Official", SourceURL: "https://platform.openai.com/docs/api-reference/chat/create-chat-completion", OriginalAuthSummary: "Bearer upstream key", VerificationStatus: model.ParamSupportOfficialConfirmed}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}
	param := model.ModelAPIParamVerification{DocID: doc.ID, TokenHubParam: "model", ProviderParam: "model", ParamType: "string", Required: true, SupportStatus: model.ParamSupportOfficialConfirmed, VerificationStatus: model.ParamSupportOfficialConfirmed}
	if err := db.Create(&param).Error; err != nil {
		t.Fatalf("create param verification: %v", err)
	}
	return doc
}

func TestPublicDocHidesAdminSources(t *testing.T) {
	svc, db := newTestService(t)
	doc := seedDoc(t, db)
	got, err := svc.GetPublicBySlug(context.Background(), doc.Slug)
	if err != nil {
		t.Fatalf("GetPublicBySlug: %v", err)
	}
	if got.TokenHubAuth != "Authorization: Bearer sk-your-tokenhubhk-key" {
		t.Fatalf("expected TokenHubHK auth, got %q", got.TokenHubAuth)
	}
	if len(got.ParamVerifications) != 1 || got.ParamVerifications[0].TokenHubParam != "model" {
		t.Fatalf("expected public param verification, got %#v", got.ParamVerifications)
	}
	if string(got.FAQs) == "" || !strings.Contains(string(got.FAQs), "API interface feedback") {
		t.Fatalf("expected public FAQs, got %s", string(got.FAQs))
	}
}

func TestAdminDocIncludesOfficialSources(t *testing.T) {
	svc, db := newTestService(t)
	doc := seedDoc(t, db)
	got, err := svc.GetAdminByID(context.Background(), doc.ID)
	if err != nil {
		t.Fatalf("GetAdminByID: %v", err)
	}
	if len(got.Sources) != 1 {
		t.Fatalf("expected one source, got %d", len(got.Sources))
	}
	if got.Sources[0].SourceURL == "" || got.Sources[0].OriginalAuthSummary == "" {
		t.Fatalf("expected source URL and original auth summary, got %#v", got.Sources[0])
	}
}

func TestListPublicFiltersUnpublishedDocs(t *testing.T) {
	svc, db := newTestService(t)
	doc := seedDoc(t, db)
	draft := doc
	draft.ID = 0
	draft.Slug = "draft-doc"
	draft.Status = model.ModelAPIDocStatusDraft
	draft.IsPublished = false
	if err := db.Create(&draft).Error; err != nil {
		t.Fatalf("create draft: %v", err)
	}
	items, total, err := svc.ListPublic(context.Background(), ListOptions{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListPublic: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Slug != doc.Slug {
		t.Fatalf("expected only published doc, total=%d items=%#v", total, items)
	}
}

func TestListPublicLocaleFallback(t *testing.T) {
	svc, db := newTestService(t)
	doc := seedDoc(t, db)

	docs, total, err := svc.ListPublic(context.Background(), ListOptions{Locale: "en-US", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListPublic returned error: %v", err)
	}
	if total != 1 || len(docs) != 1 {
		t.Fatalf("expected zh fallback doc, total=%d docs=%d", total, len(docs))
	}
	if docs[0].Locale != "zh" || docs[0].Slug != doc.Slug {
		t.Fatalf("expected zh fallback doc, got locale=%q slug=%q", docs[0].Locale, docs[0].Slug)
	}
}
