package pricescraper

import (
	"strings"
	"testing"
)

// TestQianfanCoverage 验证补充表对实际 API 模型列表的覆盖率
// 数据来源：从生产数据库导出的当前 144 个 auto 同步模型
func TestQianfanCoverage(t *testing.T) {
	// 来自 DB 的 auto 模型完整列表（截止 2026-04-17）
	apiModels := []string{
		"aquilachat-7b", "bge-large-en", "bge-large-en-v1.5", "bge-large-zh", "bloomz-7b",
		"chatglm2-6b-32k", "codellama-7b-instruct", "deepseek-chat-v3.1", "deepseek-r1",
		"deepseek-r1-distill-qwen-1.5b", "deepseek-r1-distill-qwen-32b", "deepseek-v3",
		"deepseek-v3.1", "deepseek-v3.2", "embedding-v1", "ernie-3.5-128k-preview",
		"ernie-3.5-8k-0613", "ernie-3.5-8k-0701", "ernie-3.5-8k-preview", "ernie-4.0-8k-0613",
		"ernie-4.0-8k-preview", "ernie-4.0-turbo-128k", "ernie-4.0-turbo-8k-0628",
		"ernie-4.0-turbo-8k-latest", "ernie-4.0-turbo-8k-preview", "ernie-4.5-0.3b",
		"ernie-4.5-21b-a3b", "ernie-4.5-21b-a3b-thinking", "ernie-4.5-8k-preview",
		"ernie-4.5-turbo-128k-preview", "ernie-4.5-turbo-20260402", "ernie-4.5-turbo-latest",
		"ernie-4.5-turbo-vl", "ernie-4.5-turbo-vl-32k", "ernie-4.5-turbo-vl-32k-preview",
		"ernie-4.5-turbo-vl-latest", "ernie-4.5-turbo-vl-preview", "ernie-4.5-vl-28b-a3b",
		"ernie-5.0", "ernie-5.0-thinking-exp", "ernie-5.0-thinking-latest",
		"ernie-5.0-thinking-preview", "ernie-char-8k", "ernie-char-fiction-8k",
		"ernie-char-fiction-8k-preview", "ernie-irag-edit", "ernie-lite-pro-128k",
		"ernie-novel-8k", "ernie-video-1.0-i2v", "ernie-video-1.0-t2v",
		"ernie-x1-32k-preview", "ernie-x1-turbo-32k-preview", "ernie-x1-turbo-latest",
		"ernie-x1.1-preview", "flux.1-schnell", "gemma-7b-it", "internvl2.5-38b-mpo",
		"internvl3-38b", "irag-1.0", "kimi-k2-instruct", "kling-v1", "llama-2-13b-chat",
		"llama-2-70b-chat", "llama-2-7b-chat", "meta-llama-3-8b", "minimax-text-01",
		"mixtral-8x7b-instruct", "musesteamer-2.0-lite-i2v", "musesteamer-2.0-pro-i2v",
		"musesteamer-2.0-turbo-i2v", "musesteamer-2.0-turbo-i2v-audio",
		"musesteamer-2.0-turbo-i2v-effect", "musesteamer-2.0-turbo-i2v-product",
		"musesteamer-2.0-turbo-i2v-storybook", "musesteamer-2.0-turbo-i2v-wallpaper",
		"musesteamer-2.1-lite-i2v", "musesteamer-2.1-turbo-i2v", "musesteamer-air-i2v",
		"musesteamer-air-image", "paddleocr-vl-0.9b", "pp-structurev3",
		"qianfan-agent-speed", "qianfan-agent-x1-turbo", "qianfan-ocr",
		"qianfan-vl-70b", "qianfan-vl-8b", "qwen2.5-7b-instruct", "qwen3-14b",
		"qwen3-235b-a22b", "qwen3-32b", "qwen3-8b", "qwen3-embedding-0.6b",
		"qwen3-embedding-4b", "qwen3-embedding-8b", "qwen3-reranker-0.6b",
		"qwen3-reranker-4b", "qwen3-reranker-8b", "qwen3-vl-235b-a22b-instruct",
		"qwen3-vl-30b-a3b-instruct", "stable-diffusion-xl", "tao-8k", "vidu-2.0",
		"viduq3-turbo_text2video", "wan-2.1-i2v-14b-480p", "xuanyuan-70b-chat-4bit",
		"yi-34b-chat",
	}

	supplementary := getQianfanSupplementaryPrices()

	// 模拟 mergeModels 中的查询逻辑
	suppMap := make(map[string]ScrapedModel, len(supplementary))
	suppKeys := make([]string, 0, len(supplementary))
	for _, sm := range supplementary {
		k := normalizeModelID(sm.ModelName)
		suppMap[k] = sm
		suppKeys = append(suppKeys, k)
	}

	matched := 0
	matchedWithPrice := 0
	var unmatched []string
	var matchedZeroPrice []string
	for _, name := range apiModels {
		key := normalizeModelID(name)
		supp, ok := lookupSupplement(key, suppMap, suppKeys)
		if ok {
			matched++
			if supp.InputPrice > 0 || supp.OutputPrice > 0 {
				matchedWithPrice++
			} else {
				matchedZeroPrice = append(matchedZeroPrice, name)
			}
		} else {
			unmatched = append(unmatched, name)
		}
	}

	total := len(apiModels)
	t.Logf("=== 千帆爬虫覆盖率统计 ===")
	t.Logf("API 模型总数: %d", total)
	t.Logf("命中补充表: %d (%.1f%%)", matched, float64(matched)/float64(total)*100)
	t.Logf("命中且价格 > 0: %d (%.1f%%)", matchedWithPrice, float64(matchedWithPrice)/float64(total)*100)
	t.Logf("命中但价格 = 0: %d", len(matchedZeroPrice))
	t.Logf("未命中: %d", len(unmatched))

	if len(unmatched) > 0 {
		t.Logf("--- 未命中模型清单 ---")
		t.Logf("%s", strings.Join(unmatched, "\n"))
	}
	if len(matchedZeroPrice) > 0 {
		t.Logf("--- 命中但价格=0（待人工确认价格） ---")
		t.Logf("%s", strings.Join(matchedZeroPrice, "\n"))
	}

	// 期望覆盖率显著提升：原来 4/144 (2.8%)，目标 > 70%
	if matchedWithPrice < total*7/10 {
		t.Errorf("覆盖率不达标：%d/%d (%.1f%%) < 70%%", matchedWithPrice, total, float64(matchedWithPrice)/float64(total)*100)
	}
}

