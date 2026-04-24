package support

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// KnowledgeRebuilder 知识库重建器
// 负责：DocArticle / HotQuestion / AcceptedAnswer 三类来源 → 切片 → embedding → 写 knowledge_chunks
//
// 幂等：按 embedding_hash = md5(content) 判重，内容未变时跳过
type KnowledgeRebuilder struct {
	db    *gorm.DB
	embed *EmbeddingClient
	// Phase A4: VectorStore=polardb 时，每条新建/更新的 chunk 同步写入 embedding_vec 列
	vectorStore string
}

func NewKnowledgeRebuilder(db *gorm.DB, embed *EmbeddingClient) *KnowledgeRebuilder {
	return &KnowledgeRebuilder{db: db, embed: embed, vectorStore: "memory"}
}

// SetVectorStore 切换向量存储模式（由 Bootstrap 在启动期调用）
func (b *KnowledgeRebuilder) SetVectorStore(mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "polardb" {
		b.vectorStore = "polardb"
	} else {
		b.vectorStore = "memory"
	}
}

// RebuildStats 重建统计
type RebuildStats struct {
	Sources  int // 扫描的源文档数
	Created  int // 新增 chunks
	Updated  int // 更新 chunks
	Deleted  int // 删除失效 chunks（源已不存在或内容已改）
	Skipped  int // 跳过（内容未变）
	Failed   int // embedding 或写入失败数
}

// RebuildFull 全量重建
//  1. 扫全部 is_published=true 的 DocArticle / HotQuestion + approved 的 AcceptedAnswer
//  2. 对每个源切片后与现有 chunks 对比（embedding_hash）
//  3. 新增/更新向量 + 删除已不存在的 chunks
func (b *KnowledgeRebuilder) RebuildFull(ctx context.Context) (*RebuildStats, error) {
	stats := &RebuildStats{}

	// 1. DocArticle
	var docs []model.DocArticle
	if err := b.db.WithContext(ctx).Where("is_published = ?", true).Find(&docs).Error; err != nil {
		return stats, fmt.Errorf("load doc_articles: %w", err)
	}
	for i := range docs {
		s := b.rebuildSource(ctx, "doc_article", docs[i].ID, docs[i].Slug, docs[i].Title, docs[i].Content, 0)
		stats.merge(s)
	}
	stats.Sources += len(docs)

	// 2. HotQuestion
	var hqs []model.HotQuestion
	if err := b.db.WithContext(ctx).Where("is_published = ?", true).Find(&hqs).Error; err != nil {
		return stats, fmt.Errorf("load hot_questions: %w", err)
	}
	for i := range hqs {
		// 拼接 title + question_body + curated_answer 作为检索内容
		merged := "# " + hqs[i].Title + "\n\n## 问题\n" + hqs[i].QuestionBody + "\n\n## 答案\n" + hqs[i].CuratedAnswer
		s := b.rebuildSource(ctx, "hot_question", hqs[i].ID, "", hqs[i].Title, merged, 10)
		stats.merge(s)
	}
	stats.Sources += len(hqs)

	// 3. AcceptedAnswer (approved)
	var aas []model.AcceptedAnswer
	if err := b.db.WithContext(ctx).Where("status = ?", "approved").Find(&aas).Error; err != nil {
		return stats, fmt.Errorf("load accepted_answers: %w", err)
	}
	for i := range aas {
		merged := "# 用户采纳的问答\n\n## 问题\n" + aas[i].Question + "\n\n## 答案\n" + aas[i].Answer
		s := b.rebuildSource(ctx, "accepted_qa", aas[i].ID, "", aas[i].Question, merged, 10)
		stats.merge(s)
	}
	stats.Sources += len(aas)

	// 4. 清理孤儿：knowledge_chunks 中 source_id 已不存在的
	if err := b.purgeOrphans(ctx, stats); err != nil {
		logger.L.Warn("purge orphans failed", zap.Error(err))
	}

	logger.L.Info("knowledge rebuild full complete",
		zap.Int("sources", stats.Sources),
		zap.Int("created", stats.Created),
		zap.Int("updated", stats.Updated),
		zap.Int("deleted", stats.Deleted),
		zap.Int("skipped", stats.Skipped),
		zap.Int("failed", stats.Failed),
	)
	return stats, nil
}

