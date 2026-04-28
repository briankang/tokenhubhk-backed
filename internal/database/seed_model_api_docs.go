package database

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

type modelDocProviderProfile struct {
	Key                 string
	ProviderName        string
	SourceTitle         string
	SourceURL           string
	OriginalEndpoint    string
	OriginalAuthSummary string
	Overview            string
	Differentiators     []string
	SupportsThinking    bool
	ThinkingParam       string
	SupportsSearch      bool
	SearchParam         string
	SupportsTools       bool
	SupportsJSON        bool
	SupportsVision      bool
	SupportsStream      bool
	AdminNotes          string
}

func RunSeedModelAPIDocs(db *gorm.DB) {
	if db == nil {
		return
	}
	if err := seedModelAPIDocs(db); err != nil {
		logger.L.Warn("seed model api docs failed", zap.Error(err))
	}
}

func seedModelAPIDocs(db *gorm.DB) error {
	if err := ensureModelAPIDocSchema(db); err != nil {
		return err
	}
	now := time.Now().UTC()
	var models []model.AIModel
	if err := db.Preload("Supplier").Where("is_active = ?", true).Find(&models).Error; err != nil {
		return err
	}
	for _, m := range models {
		if m.Supplier.ID == 0 {
			continue
		}
		profile := providerProfileFor(m.Supplier.Code, m.ModelName)
		doc, err := buildModelAPIDoc(m, profile, now)
		if err != nil {
			return err
		}
		var existing model.ModelAPIDoc
		err = db.Where("slug = ? AND locale = ?", doc.Slug, doc.Locale).First(&existing).Error
		if err == gorm.ErrRecordNotFound {
			if err := db.Create(&doc).Error; err != nil {
				return fmt.Errorf("create model api doc %s: %w", doc.Slug, err)
			}
			existing = doc
		} else if err != nil {
			return err
		} else {
			updates := map[string]interface{}{
				"supplier_id":          doc.SupplierID,
				"model_id":             doc.ModelID,
				"title":                doc.Title,
				"summary":              doc.Summary,
				"model_name":           doc.ModelName,
				"model_type":           doc.ModelType,
				"endpoint_path":        doc.EndpointPath,
				"token_hub_auth":       doc.TokenHubAuth,
				"public_overview":      doc.PublicOverview,
				"developer_guide":      doc.DeveloperGuide,
				"capability_matrix":    doc.CapabilityMatrix,
				"request_schema":       doc.RequestSchema,
				"response_schema":      doc.ResponseSchema,
				"stream_schema":        doc.StreamSchema,
				"parameter_mappings":   doc.ParameterMappings,
				"code_examples":        doc.CodeExamples,
				"faqs":                 doc.FAQs,
				"verification_summary": doc.VerificationSummary,
				"verified_at":          doc.VerifiedAt,
				"status":               doc.Status,
				"is_published":         doc.IsPublished,
			}
			if err := db.Model(&model.ModelAPIDoc{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
				return fmt.Errorf("update model api doc %s: %w", doc.Slug, err)
			}
		}
		if err := replaceModelAPIDocSources(db, existing.ID, profile, now); err != nil {
			return err
		}
		if err := replaceModelAPIParamVerifications(db, existing.ID, m, profile, now); err != nil {
			return err
		}
	}
	return nil
}

func ensureModelAPIDocSchema(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(&model.ModelAPIDoc{}) {
		return nil
	}
	if !db.Migrator().HasColumn(&model.ModelAPIDoc{}, "faqs") {
		if err := db.Migrator().AddColumn(&model.ModelAPIDoc{}, "FAQs"); err != nil {
			return fmt.Errorf("add model_api_docs.faqs column: %w", err)
		}
	}
	return nil
}

func buildModelAPIDoc(m model.AIModel, p modelDocProviderProfile, checkedAt time.Time) (model.ModelAPIDoc, error) {
	modelID := m.ID
	capabilities := buildCapabilityMatrix(m, p)
	requestSchema := map[string]interface{}{
		"base_url": "https://www.tokenhubhk.com",
		"method":   "POST",
		"path":     "/v1/chat/completions",
		"headers": map[string]string{
			"Authorization": "Bearer sk-your-tokenhubhk-key",
			"Content-Type":  "application/json",
		},
		"required":            []string{"model", "messages"},
		"standard_params":     []string{"model", "messages", "temperature", "top_p", "max_tokens", "stream", "tools", "tool_choice", "response_format"},
		"tokenhub_extensions": []string{"reasoning_effort", "enable_thinking", "thinking_budget", "enable_search", "extra_body", "custom_params"},
	}
	responseSchema := buildResponseSchema(m, p)
	streamSchema := buildStreamSchema(m, p)
	paramMappings := map[string]interface{}{
		"auth":        "Always use TokenHubHK Authorization: Bearer sk-...; keys and signatures from other platforms are never required in user requests.",
		"thinking":    map[string]interface{}{"supported": p.SupportsThinking, "tokenhub_param": preferredThinkingParam(p), "provider_param": p.ThinkingParam},
		"search":      map[string]interface{}{"supported": p.SupportsSearch, "tokenhub_param": "enable_search", "provider_param": p.SearchParam},
		"passthrough": []string{"extra_body", "custom_params"},
	}
	verifySummary := map[string]interface{}{
		"checked_at":       checkedAt.Format("2006-01-02"),
		"standard":         "Core chat parameters are confirmed against official model documentation. Model-specific controls are marked per model and must pass runtime checks before being upgraded to runtime_verified.",
		"runtime_verified": false,
	}
	publicOverview := fmt.Sprintf("%s\n\nTokenHubHK exposes %s through the OpenAI-compatible `/v1/chat/completions` endpoint. Developers only need a TokenHubHK API key; do not use other platform keys, x-api-key headers, or signatures in client code.", p.Overview, m.ModelName)
	developerGuide := buildDeveloperGuide(m, p)
	examples := buildCodeExamples(m.ModelName)
	faqs := buildModelAPIDocFAQs(m, p)
	doc := model.ModelAPIDoc{
		SupplierID:          m.SupplierID,
		ModelID:             &modelID,
		Slug:                slugify(m.Supplier.Code + "-" + m.ModelName),
		Locale:              "zh",
		Title:               fmt.Sprintf("%s API 文档", displayModelName(m)),
		Summary:             fmt.Sprintf("%s via TokenHubHK. 重点说明模型能力差异、参数映射、多语言代码示例和验证状态。", displayModelName(m)),
		ModelName:           m.ModelName,
		ModelType:           defaultString(m.ModelType, model.ModelTypeLLM),
		SortOrder:           int(m.ID),
		Status:              model.ModelAPIDocStatusPublished,
		IsPublished:         true,
		EndpointPath:        "/v1/chat/completions",
		TokenHubAuth:        "Authorization: Bearer sk-your-tokenhubhk-key",
		PublicOverview:      publicOverview,
		DeveloperGuide:      developerGuide,
		CapabilityMatrix:    mustMarshalJSON(capabilities),
		RequestSchema:       mustMarshalJSON(requestSchema),
		ResponseSchema:      mustMarshalJSON(responseSchema),
		StreamSchema:        mustMarshalJSON(streamSchema),
		ParameterMappings:   mustMarshalJSON(paramMappings),
		CodeExamples:        mustMarshalJSON(examples),
		FAQs:                mustMarshalJSON(faqs),
		VerificationSummary: mustMarshalJSON(verifySummary),
		VerifiedAt:          &checkedAt,
	}
	return doc, nil
}

func replaceModelAPIDocSources(db *gorm.DB, docID uint, p modelDocProviderProfile, checkedAt time.Time) error {
	if err := db.Unscoped().Where("doc_id = ?", docID).Delete(&model.ModelAPIDocSource{}).Error; err != nil {
		return err
	}
	source := model.ModelAPIDocSource{
		DocID:               docID,
		ProviderName:        p.ProviderName,
		SourceTitle:         p.SourceTitle,
		SourceURL:           p.SourceURL,
		OriginalEndpoint:    p.OriginalEndpoint,
		OriginalAuthSummary: p.OriginalAuthSummary,
		CheckedAt:           &checkedAt,
		VerificationStatus:  model.ParamSupportOfficialConfirmed,
		AdminNotes:          p.AdminNotes + " User-facing docs are rewritten to TokenHubHK Bearer auth.",
	}
	return db.Create(&source).Error
}

func replaceModelAPIParamVerifications(db *gorm.DB, docID uint, m model.AIModel, p modelDocProviderProfile, checkedAt time.Time) error {
	if err := db.Unscoped().Where("doc_id = ?", docID).Delete(&model.ModelAPIParamVerification{}).Error; err != nil {
		return err
	}
	items := []model.ModelAPIParamVerification{
		paramV(docID, "model", "model", "string", true, "", "", model.ParamSupportOfficialConfirmed, "Required TokenHubHK model id.", checkedAt),
		paramV(docID, "messages", "messages", "array", true, "", "OpenAI chat messages", model.ParamSupportOfficialConfirmed, "Multi-turn conversation history.", checkedAt),
		paramV(docID, "stream", "stream", "bool", false, "false", "true,false", model.ParamSupportOfficialConfirmed, "Streams OpenAI-compatible SSE chunks when supported by route.", checkedAt),
		paramV(docID, "temperature", "temperature", "float", false, "", "0-2; model-specific caps may apply", model.ParamSupportPlatformMapped, "Sampling parameter; TokenHubHK model docs are the source of truth.", checkedAt),
		paramV(docID, "top_p", "top_p", "float", false, "", "0-1", model.ParamSupportPlatformMapped, "Sampling parameter; platform mapping table is the source of truth.", checkedAt),
		paramV(docID, "max_tokens", "max_tokens", "int", false, "", "limited by model max output", model.ParamSupportOfficialConfirmed, "Use model output limits shown on TokenHubHK.", checkedAt),
		paramV(docID, "extra_body", "extra_body", "object", false, "{}", "JSON object", model.ParamSupportPlatformMapped, "Advanced model options merged after TokenHubHK standard validation.", checkedAt),
		paramV(docID, "custom_params", "custom_params", "object", false, "{}", "JSON object", model.ParamSupportPlatformMapped, "Custom key/value passthrough after standard validation.", checkedAt),
	}
	if p.SupportsThinking {
		items = append(items, paramV(docID, preferredThinkingParam(p), p.ThinkingParam, "string|bool|int|object", false, "", "", model.ParamSupportPlatformMapped, "Thinking control is normalized by TokenHubHK for this model.", checkedAt))
	} else {
		items = append(items, paramV(docID, "enable_thinking", p.ThinkingParam, "bool", false, "false", "true,false", model.ParamSupportUnsupported, "This model has no stable TokenHubHK thinking switch; choose a reasoning model if needed.", checkedAt))
	}
	if p.SupportsSearch {
		items = append(items, paramV(docID, "enable_search", p.SearchParam, "bool|object", false, "false", "true,false", model.ParamSupportPlatformMapped, "Search is normalized by TokenHubHK and follows this model's supported request shape.", checkedAt))
	} else {
		items = append(items, paramV(docID, "enable_search", p.SearchParam, "bool", false, "false", "true,false", model.ParamSupportUnsupported, "No stable search mapping is enabled for this model.", checkedAt))
	}
	for i := range items {
		items[i].VerificationStatus = items[i].SupportStatus
		items[i].VerifiedAt = &checkedAt
	}
	return db.CreateInBatches(items, 50).Error
}

func paramV(docID uint, tokenhubParam, providerParam, paramType string, required bool, def, allowed, status, behavior string, t time.Time) model.ModelAPIParamVerification {
	return model.ModelAPIParamVerification{
		DocID:              docID,
		TokenHubParam:      tokenhubParam,
		ProviderParam:      providerParam,
		ParamType:          paramType,
		Required:           required,
		DefaultValue:       def,
		AllowedValues:      allowed,
		SupportStatus:      status,
		VerificationStatus: status,
		PlatformBehavior:   behavior,
		TestPayloadSummary: "Minimal request and boundary request templates are generated in the docs examples. Runtime execution requires a configured model route.",
		TestResultSummary:  "Official documentation and TokenHubHK mapping reviewed; runtime status remains non-runtime_verified until a live channel check is recorded.",
		VerifiedAt:         &t,
	}
}

func buildCapabilityMatrix(m model.AIModel, p modelDocProviderProfile) map[string]interface{} {
	return map[string]interface{}{
		"text_chat":       true,
		"streaming":       p.SupportsStream,
		"thinking":        p.SupportsThinking,
		"thinking_param":  preferredThinkingParam(p),
		"web_search":      p.SupportsSearch,
		"search_param":    "enable_search",
		"tool_calling":    p.SupportsTools,
		"json_output":     p.SupportsJSON,
		"vision_input":    p.SupportsVision || strings.Contains(strings.ToLower(string(m.InputModalities)), "image"),
		"model_type":      defaultString(m.ModelType, model.ModelTypeLLM),
		"context_window":  firstPositive(m.ContextWindow, m.MaxInputTokens),
		"max_output":      firstPositive(m.MaxOutputTokens, m.MaxTokens),
		"differentiators": p.Differentiators,
	}
}

func buildResponseSchema(m model.AIModel, p modelDocProviderProfile) map[string]interface{} {
	content := "Hello from TokenHubHK."
	message := map[string]interface{}{
		"role":    "assistant",
		"content": content,
	}
	if p.SupportsThinking {
		message["reasoning_content"] = "Reasoning text may be returned only by thinking-capable models and only when the route preserves it."
	}
	return map[string]interface{}{
		"format": "OpenAI-compatible chat.completion",
		"fields": []map[string]string{
			{"path": "id", "type": "string", "description": "Unique request identifier generated for this completion."},
			{"path": "object", "type": "string", "description": "Always chat.completion for non-streaming chat responses."},
			{"path": "created", "type": "integer", "description": "Unix timestamp in seconds."},
			{"path": "model", "type": "string", "description": "TokenHubHK model ID used for routing and billing."},
			{"path": "choices[].message.role", "type": "string", "description": "Assistant role for the generated message."},
			{"path": "choices[].message.content", "type": "string", "description": "Final generated text for this choice."},
			{"path": "choices[].message.reasoning_content", "type": "string", "description": "Optional thinking output for supported reasoning models."},
			{"path": "choices[].finish_reason", "type": "string", "description": "stop, length, tool_calls, content_filter, or a compatible provider reason."},
			{"path": "usage.prompt_tokens", "type": "integer", "description": "Input tokens billed by the platform."},
			{"path": "usage.completion_tokens", "type": "integer", "description": "Output tokens billed by the platform."},
			{"path": "usage.total_tokens", "type": "integer", "description": "Total prompt and completion tokens."},
		},
		"non_stream_example": map[string]interface{}{
			"id":      "chatcmpl-tokenhubhk-example",
			"object":  "chat.completion",
			"created": 1710000000,
			"model":   m.ModelName,
			"choices": []map[string]interface{}{
				{
					"index":         0,
					"message":       message,
					"finish_reason": "stop",
					"logprobs":      nil,
				},
			},
			"usage": map[string]interface{}{
				"prompt_tokens":     22,
				"completion_tokens": 8,
				"total_tokens":      30,
				"prompt_tokens_details": map[string]interface{}{
					"cached_tokens": 0,
				},
			},
		},
		"notes": []string{
			"Non-streaming responses are returned as one JSON object.",
			"Use choices[0].message.content for normal text output.",
			"Always read usage from the response body for billing reconciliation when present.",
			"Tool-call arguments are model-generated strings and must be validated before execution.",
		},
	}
}

func buildStreamSchema(m model.AIModel, p modelDocProviderProfile) map[string]interface{} {
	firstDelta := map[string]interface{}{
		"role":    "assistant",
		"content": "",
	}
	if p.SupportsThinking {
		firstDelta["reasoning_content"] = ""
	}
	return map[string]interface{}{
		"format":           "OpenAI-compatible chat.completion.chunk over SSE",
		"content_type":     "text/event-stream",
		"done":             "data: [DONE]",
		"request_controls": []string{"stream=true", "stream_options.include_usage=true when final usage is required"},
		"fields": []map[string]string{
			{"path": "id", "type": "string", "description": "Same request identifier across chunks."},
			{"path": "object", "type": "string", "description": "Always chat.completion.chunk for streaming chunks."},
			{"path": "choices[].delta.role", "type": "string", "description": "Usually appears in the first chunk."},
			{"path": "choices[].delta.content", "type": "string", "description": "Incremental generated text. Concatenate chunks in order."},
			{"path": "choices[].delta.reasoning_content", "type": "string", "description": "Incremental thinking output for supported models."},
			{"path": "choices[].finish_reason", "type": "string", "description": "Null while generating; set on the final content chunk."},
			{"path": "usage", "type": "object", "description": "Usually null until the final usage chunk when include_usage is true."},
		},
		"stream_example": []string{
			jsonLine(map[string]interface{}{
				"id":      "chatcmpl-tokenhubhk-example",
				"object":  "chat.completion.chunk",
				"created": 1710000000,
				"model":   m.ModelName,
				"choices": []map[string]interface{}{{"index": 0, "delta": firstDelta, "finish_reason": nil}},
				"usage":   nil,
			}),
			jsonLine(map[string]interface{}{
				"id":      "chatcmpl-tokenhubhk-example",
				"object":  "chat.completion.chunk",
				"created": 1710000000,
				"model":   m.ModelName,
				"choices": []map[string]interface{}{{"index": 0, "delta": map[string]interface{}{"content": "Hello"}, "finish_reason": nil}},
				"usage":   nil,
			}),
			jsonLine(map[string]interface{}{
				"id":      "chatcmpl-tokenhubhk-example",
				"object":  "chat.completion.chunk",
				"created": 1710000000,
				"model":   m.ModelName,
				"choices": []map[string]interface{}{{"index": 0, "delta": map[string]interface{}{"content": ""}, "finish_reason": "stop"}},
				"usage":   nil,
			}),
			jsonLine(map[string]interface{}{
				"id":      "chatcmpl-tokenhubhk-example",
				"object":  "chat.completion.chunk",
				"created": 1710000000,
				"model":   m.ModelName,
				"choices": []interface{}{},
				"usage":   map[string]interface{}{"prompt_tokens": 22, "completion_tokens": 8, "total_tokens": 30},
			}),
			"data: [DONE]",
		},
		"notes": []string{
			"Each SSE event begins with data: followed by a JSON chunk.",
			"Concatenate choices[].delta.content in arrival order.",
			"When include_usage is true, the final usage chunk can have an empty choices array.",
			"Stop reading only after data: [DONE] or the network stream ends with a handled error.",
		},
	}
}

func buildModelAPIDocFAQs(m model.AIModel, p modelDocProviderProfile) []map[string]string {
	modelID := m.ModelName
	faqs := []map[string]string{
		{
			"question": "Why is the authentication different from the official platform?",
			"answer":   "Client requests must use TokenHubHK authentication only: Authorization: Bearer sk-your-tokenhubhk-key. TokenHubHK handles route credentials and signing inside the platform.",
		},
		{
			"question": "What is the difference between streaming and non-streaming responses?",
			"answer":   "Non-streaming returns one chat.completion JSON object. Streaming returns SSE chat.completion.chunk events; concatenate delta.content and wait for data: [DONE].",
		},
		{
			"question": "When can I get usage tokens in streaming mode?",
			"answer":   "Set stream=true and stream_options.include_usage=true. Compatible routes return a final usage chunk before data: [DONE].",
		},
		{
			"question": "Can I execute tool-call arguments directly?",
			"answer":   "No. Tool arguments are generated text. Validate them with your own JSON Schema and permission checks before any business action.",
		},
		{
			"question": "How should custom parameters be passed?",
			"answer":   "Use standard TokenHubHK parameters first. Put confirmed advanced options in extra_body or custom_params, and never override model, messages, stream, Authorization, api_key, secret_key, or billing fields.",
		},
	}
	if p.SupportsThinking {
		faqs = append(faqs, map[string]string{
			"question": "How do I enable thinking for " + modelID + "?",
			"answer":   "Use the TokenHubHK thinking parameter shown on this page: " + preferredThinkingParam(p) + ". The exact behavior is model-dependent and follows the parameter verification status.",
		})
	}
	if p.SupportsSearch {
		faqs = append(faqs, map[string]string{
			"question": "How do I enable web search for " + modelID + "?",
			"answer":   "Use TokenHubHK enable_search only when this page marks search as supported or platform_mapped. If it is unsupported, do not force it through custom parameters.",
		})
	}
	return faqs
}

func buildDeveloperGuide(m model.AIModel, p modelDocProviderProfile) string {
	lines := []string{
		"## TokenHubHK endpoint",
		"Use `POST https://www.tokenhubhk.com/v1/chat/completions` with `Authorization: Bearer sk-your-tokenhubhk-key`.",
		"",
		"## Model-specific notes",
	}
	for _, d := range p.Differentiators {
		lines = append(lines, "- "+d)
	}
	lines = append(lines,
		"",
		"## Parameter policy",
		"- `model`, `messages`, `stream`, `temperature`, `top_p`, `max_tokens`, `tools`, `tool_choice`, and `response_format` follow TokenHubHK's OpenAI-compatible contract.",
		"- `extra_body` and `custom_params` are advanced option containers. Prefer standard TokenHubHK parameters first.",
		"- Keys, AK/SK signatures, `x-api-key`, and auth headers from other platforms must not be used in user requests.",
	)
	if p.SupportsThinking {
		lines = append(lines, fmt.Sprintf("- Thinking is controlled with TokenHubHK `%s` for this model.", preferredThinkingParam(p)))
	}
	if p.SupportsSearch {
		lines = append(lines, "- Search is controlled with TokenHubHK `enable_search` when this model marks search as supported.")
	}
	lines = append(lines,
		"",
		"## Validation standard",
		"Each parameter is labeled as `official_confirmed`, `platform_mapped`, `runtime_verified`, or `unsupported`. Do not treat `official_confirmed` as a live-channel guarantee until a runtime check has been recorded by an administrator.",
	)
	return strings.Join(lines, "\n")
}

func buildCodeExamples(modelName string) map[string]string {
	body := fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"Hello from TokenHubHK"}],"stream":false}`, modelName)
	return map[string]string{
		"curl":       fmt.Sprintf("curl https://www.tokenhubhk.com/v1/chat/completions \\\n  -H \"Authorization: Bearer $TOKENHUBHK_API_KEY\" \\\n  -H \"Content-Type: application/json\" \\\n  -d '%s'", body),
		"javascript": fmt.Sprintf("const res = await fetch('https://www.tokenhubhk.com/v1/chat/completions', {\n  method: 'POST',\n  headers: { Authorization: `Bearer ${process.env.TOKENHUBHK_API_KEY}`, 'Content-Type': 'application/json' },\n  body: JSON.stringify({ model: '%s', messages: [{ role: 'user', content: 'Hello from TokenHubHK' }] })\n});\nconsole.log(await res.json());", modelName),
		"typescript": fmt.Sprintf("type ChatResponse = { id: string; choices: Array<{ message?: { content?: string } }> };\nconst response = await fetch('https://www.tokenhubhk.com/v1/chat/completions', {\n  method: 'POST',\n  headers: { Authorization: `Bearer ${process.env.TOKENHUBHK_API_KEY}`, 'Content-Type': 'application/json' },\n  body: JSON.stringify({ model: '%s', messages: [{ role: 'user', content: 'Hello from TokenHubHK' }] })\n});\nconst data = await response.json() as ChatResponse;\nconsole.log(data.choices[0]?.message?.content);", modelName),
		"python":     fmt.Sprintf("import os, requests\nresp = requests.post(\n    'https://www.tokenhubhk.com/v1/chat/completions',\n    headers={'Authorization': f\"Bearer {os.environ['TOKENHUBHK_API_KEY']}\", 'Content-Type': 'application/json'},\n    json={'model': '%s', 'messages': [{'role': 'user', 'content': 'Hello from TokenHubHK'}]},\n    timeout=60,\n)\nresp.raise_for_status()\nprint(resp.json())", modelName),
		"go":         fmt.Sprintf("payload := strings.NewReader(`{\"model\":\"%s\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello from TokenHubHK\"}]}`)\nreq, _ := http.NewRequest(\"POST\", \"https://www.tokenhubhk.com/v1/chat/completions\", payload)\nreq.Header.Set(\"Authorization\", \"Bearer \"+os.Getenv(\"TOKENHUBHK_API_KEY\"))\nreq.Header.Set(\"Content-Type\", \"application/json\")\nresp, err := http.DefaultClient.Do(req)\nif err != nil { panic(err) }\ndefer resp.Body.Close()", modelName),
		"java":       fmt.Sprintf("HttpRequest request = HttpRequest.newBuilder()\n    .uri(URI.create(\"https://www.tokenhubhk.com/v1/chat/completions\"))\n    .header(\"Authorization\", \"Bearer \" + System.getenv(\"TOKENHUBHK_API_KEY\"))\n    .header(\"Content-Type\", \"application/json\")\n    .POST(HttpRequest.BodyPublishers.ofString(\"{\\\"model\\\":\\\"%s\\\",\\\"messages\\\":[{\\\"role\\\":\\\"user\\\",\\\"content\\\":\\\"Hello from TokenHubHK\\\"}]}\"))\n    .build();", modelName),
		"php":        fmt.Sprintf("$payload = json_encode(['model' => '%s', 'messages' => [['role' => 'user', 'content' => 'Hello from TokenHubHK']]]);\n$ch = curl_init('https://www.tokenhubhk.com/v1/chat/completions');\ncurl_setopt_array($ch, [CURLOPT_POST => true, CURLOPT_HTTPHEADER => ['Authorization: Bearer '.getenv('TOKENHUBHK_API_KEY'), 'Content-Type: application/json'], CURLOPT_POSTFIELDS => $payload, CURLOPT_RETURNTRANSFER => true]);\necho curl_exec($ch);", modelName),
		"csharp":     fmt.Sprintf("using var client = new HttpClient();\nclient.DefaultRequestHeaders.Authorization = new AuthenticationHeaderValue(\"Bearer\", Environment.GetEnvironmentVariable(\"TOKENHUBHK_API_KEY\"));\nvar json = \"{\\\"model\\\":\\\"%s\\\",\\\"messages\\\":[{\\\"role\\\":\\\"user\\\",\\\"content\\\":\\\"Hello from TokenHubHK\\\"}]}\";\nvar res = await client.PostAsync(\"https://www.tokenhubhk.com/v1/chat/completions\", new StringContent(json, Encoding.UTF8, \"application/json\"));\nConsole.WriteLine(await res.Content.ReadAsStringAsync());", modelName),
		"ruby":       fmt.Sprintf("uri = URI('https://www.tokenhubhk.com/v1/chat/completions')\nreq = Net::HTTP::Post.new(uri)\nreq['Authorization'] = \"Bearer #{ENV['TOKENHUBHK_API_KEY']}\"\nreq['Content-Type'] = 'application/json'\nreq.body = { model: '%s', messages: [{ role: 'user', content: 'Hello from TokenHubHK' }] }.to_json\nputs Net::HTTP.start(uri.hostname, uri.port, use_ssl: true) { |http| http.request(req) }.body", modelName),
	}
}

