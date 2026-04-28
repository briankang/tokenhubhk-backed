package middleware

import (
	"context"
	"errors"
	"fmt"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/usercache"
)

const defaultPaidThresholdCredits int64 = 100000

type paidUserStatus struct {
	IsPaid               bool  `json:"is_paid"`
	TotalRecharged       int64 `json:"total_recharged"`
	PaidThresholdCredits int64 `json:"paid_threshold_credits"`
}

// EnsurePaidUserContext ensures Gin context carries the paid/free user flag.
func EnsurePaidUserContext(c *gin.Context, db *gorm.DB, userID uint) bool {
	if c == nil || userID == 0 {
		return false
	}
	if raw, ok := c.Get("isPaidUser"); ok {
		if isPaid, ok := raw.(bool); ok {
			return isPaid
		}
	}
	isPaid, err := LoadPaidUserStatus(c.Request.Context(), db, userID)
	if err != nil {
		c.Set("isPaidUser", false)
		return false
	}
	c.Set("isPaidUser", isPaid)
	return isPaid
}

// LoadPaidUserStatus returns whether the user has crossed the paid threshold.
func LoadPaidUserStatus(ctx context.Context, db *gorm.DB, userID uint) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("db is nil")
	}
	if userID == 0 {
		return false, fmt.Errorf("user id is empty")
	}
	status, err := usercache.GetOrLoadPaidStatus[paidUserStatus](ctx, userID, func(ctx context.Context) (paidUserStatus, error) {
		totalRecharged, err := loadTotalRecharged(ctx, db, userID)
		if err != nil {
			return paidUserStatus{}, err
		}
		threshold, err := loadPaidThresholdCredits(ctx, db)
		if err != nil {
			return paidUserStatus{}, err
		}
		return paidUserStatus{
			IsPaid:               totalRecharged >= threshold,
			TotalRecharged:       totalRecharged,
			PaidThresholdCredits: threshold,
		}, nil
	})
	if err != nil {
		return false, err
	}
	return status.IsPaid, nil
}

func loadTotalRecharged(ctx context.Context, db *gorm.DB, userID uint) (int64, error) {
	var balance model.UserBalance
	err := db.WithContext(ctx).
		Select("total_recharged").
		Where("user_id = ?", userID).
		First(&balance).Error
	if err == nil {
		return balance.TotalRecharged, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return 0, err
}

func loadPaidThresholdCredits(ctx context.Context, db *gorm.DB) (int64, error) {
	var cfg model.QuotaConfig
	err := db.WithContext(ctx).
		Select("paid_threshold_credits").
		Where("is_active = ?", true).
		Order("id DESC").
		First(&cfg).Error
	if err == nil {
		return cfg.PaidThresholdCredits, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return defaultPaidThresholdCredits, nil
	}
	return 0, err
}
