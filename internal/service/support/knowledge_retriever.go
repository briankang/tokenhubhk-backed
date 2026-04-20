package support

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// ScoredChunk 带相似度评分的检索结果
type ScoredChunk struct {
	Chunk *model.KnowledgeChunk
	Score float32 // priority 加权后
	Raw   float32 // 原始 cosine 分数
}

// KnowledgeRetriever RAG 检索器
//
// 策略：
//  1. embedding(query) → 内存余弦相似度计算
//  2. priority 加权：hot_question/accepted_qa 高于 doc_article
//  3. 来源多样性：保证每种 source_type 至少返回 1 条
//  4. 阈值过滤（默认 0.5）
//  5. 结果缓存 5 min
//
// 容量假设：chunks 总量 < 2000 时纯内存计算性能可接受；
// 超过 5000 时建议接入外部向量库（pgvector / Milvus）。
type KnowledgeRetriever struct {
	db       *gorm.DB
	redis    *goredis.Client
	embed    *EmbeddingClient
	resolver *DynamicValueResolver

	// 内存 cache：所有 active chunks 的向量（5 min 刷新）
	mu           sync.RWMutex
	cacheAt      time.Time
	cacheEntries []cacheEntry
}

type cacheEntry struct {
	chunk  model.KnowledgeChunk
	vector []float32
}

// NewKnowledgeRetriever 构造检索器
func NewKnowledgeRetriever(db *gorm.DB, redis *goredis.Client, embed *EmbeddingClient, resolver *DynamicValueResolver) *KnowledgeRetriever {
	return &KnowledgeRetriever{db: db, redis: redis, embed: embed, resolver: resolver}
}

// RetrieveOptions 检索选项
type RetrieveOptions struct {
	TopK        int
	Threshold   float32 // 最低相似度（默认 0.5）
	MultiSource bool    // 保证来源多样性（默认 true）
}

// Retrieve 根据用户问题返回 TopK 最相关的 chunks
// query 应已翻译为中文（若用户用其他语言提问）
func (r *KnowledgeRetriever) Retrieve(ctx context.Context, query string, opts RetrieveOptions) ([]ScoredChunk, error) {
	if opts.TopK <= 0 {
		opts.TopK = 5
	}
	if opts.Threshold <= 0 {
		opts.Threshold = 0.5
	}

	// 1. Redis 缓存（key = md5(query) + topK）
	cacheKey := "support:kb:q:" + md5Hex(query) + fmt.Sprintf(":k%d", opts.TopK)
	if r.redis != nil {
		if ids, err := r.redis.Get(ctx, cacheKey).Result(); err == nil && ids != "" {
			return r.loadChunksByIDString(ctx, ids), nil
		}
	}

	// 2. 生成 query vector
	qVec, err := r.embed.Embed(ctx, query)
	if err != nil {
		logger.L.Warn("query embedding failed, fallback to keyword search", zap.Error(err))
		return r.keywordFallback(ctx, query, opts.TopK), nil
	}

	// 3. 加载所有 active chunks（内存 cache 5 min）
	entries := r.loadAllEntries(ctx)
	if len(entries) == 0 {
		return nil, nil
	}

	// 4. 并发算 cosine
	scored := make([]ScoredChunk, 0, len(entries))
	for i := range entries {
		raw := Cosine(qVec, entries[i].vector)
		// priority 加权：score * (1 + priority/100)
		weighted := raw * (1.0 + float32(entries[i].chunk.Priority)/100.0)
		scored = append(scored, ScoredChunk{
			Chunk: &entries[i].chunk,
			Score: weighted,
			Raw:   raw,
		})
	}

	// 5. 按 weighted 降序
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })

	// 6. 阈值过滤
	filtered := make([]ScoredChunk, 0, opts.TopK)
	for _, s := range scored {
		if s.Raw < opts.Threshold {
			break
		}
		filtered = append(filtered, s)
	}

	// 7. 来源多样性：按 source_type 去重保证至少 1 条（按权重顺序 hot_question > accepted_qa > faq > doc_article）
	var final []ScoredChunk
	if opts.MultiSource {
		final = diverseTopK(filtered, opts.TopK)
	} else {
		if len(filtered) > opts.TopK {
			final = filtered[:opts.TopK]
		} else {
			final = filtered
		}
	}

	// 8. fallback：若 < 2 条命中，补充关键字搜索
	if len(final) < 2 {
		kw := r.keywordFallback(ctx, query, 3)
		final = mergeDedupe(final, kw, opts.TopK)
	}

	// 9. 异步累加 HitCount
	if len(final) > 0 {
		go r.incrementHitCounts(final)
	}

	// 10. 缓存结果（仅 id 列表）
	if r.redis != nil && len(final) > 0 {
		ids := make([]string, len(final))
		for i, c := range final {
			ids[i] = fmt.Sprintf("%d", c.Chunk.ID)
		}
		_ = r.redis.Set(ctx, cacheKey, joinStr(ids, ","), 5*time.Minute).Err()
	}

	return final, nil
}

