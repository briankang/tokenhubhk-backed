package aimodel

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// ============================================================
// ReportService: 能力测试汇总报告 + 自动建议生成
// 原计划定义为单独 service，此处合入 CapabilityTester（同 package，共享 db）
// 方便 RunTests 完成后直接调用
// ============================================================

// SuggestionReport 汇总报告（写入 capability_test_tasks.result_json）
type SuggestionReport struct {
	Summary         ReportSummary            `json:"summary"`
	ByModelType     map[string]TypeStats     `json:"by_model_type"`
	FailureCluster  []ClusterGroup           `json:"failure_cluster,omitempty"`
	Regressions     []RegressionItem         `json:"regressions,omitempty"`
	UXFailures      []UXFailure              `json:"ux_failures,omitempty"`
	Prioritized     []ModelCapabilityUpdate  `json:"prioritized,omitempty"`
}

type ReportSummary struct {
	Total      int `json:"total"`
	Passed     int `json:"passed"`
	Failed     int `json:"failed"`
	Skipped    int `json:"skipped"`
	Regression int `json:"regression"`
}

type TypeStats struct {
	Total       int              `json:"total"`
	Passed      int              `json:"passed"`
	Failed      int              `json:"failed"`
	PassRate    float64          `json:"pass_rate"`
	TopFailures []FailureBucket  `json:"top_failures,omitempty"`
}

type FailureBucket struct {
	ErrorCategory string `json:"error_category"`
	Count         int    `json:"count"`
}

// ClusterGroup 失败聚类：同 category+upstream_status 跨 ≥3 个模型
// → 标记"疑似上游整体问题"，不触发 Features 更新
type ClusterGroup struct {
	ErrorCategory  string   `json:"error_category"`
	UpstreamStatus int      `json:"upstream_status"`
	ModelCount     int      `json:"model_count"`
	CaseNames      []string `json:"case_names"`
	Hint           string   `json:"hint"`
}

// RegressionItem 回归告警：基线 pass→fail 或延迟恶化
type RegressionItem struct {
	ModelID        uint   `json:"model_id"`
	ModelName      string `json:"model_name"`
	CaseID         uint   `json:"case_id"`
	CaseName       string `json:"case_name"`
	BaselineStatus string `json:"baseline_status"`
	CurrentStatus  string `json:"current_status"`
	BaselineLatMS  int    `json:"baseline_latency_ms"`
	CurrentLatMS   int    `json:"current_latency_ms"`
	Reason         string `json:"reason"`
}

// UXFailure UX 流程失败（需红框高亮）
type UXFailure struct {
	CaseID       uint   `json:"case_id"`
	CaseName     string `json:"case_name"`
	ModelName    string `json:"model_name,omitempty"`
	ErrorMessage string `json:"error_message"`
}

// FailureSample 建议项下的代表性失败详情（供前端展示失败原因、辅助人工复核）
type FailureSample struct {
	CaseName             string `json:"case_name"`
	UpstreamStatus       int    `json:"upstream_status"`
	ErrorCategory        string `json:"error_category"`
	ErrorMessage         string `json:"error_message"`          // 截断 200 字符
	FirstFailedAssertion string `json:"first_failed_assertion"` // 从 assertion_results 解析，取第一条 passed=false 的 reason
	ResponseSnippet      string `json:"response_snippet"`       // 截断 120 字符
	Suggestion           string `json:"suggestion"`             // 中文修复建议（基于 error_category）
}

// ModelCapabilityUpdate 建议项：某模型某能力应该启用/禁用
type ModelCapabilityUpdate struct {
	ModelID     uint   `json:"model_id"`
	ModelName   string `json:"model_name"`
	Capability  string `json:"capability"`
	Action      string `json:"action"`   // enable/disable/unverified/mixed
	Current     string `json:"current"`  // 当前 features 中的值（true/false/unset）
	Verdict     string `json:"verdict"`  // 同 Action，供前端 UI 区分颜色
	Suggested   string `json:"suggested"` // 同 Action，供前端 handleApply 使用
	Reason      string `json:"reason"`
	FailedCount int    `json:"failed_count"`
	TotalCount  int    `json:"total_count"`

	// Phase 2 精准度增强：置信度 + 失败详情
	Confidence       string          `json:"confidence"`        // high/medium/low
	DominantCategory string          `json:"dominant_category"` // 最常见 error_category
	CategoryLabel    string          `json:"category_label"`    // 中文标签（对齐 categorizeCheckError）
	FailureSamples   []FailureSample `json:"failure_samples"`   // ≤3 条代表性失败
	AmbiguousCount   int             `json:"ambiguous_count"`   // 被忽略的歧义失败数（限流/超时/认证/服务端 5xx 等）
	EffectiveTotal   int             `json:"effective_total"`   // total - ambiguous，用于判决
}

