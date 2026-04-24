package main

import (
	"fmt"
	"log"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func main() {
	dsn := "root:root@tcp(127.0.0.1:3306)/tokenhub?charset=utf8mb4&parseTime=True&loc=Local"
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("failed to connect database: %v", err)
	}

	// Find the Alibaba supplier
	var supplier model.Supplier
	if err := db.Unscoped().Where("code IN ?", []string{"alibaba", "aliyun_dashscope"}).First(&supplier).Error; err != nil {
		log.Fatalf("failed to find Alibaba supplier: %v", err)
	}

	fmt.Printf("Found supplier: ID=%d, Name=%s, DeletedAt=%v\n", supplier.ID, supplier.Name, supplier.DeletedAt)

	// Restore it
	if supplier.DeletedAt.Valid {
		fmt.Println("Restoring deleted supplier...")
		if err := db.Unscoped().Model(&supplier).Update("deleted_at", nil).Error; err != nil {
			log.Fatalf("failed to restore supplier: %v", err)
		}
	}

	// Update discount to 0.6
	fmt.Println("Updating discount to 0.6...")
	if err := db.Model(&supplier).Updates(map[string]interface{}{
		"discount": 0.6,
		"status":   "active",
		"is_active": true,
	}).Error; err != nil {
		log.Fatalf("failed to update discount: %v", err)
	}

	fmt.Println("Done.")
}
