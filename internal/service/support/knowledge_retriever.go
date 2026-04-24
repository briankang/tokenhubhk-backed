package support

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
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
//  1. embedding(query) → 内存余弦相似度计算（VectorStore="memory" 默认）
//     或 PolarDB PolarSearch HNSW 索引检索（VectorStore="polardb"）
//  2. priority 加权：hot_question/accepted_qa 高于 doc_article
//  3. 来源多样性：保证每种 source_type 至少返回 1 条
//  4. 阈值过滤（默认 0.5）
//  5. 结果缓存 5 min
//
// 容量假设：memory 模式 chunks < 2000 时性能可接受；
// PolarSearch 模式依赖向量索引，P99 < 10ms，适合 10k+ chunks。
type KnowledgeRetriever struct {
	db       *gorm.DB
	redis    *goredis.Client
	embed    *EmbeddingClient
	resolver *DynamicValueResolver

	// Phase A3: 向量存储模式（"memory" | "polardb"），决定走内存扫描还是 SQL 索引
	vectorStore string

	// 内存 cache：所有 active chunks 的向量（5 min 刷新，仅 memory 模式使用）
	mu           sync.RWMutex
	cacheAt      time.Time
	cacheEntries []cacheEntry
}

type cacheEntry struct {
	chunk  model.KnowledgeChunk
	vector []float32
}

// NewKnowledgeRetriever 构造检索器（默认 memory 模式，保持向后兼容）
func NewKnowledgeRetriever(db *gorm.DB, redis *goredis.Client, embed *EmbeddingClient, resolver *DynamicValueResolver) *KnowledgeRetriever {
	return &KnowledgeRetriever{db: db, redis: redis, embed: embed, resolver: resolver, vectorStore: "memory"}
}

