package main

import (
	"fmt"
	"os"
	"strconv"

	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"tokenhub-server/internal/database"
	"tokenhub-server/internal/pkg/logger"
)

func main() {
	_ = logger.Init(logger.Config{Level: "info", Dir: "./logs"})

	host := getenv("DATABASE_HOST", "127.0.0.1")
	port := getenvInt("DATABASE_PORT", 3306)
	user := getenv("DATABASE_USER", "")
	pass := getenv("DATABASE_PASSWORD", "")
	name := getenv("DATABASE_DBNAME", "")
	if user == "" || name == "" {
		panic("DATABASE_USER and DATABASE_DBNAME are required")
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		user, pass, host, port, name)
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		panic(err)
	}
	database.RunSeedWangsuVideo(db)
	logger.L.Info("seed-wangsu-video complete", zap.String("database", name))
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
