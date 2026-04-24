package health

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLivenessHandler_AlwaysOK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/livez", LivenessHandler("test"))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/livez", nil)
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !contains(w.Body.String(), `"role":"test"`) {
		t.Fatalf("expected role field, body=%s", w.Body.String())
	}
	if !contains(w.Body.String(), `"mode":"liveness"`) {
		t.Fatalf("expected liveness mode, body=%s", w.Body.String())
	}
}

func TestReadinessHandler_NilDeps_Return200(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/readyz", ReadinessHandler("test", nil, nil))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/readyz", nil)
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200 when no deps, got %d", w.Code)
	}
	if !contains(w.Body.String(), `"status":"ready"`) {
		t.Fatalf("expected ready, body=%s", w.Body.String())
	}
}

func TestState_IsReady_InitFalse(t *testing.T) {
	s := &State{}
	if s.IsReady() {
		t.Fatal("expected initial IsReady=false")
	}
}

func TestState_IsReady_BothHealthy(t *testing.T) {
	s := &State{}
	s.dbHealthy.Store(true)
	s.redisHealthy.Store(true)
	if !s.IsReady() {
		t.Fatal("expected IsReady=true when both healthy")
	}
}

func TestState_IsReady_OnlyDBHealthy(t *testing.T) {
	s := &State{}
	s.dbHealthy.Store(true)
	if s.IsReady() {
		t.Fatal("expected IsReady=false when redis unhealthy")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
