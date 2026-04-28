package database

import (
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestSeedModelAPIDocsKeepsPublicCopyUserFacing(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:model_api_docs_seed?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Supplier{},
		&model.ModelCategory{},
		&model.AIModel{},
		&model.ModelAPIDoc{},
		&model.ModelAPIDocSource{},
		&model.ModelAPIParamVerification{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	sup := model.Supplier{Name: "OpenAI", Code: "openai", IsActive: true, AccessType: "api"}
	if err := db.Create(&sup).Error; err != nil {
		t.Fatalf("create supplier: %v", err)
	}
	cat := model.ModelCategory{Name: "Chat", Code: "chat", SupplierID: sup.ID}
	if err := db.Create(&cat).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	aiModel := model.AIModel{
		SupplierID:    sup.ID,
		CategoryID:    cat.ID,
		ModelName:     "gpt-4o-mini",
		DisplayName:   "GPT-4o Mini",
		IsActive:      true,
		Status:        "online",
		ModelType:     model.ModelTypeLLM,
		MaxTokens:     4096,
		ContextWindow: 128000,
	}
	if err := db.Create(&aiModel).Error; err != nil {
		t.Fatalf("create model: %v", err)
	}

	if err := seedModelAPIDocs(db); err != nil {
		t.Fatalf("seed model api docs: %v", err)
	}

	var doc model.ModelAPIDoc
	if err := db.Where("model_name = ? AND locale = ?", aiModel.ModelName, "zh").First(&doc).Error; err != nil {
		t.Fatalf("find model api doc: %v", err)
	}
	publicCopy := strings.ToLower(strings.Join([]string{
		doc.Title,
		doc.Summary,
		doc.PublicOverview,
		doc.DeveloperGuide,
		string(doc.RequestSchema),
		string(doc.ResponseSchema),
		string(doc.StreamSchema),
		string(doc.ParameterMappings),
		string(doc.CodeExamples),
		string(doc.FAQs),
		string(doc.VerificationSummary),
	}, "\n"))
	for _, forbidden := range []string{"provider_extra", "upstream", "supplier-specific", "vendor-specific", "admin verification"} {
		if strings.Contains(publicCopy, forbidden) {
			t.Fatalf("public model API doc still contains %q", forbidden)
		}
	}

	var params []model.ModelAPIParamVerification
	if err := db.Where("doc_id = ?", doc.ID).Find(&params).Error; err != nil {
		t.Fatalf("list params: %v", err)
	}
	for _, param := range params {
		combined := strings.ToLower(param.TokenHubParam + " " + param.PlatformBehavior + " " + param.TestPayloadSummary)
		if strings.Contains(combined, "provider_extra") || strings.Contains(combined, "upstream") || strings.Contains(combined, "supplier") {
			t.Fatalf("public param verification leaks internal wording: %#v", param)
		}
	}
}

func TestSeedModelAPIDocsIncludesResponseExamplesAndFAQs(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:model_api_docs_response_examples?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Supplier{},
		&model.ModelCategory{},
		&model.AIModel{},
		&model.ModelAPIDoc{},
		&model.ModelAPIDocSource{},
		&model.ModelAPIParamVerification{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sup := model.Supplier{Name: "Alibaba Cloud", Code: "aliyun_dashscope", IsActive: true, AccessType: "api"}
	if err := db.Create(&sup).Error; err != nil {
		t.Fatalf("create supplier: %v", err)
	}
	cat := model.ModelCategory{Name: "Chat", Code: "chat", SupplierID: sup.ID}
	if err := db.Create(&cat).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	aiModel := model.AIModel{
		SupplierID:    sup.ID,
		CategoryID:    cat.ID,
		ModelName:     "qwen-plus",
		DisplayName:   "Qwen Plus",
		IsActive:      true,
		Status:        "online",
		ModelType:     model.ModelTypeLLM,
		MaxTokens:     8192,
		ContextWindow: 131072,
	}
	if err := db.Create(&aiModel).Error; err != nil {
		t.Fatalf("create model: %v", err)
	}

	if err := seedModelAPIDocs(db); err != nil {
		t.Fatalf("seed model api docs: %v", err)
	}

	var doc model.ModelAPIDoc
	if err := db.Where("model_name = ? AND locale = ?", aiModel.ModelName, "zh").First(&doc).Error; err != nil {
		t.Fatalf("find model api doc: %v", err)
	}
	response := string(doc.ResponseSchema)
	for _, want := range []string{"non_stream_example", "chat.completion", "choices", "message", "usage"} {
		if !strings.Contains(response, want) {
			t.Fatalf("response schema missing %q: %s", want, response)
		}
	}
	stream := string(doc.StreamSchema)
	for _, want := range []string{"stream_example", "chat.completion.chunk", "data: [DONE]", "stream_options.include_usage"} {
		if !strings.Contains(stream, want) {
			t.Fatalf("stream schema missing %q: %s", want, stream)
		}
	}
	faqs := string(doc.FAQs)
	for _, want := range []string{"TokenHubHK", "streaming", "enable_search", "qwen-plus"} {
		if !strings.Contains(faqs, want) {
			t.Fatalf("FAQ payload missing %q: %s", want, faqs)
		}
	}
}