// categoryLabel 将 error_category 映射为中文标签（对齐 model_checker.go:categorizeCheckError）
// 用于前端 UI 展示，避免前端硬编码；与 categorizeCheckError 返回的 category 完全对应。
func categoryLabel(cat string) string {
	switch cat {
	case "timeout":
		return "请求超时"
	case "rate_limited":
		return "速率限制 (429)"
	case "invalid_request":
		return "参数错误 (400)"
	case "auth_error":
		return "认证失败 (401)"
	case "permission_denied":
		return "权限不足 (403)"
	case "model_not_found":
		return "模型不存在"
	case "api_mismatch":
		return "API 端点不匹配"
	case "assertion_failed":
		return "断言失败"
	case "connection_error":
		return "连接中断"
	case "server_error":
		return "上游服务端错误"
	case "image_not_supported":
		return "图像模型不支持"
	case "quota_exhausted":
		return "配额不足"
	case "product_not_activated":
		return "产品未激活"
	case "no_route":
		return "无可用渠道"
	case "skipped":
		return "已跳过"
	case "":
		return ""
	default:
		return "未分类错误"
	}
}

// categorySuggestion 将 error_category 映射为中文修复建议（对齐 categorizeCheckError 的 suggestion 文本）
func categorySuggestion(cat string) string {
	switch cat {
	case "timeout":
		return "稍后重试；检查网络连通性；确认供应商服务状态"
	case "rate_limited":
		return "模型本身可用，无需处理；如持续限流，考虑提升配额或分散请求"
	case "invalid_request":
		return "确认模型类型是否为 LLM/VLM；检查模型是否需要特殊参数"
	case "auth_error":
		return "到「供应商接入点」页面更新 API Key"
	case "permission_denied":
		return "确认 API Key 是否已开通该模型的访问权限；联系供应商确认订阅状态"
	case "model_not_found":
		return "到供应商控制台确认模型是否可用；检查 actual_model 名称是否正确"
	case "api_mismatch":
		return "确认模型类型是否正确；检查渠道路由配置"
	case "assertion_failed":
		return "查看首个失败断言的详细原因，检查模型是否真的支持该能力"
	case "connection_error":
		return "稍后重试；减少并发检测数量；检查供应商是否有 QPS 限制"
	case "server_error":
		return "稍后重试，若持续出现请联系供应商确认服务状态"
	case "image_not_supported":
		return "确认渠道 endpoint 指向正确的图像生成 API"
	case "quota_exhausted":
		return "到供应商控制台充值或检查账单状态"
	case "product_not_activated":
		return "到供应商控制台激活该模型/产品后重试"
	case "no_route":
		return "在「渠道管理」中为该模型添加路由，或执行「刷新默认渠道」"
	default:
		return "查看详细错误信息排查原因"
	}
}

