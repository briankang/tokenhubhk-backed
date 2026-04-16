package database_test

import (
	"context"
	"os"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
)

// TestUTF8Support 测试数据库对多语言字符的支持
// 验证中文、日文、韩文、阿拉伯文、Emoji 等字符的正确存储和读取
func TestUTF8Support(t *testing.T) {
	// 连接测试数据库
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "root:root123456@tcp(localhost:3306)/tokenhubhk?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=True&loc=Local"
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Skip("Database not available, skipping test")
		return
	}

	// 设置连接字符集（与生产环境一致）
	db.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci")

	ctx := context.Background()

	// 测试用例：各种语言和特殊字符
	testCases := []struct {
		name     string
		language string
		text     string
	}{
		{
			name:     "Chinese Simplified",
			language: "zh-CN",
			text:     "你好世界，这是中文测试！",
		},
		{
			name:     "Chinese Traditional",
			language: "zh-TW",
			text:     "你好世界，這是繁體中文測試！",
		},
		{
			name:     "Japanese",
			language: "ja",
			text:     "こんにちは世界、これは日本語のテストです！",
		},
		{
			name:     "Korean",
			language: "ko",
			text:     "안녕하세요 세계, 이것은 한국어 테스트입니다!",
		},
		{
			name:     "Arabic",
			language: "ar",
			text:     "مرحبا بالعالم، هذا اختبار باللغة العربية!",
		},
		{
			name:     "Russian",
			language: "ru",
			text:     "Привет мир, это тест на русском языке!",
		},
		{
			name:     "German",
			language: "de",
			text:     "Hallo Welt, das ist ein Test auf Deutsch! Ä Ö Ü ß",
		},
		{
			name:     "French",
			language: "fr",
			text:     "Bonjour le monde, c'est un test en français! É È Ê Ç",
		},
		{
			name:     "Spanish",
			language: "es",
			text:     "¡Hola mundo, esta es una prueba en español! ñ á é í ó ú",
		},
		{
			name:     "Thai",
			language: "th",
			text:     "สวัสดีชาวโลก นี่คือการทดสอบภาษาไทย!",
		},
		{
			name:     "Vietnamese",
			language: "vi",
			text:     "Xin chào thế giới, đây là bài kiểm tra tiếng Việt!",
		},
		{
			name:     "Emoji",
			language: "emoji",
			text:     "Hello 👋 World 🌍! Testing emojis: 😀 🎉 ❤️ 🚀 ✨",
		},
		{
			name:     "Mixed Languages",
			language: "mixed",
			text:     "Hello 你好 こんにちは 안녕하세요 مرحبا Привет 🌏",
		},
		{
			name:     "Special Characters",
			language: "special",
			text:     "Special: © ® ™ € £ ¥ § ¶ † ‡ • … ‰ ′ ″",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// 使用 partner_applications 表测试（已知支持 utf8mb4）
			app := model.PartnerApplication{
				Name:            tc.text,
				Email:           "utf8test@example.com",
				CooperationType: "other",
				Message:         tc.text + " - Message field test",
				Status:          "pending",
			}

			// 创建记录
			if err := db.WithContext(ctx).Create(&app).Error; err != nil {
				t.Fatalf("Failed to create record: %v", err)
			}

			// 清理
			defer func() {
				db.WithContext(ctx).Unscoped().Delete(&app)
			}()

			// 读取记录
			var retrieved model.PartnerApplication
			if err := db.WithContext(ctx).First(&retrieved, app.ID).Error; err != nil {
				t.Fatalf("Failed to retrieve record: %v", err)
			}

			// 验证数据完整性
			if retrieved.Name != tc.text {
				t.Errorf("Name mismatch:\nExpected: %s\nGot:      %s", tc.text, retrieved.Name)
			}

			expectedMessage := tc.text + " - Message field test"
			if retrieved.Message != expectedMessage {
				t.Errorf("Message mismatch:\nExpected: %s\nGot:      %s", expectedMessage, retrieved.Message)
			}

			// 验证字节长度（确保是 UTF-8 编码）
			if len(retrieved.Name) != len(tc.text) {
				t.Errorf("Byte length mismatch: expected %d, got %d", len(tc.text), len(retrieved.Name))
			}

			t.Logf("✅ %s: Successfully stored and retrieved", tc.language)
		})
	}
}

