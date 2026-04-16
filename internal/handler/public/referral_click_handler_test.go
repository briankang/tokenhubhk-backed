package public_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/handler/public"
	"tokenhub-server/internal/model"
)

func setupClickRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	db := openTestDB(t)
	if db == nil {
		return nil, nil
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := public.NewReferralClickHandler(db)
	r.POST("/public/referral/click", h.Track)
	return r, db
}

// TestReferralClick_Success 有效 code 返回 200
func TestReferralClick_Success(t *testing.T) {
	r, db := setupClickRouter(t)
	if r == nil {
		return
	}

	// 准备测试用邀请链接
	userID := uint(830001)
	link := model.ReferralLink{
		UserID:   userID,
		TenantID: 1,
		Code:     "TESTCLK1",
	}
	db.Unscoped().Where("code = ?", link.Code).Delete(&model.ReferralLink{})
	db.Create(&link)
	t.Cleanup(func() {
		db.Unscoped().Where("code = ?", link.Code).Delete(&model.ReferralLink{})
	})

	initialCount := link.ClickCount

	body, _ := json.Marshal(map[string]string{"code": "TESTCLK1"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/public/referral/click", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// 验证 click_count 已增加
	var updated model.ReferralLink
	db.Where("code = ?", "TESTCLK1").First(&updated)
	if updated.ClickCount != initialCount+1 {
		t.Errorf("expected click_count %d, got %d", initialCount+1, updated.ClickCount)
	}
	t.Logf("click_count: %d -> %d", initialCount, updated.ClickCount)
}

// TestReferralClick_NonExistentCode 不存在的 code 静默成功（不暴露信息）
func TestReferralClick_NonExistentCode(t *testing.T) {
	r, db := setupClickRouter(t)
	if r == nil {
		return
	}
	_ = db

	body, _ := json.Marshal(map[string]string{"code": "NOTEXIST"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/public/referral/click", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (silent fail), got %d", w.Code)
	}
}

// TestReferralClick_EmptyCode 空 code 返回 400
func TestReferralClick_EmptyCode(t *testing.T) {
	r, db := setupClickRouter(t)
	if r == nil {
		return
	}
	_ = db

	body, _ := json.Marshal(map[string]string{"code": ""})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/public/referral/click", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// TestReferralClick_MissingBody 无 body 返回 400
func TestReferralClick_MissingBody(t *testing.T) {
	r, db := setupClickRouter(t)
	if r == nil {
		return
	}
	_ = db

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/public/referral/click", nil)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// TestReferralClick_MultipleClicks 多次点击累加
func TestReferralClick_MultipleClicks(t *testing.T) {
	r, db := setupClickRouter(t)
	if r == nil {
		return
	}

	link := model.ReferralLink{UserID: 830002, TenantID: 1, Code: "TESTCLK2"}
	db.Unscoped().Where("code = ?", link.Code).Delete(&model.ReferralLink{})
	db.Create(&link)
	t.Cleanup(func() {
		db.Unscoped().Where("code = ?", link.Code).Delete(&model.ReferralLink{})
	})

	for i := 0; i < 5; i++ {
		body, _ := json.Marshal(map[string]string{"code": "TESTCLK2"})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/public/referral/click", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("click %d failed: %d", i+1, w.Code)
		}
	}

	var updated model.ReferralLink
	db.Where("code = ?", "TESTCLK2").First(&updated)
	if updated.ClickCount != 5 {
		t.Errorf("expected click_count 5, got %d", updated.ClickCount)
	}
	t.Logf("5 clicks recorded correctly")
}
