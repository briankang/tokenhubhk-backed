package database

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/logger"
)

// RunPolarDBVectorMigration 将 knowledge_chunks 表升级为支持 PolarSearch 向量检索。
//
// 仅在 config.Global.Support.VectorStore == "polardb" 时调用。
// 对开源 MySQL / SQLite / PolarDB 之外的方言，应直接跳过（由调用方保证）。
//
// 执行步骤（全部幂等）：
//  1. 把 knowledge_chunks 表切换为 COLUMNAR=1（PolarSearch 需要列存表属性）。
//  2. 添加 embedding_vec VECTOR(1024) 列 + HNSW 索引 COMMENT。
//  3. 把存量 knowledge_chunks.embedding（JSON 字符串）批量回填到 embedding_vec
//     列（STRING_TO_VECTOR('[...]')）。
//  4. 打开当前会话的 imci_enable_vector_search 开关（仅提示，不影响存量）。
//
// 失败时输出 warn 日志，不 panic——对普通 MySQL 实例这些 DDL 会全部失败并被静默忽略，
// VectorStore 运行时再次降级到 memory 扫描即可。
func RunPolarDBVectorMigration(db *gorm.DB) {
	start := time.Now()

	// Step 1: 设置表属性为列存
	if err := db.Exec("ALTER TABLE knowledge_chunks COMMENT='COLUMNAR=1'").Error; err != nil {
		logger.L.Warn("polardb vector: set COLUMNAR=1 failed (non-PolarDB instance?)",
			zap.Error(err))
		// 非 PolarDB 时直接返回，不继续执行下面的 DDL
		return
	}

	// Step 2: 添加 VECTOR 列（幂等：列已存在会报错，忽略即可）
	addCol := `ALTER TABLE knowledge_chunks
		ADD COLUMN embedding_vec VECTOR(1024)
		COMMENT 'imci_vector_index=HNSW(metric=COSINE, max_degree=16)'`
	if err := db.Exec(addCol).Error; err != nil {
		if !isDuplicateColumnErr(err) {
			logger.L.Warn("polardb vector: add embedding_vec column failed", zap.Error(err))
			return
		}
		logger.L.Info("polardb vector: embedding_vec column already exists, skipping create")
	}

	// Step 3: 批量回填 embedding JSON → embedding_vec VECTOR
	backfilled, err := backfillPolarDBVectors(db)
	if err != nil {
		logger.L.Warn("polardb vector: backfill failed", zap.Error(err))
	}

	// Step 4: 会话级开关提示（非持久化，检索前还需要每个连接 SET ON）
	_ = db.Exec("SET imci_enable_vector_search = ON").Error

	logger.L.Info("polardb vector migration: complete",
		zap.Int("backfilled", backfilled),
		zap.Duration("duration", time.Since(start)))
}

// backfillPolarDBVectors 扫描 knowledge_chunks 中 embedding_vec 为 NULL 但 embedding JSON 非空的行，
// 逐条调用 STRING_TO_VECTOR() 写入列。
//
// 分批 500 条避免长事务。仅操作 is_active=true 的行，历史下架的 chunk 不回填。
func backfillPolarDBVectors(db *gorm.DB) (int, error) {
	type row struct {
		ID        uint
		Embedding string
	}
	total := 0
	const batch = 500
	for {
		var rows []row
		if err := db.Raw(`
			SELECT id, embedding
			FROM knowledge_chunks
			WHERE is_active = 1
			  AND embedding_vec IS NULL
			  AND embedding IS NOT NULL
			  AND CHAR_LENGTH(embedding) > 10
			LIMIT ?`, batch).Scan(&rows).Error; err != nil {
			return total, fmt.Errorf("scan rows: %w", err)
		}
		if len(rows) == 0 {
			break
		}

		for _, r := range rows {
			// 校验是否为合法 float32 数组 JSON
			var vec []float32
			if err := json.Unmarshal([]byte(r.Embedding), &vec); err != nil || len(vec) == 0 {
				// 无效 JSON → 标记为空字符串绕过循环（防止下次再 hit）
				db.Exec(`UPDATE knowledge_chunks SET embedding='' WHERE id=?`, r.ID)
				continue
			}
			str := vectorToPolarDBString(vec)
			if err := db.Exec(`
				UPDATE knowledge_chunks
				SET embedding_vec = STRING_TO_VECTOR(?)
				WHERE id = ?`, str, r.ID).Error; err != nil {
				logger.L.Warn("polardb vector: backfill row failed",
					zap.Uint("id", r.ID), zap.Error(err))
				continue
			}
			total++
		}

		if len(rows) < batch {
			break
		}
	}
	return total, nil
}

// vectorToPolarDBString 把 float32 切片转成 PolarDB STRING_TO_VECTOR 接受的字符串
// 格式: "[0.1,0.2,0.3,...]"
func vectorToPolarDBString(v []float32) string {
	sb := strings.Builder{}
	sb.Grow(len(v) * 10)
	sb.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		// 保留 6 位精度，与 text-embedding-v3 输出精度相当
		fmt.Fprintf(&sb, "%.6f", x)
	}
	sb.WriteByte(']')
	return sb.String()
}

// isDuplicateColumnErr 识别 MySQL/PolarDB 的「列已存在」错误
func isDuplicateColumnErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "error 1060")
}
