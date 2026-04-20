package exchange

import (
	"errors"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func setupLoaderTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestLoadConfigFromDB_EmptyDB_UsesFallback DB 空时应返回 fallback 原样
func TestLoadConfigFromDB_EmptyDB_UsesFallback(t *testing.T) {
	db := setupLoaderTestDB(t)
	fallback := Config{
		PrimaryURL:     "https://primary-fb",
		BackupURL:      "https://backup-fb",
		PublicURL:      "https://public-fb",
		AppCode:        "FALLBACK_APPCODE",
		AppKey:         "FALLBACK_APPKEY",
		AppSecret:      "FALLBACK_SECRET",
		CacheTTL:       24 * time.Hour,
		DefaultRate:    7.2,
		RequestTimeout: 10 * time.Second,
	}
	got := LoadConfigFromDB(db, fallback, nil)

	if got.PrimaryURL != fallback.PrimaryURL {
		t.Errorf("PrimaryURL: got %s, want %s", got.PrimaryURL, fallback.PrimaryURL)
	}
	if got.BackupURL != fallback.BackupURL {
		t.Errorf("BackupURL mismatch")
	}
	if got.AppCode != fallback.AppCode {
		t.Errorf("AppCode mismatch")
	}
	if got.CacheTTL != fallback.CacheTTL {
		t.Errorf("CacheTTL mismatch: %v vs %v", got.CacheTTL, fallback.CacheTTL)
	}
}

// TestLoadConfigFromDB_OverridesFallback DB 有值时覆盖 fallback
func TestLoadConfigFromDB_OverridesFallback(t *testing.T) {
	db := setupLoaderTestDB(t)

	seeds := map[string]string{
		CfgKeyPrimaryURL:     "https://db-primary",
		CfgKeyBackupURL:      "https://db-backup",
		CfgKeyPublicURL:      "https://db-public",
		CfgKeyAppCode:        "DB_APPCODE",
		CfgKeyAppKey:         "DB_APPKEY",
		CfgKeyCacheTTL:       "3600",
		CfgKeyDefaultRate:    "6.85",
		CfgKeyRequestTimeout: "20",
	}
	for k, v := range seeds {
		db.Create(&model.SystemConfig{Key: k, Value: v})
	}

	fallback := Config{
		PrimaryURL:     "https://fallback-primary",
		AppCode:        "FALLBACK",
		CacheTTL:       24 * time.Hour,
		DefaultRate:    7.2,
		RequestTimeout: 10 * time.Second,
	}
	got := LoadConfigFromDB(db, fallback, nil)

	if got.PrimaryURL != "https://db-primary" {
		t.Errorf("PrimaryURL not overridden: %s", got.PrimaryURL)
	}
	if got.BackupURL != "https://db-backup" {
		t.Errorf("BackupURL not overridden: %s", got.BackupURL)
	}
	if got.PublicURL != "https://db-public" {
		t.Errorf("PublicURL not overridden: %s", got.PublicURL)
	}
	if got.AppCode != "DB_APPCODE" {
		t.Errorf("AppCode not overridden: %s", got.AppCode)
	}
	if got.CacheTTL != 3600*time.Second {
		t.Errorf("CacheTTL not overridden: %v (want 3600s)", got.CacheTTL)
	}
	if got.DefaultRate != 6.85 {
		t.Errorf("DefaultRate not overridden: %f", got.DefaultRate)
	}
	if got.RequestTimeout != 20*time.Second {
		t.Errorf("RequestTimeout not overridden: %v", got.RequestTimeout)
	}
}

// TestLoadConfigFromDB_AppSecretDecryption 加密字段往返
func TestLoadConfigFromDB_AppSecretDecryption(t *testing.T) {
	db := setupLoaderTestDB(t)
	db.Create(&model.SystemConfig{
		Key:   CfgKeyAppSecretEncrypted,
		Value: "MOCK_CIPHERTEXT_BASE64",
	})

	decryptFn := func(encoded string) (string, error) {
		if encoded == "MOCK_CIPHERTEXT_BASE64" {
			return "PLAIN_SECRET", nil
		}
		return "", errors.New("invalid ciphertext")
	}

	got := LoadConfigFromDB(db, Config{AppSecret: "FALLBACK"}, decryptFn)
	if got.AppSecret != "PLAIN_SECRET" {
		t.Errorf("AppSecret not decrypted: %s", got.AppSecret)
	}
}

// TestLoadConfigFromDB_DecryptionFailureUsesFallback 解密失败时保留 fallback
func TestLoadConfigFromDB_DecryptionFailureUsesFallback(t *testing.T) {
	db := setupLoaderTestDB(t)
	db.Create(&model.SystemConfig{
		Key:   CfgKeyAppSecretEncrypted,
		Value: "CORRUPTED",
	})
	decryptFn := func(encoded string) (string, error) {
		return "", errors.New("decrypt failed")
	}
	got := LoadConfigFromDB(db, Config{AppSecret: "FALLBACK"}, decryptFn)
	if got.AppSecret != "FALLBACK" {
		t.Errorf("AppSecret should fall back on decrypt failure: %s", got.AppSecret)
	}
}

// TestLoadConfigFromDB_NilDecryptFn 解密函数为 nil 时不尝试解密
func TestLoadConfigFromDB_NilDecryptFn(t *testing.T) {
	db := setupLoaderTestDB(t)
	db.Create(&model.SystemConfig{Key: CfgKeyAppSecretEncrypted, Value: "CIPHER"})
	got := LoadConfigFromDB(db, Config{AppSecret: "FALLBACK"}, nil)
	if got.AppSecret != "FALLBACK" {
		t.Errorf("AppSecret should remain fallback when decryptFn is nil")
	}
}

// TestLoadConfigFromDB_InvalidIntValue 整数字段解析失败时回退
func TestLoadConfigFromDB_InvalidIntValue(t *testing.T) {
	db := setupLoaderTestDB(t)
	db.Create(&model.SystemConfig{Key: CfgKeyCacheTTL, Value: "not-a-number"})
	fallback := Config{CacheTTL: 24 * time.Hour}
	got := LoadConfigFromDB(db, fallback, nil)
	if got.CacheTTL != 24*time.Hour {
		t.Errorf("CacheTTL should fall back on parse error: %v", got.CacheTTL)
	}
}

// TestLoadConfigFromDB_EmptyStringValueFallsBack value="" 视为缺失
func TestLoadConfigFromDB_EmptyStringValueFallsBack(t *testing.T) {
	db := setupLoaderTestDB(t)
	db.Create(&model.SystemConfig{Key: CfgKeyPrimaryURL, Value: ""})
	fallback := Config{PrimaryURL: "https://fallback"}
	got := LoadConfigFromDB(db, fallback, nil)
	if got.PrimaryURL != "https://fallback" {
		t.Errorf("Empty DB value should not override fallback: %s", got.PrimaryURL)
	}
}

// TestLoadConfigFromDB_NilDB nil DB 不 panic
func TestLoadConfigFromDB_NilDB(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("LoadConfigFromDB(nil) should not panic: %v", r)
		}
	}()
	// 故意传 nil 验证安全性
	fallback := Config{AppCode: "FB"}
	got := LoadConfigFromDB(nil, fallback, nil)
	if got.AppCode != "FB" {
		t.Errorf("nil DB should return fallback unchanged")
	}
}

func TestGetIntSystemConfig(t *testing.T) {
	db := setupLoaderTestDB(t)
	db.Create(&model.SystemConfig{Key: "num_ok", Value: "42"})
	db.Create(&model.SystemConfig{Key: "num_bad", Value: "abc"})

	if v := getIntSystemConfig(db, "num_ok"); v != 42 {
		t.Errorf("expected 42, got %d", v)
	}
	if v := getIntSystemConfig(db, "num_bad"); v != 0 {
		t.Errorf("bad value should return 0, got %d", v)
	}
	if v := getIntSystemConfig(db, "missing"); v != 0 {
		t.Errorf("missing key should return 0, got %d", v)
	}
}

func TestGetFloatSystemConfig(t *testing.T) {
	db := setupLoaderTestDB(t)
	db.Create(&model.SystemConfig{Key: "rate_ok", Value: "7.2345"})
	if v := getFloatSystemConfig(db, "rate_ok"); v != 7.2345 {
		t.Errorf("expected 7.2345, got %f", v)
	}
	if v := getFloatSystemConfig(db, "missing"); v != 0 {
		t.Errorf("missing key should return 0")
	}
}
