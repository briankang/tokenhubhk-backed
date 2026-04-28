package public

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestCheckPhoneHandler_CheckPhone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	phone := "+8613812345678"
	if err := db.Create(&model.User{
		TenantID:       1,
		Email:          "phone-check@example.com",
		PhoneE164:      &phone,
		PasswordHash:   "hash",
		Name:           "Phone User",
		IsActive:       true,
		CountryCode:    "CN",
		RegisterSource: "phone",
	}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	r := gin.New()
	group := r.Group("/public")
	NewCheckPhoneHandler(db).Register(group)

	body, _ := json.Marshal(map[string]string{"phone": "13812345678"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/public/check-phone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var payload struct {
		Code int `json:"code"`
		Data struct {
			Exists bool `json:"exists"`
			Valid  bool `json:"valid"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != 0 || !payload.Data.Valid || !payload.Data.Exists {
		t.Fatalf("unexpected response: %+v", payload)
	}

	body, _ = json.Marshal(map[string]string{"phone": "12812345678"})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/public/check-phone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected invalid phone to return 200 with valid=false, got %d", w.Code)
	}
	payload = struct {
		Code int `json:"code"`
		Data struct {
			Exists bool `json:"exists"`
			Valid  bool `json:"valid"`
		} `json:"data"`
	}{}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode invalid response: %v", err)
	}
	if payload.Data.Valid || payload.Data.Exists {
		t.Fatalf("invalid phone should not be valid or existing: %+v", payload)
	}
}