// TestDatabaseCharsetConfiguration 测试数据库字符集配置
func TestDatabaseCharsetConfiguration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "root:root123456@tcp(localhost:3306)/tokenhubhk?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=True&loc=Local"
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Skip("Database not available, skipping test")
		return
	}

	// 设置连接字符集
	db.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci")

	// 检查连接字符集
	var charsetClient, charsetConnection, charsetResults string
	db.Raw("SELECT @@character_set_client, @@character_set_connection, @@character_set_results").
		Row().Scan(&charsetClient, &charsetConnection, &charsetResults)

	t.Logf("Connection charset: client=%s, connection=%s, results=%s",
		charsetClient, charsetConnection, charsetResults)

	// 验证字符集配置
	if charsetClient != "utf8mb4" {
		t.Errorf("character_set_client should be utf8mb4, got %s", charsetClient)
	}
	if charsetConnection != "utf8mb4" {
		t.Errorf("character_set_connection should be utf8mb4, got %s", charsetConnection)
	}
	if charsetResults != "utf8mb4" {
		t.Errorf("character_set_results should be utf8mb4, got %s", charsetResults)
	}

	// 检查数据库默认字符集
	var dbCharset, dbCollation string
	db.Raw("SELECT DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = DATABASE()").
		Row().Scan(&dbCharset, &dbCollation)

	t.Logf("Database charset: %s, collation: %s", dbCharset, dbCollation)

	if dbCharset != "utf8mb4" {
		t.Errorf("Database charset should be utf8mb4, got %s", dbCharset)
	}
	if dbCollation != "utf8mb4_unicode_ci" {
		t.Errorf("Database collation should be utf8mb4_unicode_ci, got %s", dbCollation)
	}
}

// TestTableCharsetConfiguration 测试所有表的字符集配置
func TestTableCharsetConfiguration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "root:root123456@tcp(localhost:3306)/tokenhubhk?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=True&loc=Local"
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Skip("Database not available, skipping test")
		return
	}

	db.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci")

	// 检查所有表的字符集
	type TableInfo struct {
		TableName      string
		TableCollation string
	}

	var tables []TableInfo
	db.Raw(`
		SELECT TABLE_NAME, TABLE_COLLATION
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE()
		AND TABLE_TYPE = 'BASE TABLE'
		ORDER BY TABLE_NAME
	`).Scan(&tables)

	if len(tables) == 0 {
		t.Skip("No tables found in database")
		return
	}

	t.Logf("Checking %d tables...", len(tables))

	var incorrectTables []string
	for _, table := range tables {
		if table.TableCollation != "utf8mb4_unicode_ci" {
			incorrectTables = append(incorrectTables, table.TableName)
			t.Errorf("Table %s has incorrect collation: %s (expected utf8mb4_unicode_ci)",
				table.TableName, table.TableCollation)
		}
	}

	if len(incorrectTables) == 0 {
		t.Logf("✅ All %d tables use utf8mb4_unicode_ci collation", len(tables))
	} else {
		t.Errorf("❌ %d tables have incorrect collation: %v", len(incorrectTables), incorrectTables)
	}
}

// TestColumnCharsetConfiguration 测试所有文本列的字符集配置
func TestColumnCharsetConfiguration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "root:root123456@tcp(localhost:3306)/tokenhubhk?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=True&loc=Local"
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Skip("Database not available, skipping test")
		return
	}

	db.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci")

	// 检查所有文本列的字符集
	type ColumnInfo struct {
		TableName        string
		ColumnName       string
		CharacterSetName string
		CollationName    string
	}

	var columns []ColumnInfo
	db.Raw(`
		SELECT TABLE_NAME, COLUMN_NAME, CHARACTER_SET_NAME, COLLATION_NAME
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE()
		AND DATA_TYPE IN ('varchar', 'char', 'text', 'mediumtext', 'longtext')
		AND (CHARACTER_SET_NAME != 'utf8mb4' OR COLLATION_NAME != 'utf8mb4_unicode_ci')
		ORDER BY TABLE_NAME, COLUMN_NAME
	`).Scan(&columns)

	if len(columns) > 0 {
		t.Errorf("❌ Found %d columns with incorrect charset/collation:", len(columns))
		for _, col := range columns {
			t.Errorf("  - %s.%s: %s / %s",
				col.TableName, col.ColumnName, col.CharacterSetName, col.CollationName)
		}
	} else {
		t.Logf("✅ All text columns use utf8mb4_unicode_ci collation")
	}
}
