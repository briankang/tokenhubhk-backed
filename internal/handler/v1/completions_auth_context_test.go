package v1

import (
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAPIKeyInfoFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := &gin.Context{}
	customChannelID := uint(119)

	c.Set("apiKeyId", uint(129))
	c.Set("userId", uint(1))
	c.Set("tenantId", uint(1))
	c.Set("customChannelID", customChannelID)
	c.Set("allowedModels", `["mock-chat"]`)
	c.Set("creditLimit", int64(1000))
	c.Set("creditUsed", int64(12))
	c.Set("rateLimitRPM", 600)
	c.Set("rateLimitTPM", 100000)

	info, ok := apiKeyInfoFromContext(c)
	if !ok {
		t.Fatal("expected api key info from context")
	}
	if info.KeyID != 129 || info.UserID != 1 || info.TenantID != 1 {
		t.Fatalf("unexpected identity: %+v", info)
	}
	if info.CustomChannelID == nil || *info.CustomChannelID != customChannelID {
		t.Fatalf("unexpected custom channel id: %+v", info.CustomChannelID)
	}
	if info.AllowedModels != `["mock-chat"]` {
		t.Fatalf("allowed models = %q", info.AllowedModels)
	}
	if info.CreditLimit != 1000 || info.CreditUsed != 12 {
		t.Fatalf("unexpected credit fields: %+v", info)
	}
	if info.RateLimitRPM != 600 || info.RateLimitTPM != 100000 {
		t.Fatalf("unexpected rate limits: %+v", info)
	}
}

func TestAPIKeyInfoFromContextMissingIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := &gin.Context{}
	c.Set("apiKeyId", uint(129))
	c.Set("userId", uint(1))

	if _, ok := apiKeyInfoFromContext(c); ok {
		t.Fatal("expected missing tenant id to fail")
	}
}
