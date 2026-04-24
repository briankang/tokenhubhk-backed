package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/apikey"
	"tokenhub-server/internal/service/balance"
)

var videoModels = []string{
	"sora-2",
	"MiniMax-Hailuo-02",
	"veo-3.1-fast-generate-preview",
	"veo-3.1-generate-preview",
	"viduq3-pro",
}

func main() {
	host := getenv("DATABASE_HOST", "127.0.0.1")
	port := getenvInt("DATABASE_PORT", 3306)
	user := getenv("DATABASE_USER", "")
	pass := getenv("DATABASE_PASSWORD", "")
	name := getenv("DATABASE_DBNAME", "")
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		user, pass, host, port, name)
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		panic(err)
	}

	ctx := context.Background()
	var u model.User
	if err := db.Where("email = ?", "admin@tokenhubhk.com").First(&u).Error; err != nil {
		if err := db.Where("is_active = ?", true).Order("id").First(&u).Error; err != nil {
			panic(err)
		}
	}

	bal := balance.NewBalanceService(db, nil)
	_, _ = bal.GetBalance(ctx, u.ID, u.TenantID)
	_, _ = bal.Recharge(ctx, u.ID, u.TenantID, 1000000, "Wangsu video playground debug", "wangsu-video-debug")

	var wangsuChannel model.Channel
	if err := db.Where("name = ?", "Wangsu AI Gateway - Video").First(&wangsuChannel).Error; err != nil {
		panic(err)
	}
	ccID := ensureDebugCustomChannel(ctx, db, wangsuChannel.ID)
	svc := apikey.NewApiKeyService(db, nil, getenv("JWT_SECRET", ""))
	res, err := svc.GenerateWithOptions(ctx, u.ID, u.TenantID, apikey.CreateKeyOptions{
		Name:            "wangsu-video-debug",
		CustomChannelID: &ccID,
		AllowedModels:   `["sora-2","MiniMax-Hailuo-02","veo-3.1-fast-generate-preview","veo-3.1-generate-preview","viduq3-pro"]`,
		RateLimitRPM:    60,
		RateLimitTPM:    100000,
	})
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s\n", res.Key)
}

func ensureDebugCustomChannel(ctx context.Context, db *gorm.DB, channelID uint) uint {
	var cc model.CustomChannel
	if err := db.WithContext(ctx).Where("name = ?", "Wangsu Video Debug").First(&cc).Error; err != nil {
		cc = model.CustomChannel{
			Name:        "Wangsu Video Debug",
			Description: "Dedicated route for Wangsu AI Gateway video playground verification",
			Strategy:    "priority",
			AutoRoute:   false,
			Visibility:  "all",
			IsActive:    true,
		}
		if err := db.WithContext(ctx).Create(&cc).Error; err != nil {
			panic(err)
		}
	} else {
		_ = db.WithContext(ctx).Model(&cc).Updates(map[string]any{
			"strategy":   "priority",
			"auto_route": false,
			"visibility": "all",
			"is_active":  true,
		}).Error
	}
	for _, modelName := range videoModels {
		route := model.CustomChannelRoute{
			CustomChannelID: cc.ID,
			AliasModel:      modelName,
			ChannelID:       channelID,
			ActualModel:     modelName,
			Weight:          100,
			Priority:        100,
			IsActive:        true,
		}
		var existing model.CustomChannelRoute
		err := db.WithContext(ctx).Where(
			"custom_channel_id = ? AND alias_model = ? AND channel_id = ?",
			cc.ID, modelName, channelID,
		).First(&existing).Error
		if err == nil {
			_ = db.WithContext(ctx).Model(&existing).Updates(map[string]any{
				"actual_model": modelName,
				"weight":       100,
				"priority":     100,
				"is_active":    true,
			}).Error
			continue
		}
		if err := db.WithContext(ctx).Create(&route).Error; err != nil {
			panic(err)
		}
	}
	return cc.ID
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}
