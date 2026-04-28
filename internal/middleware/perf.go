package middleware

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const perfTraceKey = "perfTrace"

// IsHealthPath reports whether the request targets a probe endpoint.
func IsHealthPath(path string) bool {
	return path == "/health" || path == "/livez" || path == "/readyz"
}

type perfTrace struct {
	start  time.Time
	last   time.Time
	stages []string
}

func startPerfTrace(c *gin.Context, now time.Time) {
	c.Set(perfTraceKey, &perfTrace{start: now, last: now})
}

func addPerfStage(c *gin.Context, name string, now time.Time) {
	v, ok := c.Get(perfTraceKey)
	if !ok {
		return
	}
	trace, ok := v.(*perfTrace)
	if !ok || trace.last.IsZero() {
		return
	}
	trace.stages = append(trace.stages, fmt.Sprintf("%s=%dms", name, now.Sub(trace.last).Milliseconds()))
	trace.last = now
}

func finishPerfTrace(c *gin.Context, now time.Time) string {
	v, ok := c.Get(perfTraceKey)
	if !ok {
		return ""
	}
	trace, ok := v.(*perfTrace)
	if !ok || trace.start.IsZero() {
		return ""
	}
	if !trace.last.IsZero() {
		trace.stages = append(trace.stages, fmt.Sprintf("handler=%dms", now.Sub(trace.last).Milliseconds()))
	}
	trace.stages = append(trace.stages, fmt.Sprintf("total=%dms", now.Sub(trace.start).Milliseconds()))
	return strings.Join(trace.stages, ",")
}

// PerfMark records elapsed time since the previous mark in RequestLogger.
// It is intentionally tiny and only adds a field to access/slow logs.
func PerfMark(name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		addPerfStage(c, name, time.Now())
		c.Next()
	}
}
