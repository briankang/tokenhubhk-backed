package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type freeTierModelRequest struct {
	Model string `json:"model"`
}

// FreeTierAccessGuard blocks free users from calling non-free-tier models.
func FreeTierAccessGuard(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if db == nil || c.Request == nil {
			c.Next()
			return
		}
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		if isPaidUser(c) {
			c.Next()
			return
		}

		rawBody, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.Next()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(rawBody))

		var req freeTierModelRequest
		if err := json.Unmarshal(rawBody, &req); err != nil {
			c.Next()
			return
		}
		modelName := strings.TrimSpace(req.Model)
		if modelName == "" {
			c.Next()
			return
		}

		allowed, known, err := IsFreeTierModelAllowed(c.Request.Context(), db, modelName)
		if err != nil || !known || allowed {
			c.Next()
			return
		}

		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"message": fmt.Sprintf("Model %s is a premium model. Please recharge at least ¥10 to unlock all models.", modelName),
				"type":    "access_denied",
				"code":    "premium_model_only",
			},
		})
		c.Abort()
	}
}

// IsFreeTierModelAllowed returns whether a known model is available to free users.
func IsFreeTierModelAllowed(ctx context.Context, db *gorm.DB, modelName string) (allowed bool, known bool, err error) {
	if db == nil || strings.TrimSpace(modelName) == "" {
		return false, false, nil
	}
	var row struct {
		IsFreeTier bool `gorm:"column:is_free_tier"`
	}
	err = db.WithContext(ctx).
		Table("ai_models").
		Select("is_free_tier").
		Where("model_name = ?", strings.TrimSpace(modelName)).
		Take(&row).Error
	if err == nil {
		return row.IsFreeTier, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, false, nil
	}
	return false, false, err
}

func isPaidUser(c *gin.Context) bool {
	raw, ok := c.Get("isPaidUser")
	if !ok {
		return false
	}
	isPaid, ok := raw.(bool)
	return ok && isPaid
}