// TestLookupSupplementPrefixMatch 单元测试：反向前缀匹配
func TestLookupSupplementPrefixMatch(t *testing.T) {
	suppMap := map[string]ScrapedModel{
		"ernie-4.0-8k":       {ModelName: "ernie-4.0-8k", InputPrice: 30.0},
		"ernie-4.0-turbo-8k": {ModelName: "ernie-4.0-turbo-8k", InputPrice: 20.0},
		"ernie-4.5-vl":       {ModelName: "ernie-4.5-vl", InputPrice: 1.0},
	}
	suppKeys := []string{"ernie-4.0-8k", "ernie-4.0-turbo-8k", "ernie-4.5-vl"}

	cases := []struct {
		input    string
		wantName string
		wantOK   bool
	}{
		{"ernie-4.0-8k", "ernie-4.0-8k", true},                   // 精确
		{"ernie-4.0-8k-latest", "ernie-4.0-8k", true},            // 前缀
		{"ernie-4.0-8k-preview", "ernie-4.0-8k", true},           // 前缀
		{"ernie-4.0-8k-0613", "ernie-4.0-8k", true},              // 前缀
		{"ernie-4.0-turbo-8k-latest", "ernie-4.0-turbo-8k", true}, // 选最长前缀
		{"ernie-4.5-vl-28b-a3b", "ernie-4.5-vl", true},           // 前缀
		{"unknown-model", "", false},                              // 无匹配
		// 注意：ernie-4.0-turbo-8k 不应匹配为 ernie-4.0-8k 的前缀（因为 turbo 不是 8k 后缀）
	}
	for _, c := range cases {
		got, ok := lookupSupplement(c.input, suppMap, suppKeys)
		if ok != c.wantOK {
			t.Errorf("lookupSupplement(%q) ok=%v, want %v", c.input, ok, c.wantOK)
			continue
		}
		if ok && got.ModelName != c.wantName {
			t.Errorf("lookupSupplement(%q) name=%q, want %q", c.input, got.ModelName, c.wantName)
		}
	}
}