func providerProfileFor(supplierCode, modelName string) modelDocProviderProfile {
	key := canonicalProviderKey(supplierCode, modelName)
	profiles := map[string]modelDocProviderProfile{
		"openai": {
			Key: "openai", ProviderName: "OpenAI", SourceTitle: "OpenAI Chat Completions API Reference", SourceURL: "https://platform.openai.com/docs/api-reference/chat/create-chat-completion", OriginalEndpoint: "POST https://api.openai.com/v1/chat/completions", OriginalAuthSummary: "Bearer API key", SupportsThinking: true, ThinkingParam: "reasoning_effort", SupportsSearch: true, SearchParam: "web_search_options", SupportsTools: true, SupportsJSON: true, SupportsVision: true, SupportsStream: true,
			Overview:        "OpenAI models use an OpenAI-compatible chat completion schema with optional tools, structured output, vision input, and reasoning controls depending on the model family.",
			Differentiators: []string{"Reasoning models use `reasoning_effort` rather than a thinking budget.", "Web search uses this model family's tool/search options, while TokenHubHK exposes a stable platform search control.", "Some new OpenAI features are Responses-API-first; TokenHubHK documents chat-compatible behavior here."},
			AdminNotes:      "Official docs confirm Bearer auth, chat completion request body, streaming chunks, tools, response_format, and web_search_options.",
		},
		"anthropic": {
			Key: "anthropic", ProviderName: "Anthropic", SourceTitle: "Anthropic Messages API and Claude tools documentation", SourceURL: "https://docs.anthropic.com/en/api/messages", OriginalEndpoint: "POST https://api.anthropic.com/v1/messages", OriginalAuthSummary: "x-api-key plus anthropic-version header", SupportsThinking: true, ThinkingParam: "thinking.budget_tokens", SupportsSearch: true, SearchParam: "tools[type=web_search_*]", SupportsTools: true, SupportsJSON: false, SupportsVision: true, SupportsStream: true,
			Overview:        "Claude models use a Messages-style request shape behind the scenes. TokenHubHK normalizes Claude access into the OpenAI-compatible chat endpoint for developers.",
			Differentiators: []string{"Extended thinking uses a nested `thinking` object in this model family.", "Web search is a server tool rather than a flat boolean.", "JSON mode is not the same as OpenAI `response_format`; use tool schemas or prompting unless TokenHubHK marks a mapping as supported."},
			AdminNotes:      "Official docs confirm Messages API, extended thinking, web search tool, tool use, and image/PDF support by model family.",
		},
		"gemini": {
			Key: "gemini", ProviderName: "Google Gemini", SourceTitle: "Gemini API text generation documentation", SourceURL: "https://ai.google.dev/gemini-api/docs/text-generation", OriginalEndpoint: "POST https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent", OriginalAuthSummary: "x-goog-api-key or Google auth depending on endpoint", SupportsThinking: true, ThinkingParam: "generationConfig.thinkingConfig.thinkingBudget", SupportsSearch: true, SearchParam: "tools.google_search", SupportsTools: true, SupportsJSON: true, SupportsVision: true, SupportsStream: true,
			Overview:        "Gemini model families use model-specific thinking configuration. TokenHubHK exposes Gemini through the unified chat completion contract.",
			Differentiators: []string{"Thinking is budget/config-driven and model dependent.", "Grounding/search uses tools configuration for supported models.", "Gemini supports broad multimodal input on selected models; check TokenHubHK capability matrix per model."},
			AdminNotes:      "Official docs confirm generateContent, thinkingConfig, SDK examples, and model-dependent thinking behavior.",
		},
		"volcengine": {
			Key: "volcengine", ProviderName: "Volcengine Ark", SourceTitle: "Volcengine Ark chat_completions API reference", SourceURL: "https://www.volcengine.com/docs/82379/1511946", OriginalEndpoint: "POST https://ark.cn-beijing.volces.com/api/v3/chat/completions", OriginalAuthSummary: "Bearer API key", SupportsThinking: false, ThinkingParam: "", SupportsSearch: false, SearchParam: "", SupportsTools: true, SupportsJSON: true, SupportsVision: true, SupportsStream: true,
			Overview:        "Volcengine Ark offers OpenAI-compatible chat completions for Doubao and related models. TokenHubHK keeps authentication and model naming under its own API key system.",
			Differentiators: []string{"Endpoint compatibility is high for standard chat parameters.", "Thinking/search controls vary by model and are not exposed as stable TokenHubHK public controls unless mapping coverage marks them supported.", "Use `extra_body` only after this model page confirms the option is supported."},
			AdminNotes:      "Official Ark API list confirms chat_completions endpoint. Model-specific options require per-channel runtime verification.",
		},
		"deepseek": {
			Key: "deepseek", ProviderName: "DeepSeek", SourceTitle: "DeepSeek Create Chat Completion API Docs", SourceURL: "https://api-docs.deepseek.com/api/create-chat-completion", OriginalEndpoint: "POST https://api.deepseek.com/chat/completions", OriginalAuthSummary: "Bearer API key", SupportsThinking: false, ThinkingParam: "reasoning_content(output only)", SupportsSearch: false, SearchParam: "", SupportsTools: true, SupportsJSON: true, SupportsVision: false, SupportsStream: true,
			Overview:        "DeepSeek exposes OpenAI-compatible chat completions. Reasoning models return reasoning content, but do not use a portable thinking budget control.",
			Differentiators: []string{"`deepseek-reasoner` returns `reasoning_content`; choose the reasoning model rather than toggling thinking.", "FIM/completions are separate capabilities from chat completions.", "Cache hit/miss tokens may appear in usage details."},
			AdminNotes:      "Official docs confirm chat completion schema, streaming, tool_calls, JSON output, and reasoning_content for reasoner models.",
		},
		"aliyun_dashscope": {
			Key: "aliyun_dashscope", ProviderName: "Alibaba Cloud Model Studio / DashScope", SourceTitle: "Alibaba Cloud OpenAI Chat API reference", SourceURL: "https://help.aliyun.com/zh/model-studio/qwen-api-via-openai-chat-completions", OriginalEndpoint: "POST https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions", OriginalAuthSummary: "Bearer DashScope API key", SupportsThinking: true, ThinkingParam: "enable_thinking / thinking_budget", SupportsSearch: true, SearchParam: "enable_search", SupportsTools: true, SupportsJSON: true, SupportsVision: true, SupportsStream: true,
			Overview:        "Qwen models on DashScope support OpenAI-compatible chat completions, with Qwen-specific options such as thinking and search surfaced by TokenHubHK mappings.",
			Differentiators: []string{"`enable_thinking` applies to supported Qwen thinking-capable model families.", "`enable_search` is available on supported models.", "Some vision options are non-OpenAI parameters and should go through `extra_body` or `custom_params` after this model page confirms support."},
			AdminNotes:      "Official docs confirm compatible-mode endpoint, Bearer auth, enable_thinking, and extra_body guidance for non-standard parameters.",
		},
		"baidu_qianfan": {
			Key: "baidu_qianfan", ProviderName: "Baidu Qianfan ModelBuilder", SourceTitle: "Baidu Qianfan ModelBuilder inference API documentation", SourceURL: "https://cloud.baidu.com/doc/WENXINWORKSHOP/s/blqdpscp3", OriginalEndpoint: "https://qianfan.baidubce.com/v2/chat/completions or legacy Wenxin workshop endpoints", OriginalAuthSummary: "Baidu API key / Bearer depending on endpoint generation", SupportsThinking: false, ThinkingParam: "", SupportsSearch: false, SearchParam: "", SupportsTools: true, SupportsJSON: true, SupportsVision: true, SupportsStream: true,
			Overview:        "Baidu Qianfan ModelBuilder provides inference APIs for chat, completions, embeddings, and batch prediction. TokenHubHK presents a stable OpenAI-compatible chat surface.",
			Differentiators: []string{"Qianfan endpoint generations differ; TokenHubHK hides auth and endpoint differences.", "Thinking controls are model/endpoint-specific and must remain unsupported until mapped.", "Use TokenHubHK model capability tags for vision/search rather than assuming all Qianfan models share one feature set."},
			AdminNotes:      "Official Qianfan docs confirm inference API categories; exact per-model options require runtime verification against the configured endpoint.",
		},
		"moonshot": {
			Key: "moonshot", ProviderName: "Moonshot AI / Kimi", SourceTitle: "Kimi API platform concepts and Chat Completions", SourceURL: "https://platform.moonshot.cn/docs/introduction", OriginalEndpoint: "POST https://api.moonshot.cn/v1/chat/completions", OriginalAuthSummary: "Bearer API key", SupportsThinking: true, ThinkingParam: "model-selected thinking mode", SupportsSearch: true, SearchParam: "web_search tool", SupportsTools: true, SupportsJSON: true, SupportsVision: true, SupportsStream: true,
			Overview:        "Kimi models are OpenAI-compatible at the API layer and include long-context, multimodal, agent, and thinking variants depending on model generation.",
			Differentiators: []string{"Kimi thinking is primarily model-selected; choose a thinking-capable model when exact control is not mapped.", "Official web-search tools are model/platform features and should be exposed through TokenHubHK search mapping only after verification.", "Long context and file-related behavior are model dependent."},
			AdminNotes:      "Official docs confirm Chat Completions as the main interface, OpenAI SDK compatibility, K2.5 multimodal/thinking notes, and streaming behavior.",
		},
		"zhipu": {
			Key: "zhipu", ProviderName: "Zhipu AI / BigModel", SourceTitle: "Zhipu AI chat completions API reference", SourceURL: "https://docs.bigmodel.cn/api-reference", OriginalEndpoint: "POST https://open.bigmodel.cn/api/paas/v4/chat/completions", OriginalAuthSummary: "Bearer API key", SupportsThinking: true, ThinkingParam: "model-specific thinking controls", SupportsSearch: true, SearchParam: "model-specific search/tools", SupportsTools: true, SupportsJSON: true, SupportsVision: true, SupportsStream: true,
			Overview:        "GLM models use the BigModel chat completions API. TokenHubHK keeps public calls OpenAI-compatible and records GLM-specific controls as verified mappings only when supported.",
			Differentiators: []string{"GLM reasoning/search capabilities are model-specific.", "Vision models such as GLM-V families need image-capable message payloads.", "Tool calling and JSON output should be checked per model generation."},
			AdminNotes:      "Official docs confirm BigModel v4 chat completions endpoint and GLM model examples.",
		},
		"hunyuan": {
			Key: "hunyuan", ProviderName: "Tencent Hunyuan", SourceTitle: "Tencent Hunyuan OpenAI-compatible chat examples", SourceURL: "https://cloud.tencent.com/document/product/1729/111007", OriginalEndpoint: "POST https://api.hunyuan.cloud.tencent.com/v1/chat/completions", OriginalAuthSummary: "Bearer Hunyuan API key", SupportsThinking: true, ThinkingParam: "model-specific thinking model", SupportsSearch: false, SearchParam: "enable_enhancement", SupportsTools: true, SupportsJSON: true, SupportsVision: true, SupportsStream: true,
			Overview:        "Tencent Hunyuan offers OpenAI-compatible chat completions. TokenHubHK exposes Hunyuan through the same Bearer API key flow as all other suppliers.",
			Differentiators: []string{"Hunyuan custom parameters should be passed only through verified TokenHubHK mappings.", "Thinking is generally model-family specific.", "Some enhancement/search-like options are not equivalent to web search."},
			AdminNotes:      "Official Tencent docs confirm OpenAI-compatible endpoint, base_url, Bearer auth, and custom parameter examples.",
		},
	}
	if p, ok := profiles[key]; ok {
		return p
	}
	p := profiles["openai"]
	p.Key = key
	p.ProviderName = "OpenAI-compatible model"
	p.SourceTitle = "OpenAI-compatible API baseline"
	p.SourceURL = "https://platform.openai.com/docs/api-reference/chat/create-chat-completion"
	p.OriginalEndpoint = "OpenAI-compatible /chat/completions"
	p.AdminNotes = "Fallback profile. Replace source and parameter verification with official model docs when available."
	return p
}