// truncateWithEllipsis 截断字符串（按字符数，避免破坏多字节 UTF-8，溢出时追加省略号）
func truncateWithEllipsis(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// parseFirstFailedAssertion 从 assertion_results JSON 解析第一条 passed=false 的 reason
// JSON 结构: [{"name":"...","passed":true/false,"reason":"..."}]
func parseFirstFailedAssertion(assertionResults string) string {
	if assertionResults == "" {
		return ""
	}
	var arr []struct {
		Name   string `json:"name"`
		Passed bool   `json:"passed"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(assertionResults), &arr); err != nil {
		return ""
	}
	for _, a := range arr {
		if !a.Passed {
			if a.Name != "" {
				return fmt.Sprintf("[%s] %s", a.Name, a.Reason)
			}
			return a.Reason
		}
	}
	return ""
}

// computeSuggestionsV2 基于一次任务的全部 results 生成报告
func (t *CapabilityTester) computeSuggestionsV2(ctx context.Context, taskID uint) (*SuggestionReport, error) {
	var results []model.CapabilityTestResult
	if err := t.db.WithContext(ctx).
		Where("task_id = ?", taskID).
		Find(&results).Error; err != nil {
		return nil, fmt.Errorf("加载结果失败: %w", err)
	}

	report := &SuggestionReport{
		ByModelType: make(map[string]TypeStats),
	}

	// 加载用例元数据（capability/model_type 映射）
	caseMap := make(map[uint]model.CapabilityTestCase)
	var caseIDs []uint
	for _, r := range results {
		caseIDs = append(caseIDs, r.CaseID)
	}
	if len(caseIDs) > 0 {
		var cases []model.CapabilityTestCase
		t.db.WithContext(ctx).Where("id IN ?", caseIDs).Find(&cases)
		for _, c := range cases {
			caseMap[c.ID] = c
		}
	}

	// 批量加载涉及模型的 features（用于填充建议的 Current 值）
	modelFeatureMap := make(map[uint]map[string]interface{})
	var modelIDs []uint
	seenModels := make(map[uint]bool)
	for _, r := range results {
		if !seenModels[r.ModelID] {
			seenModels[r.ModelID] = true
			modelIDs = append(modelIDs, r.ModelID)
		}
	}
	if len(modelIDs) > 0 {
		var aiModels []model.AIModel
		t.db.WithContext(ctx).Select("id, features").Where("id IN ?", modelIDs).Find(&aiModels)
		for _, m := range aiModels {
			feats := make(map[string]interface{})
			if len(m.Features) > 0 {
				_ = json.Unmarshal([]byte(m.Features), &feats)
			}
			modelFeatureMap[m.ID] = feats
		}
	}

	// 汇总 summary + by_model_type
	typeBuckets := make(map[string]*TypeStats)
	typeFailures := make(map[string]map[string]int) // type → {category → count}
	for _, r := range results {
		report.Summary.Total++
		switch r.Status {
		case "passed":
			report.Summary.Passed++
		case "failed":
			report.Summary.Failed++
		case "skipped":
			report.Summary.Skipped++
		case "regression":
			report.Summary.Regression++
		}

		c, ok := caseMap[r.CaseID]
		mtype := "chat"
		if ok && c.ModelType != "" {
			mtype = c.ModelType
		}
		ts := typeBuckets[mtype]
		if ts == nil {
			ts = &TypeStats{}
			typeBuckets[mtype] = ts
			typeFailures[mtype] = make(map[string]int)
		}
		ts.Total++
		switch r.Status {
		case "passed":
			ts.Passed++
		case "failed", "regression":
			ts.Failed++
			cat := r.ErrorCategory
			if cat == "" {
				cat = "unknown"
			}
			typeFailures[mtype][cat]++
		}
	}
	for mt, ts := range typeBuckets {
		if ts.Total > 0 {
			ts.PassRate = float64(ts.Passed) / float64(ts.Total)
		}
		// top-3 失败原因
		buckets := []FailureBucket{}
		for cat, cnt := range typeFailures[mt] {
			buckets = append(buckets, FailureBucket{ErrorCategory: cat, Count: cnt})
		}
		sort.Slice(buckets, func(i, j int) bool { return buckets[i].Count > buckets[j].Count })
		if len(buckets) > 3 {
			buckets = buckets[:3]
		}
		ts.TopFailures = buckets
		report.ByModelType[mt] = *ts
	}

	// 失败聚类：GROUP BY error_category, upstream_status HAVING COUNT(DISTINCT model_id) >= 3
	type clusterKey struct {
		cat    string
		status int
	}
	clusters := make(map[clusterKey]map[uint]bool) // key → modelIDs set
	clusterCases := make(map[clusterKey]map[string]bool)
	for _, r := range results {
		if r.Status != "failed" && r.Status != "regression" {
			continue
		}
		if r.ErrorCategory == "" || r.ErrorCategory == "assertion_failed" {
			continue
		}
		k := clusterKey{r.ErrorCategory, r.UpstreamStatus}
		if clusters[k] == nil {
			clusters[k] = make(map[uint]bool)
			clusterCases[k] = make(map[string]bool)
		}
		clusters[k][r.ModelID] = true
		clusterCases[k][r.CaseName] = true
	}
	for k, models := range clusters {
		if len(models) >= 3 {
			cases := []string{}
			for c := range clusterCases[k] {
				cases = append(cases, c)
			}
			sort.Strings(cases)
			report.FailureCluster = append(report.FailureCluster, ClusterGroup{
				ErrorCategory:  k.cat,
				UpstreamStatus: k.status,
				ModelCount:     len(models),
				CaseNames:      cases,
				Hint:           "疑似上游整体问题：同分类错误跨多模型出现，不触发单模型 Features 更新",
			})
		}
	}

	// 回归列表
	for _, r := range results {
		if r.Status != "regression" {
			continue
		}
		c := caseMap[r.CaseID]
		item := RegressionItem{
			ModelID: r.ModelID, ModelName: r.ModelName,
			CaseID: r.CaseID, CaseName: c.Name,
			CurrentStatus: r.Status, CurrentLatMS: r.LatencyMS,
			Reason: "基线对比异常（pass→fail 或延迟恶化 1.5x）",
		}
		var baseline model.CapabilityTestBaseline
		if err := t.db.WithContext(ctx).
			Where("model_id = ? AND case_id = ?", r.ModelID, r.CaseID).
			First(&baseline).Error; err == nil {
			item.BaselineStatus = baseline.Outcome
			item.BaselineLatMS = baseline.LatencyMS
		}
		report.Regressions = append(report.Regressions, item)
	}

	// UX 失败
	for _, r := range results {
		c := caseMap[r.CaseID]
		if c.ModelType != "ux_flow" {
			continue
		}
		if r.Status == "failed" || r.Status == "regression" {
			report.UXFailures = append(report.UXFailures, UXFailure{
				CaseID: r.CaseID, CaseName: r.CaseName, ModelName: r.ModelName,
				ErrorMessage: r.ErrorMessage,
			})
		}
	}

	// 自动建议：(model_id, capability) 维度聚合（Phase 2 精准度增强）
	//
	// 精准度改造要点：
	//   1. 歧义错误（rate_limited/timeout/connection_error/server_error/auth_error/
	//      quota_exhausted/product_not_activated/no_route）不计入 failed，改为 ambiguous，
	//      effectiveTotal = total - ambiguous 作为真正的决策样本数
	//   2. 硬失败错误（invalid_request/api_mismatch/assertion_failed/image_not_supported/
	//      model_not_found）才计入 failed，代表"模型确实不支持该能力"
	//   3. 判决要求 effectiveTotal ≥ 2 才能触发 disable，单样本降级为 unverified
	//   4. 置信度 high 仅当 effectiveTotal ≥ 2 且主因属于硬失败类别
	//   5. 同时收集 ≤3 条代表性 FailureSample 供前端展示
	type capKey struct {
		modelID uint
		cap     string
	}
	type capStat struct {
		name           string
		total          int
		failed         int // 硬失败（参数拒绝类）
		ambiguous      int // 歧义错误（限流/超时/认证/服务端/无渠道）
		categoryCount  map[string]int
		failureSamples []FailureSample // ≤3 条，优先不同 case_name
		sampledCases   map[string]bool
	}
	capStats := make(map[capKey]*capStat)
	clusteredKeys := make(map[clusterKey]bool)
	for _, cg := range report.FailureCluster {
		clusteredKeys[clusterKey{cg.ErrorCategory, cg.UpstreamStatus}] = true
	}

	// 歧义错误：不计入 failed，计为 ambiguous（不代表模型不支持该能力，只是环境/网络问题）
	ambiguousCategories := map[string]bool{
		"rate_limited":          true,
		"timeout":               true,
		"connection_error":      true,
		"server_error":          true,
		"auth_error":            true,
		"quota_exhausted":       true,
		"product_not_activated": true,
		"no_route":              true,
	}
	// 硬失败错误：计入 failed，代表模型确实不支持该能力
	hardFailCategories := map[string]bool{
		"invalid_request":     true,
		"api_mismatch":        true,
		"assertion_failed":    true,
		"image_not_supported": true,
		"model_not_found":     true,
	}

	for _, r := range results {
		if r.Status == "skipped" {
			continue
		}
		c, ok := caseMap[r.CaseID]
		if !ok || c.Capability == "" {
			continue
		}
		k := capKey{r.ModelID, c.Capability}
		s := capStats[k]
		if s == nil {
			s = &capStat{
				name:          r.ModelName,
				categoryCount: make(map[string]int),
				sampledCases:  make(map[string]bool),
			}
			capStats[k] = s
		}
		s.total++
		if r.Status == "failed" || r.Status == "regression" {
			// 若属整体故障集群则不计入（避免误伤）
			if clusteredKeys[clusterKey{r.ErrorCategory, r.UpstreamStatus}] {
				s.ambiguous++
				continue
			}
			cat := r.ErrorCategory
			s.categoryCount[cat]++
			switch {
			case ambiguousCategories[cat]:
				s.ambiguous++
			case hardFailCategories[cat]:
				s.failed++
			default:
				// 未分类错误保守处理：计入 ambiguous（避免误触发 disable）
				s.ambiguous++
			}
			// 收集 ≤3 条 FailureSample，优先不同 case_name
			if len(s.failureSamples) < 3 && !s.sampledCases[r.CaseName] {
				s.failureSamples = append(s.failureSamples, FailureSample{
					CaseName:             r.CaseName,
					UpstreamStatus:       r.UpstreamStatus,
					ErrorCategory:        cat,
					ErrorMessage:         truncateWithEllipsis(r.ErrorMessage, 200),
					FirstFailedAssertion: parseFirstFailedAssertion(r.AssertionResults),
					ResponseSnippet:      truncateWithEllipsis(r.ResponseSnippet, 120),
					Suggestion:           categorySuggestion(cat),
				})
				s.sampledCases[r.CaseName] = true
			}
		}
	}

	for k, s := range capStats {
		if s.total == 0 {
			continue
		}
		effectiveTotal := s.total - s.ambiguous
		// 主因：categoryCount 中频次最高的类别
		dominantCat := ""
		dominantCnt := 0
		for cat, cnt := range s.categoryCount {
			if cnt > dominantCnt {
				dominantCnt = cnt
				dominantCat = cat
			}
		}
		upd := ModelCapabilityUpdate{
			ModelID:        k.modelID,
			ModelName:      s.name,
			Capability:     k.cap,
			FailedCount:    s.failed,
			TotalCount:     s.total,
			EffectiveTotal: effectiveTotal,
			AmbiguousCount: s.ambiguous,
			FailureSamples: s.failureSamples,
			DominantCategory: dominantCat,
			CategoryLabel:  categoryLabel(dominantCat),
		}
		// 判决逻辑（Phase 2 精准化）
		switch {
		case s.failed > 0 && s.failed == effectiveTotal && effectiveTotal >= 2:
			upd.Action = "disable"
			upd.Reason = fmt.Sprintf("%d 次有效测试全部因参数拒绝失败，建议关闭该能力", effectiveTotal)
		case s.failed > 0 && s.failed == effectiveTotal && effectiveTotal == 1:
			upd.Action = "unverified"
			upd.Reason = "仅 1 次测试失败，样本不足，建议再次测试确认"
		case s.failed == 0 && effectiveTotal >= 1:
			upd.Action = "enable"
			upd.Reason = fmt.Sprintf("%d 次有效测试全部通过，建议启用该能力", effectiveTotal)
		case s.failed == 0 && effectiveTotal == 0:
			upd.Action = "unverified"
			upd.Reason = "所有测试均因歧义错误（限流/超时/认证/服务端）被跳过，需重新测试"
		default:
			upd.Action = "mixed"
			upd.Reason = fmt.Sprintf("混合结果 %d/%d 硬失败，建议人工复核", s.failed, effectiveTotal)
		}
		// 置信度评分
		switch {
		case upd.Action == "disable" && effectiveTotal >= 2 &&
			(dominantCat == "invalid_request" || dominantCat == "api_mismatch" || dominantCat == "assertion_failed"):
			upd.Confidence = "high"
		case upd.Action == "enable" && effectiveTotal >= 2:
			upd.Confidence = "high"
		case upd.Action == "enable" && effectiveTotal == 1:
			upd.Confidence = "medium"
		case upd.Action == "mixed":
			upd.Confidence = "medium"
		default:
			upd.Confidence = "low"
		}
		upd.Verdict = upd.Action
		upd.Suggested = upd.Action
		// 读取当前 features 中的值
		if feats, ok := modelFeatureMap[k.modelID]; ok {
			if val, exists := feats[k.cap]; exists {
				upd.Current = fmt.Sprintf("%v", val)
			} else {
				upd.Current = "unset"
			}
		} else {
			upd.Current = "unset"
		}
		report.Prioritized = append(report.Prioritized, upd)
	}
	// 优先级：failed > mixed > enable；按失败数降序
	actionPrio := map[string]int{"disable": 0, "mixed": 1, "enable": 2, "unverified": 3}
	sort.SliceStable(report.Prioritized, func(i, j int) bool {
		pi := report.Prioritized[i]
		pj := report.Prioritized[j]
		if actionPrio[pi.Action] != actionPrio[pj.Action] {
			return actionPrio[pi.Action] < actionPrio[pj.Action]
		}
		return pi.FailedCount > pj.FailedCount
	})

	return report, nil
}

// ApplySuggestions 将 Prioritized 中勾选的项写回 AIModel.Features / ExtraParams
// selected 为 (modelID, capability) → action 映射（action 为空用默认建议）
func (t *CapabilityTester) ApplySuggestions(ctx context.Context, taskID uint, selected map[string]string) (int, error) {
	report, err := t.computeSuggestionsV2(ctx, taskID)
	if err != nil {
		return 0, err
	}

	applied := 0
	for _, upd := range report.Prioritized {
		key := fmt.Sprintf("%d:%s", upd.ModelID, upd.Capability)
		act, ok := selected[key]
		if !ok {
			continue // 未勾选
		}
		if act == "" {
			act = upd.Action
		}
		if act == "mixed" || act == "unverified" {
			continue
		}

		var m model.AIModel
		if err := t.db.WithContext(ctx).First(&m, upd.ModelID).Error; err != nil {
			continue
		}
		if err := applyCapabilityChange(t.db.WithContext(ctx), &m, upd.Capability, act == "enable"); err != nil {
			continue
		}
		applied++
	}
	return applied, nil
}

// applyCapabilityChange 更新 AIModel 的 Features/ExtraParams/SupportsCache 字段
// modelID 参数用于显式 WHERE，避免 m.ID=0 时 GORM 报 ErrMissingWhereClause
func applyCapabilityChange(db *gorm.DB, m *model.AIModel, capability string, enable bool) error {
	if m.ID == 0 {
		return fmt.Errorf("applyCapabilityChange: model ID is 0, skipping")
	}
	updates := map[string]interface{}{}

	// supports_cache 特殊处理
	if capability == "supports_cache" {
		updates["supports_cache"] = enable
	}

	// Features 字段（JSON map）
	features := map[string]interface{}{}
	if len(m.Features) > 0 {
		_ = json.Unmarshal(m.Features, &features)
	}
	features[capability] = enable
	fb, _ := json.Marshal(features)
	updates["features"] = fb

	// param:xxx 形式：从 ExtraParams 删除或添加
	if strings.HasPrefix(capability, "param:") {
		paramName := strings.TrimPrefix(capability, "param:")
		extra := map[string]interface{}{}
		if len(m.ExtraParams) > 0 {
			_ = json.Unmarshal(m.ExtraParams, &extra)
		}
		if enable {
			if _, ok := extra[paramName]; !ok {
				extra[paramName] = true
			}
		} else {
			delete(extra, paramName)
		}
		eb, _ := json.Marshal(extra)
		updates["extra_params"] = eb
	}

	// 显式使用 WHERE id=? 避免 GORM ErrMissingWhereClause
	return db.Model(&model.AIModel{}).Where("id = ?", m.ID).Updates(updates).Error
}

// PropagateToModels 将本次任务中 action=enable 的建议推广到同系列其他模型
// 用于：测试 qwen3-8b 后，将其能力结论推广到 qwen3-0.6b/1.7b 等未配置模型
func (t *CapabilityTester) PropagateToModels(ctx context.Context, taskID uint, targetModelIDs []uint) (int, error) {
	if len(targetModelIDs) == 0 {
		return 0, nil
	}
	report, err := t.computeSuggestionsV2(ctx, taskID)
	if err != nil {
		return 0, err
	}
	// 收集所有 enable 类能力
	enabledCaps := make(map[string]bool)
	for _, upd := range report.Prioritized {
		if upd.Action == "enable" {
			enabledCaps[upd.Capability] = true
		}
	}
	if len(enabledCaps) == 0 {
		return 0, nil
	}
	propagated := 0
	for _, modelID := range targetModelIDs {
		var m model.AIModel
		if err := t.db.WithContext(ctx).First(&m, modelID).Error; err != nil {
			continue
		}
		for cap := range enabledCaps {
			if err := applyCapabilityChange(t.db.WithContext(ctx), &m, cap, true); err == nil {
				propagated++
				// 更新内存中的 features 以便下一个 cap 正确读取
				if len(m.Features) > 0 {
					feats := map[string]interface{}{}
					if json.Unmarshal(m.Features, &feats) == nil {
						feats[cap] = true
						if fb, err2 := json.Marshal(feats); err2 == nil {
							m.Features = fb
						}
					}
				}
			}
		}
	}
	return propagated, nil
}
