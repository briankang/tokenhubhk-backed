package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
	emailsvc "tokenhub-server/internal/service/email"
)

func setupEmailHandlerTest(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", name)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.EmailProviderConfig{}, &model.EmailTemplate{}, &model.EmailSendLog{}); err != nil {
		t.Fatalf("migrate email tables: %v", err)
	}
	cfg := emailsvc.NewConfigService(db)
	tpl := emailsvc.NewTemplateService(db)
	mail := emailsvc.NewEmailService(db, cfg, tpl, emailsvc.NewSender(cfg))

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userId", uint(99))
		c.Next()
	})
	NewEmailHandler(cfg, tpl, mail).Register(r.Group("/admin"))
	return r, db
}

func emailHandlerRequest(t *testing.T, r *gin.Engine, method, path string, body interface{}) (int, map[string]interface{}) {
	t.Helper()
	buf := bytes.NewBuffer(nil)
	if body != nil {
		data, _ := json.Marshal(body)
		buf = bytes.NewBuffer(data)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var payload map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &payload)
	return w.Code, payload
}

func TestEmailHandlerProviderConfigValidationAndMaskPreservation(t *testing.T) {
	r, _ := setupEmailHandlerTest(t)

	code, resp := emailHandlerRequest(t, r, http.MethodGet, "/admin/email/providers", nil)
	if code != http.StatusOK {
		t.Fatalf("list providers status=%d resp=%v", code, resp)
	}
	providers := resp["data"].([]interface{})
	if len(providers) != 2 {
		t.Fatalf("provider placeholders len=%d, want 2", len(providers))
	}

	code, _ = emailHandlerRequest(t, r, http.MethodPut, "/admin/email/providers/invalid", map[string]interface{}{})
	if code != http.StatusBadRequest {
		t.Fatalf("invalid channel status=%d, want 400", code)
	}
	code, _ = emailHandlerRequest(t, r, http.MethodPut, "/admin/email/providers/notification", map[string]interface{}{
		"api_key":    emailsvc.MaskedSecret,
		"from_email": "notice@example.com",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("new masked secret status=%d, want 400", code)
	}
	code, _ = emailHandlerRequest(t, r, http.MethodPut, "/admin/email/providers/notification", map[string]interface{}{
		"api_user":   "notice-user",
		"api_key":    "plain-secret",
		"from_email": "bad-email",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("invalid from_email status=%d, want 400", code)
	}

	code, resp = emailHandlerRequest(t, r, http.MethodPut, "/admin/email/providers/notification", map[string]interface{}{
		"api_user":    "notice-user",
		"api_key":     "plain-secret",
		"from_email":  "notice@example.com",
		"from_name":   "Notice",
		"domain":      "example.com",
		"is_active":   true,
		"daily_limit": 100,
	})
	if code != http.StatusOK {
		t.Fatalf("create provider status=%d resp=%v", code, resp)
	}
	created := resp["data"].(map[string]interface{})
	if created["has_api_key"] != true || created["daily_limit"].(float64) != 100 {
		t.Fatalf("created provider dto mismatch: %#v", created)
	}

	code, resp = emailHandlerRequest(t, r, http.MethodPut, "/admin/email/providers/notification", map[string]interface{}{
		"api_key":     emailsvc.MaskedSecret,
		"from_email":  "notice2@example.com",
		"from_name":   "Notice Two",
		"is_active":   false,
		"daily_limit": 5,
	})
	if code != http.StatusOK {
		t.Fatalf("masked update status=%d resp=%v", code, resp)
	}
	updated := resp["data"].(map[string]interface{})
	if updated["has_api_key"] != true || updated["from_name"] != "Notice Two" || updated["is_active"] != false || updated["daily_limit"].(float64) != 5 {
		t.Fatalf("masked update dto mismatch: %#v", updated)
	}
}

func TestEmailHandlerTemplatePreviewAndSendValidation(t *testing.T) {
	r, _ := setupEmailHandlerTest(t)

	code, _ := emailHandlerRequest(t, r, http.MethodPost, "/admin/email/templates", map[string]interface{}{
		"code":      "bad_tpl",
		"name":      "Bad",
		"subject":   "Hello {{",
		"html_body": "<p>broken</p>",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("invalid template syntax status=%d, want 400", code)
	}

	code, resp := emailHandlerRequest(t, r, http.MethodPost, "/admin/email/templates", map[string]interface{}{
		"code":      "welcome",
		"name":      "Welcome",
		"subject":   "Hi {{.Name}}",
		"html_body": "<p>{{.Name}}</p>",
		"text_body": "Hi {{.Name}}",
		"variables_schema": []map[string]interface{}{
			{"key": "Name", "required": true},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("create template status=%d resp=%v", code, resp)
	}
	templateID := uint(resp["data"].(map[string]interface{})["id"].(float64))

	code, _ = emailHandlerRequest(t, r, http.MethodPost, fmt.Sprintf("/admin/email/templates/%d/preview", templateID), map[string]interface{}{
		"variables": map[string]interface{}{},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("preview missing variable status=%d, want 400", code)
	}
	code, resp = emailHandlerRequest(t, r, http.MethodPost, fmt.Sprintf("/admin/email/templates/%d/preview", templateID), map[string]interface{}{
		"variables": map[string]interface{}{"Name": "<script>alert(1)</script>"},
	})
	if code != http.StatusOK {
		t.Fatalf("preview status=%d resp=%v", code, resp)
	}
	rendered := resp["data"].(map[string]interface{})
	if !strings.Contains(rendered["subject"].(string), "<script>") {
		t.Fatalf("subject should keep text value: %#v", rendered)
	}
	if strings.Contains(rendered["html_body"].(string), "<script>") || !strings.Contains(rendered["html_body"].(string), "&lt;script&gt;") {
		t.Fatalf("html body should be escaped: %#v", rendered)
	}

	code, _ = emailHandlerRequest(t, r, http.MethodPost, "/admin/email/send", map[string]interface{}{
		"to":      []string{"bad-email"},
		"subject": "Hello",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("invalid recipient status=%d, want 400", code)
	}
	code, _ = emailHandlerRequest(t, r, http.MethodPost, "/admin/email/send", map[string]interface{}{
		"to": []string{"user@example.com"},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("missing template/subject status=%d, want 400", code)
	}
	code, _ = emailHandlerRequest(t, r, http.MethodPost, "/admin/email/providers/notification/test", map[string]interface{}{
		"to": "bad-email",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("provider test invalid recipient status=%d, want 400", code)
	}
}
