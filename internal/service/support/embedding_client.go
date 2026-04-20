package support

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/logger"
)

// EmbeddingClient 轻量 embedding 客户端
//
// 通过 HTTP 自调用 /v1/embeddings 端点，复用平台已有的：
//   - 渠道路由 + Failover
//   - 余额扣减（走系统级 API Key）
//   - 调用日志与审计
//
// 系统级 API Key 从 system_configs["support.internal_api_key"] 读取（或传入构造参数）
type EmbeddingClient struct {
	baseURL string
	apiKey  string
	model   string
	httpCli *http.Client
}

// NewEmbeddingClient 构造客户端
// baseURL: 例如 http://localhost:8080（或 gateway 容器内部地址）
// model: text-embedding-v3
func NewEmbeddingClient(baseURL, apiKey, model string) *EmbeddingClient {
	if model == "" {
		model = "text-embedding-v3"
	}
	return &EmbeddingClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		httpCli: &http.Client{Timeout: 30 * time.Second},
	}
}

// Embed 生成单个文本的向量
func (c *EmbeddingClient) Embed(ctx context.Context, text string) ([]float32, error) {
	vectors, err := c.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) == 0 {
		return nil, errors.New("empty embedding response")
	}
	return vectors[0], nil
}

// EmbedBatch 批量生成向量（单次 API 调用）
// 建议批量大小 ≤ 25（阿里云单次上限）
func (c *EmbeddingClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if c.baseURL == "" || c.apiKey == "" {
		return nil, errors.New("embedding client not configured (baseURL/apiKey missing)")
	}

	body := map[string]any{
		"model": c.model,
		"input": texts,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/embeddings", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpCli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding http: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		logger.L.Warn("embedding request failed",
			zap.Int("status", resp.StatusCode),
			zap.String("body", truncate(string(data), 300)))
		return nil, fmt.Errorf("embedding api %d: %s", resp.StatusCode, truncate(string(data), 200))
	}

	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parse embedding response: %w", err)
	}
	out := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index >= 0 && d.Index < len(texts) {
			out[d.Index] = d.Embedding
		}
	}
	// 检查未填充位置
	for i, v := range out {
		if v == nil {
			return nil, fmt.Errorf("embedding response missing index %d", i)
		}
	}
	return out, nil
}

// MarshalVector 把 []float32 编码为 JSON 字符串存数据库
func MarshalVector(v []float32) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// UnmarshalVector 从数据库 JSON 字符串还原 []float32
func UnmarshalVector(s string) ([]float32, error) {
	if s == "" {
		return nil, nil
	}
	var v []float32
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, err
	}
	return v, nil
}

// Cosine 余弦相似度（两向量均需已归一化或返回合理值）
func Cosine(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	// sqrt(na * nb) via squared trick: use fast inverse would need math; use math.Sqrt
	return dot / (float32(sqrt64(float64(na))) * float32(sqrt64(float64(nb))))
}

func sqrt64(x float64) float64 {
	// 直接 import math 的 Sqrt 即可，这里局部 helper 便于未来替换为 SIMD
	if x <= 0 {
		return 0
	}
	// 牛顿法：收敛快
	z := x
	for i := 0; i < 8; i++ {
		z = (z + x/z) / 2
	}
	return z
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