// loadAllEntries 加载所有 active chunks（带 5 min 内存缓存）
func (r *KnowledgeRetriever) loadAllEntries(ctx context.Context) []cacheEntry {
	r.mu.RLock()
	if time.Since(r.cacheAt) < 5*time.Minute && r.cacheEntries != nil {
		entries := r.cacheEntries
		r.mu.RUnlock()
		return entries
	}
	r.mu.RUnlock()

	// 缓存过期，重新加载
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.cacheAt) < 5*time.Minute && r.cacheEntries != nil {
		return r.cacheEntries
	}

	var chunks []model.KnowledgeChunk
	if err := r.db.WithContext(ctx).Where("is_active = ?", true).
		Where("embedding IS NOT NULL AND embedding != ''").
		Find(&chunks).Error; err != nil {
		logger.L.Error("load knowledge chunks", zap.Error(err))
		return nil
	}

	entries := make([]cacheEntry, 0, len(chunks))
	for i := range chunks {
		v, err := UnmarshalVector(chunks[i].Embedding)
		if err != nil || len(v) == 0 {
			continue
		}
		entries = append(entries, cacheEntry{chunk: chunks[i], vector: v})
	}
	r.cacheEntries = entries
	r.cacheAt = time.Now()
	logger.L.Info("knowledge chunks loaded", zap.Int("count", len(entries)))
	return entries
}

// InvalidateCache 强制刷新内存缓存
func (r *KnowledgeRetriever) InvalidateCache() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cacheEntries = nil
	r.cacheAt = time.Time{}
}

// diverseTopK 保证每种 source_type 至少 1 条（若有），剩余按分数填
func diverseTopK(scored []ScoredChunk, topK int) []ScoredChunk {
	if len(scored) <= topK {
		return scored
	}
	// 按 source_type 分组取 top1
	priority := []string{"hot_question", "accepted_qa", "faq", "doc_article"}
	seen := make(map[string]bool)
	var result []ScoredChunk
	for _, p := range priority {
		for _, s := range scored {
			if s.Chunk.SourceType == p && !seen[p] {
				result = append(result, s)
				seen[p] = true
				break
			}
		}
		if len(result) >= topK {
			return result[:topK]
		}
	}
	// 剩余槽位按原顺序填（已排序）
	usedIDs := make(map[uint]bool)
	for _, r := range result {
		usedIDs[r.Chunk.ID] = true
	}
	for _, s := range scored {
		if usedIDs[s.Chunk.ID] {
			continue
		}
		result = append(result, s)
		if len(result) >= topK {
			break
		}
	}
	return result
}

// keywordFallback 关键字 LIKE 搜索（embedding 失败 / 结果不足时兜底）
func (r *KnowledgeRetriever) keywordFallback(ctx context.Context, query string, limit int) []ScoredChunk {
	pattern := "%" + query + "%"
	if len([]rune(query)) > 30 {
		pattern = "%" + string([]rune(query)[:30]) + "%"
	}
	var chunks []model.KnowledgeChunk
	if err := r.db.WithContext(ctx).Where("is_active = ?", true).
		Where("title LIKE ? OR content LIKE ?", pattern, pattern).
		Order("priority DESC, hit_count DESC").
		Limit(limit).Find(&chunks).Error; err != nil {
		return nil
	}
	out := make([]ScoredChunk, 0, len(chunks))
	for i := range chunks {
		out = append(out, ScoredChunk{
			Chunk: &chunks[i],
			Score: 0.4, // 合理兜底分数（低于阈值但可展示）
			Raw:   0.4,
		})
	}
	return out
}

func mergeDedupe(a, b []ScoredChunk, maxN int) []ScoredChunk {
	seen := make(map[uint]bool)
	out := make([]ScoredChunk, 0, maxN)
	for _, x := range a {
		if !seen[x.Chunk.ID] {
			out = append(out, x)
			seen[x.Chunk.ID] = true
		}
		if len(out) >= maxN {
			return out
		}
	}
	for _, x := range b {
		if !seen[x.Chunk.ID] {
			out = append(out, x)
			seen[x.Chunk.ID] = true
		}
		if len(out) >= maxN {
			return out
		}
	}
	return out
}

func (r *KnowledgeRetriever) loadChunksByIDString(ctx context.Context, ids string) []ScoredChunk {
	if ids == "" {
		return nil
	}
	parts := splitStr(ids, ",")
	if len(parts) == 0 {
		return nil
	}
	var chunks []model.KnowledgeChunk
	if err := r.db.WithContext(ctx).Where("id IN ?", parts).Find(&chunks).Error; err != nil {
		return nil
	}
	// 保序（按 ids 顺序）
	idxMap := make(map[uint]int, len(parts))
	for i, p := range parts {
		var id uint
		_, _ = fmt.Sscanf(p, "%d", &id)
		idxMap[id] = i
	}
	out := make([]ScoredChunk, len(chunks))
	for i := range chunks {
		out[i] = ScoredChunk{Chunk: &chunks[i]}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return idxMap[out[i].Chunk.ID] < idxMap[out[j].Chunk.ID]
	})
	return out
}

func (r *KnowledgeRetriever) incrementHitCounts(chunks []ScoredChunk) {
	if len(chunks) == 0 {
		return
	}
	ids := make([]uint, 0, len(chunks))
	for _, c := range chunks {
		ids = append(ids, c.Chunk.ID)
	}
	// 批量 +1
	_ = r.db.Model(&model.KnowledgeChunk{}).Where("id IN ?", ids).
		UpdateColumn("hit_count", gorm.Expr("hit_count + ?", 1)).Error
}

// ---- helpers ----

func md5Hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func joinStr(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

func splitStr(s, sep string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if i+len(sep) <= len(s) && s[i:i+len(sep)] == sep {
			out = append(out, s[start:i])
			start = i + len(sep)
			i += len(sep) - 1
		}
	}
	out = append(out, s[start:])
	return out
}
