package main

import (
	"encoding/json"
	"fmt"
	"log"
	"tokenhub-server/internal/database"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/config"
	"os"
)

func main() {
	// Initialize config
	cfg := config.LoadConfig()
	
	// Initialize database
	err := database.Init(cfg.Database, nil)
	if err != nil {
		log.Fatalf("Failed to init database: %v", err)
	}
	db := database.DB

	fmt.Println("--- Supplier Info ---")
	var sups []model.Supplier
	db.Where("code = ?", "aliyun_dashscope").Find(&sups)
	for _, s := range sups {
		fmt.Printf("ID: %d, Code: %s, AccessType: %s, DefaultFeatures: %s\n", s.ID, s.Code, s.AccessType, string(s.DefaultFeatures))
	}

	fmt.Println("\n--- Qwen Model Features ---")
	var models []model.AIModel
	db.Where("model_name LIKE ?", "qwen%").Find(&models)
	for _, m := range models {
		fmt.Printf("ModelName: %s, Type: %s, Features: %s\n", m.ModelName, m.ModelType, string(m.Features))
	}
}