// RebuildSource 单源重建（DocArticle Update 后调用，指定单个 source_id 重建）
// sourceType: doc_article / hot_question / accepted_qa
func (b *KnowledgeRebuilder) RebuildSource(ctx context.Context, sourceType string, sourceID uint) (*RebuildStats, error) {
	stats := &RebuildStats{Sources: 1}
	switch sourceType {
	case "doc_article":
		var doc model.DocArticle
		if err := b.db.WithContext(ctx).First(&doc, sourceID).Error; err != nil {
			return stats, err
		}
		if !doc.IsPublished {
			// 下架 → 删除相关 chunks
			if err := b.deleteBySource(ctx, "doc_article", sourceID); err != nil {
				return stats, err
			}
			stats.Deleted++
			return stats, nil
		}
		s := b.rebuildSource(ctx, "doc_article", doc.ID, doc.Slug, doc.Title, doc.Content, 0)
		stats.merge(s)
	case "hot_question":
		var hq model.HotQuestion
		if err := b.db.WithContext(ctx).First(&hq, sourceID).Error; err != nil {
			return stats, err
		}
		if !hq.IsPublished {
			_ = b.deleteBySource(ctx, "hot_question", sourceID)
			stats.Deleted++
			return stats, nil
		}
		merged := "# " + hq.Title + "\n\n## 问题\n" + hq.QuestionBody + "\n\n## 答案\n" + hq.CuratedAnswer
		s := b.rebuildSource(ctx, "hot_question", hq.ID, "", hq.Title, merged, 10)
		stats.merge(s)
	case "accepted_qa":
		var aa model.AcceptedAnswer
		if err := b.db.WithContext(ctx).First(&aa, sourceID).Error; err != nil {
			return stats, err
		}
		if aa.Status != "approved" {
			_ = b.deleteBySource(ctx, "accepted_qa", sourceID)
			stats.Deleted++
			return stats, nil
		}
		merged := "# 用户采纳的问答\n\n## 问题\n" + aa.Question + "\n\n## 答案\n" + aa.Answer
		s := b.rebuildSource(ctx, "accepted_qa", aa.ID, "", aa.Question, merged, 10)
		stats.merge(s)
	default:
		return stats, fmt.Errorf("unsupported source_type: %s", sourceType)
	}
	return stats, nil
}

// rebuildSource 为单个源做切片 + embedding + 写入
func (b *KnowledgeRebuilder) rebuildSource(ctx context.Context, sourceType string, sourceID uint, slug, title, content string, priority int) RebuildStats {
	stats := RebuildStats{}
	chunks := SplitMarkdown(title, content)
	if len(chunks) == 0 {
		return stats
	}

	// 当前源已存在的 chunks
	var existing []model.KnowledgeChunk
	if err := b.db.WithContext(ctx).
		Where("source_type = ? AND source_id = ?", sourceType, sourceID).
		Find(&existing).Error; err != nil {
		stats.Failed++
		return stats
	}
	// 按 chunk_index 映射
	existingByIdx := make(map[int]*model.KnowledgeChunk, len(existing))
	for i := range existing {
		existingByIdx[existing[i].ChunkIndex] = &existing[i]
	}

	// 准备待 embedding 的文本
	type pendingChunk struct {
		targetIdx   int
		chunk       Chunk
		hash        string
		existingRow *model.KnowledgeChunk
	}
	var toEmbed []pendingChunk
	for _, ch := range chunks {
		contentHash := hash(ch.Content)
		row := existingByIdx[ch.ChunkIndex]
		if row != nil && row.EmbeddingHash == contentHash && row.IsActive {
			stats.Skipped++
			continue
		}
		toEmbed = append(toEmbed, pendingChunk{
			targetIdx:   ch.ChunkIndex,
			chunk:       ch,
			hash:        contentHash,
			existingRow: row,
		})
	}

	// 批量 embed（每 25 条一批）
	for i := 0; i < len(toEmbed); i += 25 {
		end := i + 25
		if end > len(toEmbed) {
			end = len(toEmbed)
		}
		batch := toEmbed[i:end]
		texts := make([]string, len(batch))
		for j, p := range batch {
			texts[j] = p.chunk.Content
		}
		vectors, err := b.embed.EmbedBatch(ctx, texts)
		if err != nil {
			logger.L.Warn("embedding batch failed",
				zap.String("source_type", sourceType),
				zap.Uint("source_id", sourceID),
				zap.Error(err))
			stats.Failed += len(batch)
			continue
		}

		// 写库
		for j, p := range batch {
			vecStr, _ := MarshalVector(vectors[j])
			var rowID uint
			if p.existingRow != nil {
				// 更新
				p.existingRow.Title = p.chunk.Title
				p.existingRow.Content = p.chunk.Content
				p.existingRow.Embedding = vecStr
				p.existingRow.EmbeddingHash = p.hash
				p.existingRow.Tokens = EstimateTokens(p.chunk.Content)
				p.existingRow.Priority = priority
				p.existingRow.SourceSlug = slug
				p.existingRow.IsActive = true
				if err := b.db.Save(p.existingRow).Error; err != nil {
					stats.Failed++
					continue
				}
				stats.Updated++
				rowID = p.existingRow.ID
			} else {
				// 新建
				row := model.KnowledgeChunk{
					SourceType:    sourceType,
					SourceID:      sourceID,
					SourceSlug:    slug,
					Title:         p.chunk.Title,
					Content:       p.chunk.Content,
					ChunkIndex:    p.targetIdx,
					Embedding:     vecStr,
					EmbeddingHash: p.hash,
					Tokens:        EstimateTokens(p.chunk.Content),
					Priority:      priority,
					IsActive:      true,
				}
				if err := b.db.Create(&row).Error; err != nil {
					stats.Failed++
					continue
				}
				stats.Created++
				rowID = row.ID
			}
			// Phase A4: PolarDB 模式下同步写 embedding_vec 列
			// 单行 UPDATE 失败不阻塞主流程（warn + 下次全量重建可补齐）
			if b.vectorStore == "polardb" && rowID > 0 {
				b.writeEmbeddingVec(ctx, rowID, vectors[j])
			}
		}
	}

	// 删除：chunks 数量减少时，清理多余 existing
	currentMaxIdx := -1
	for _, ch := range chunks {
		if ch.ChunkIndex > currentMaxIdx {
			currentMaxIdx = ch.ChunkIndex
		}
	}
	for idx, row := range existingByIdx {
		if idx > currentMaxIdx {
			if err := b.db.Delete(row).Error; err == nil {
				stats.Deleted++
			}
		}
	}

	return stats
}

