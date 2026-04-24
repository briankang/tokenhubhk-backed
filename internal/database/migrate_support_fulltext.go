package database

import (
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/logger"
)

// RunSupportFullTextMigration 在 knowledge_chunks 表上创建 MySQL / PolarDB
// CJK 安全的 FULLTEXT 索引（使用 ngram 分词器）。
//
// 背景：
//   - 原 keywordFallback 使用 `title LIKE ? OR content LIKE ?` 无法走索引，全表扫描；
//     当 knowledge_chunks 达到数万行时兜底查询会成为瓶颈。
//   - MySQL 5.7.6+ / PolarDB 均内置 ngram 分词器（`ngram_token_size` 默认 2），
//     对中文二字词特别友好（"充值" / "余额" / "提现" 等高频运营词都能命中）。
//
// 执行步骤（全部幂等）：
//  1. 检查 knowledge_chunks 上是否已存在 `ft_knowledge_chunks_title_content` 索引，存在则跳过
//  2. 执行 `ALTER TABLE ADD FULLTEXT INDEX ... WITH PARSER ngram` 创建索引
//  3. 失败 → warn 日志，不 panic（允许在不支持 ngram 的 MySQL 变种 / SQLite 上静默降级）
//
// 为什么用 ADD FULLTEXT INDEX 而非 CREATE FULLTEXT INDEX：
//   ALTER TABLE 语法在 MySQL/PolarDB 下更稳妥，且能原子附着到表的元数据。
//
// 为什么不在 AutoMigrate 里声明：
//   GORM AutoMigrate 的 `index:,class:FULLTEXT` tag 不支持 `WITH PARSER ngram` 子句，
//   只能通过原生 SQL 迁移补齐。
func RunSupportFullTextMigration(db *gorm.DB) {
	start := time.Now()

	// Step 1: 索引存在性检查
	exists, err := fullTextIndexExists(db, "knowledge_chunks", "ft_knowledge_chunks_title_content")
	if err != nil {
		logger.L.Warn("support fulltext: existence check failed (non-MySQL instance?)", zap.Error(err))
		return
	}
	if exists {
		logger.L.Info("support fulltext: index already exists, skipping")
		return
	}

	// Step 2: 创建索引
	ddl := `ALTER TABLE knowledge_chunks
		ADD FULLTEXT INDEX ft_knowledge_chunks_title_content (title, content)
		WITH PARSER ngram`
	if err := db.Exec(ddl).Error; err != nil {
		// 已存在（并发创建 / AutoMigrate 差异）
		if isDuplicateFullTextIndexErr(err) {
			logger.L.Info("support fulltext: index already exists (duplicate), skipping")
			return
		}
		logger.L.Warn("support fulltext: create FULLTEXT index failed",
			zap.Error(err),
			zap.String("hint", "need MySQL 5.7.6+ or PolarDB with ngram parser"))
		return
	}

	logger.L.Info("support fulltext migration: complete",
		zap.Duration("duration", time.Since(start)))
}

// fullTextIndexExists 查询 INFORMATION_SCHEMA.STATISTICS，判断索引是否存在。
// 返回 (exists, err)：err != nil 表示查询失败（通常是 SQLite 等无该视图的数据库）。
func fullTextIndexExists(db *gorm.DB, table, indexName string) (bool, error) {
	var cnt int64
	err := db.Raw(`
		SELECT COUNT(*)
		FROM INFORMATION_SCHEMA.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE()
		  AND TABLE_NAME = ?
		  AND INDEX_NAME = ?`, table, indexName).Row().Scan(&cnt)
	if err != nil {
		return false, err
	}
	return cnt > 0, nil
}

// isDuplicateFullTextIndexErr 识别 MySQL/PolarDB 的「索引已存在」错误码 1061 / 相关文案
func isDuplicateFullTextIndexErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate key name") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "error 1061")
}