// SetVectorStore 切换向量存储模式（由 Bootstrap 在启动期调用）
// 合法值："memory" | "polardb"；未识别值视作 memory
func (r *KnowledgeRetriever) SetVectorStore(mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "polardb" {
		r.vectorStore = "polardb"
	} else {
		r.vectorStore = "memory"
	}
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

	// Phase A3: PolarDB PolarSearch 分发
	// 当 VectorStore=polardb 时走 HNSW 索引 SQL，否则回退内存扫描
	if r.vectorStore == "polardb" {
		if polar := r.retrieveViaPolarSearch(ctx, qVec, opts); polar != nil {
			// PolarSearch 成功：直接进入 fallback 补充 + 缓存 + 命中计数
			return r.finalizeRetrieval(ctx, polar, query, cacheKey, opts), nil
		}
		// PolarSearch 失败（索引未就绪 / SQL 报错）→ 降级到内存路径
		logger.L.Warn("polarsearch returned nil, falling back to memory scan")
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

// retrieveViaPolarSearch 通过 PolarDB PolarSearch HNSW 索引检索。
//
// SQL 策略：
//  1. `SET imci_enable_vector_search = ON` 会话级开关（每次连接复位，必须每次设）
//  2. `ORDER BY DISTANCE(embedding_vec, STRING_TO_VECTOR(?), 'COSINE') ASC` 走索引
//  3. `LIMIT topK * 4` 过采样，之后应用 priority 加权 + 阈值过滤
//  4. 只选 `is_active=1 AND embedding_vec IS NOT NULL` 的行
//
// 返回 nil 表示 SQL 出错（调用方会 fallback 到内存扫描）；
// 返回空切片 [] 表示 SQL 成功但无匹配，作为终态返回，不再 fallback。
func (r *KnowledgeRetriever) retrieveViaPolarSearch(ctx context.Context, qVec []float32, opts RetrieveOptions) []ScoredChunk {
	if len(qVec) == 0 {
		return nil
	}

	// 1. 会话级开关
	if err := r.db.WithContext(ctx).Exec("SET imci_enable_vector_search = ON").Error; err != nil {
		logger.L.Warn("polarsearch: set imci_enable_vector_search failed", zap.Error(err))
		return nil
	}

	// 2. 构造向量字面量
	qStr := vectorToPolarDBLiteral(qVec)

	// 3. 过采样 LIMIT（为后续阈值过滤预留）
	oversample := opts.TopK * 4
	if oversample < 20 {
		oversample = 20
	}

	// 4. 执行 KNN SQL
	type polarRow struct {
		ID            uint
		SourceType    string
		SourceID      uint
		SourceSlug    string
		Title         string
		Content       string
		ChunkIndex    int
		Tokens        int
		Priority      int
		HitCount      int
		EmbeddingHash string
		IsActive      bool
		Distance      float64
	}
	var rows []polarRow
	sqlText := `
		SELECT id, source_type, source_id, source_slug, title, content, chunk_index, tokens,
		       priority, hit_count, embedding_hash, is_active,
		       DISTANCE(embedding_vec, STRING_TO_VECTOR(?), 'COSINE') AS distance
		FROM knowledge_chunks
		WHERE is_active = 1 AND embedding_vec IS NOT NULL
		ORDER BY DISTANCE(embedding_vec, STRING_TO_VECTOR(?), 'COSINE') ASC
		LIMIT ?`
	if err := r.db.WithContext(ctx).Raw(sqlText, qStr, qStr, oversample).Scan(&rows).Error; err != nil {
		logger.L.Warn("polarsearch: KNN query failed", zap.Error(err))
		return nil
	}
	if len(rows) == 0 {
		return []ScoredChunk{} // 空结果 ≠ 错误
	}

	// 5. 转换为 ScoredChunk + priority 加权 + 阈值过滤
	// COSINE DISTANCE = 1 - cosine_similarity; 所以 similarity = 1 - distance
	scored := make([]ScoredChunk, 0, len(rows))
	for i := range rows {
		raw := float32(1.0 - rows[i].Distance)
		if raw < opts.Threshold {
			continue
		}
		weighted := raw * (1.0 + float32(rows[i].Priority)/100.0)
		chunk := model.KnowledgeChunk{
			SourceType:    rows[i].SourceType,
			SourceID:      rows[i].SourceID,
			SourceSlug:    rows[i].SourceSlug,
			Title:         rows[i].Title,
			Content:       rows[i].Content,
			ChunkIndex:    rows[i].ChunkIndex,
			Tokens:        rows[i].Tokens,
			Priority:      rows[i].Priority,
			HitCount:      rows[i].HitCount,
			EmbeddingHash: rows[i].EmbeddingHash,
			IsActive:      rows[i].IsActive,
		}
		chunk.ID = rows[i].ID
		scored = append(scored, ScoredChunk{Chunk: &chunk, Score: weighted, Raw: raw})
	}
	if len(scored) == 0 {
		return []ScoredChunk{}
	}

	// 6. 按 weighted 降序（索引已按 distance 排，但 priority 加权后可能乱序）
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })

	// 7. 来源多样性 + TopK 截断
	if opts.MultiSource {
		return diverseTopK(scored, opts.TopK)
	}
	if len(scored) > opts.TopK {
		return scored[:opts.TopK]
	}
	return scored
}

// finalizeRetrieval 检索完成后的通用收尾：
//   - 若结果 < 2 条则补充关键字 fallback
//   - 异步累加命中计数
//   - 缓存 id 列表到 Redis
func (r *KnowledgeRetriever) finalizeRetrieval(ctx context.Context, final []ScoredChunk, query, cacheKey string, opts RetrieveOptions) []ScoredChunk {
	if len(final) < 2 {
		kw := r.keywordFallback(ctx, query, 3)
		final = mergeDedupe(final, kw, opts.TopK)
	}
	if len(final) > 0 {
		go r.incrementHitCounts(final)
	}
	if r.redis != nil && len(final) > 0 {
		ids := make([]string, len(final))
		for i, c := range final {
			ids[i] = fmt.Sprintf("%d", c.Chunk.ID)
		}
		_ = r.redis.Set(ctx, cacheKey, joinStr(ids, ","), 5*time.Minute).Err()
	}
	return final
}

