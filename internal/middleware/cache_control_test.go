package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestNoStoreSetsDefensiveCacheHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(NoStore())
	r.GET("/private", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/private", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	assertNoStoreHeaders(t, w.Header())
}

func TestNoStoreHeadersSurviveAbort(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(NoStore())
	r.Use(func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
	})
	r.GET("/private", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/private", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
	assertNoStoreHeaders(t, w.Header())
}

func TestApplyNoStoreKeepsExistingVaryValues(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Writer.Header().Set("Vary", "Origin, Accept-Encoding")

	ApplyNoStore(c)

	vary := w.Header().Get("Vary")
	for _, want := range []string{"Origin", "Accept-Encoding", "Authorization", "Cookie"} {
		if !headerListContains(vary, want) {
			t.Fatalf("Vary = %q, missing %q", vary, want)
		}
	}
}

func assertNoStoreHeaders(t *testing.T, h http.Header) {
	t.Helper()

	cacheControl := h.Get("Cache-Control")
	for _, want := range []string{"private", "no-store", "no-cache", "max-age=0", "must-revalidate"} {
		if !headerListContains(cacheControl, want) {
			t.Fatalf("Cache-Control = %q, missing %q", cacheControl, want)
		}
	}

	expected := map[string]string{
		"Pragma":            "no-cache",
		"Expires":           "0",
		"Surrogate-Control": "no-store",
		"CDN-Cache-Control": "no-store",
		"Edge-Control":      "no-store",
		"X-Accel-Expires":   "0",
	}
	for key, want := range expected {
		if got := h.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}

	vary := h.Get("Vary")
	for _, want := range []string{"Authorization", "Cookie"} {
		if !headerListContains(vary, want) {
			t.Fatalf("Vary = %q, missing %q", vary, want)
		}
	}
}

func headerListContains(headerValue, want string) bool {
	for _, part := range strings.Split(headerValue, ",") {
		if strings.EqualFold(strings.TrimSpace(part), want) {
			return true
		}
	}
	return false
}
