package ctxutil

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newCtx(setup func(c *gin.Context)) *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)
	if setup != nil {
		setup(c)
	}
	return c
}

func TestUserID_Missing(t *testing.T) {
	c := newCtx(nil)
	id, ok := UserID(c)
	if ok || id != 0 {
		t.Fatalf("expected (0,false), got (%d,%v)", id, ok)
	}
}

func TestUserID_WrongType(t *testing.T) {
	c := newCtx(func(c *gin.Context) {
		c.Set("userId", "notuint")
	})
	id, ok := UserID(c)
	if ok || id != 0 {
		t.Fatalf("expected (0,false) for wrong type, got (%d,%v)", id, ok)
	}
}

func TestUserID_Nil(t *testing.T) {
	c := newCtx(func(c *gin.Context) {
		c.Set("userId", nil)
	})
	id, ok := UserID(c)
	if ok || id != 0 {
		t.Fatalf("expected (0,false) for nil, got (%d,%v)", id, ok)
	}
}

func TestUserID_Zero(t *testing.T) {
	c := newCtx(func(c *gin.Context) {
		c.Set("userId", uint(0))
	})
	id, ok := UserID(c)
	if ok || id != 0 {
		t.Fatalf("expected (0,false) for zero uid (treat as not-logged-in), got (%d,%v)", id, ok)
	}
}

func TestUserID_OK(t *testing.T) {
	c := newCtx(func(c *gin.Context) {
		c.Set("userId", uint(123))
	})
	id, ok := UserID(c)
	if !ok || id != 123 {
		t.Fatalf("expected (123,true), got (%d,%v)", id, ok)
	}
}

func TestTenantID_Default(t *testing.T) {
	c := newCtx(nil)
	if got := TenantID(c); got != 0 {
		t.Fatalf("expected 0 for missing, got %d", got)
	}
}

func TestTenantID_OK(t *testing.T) {
	c := newCtx(func(c *gin.Context) { c.Set("tenantId", uint(5)) })
	if got := TenantID(c); got != 5 {
		t.Fatalf("expected 5, got %d", got)
	}
}

func TestTenantID_WrongType_ReturnsZero(t *testing.T) {
	c := newCtx(func(c *gin.Context) { c.Set("tenantId", "wrong") })
	if got := TenantID(c); got != 0 {
		t.Fatalf("expected 0 on type mismatch, got %d", got)
	}
}

func TestMustUserID_WritesUnauthorizedOnMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	id, ok := MustUserID(c)
	if ok || id != 0 {
		t.Fatalf("expected (0,false), got (%d,%v)", id, ok)
	}
	if w.Code != 401 {
		t.Fatalf("expected 401 response, got %d", w.Code)
	}
	if !c.IsAborted() {
		t.Fatalf("expected context aborted")
	}
}

func TestUserID64(t *testing.T) {
	c := newCtx(func(c *gin.Context) { c.Set("userId", uint(789)) })
	id64, ok := UserID64(c)
	if !ok || id64 != 789 {
		t.Fatalf("expected (789,true), got (%d,%v)", id64, ok)
	}
}

// TestUserID_PanicResistance 验证即使 c.Get 返回怪异类型也不会 panic
func TestUserID_PanicResistance(t *testing.T) {
	cases := []interface{}{nil, "", 0, uint64(5), int(5), float64(5), struct{}{}}
	for _, v := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("UserID panicked on value %v (%T): %v", v, v, r)
				}
			}()
			c := newCtx(func(c *gin.Context) { c.Set("userId", v) })
			_, _ = UserID(c)
		}()
	}
}