// vectorToPolarDBLiteral 把 float32 切片格式化为 PolarDB STRING_TO_VECTOR 可解析的字符串
// 格式: "[0.123456,0.234567,...]"（与 migrate_polardb_vector.go 的 vectorToPolarDBString 保持一致）
func vectorToPolarDBLiteral(v []float32) string {
	if len(v) == 0 {
		return "[]"
	}
	var sb strings.Builder
	sb.Grow(len(v) * 10)
	sb.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "%.6f", x)
	}
	sb.WriteByte(']')
	return sb.String()
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

// keywordFallback 关键字搜索（embedding 失败 / 结果不足时兜底）
//
// Phase C3: 优先走 FULLTEXT 索引（MATCH AGAINST），失败/零命中时降级 LIKE 扫描。
//
// 策略：
//  1. 首选 MySQL FULLTEXT + ngram 索引：`MATCH(title, content) AGAINST(? IN NATURAL LANGUAGE MODE)`
//     - 索引名 `ft_knowledge_chunks_title_content`（由 RunSupportFullTextMigration 创建）
//     - ngram 分词对中文二字词特别友好（"充值"/"余额"/"提现"等高频运营词均可命中）
//     - 失败原因包括：SQLite 无 MATCH / MySQL 5.6 无 ngram / 查询过短被分词器全部剔除
//  2. 降级 LIKE：`title LIKE ? OR content LIKE ?`
//     - 全表扫描，仅在 knowledge_chunks < 几千行时可接受
//     - 确保短查询 / 无 FULLTEXT 的数据库也有兜底结果
//
// 排序：priority DESC, hit_count DESC 两条路径保持一致。
// 评分：统一返回 0.4（低于 RAG 阈值但可展示，避免把关键字分数与 cosine 分数混用）。
// 查询长度：按 rune 截断到 30（中文场景下字符 ≠ 字节）。
func (r *KnowledgeRetriever) keywordFallback(ctx context.Context, query string, limit int) []ScoredChunk {
	query = strings.TrimSpace(query)
	if query == "" || limit <= 0 {
		return nil
	}
	// 30 rune 安全截断
	if runes := []rune(query); len(runes) > 30 {
		query = string(runes[:30])
	}

	// Phase C3 优先路径：FULLTEXT + ngram
	if hits := r.keywordFullText(ctx, query, limit); len(hits) > 0 {
		return hits
	}

	// 降级 LIKE：保证索引不可用 / 短查询场景也有结果
	return r.keywordLike(ctx, query, limit)
}

// keywordFullText 走 MySQL FULLTEXT + ngram 索引；任何错误都静默返回 nil 触发 LIKE fallback
func (r *KnowledgeRetriever) keywordFullText(ctx context.Context, query string, limit int) []ScoredChunk {
	var chunks []model.KnowledgeChunk
	err := r.db.WithContext(ctx).
		Where("is_active = ?", true).
		Where("MATCH(title, content) AGAINST (? IN NATURAL LANGUAGE MODE)", query).
		Order("priority DESC, hit_count DESC").
		Limit(limit).
		Find(&chunks).Error
	if err != nil {
		// 常见非致命错误：SQLite 不支持 MATCH / ngram parser 缺失 / 查询字符全部被分词器剔除
		logger.L.Debug("keywordFullText: fallback to LIKE", zap.Error(err))
		return nil
	}
	if len(chunks) == 0 {
		return nil
	}
	out := make([]ScoredChunk, 0, len(chunks))
	for i := range chunks {
		out = append(out, ScoredChunk{
			Chunk: &chunks[i],
			Score: 0.4, // 关键字命中统一兜底分（避免与 cosine 分混用）
			Raw:   0.4,
		})
	}
	return out
}

// keywordLike 降级路径：LIKE 全表扫描
func (r *KnowledgeRetriever) keywordLike(ctx context.Context, query string, limit int) []ScoredChunk {
	pattern := "%" + query + "%"
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
			Score: 0.4,
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