func canonicalProviderKey(supplierCode, modelName string) string {
	code := strings.ToLower(supplierCode)
	name := strings.ToLower(modelName)
	switch {
	case code == "wangsu_aigw" && strings.Contains(name, "claude"):
		return "anthropic"
	case code == "wangsu_aigw" && strings.Contains(name, "gemini"):
		return "gemini"
	case code == "wangsu_aigw" && (strings.Contains(name, "gpt") || strings.Contains(name, "o3") || strings.Contains(name, "o4")):
		return "openai"
	case code == "google_gemini":
		return "gemini"
	case code == "baidu_qianfan" || code == "wenxin":
		return "baidu_qianfan"
	case code == "moonshot" || strings.Contains(name, "kimi"):
		return "moonshot"
	case code == "zhipu_glm":
		return "zhipu"
	case code == "tencent_hunyuan":
		return "hunyuan"
	case code == "talkingdata":
		return "volcengine"
	default:
		return code
	}
}

func preferredThinkingParam(p modelDocProviderProfile) string {
	if !p.SupportsThinking {
		return "enable_thinking"
	}
	switch p.Key {
	case "openai":
		return "reasoning_effort"
	case "anthropic", "gemini", "aliyun_dashscope":
		return "enable_thinking / thinking_budget"
	default:
		return "enable_thinking"
	}
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func displayModelName(m model.AIModel) string {
	if strings.TrimSpace(m.DisplayName) != "" {
		return m.DisplayName
	}
	return m.ModelName
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func firstPositive(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

func mustMarshalJSON(v interface{}) model.JSON {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func jsonLine(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "data: {}"
	}
	return "data: " + string(b)
}
