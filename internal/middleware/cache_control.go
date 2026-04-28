package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const noStoreCacheControl = "private, no-store, no-cache, max-age=0, must-revalidate"

// NoStore marks responses as private and non-cacheable at browsers, proxies and CDNs.
func NoStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		ApplyNoStore(c)
		c.Next()
	}
}

// ApplyNoStore writes defensive cache headers for user/API-key scoped responses.
func ApplyNoStore(c *gin.Context) {
	h := c.Writer.Header()
	h.Set("Cache-Control", noStoreCacheControl)
	h.Set("Pragma", "no-cache")
	h.Set("Expires", "0")
	h.Set("Surrogate-Control", "no-store")
	h.Set("CDN-Cache-Control", "no-store")
	h.Set("Edge-Control", "no-store")
	h.Set("X-Accel-Expires", "0")
	appendVary(h, "Authorization", "Cookie")
}

func appendVary(h http.Header, values ...string) {
	existing := h.Values("Vary")
	seen := make(map[string]struct{})
	out := make([]string, 0, len(existing)+len(values))

	for _, raw := range existing {
		for _, part := range strings.Split(raw, ",") {
			value := strings.TrimSpace(part)
			if value == "" {
				continue
			}
			if value == "*" {
				return
			}
			key := strings.ToLower(value)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, value)
		}
	}

	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}

	if len(out) == 0 {
		h.Del("Vary")
		return
	}
	h.Set("Vary", strings.Join(out, ", "))
}