// deleteBySource 删除某 source 的全部 chunks
func (b *KnowledgeRebuilder) deleteBySource(ctx context.Context, sourceType string, sourceID uint) error {
	return b.db.WithContext(ctx).
		Where("source_type = ? AND source_id = ?", sourceType, sourceID).
		Delete(&model.KnowledgeChunk{}).Error
}

// purgeOrphans 清理 source_type 存在但 source_id 已被删除的 chunks
func (b *KnowledgeRebuilder) purgeOrphans(ctx context.Context, stats *RebuildStats) error {
	// doc_article 孤儿
	var docOrphans []uint
	if err := b.db.WithContext(ctx).Raw(`
		SELECT kc.source_id FROM knowledge_chunks kc
		LEFT JOIN doc_articles da ON da.id = kc.source_id AND da.is_published = true
		WHERE kc.source_type = 'doc_article' AND da.id IS NULL
		GROUP BY kc.source_id
	`).Scan(&docOrphans).Error; err != nil {
		return err
	}
	for _, id := range docOrphans {
		if err := b.deleteBySource(ctx, "doc_article", id); err == nil {
			stats.Deleted++
		}
	}
	// hot_question / accepted_qa 同理（简化版）
	b.db.WithContext(ctx).Exec(`
		DELETE FROM knowledge_chunks WHERE source_type = 'hot_question' AND source_id NOT IN
		(SELECT id FROM hot_questions WHERE is_published = true)
	`)
	b.db.WithContext(ctx).Exec(`
		DELETE FROM knowledge_chunks WHERE source_type = 'accepted_qa' AND source_id NOT IN
		(SELECT id FROM accepted_answers WHERE status = 'approved')
	`)
	return nil
}

func (s *RebuildStats) merge(o RebuildStats) {
	s.Created += o.Created
	s.Updated += o.Updated
	s.Deleted += o.Deleted
	s.Skipped += o.Skipped
	s.Failed += o.Failed
}

func hash(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

// writeEmbeddingVec 在 PolarDB 模式下，把 float32 向量写入 knowledge_chunks.embedding_vec 列。
// SQL 使用 STRING_TO_VECTOR(?) 把 "[v1,v2,...]" 文本转换为 VECTOR 类型。
// 失败时 warn + continue（不影响主重建流程，下次全量迁移/重建可补齐）。
func (b *KnowledgeRebuilder) writeEmbeddingVec(ctx context.Context, rowID uint, vec []float32) {
	if len(vec) == 0 {
		return
	}
	var sb strings.Builder
	sb.Grow(len(vec) * 10)
	sb.WriteByte('[')
	for i, x := range vec {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "%.6f", x)
	}
	sb.WriteByte(']')

	if err := b.db.WithContext(ctx).Exec(`
		UPDATE knowledge_chunks
		SET embedding_vec = STRING_TO_VECTOR(?)
		WHERE id = ?`, sb.String(), rowID).Error; err != nil {
		logger.L.Warn("polardb: write embedding_vec failed",
			zap.Uint("id", rowID), zap.Error(err))
	}
}
