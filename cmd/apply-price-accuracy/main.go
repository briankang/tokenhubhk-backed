package main

import (
	"fmt"
	"log"
	"os"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"tokenhub-server/internal/database"
)

func main() {
	dsn, err := databaseDSN()
	if err != nil {
		log.Fatal(err)
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("connect database: %v", err)
	}

	database.RunPriceTierInheritanceMigration(db)
	database.RunPriceTierSellingSyncMigration(db)
	database.RunModelPriceAccuracyMigration(db)
	fmt.Println("price accuracy migrations applied")
}

func databaseDSN() (string, error) {
	host := env("DATABASE_HOST", "")
	port := env("DATABASE_PORT", "3306")
	user := env("DATABASE_USER", "")
	password := env("DATABASE_PASSWORD", "")
	name := env("DATABASE_DBNAME", "")
	if host == "" || user == "" || name == "" {
		return "", fmt.Errorf("DATABASE_HOST, DATABASE_USER, and DATABASE_DBNAME are required")
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local", user, password, host, port, name), nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
