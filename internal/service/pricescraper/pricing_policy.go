package pricescraper

import (
	"strings"
)

// =====================================================
// 定价策略辅助（免费模型识别 + 缺价模型处理）
//
// 解决的问题：
//  1. 腾讯混元等供应商无公开价格 API，新模型 API 返回但爬虫硬编码表未录入 → 价格=0
//  2. 官方免费模型（如 hunyuan-lite）价格合法为 0，不应误判为"缺价"
//
// 策略：
//  - 显式白名单：官方宣布免费的模型 → 打 "Free"/"免费" 标签，保持 IsActive=true
//  - 价格全 0 且非白名单 → IsActive=false，打 "NeedsPricing" 标签待人工审核
//  - 仅 LLM/VLM/Embedding 等 Token 计费模型参与此策略；非 Token 单位（图片/语音/视频）豁免
// =====================================================

// freeModelWhitelist 官方公开宣布的免费模型清单
// 维护原则：仅加入**官方明确标注免费**的模型；普通降价不算
var freeModelWhitelist = map[string]bool{
	// 腾讯混元 — 官方免费
	"hunyuan-lite":        true,
	"hunyuan-lite-vision": true,
	// 腾讯混元 — 开源模型（官方 API 提供免费调用）
	"hunyuan-7b":   true,
	"hunyuan-a13b": true,

	// 其他供应商可按需扩充（如 DeepSeek 某些小模型、智谱 glm-4-flash 免费版等）
}

// IsFreeModel 判断模型是否为官方免费模型
// - modelName: 模型标识
// - inputRMB / outputRMB: 爬取到的价格（元/百万 token）
// 仅当价格全为 0 且名称命中白名单时返回 true
func IsFreeModel(modelName string, inputRMB, outputRMB float64) bool {
	if inputRMB > 0 || outputRMB > 0 {
		return false
	}
	return freeModelWhitelist[strings.ToLower(strings.TrimSpace(modelName))]
}

// IsPriceMissing 判断模型是否为"缺价"状态
// - 价格全为 0 且不是免费白名单模型 且 计费单位基于 Token（排除图片/语音/视频按次/按秒计费模型）
func IsPriceMissing(modelName, pricingUnit, modelType string, inputRMB, outputRMB float64) bool {
	// 非 Token 单位直接放行（图片按张、TTS 按字符、ASR 按小时等 output 天然可能为 0）
	if pricingUnit != "" && pricingUnit != PricingUnitPerMillionTokens {
		return false
	}
	// Embedding 只有 input 价，output 本就是 0 → 放行
	if strings.EqualFold(modelType, "Embedding") {
		return inputRMB <= 0
	}
	if inputRMB > 0 || outputRMB > 0 {
		return false
	}
	if IsFreeModel(modelName, inputRMB, outputRMB) {
		return false
	}
	return true
}

// AugmentTagsForPricing 在已有 tag 字符串上追加定价相关标签
// - 免费模型追加 "Free,免费"
// - 缺价模型追加 "NeedsPricing,待定价"
// 原 tag 保持不变；新增 tag 去重
func AugmentTagsForPricing(existingTags string, isFree, priceMissing bool) string {
	seen := make(map[string]bool)
	var tags []string
	if existingTags != "" {
		for _, t := range strings.Split(existingTags, ",") {
			t = strings.TrimSpace(t)
			if t != "" && !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}
	}
	add := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			tags = append(tags, t)
		}
	}
	if isFree {
		add("Free")
		add("免费")
	}
	if priceMissing {
		add("NeedsPricing")
		add("待定价")
	}
	return strings.Join(tags, ",")
}
