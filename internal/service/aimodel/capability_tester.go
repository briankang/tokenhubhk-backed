package aimodel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"text/template"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/safego"
	"tokenhub-server/internal/service/aimodel/test_assets"
)

// ============================================================
// CapabilityTester
// 对模型执行"参数组合能力测试"——与 BatchCheck 正交：
//   - BatchCheck 回答"模型在不在"
//   - CapabilityTester 回答"哪些能力/参数真的能用"
// ============================================================

type CapabilityTester struct {
	db         *gorm.DB
	mc         *ModelChecker
	httpClient *http.Client
	logger     *zap.Logger
}

func NewCapabilityTester(db *gorm.DB, mc *ModelChecker) *CapabilityTester {
	return &CapabilityTester{
		db: db,
		mc: mc,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger.L.With(zap.String("module", "capability_tester")),
	}
}

// CapabilityTestProgress 进度事件
type CapabilityTestProgress struct {
	TaskID       uint   `json:"task_id"`
	Total        int    `json:"total"`
	Completed    int    `json:"completed"`
	Passed       int    `json:"passed"`
	Failed       int    `json:"failed"`
	Skipped      int    `json:"skipped"`
	Regression   int    `json:"regression"`
	CurrentModel string `json:"current_model,omitempty"`
	CurrentCase  string `json:"current_case,omitempty"`
	Stage        string `json:"stage"` // running/completed/failed
}

// CostEstimate 运行前成本估算
type CostEstimate struct {
	ModelCount       int `json:"model_count"`
	CaseCount        int `json:"case_count"`
	PairCount        int `json:"pair_count"`         // 过滤后的实际 model×case 对数
	TotalCredits     int `json:"total_credits"`      // 预估消耗积分
	EstimatedMinutes int `json:"estimated_minutes"`  // 预估总耗时（分钟）
}

// RunTestsInput 运行参数
type RunTestsInput struct {
	TaskID               uint
	ModelIDs             []uint
	CaseIDs              []uint // 空=所有 enabled
	SkipKnownDisabled    bool   // 跳过 Features 中已确认不可用的能力（快速模式）
	AutoApplySuggestions bool   // 完成后自动调用 AutoApplySuggestions + syncModelStatus
}

// ============================================================
// 对外接口
// ============================================================

// EstimateCost 估算运行成本（触发前展示给管理员确认）
func (t *CapabilityTester) EstimateCost(ctx context.Context, modelIDs, caseIDs []uint) (*CostEstimate, error) {
	models, cases, err := t.loadModelsAndCases(ctx, modelIDs, caseIDs)
	if err != nil {
		return nil, err
	}

	totalCredits := 0
	pairs := 0
	for _, m := range models {
		for _, c := range cases {
			if !t.caseMatchesModel(c, m) {
				continue
			}
			pairs++
			totalCredits += c.CostEstimateCredits
		}
	}

	// 并发 3，平均每个 pair 约 5s（含节流）
	est := pairs * 5 / 3 / 60
	if est < 1 {
		est = 1
	}

	return &CostEstimate{
		ModelCount:       len(models),
		CaseCount:        len(cases),
		PairCount:        pairs,
		TotalCredits:     totalCredits,
		EstimatedMinutes: est,
	}, nil
}

// RunTests 执行批量测试（供 Worker 或单体模式调用）
// progressCh 不为 nil 时实时推送进度；完成后 close channel
func (t *CapabilityTester) RunTests(ctx context.Context, in RunTestsInput, progressCh chan<- CapabilityTestProgress) error {
	// 1. 加载任务、用例、模型
	var task model.CapabilityTestTask
	if err := t.db.WithContext(ctx).First(&task, in.TaskID).Error; err != nil {
		return fmt.Errorf("加载任务失败: %w", err)
	}

	models, cases, err := t.loadModelsAndCases(ctx, in.ModelIDs, in.CaseIDs)
	if err != nil {
		t.markTaskFailed(ctx, &task, err.Error())
		return err
	}

	// 2. 构建 model×case pairs + 过滤
	type pair struct {
		Model model.AIModel
		Case  model.CapabilityTestCase
	}
	var pairs []pair
	for _, m := range models {
		for _, c := range cases {
			matched, skipReason := t.caseMatchesModelV2(c, m, in.SkipKnownDisabled)
			if matched {
				pairs = append(pairs, pair{m, c})
			} else {
				// 写 skipped 结果
				t.writeSkippedResult(ctx, task.ID, m, c, skipReason)
			}
		}
	}

	// 3. 标记 running（使用 Where+Model 确保 WHERE id=? 生效）
	now := time.Now()
	t.db.WithContext(ctx).Model(&model.CapabilityTestTask{}).
		Where("id = ?", task.ID).
		Updates(map[string]interface{}{
			"status":      "running",
			"started_at":  &now,
			"total_count": len(pairs),
		})

	if progressCh != nil {
		progressCh <- CapabilityTestProgress{
			TaskID: task.ID, Total: len(pairs), Stage: "running",
		}
	}

	// 4. 构建渠道路由映射
	routeMap := t.mc.buildRouteMap(ctx, models)

	// 5. 并发执行（独立信号量）
	var (
		chatSem  = make(chan struct{}, 3)
		videoSem = make(chan struct{}, 2)
		uxSem    = make(chan struct{}, 1)
		wg       sync.WaitGroup

		passed, failed, skipped, regression int64

		mu           sync.Mutex
		supplierLast = make(map[string]time.Time) // endpoint → 上次请求时间
	)

	for _, p := range pairs {
		wg.Add(1)
		sem := chatSem
		switch p.Case.ModelType {
		case "video":
			sem = videoSem
		case "ux_flow":
			sem = uxSem
		}

		// safego 包装：防止单个测试 goroutine panic 拖死整个批量任务
		// sem 是 for 内 switch 块赋值的局部变量，每次迭代都是新变量，无需显式拷贝
		p := p // 兼容 Go<1.22 的 range 变量捕获
		semLocal := sem
		safego.Go("capability-tester-worker", func() {
			defer wg.Done()
			semLocal <- struct{}{}
			defer func() { <-semLocal }()

			route, hasRoute := routeMap[p.Model.ModelName]
			if !hasRoute && p.Case.ModelType != "ux_flow" {
				t.writeSkippedResult(ctx, task.ID, p.Model, p.Case, "no_route")
				atomic.AddInt64(&skipped, 1)
				t.pushProgress(progressCh, &task, len(pairs), &passed, &failed, &skipped, &regression)
				return
			}

			// 按 endpoint 节流 500ms
			if hasRoute {
				endpoint := strings.TrimRight(route.Endpoint, "/")
				mu.Lock()
				if last, ok := supplierLast[endpoint]; ok {
					if el := time.Since(last); el < 500*time.Millisecond {
						mu.Unlock()
						time.Sleep(500*time.Millisecond - el)
						mu.Lock()
					}
				}
				supplierLast[endpoint] = time.Now()
				mu.Unlock()
			}

			// 执行测试
			result := t.runOne(ctx, p.Model, p.Case, route)
			result.TaskID = task.ID
			result.ModelID = p.Model.ID
			result.CaseID = p.Case.ID
			result.ModelName = p.Model.ModelName
			result.CaseName = p.Case.Name

			// 回归检测
			isRegression := t.detectRegression(ctx, &result)
			if isRegression {
				result.Status = "regression"
			}

			if err := t.db.WithContext(ctx).Create(&result).Error; err != nil {
				t.logger.Warn("写入测试结果失败", zap.Error(err))
			}

			switch result.Status {
			case "passed":
				atomic.AddInt64(&passed, 1)
			case "skipped":
				atomic.AddInt64(&skipped, 1)
			case "regression":
				atomic.AddInt64(&regression, 1)
			default:
				atomic.AddInt64(&failed, 1)
			}

			t.pushProgress(progressCh, &task, len(pairs), &passed, &failed, &skipped, &regression)
		})
	}

	wg.Wait()

	// 6. 汇总 + 生成建议报告
	report, _ := t.computeSuggestionsV2(ctx, task.ID)
	reportJSON, _ := json.Marshal(report)

	// 7. 自动应用建议 + 同步模型可用状态（仅 AutoApplySuggestions=true 时）
	if in.AutoApplySuggestions {
		t.syncModelStatusFromBaseline(ctx, task.ID)
		applied, skippedMixed, _ := t.AutoApplySuggestions(ctx, task.ID)
		t.logger.Info("自动优化完成",
			zap.Uint("task_id", task.ID),
			zap.Int("applied", applied),
			zap.Int("skipped_mixed", skippedMixed))
	}

	// 8. 回写 model_check_logs —— baseline 类用例的结果作为可用性信号源
	//    让 SyncAll 的 isModelCheckFailed 能识别能力测试的失败模型
	t.writeBaselineToCheckLogs(ctx, task.ID)

	endTime := time.Now()
	pV := atomic.LoadInt64(&passed)
	fV := atomic.LoadInt64(&failed)
	sV := atomic.LoadInt64(&skipped)
	rV := atomic.LoadInt64(&regression)
	updates := map[string]interface{}{
		"status":           "completed",
		"progress":         100,
		"progress_msg":     "完成",
		"completed_at":     &endTime,
		"passed_count":     int(pV),
		"failed_count":     int(fV),
		"skipped_count":    int(sV),
		"regression_count": int(rV),
		"result_json":      string(reportJSON),
	}
	t.db.WithContext(ctx).Model(&model.CapabilityTestTask{}).
		Where("id = ?", task.ID).
		Updates(updates)

	if progressCh != nil {
		progressCh <- CapabilityTestProgress{
			TaskID: task.ID, Total: len(pairs),
			Completed: len(pairs),
			Passed:    int(pV), Failed: int(fV),
			Skipped: int(sV), Regression: int(rV),
			Stage: "completed",
		}
		close(progressCh)
	}

	t.logger.Info("能力测试完成",
		zap.Uint("task_id", task.ID),
		zap.Int64("passed", pV),
		zap.Int64("failed", fV),
		zap.Int64("skipped", sV),
		zap.Int64("regression", rV))

	return nil
}

// ============================================================
// 单次 runOne
// ============================================================

// RequestTemplateSchema 解析 request_template JSON
type RequestTemplateSchema struct {
	Path       string                 `json:"path"`
	Method     string                 `json:"method,omitempty"` // 默认 POST
	Body       map[string]interface{} `json:"body,omitempty"`
	Headers    map[string]string      `json:"headers,omitempty"`
	TimeoutSec int                    `json:"timeoutSec,omitempty"`
}

func (t *CapabilityTester) runOne(ctx context.Context, aiModel model.AIModel, tc model.CapabilityTestCase, route channelRoute) model.CapabilityTestResult {
	start := time.Now()
	result := model.CapabilityTestResult{
		ChannelID: route.ChannelID,
		Status:    "failed",
	}

	// 1. 渲染 request_template（替换 {{.ModelName}} 等变量）
	rendered, err := renderTemplate(tc.RequestTemplate, aiModel, route)
	if err != nil {
		result.ErrorMessage = "模板渲染失败: " + err.Error()
		result.ErrorCategory = "invalid_request"
		return result
	}

	var schema RequestTemplateSchema
	if err := json.Unmarshal([]byte(rendered), &schema); err != nil {
		result.ErrorMessage = "request_template JSON 解析失败: " + err.Error()
		result.ErrorCategory = "invalid_request"
		return result
	}
	if schema.Method == "" {
		schema.Method = "POST"
	}
	timeout := 20 * time.Second
	if schema.TimeoutSec > 0 {
		timeout = time.Duration(schema.TimeoutSec) * time.Second
	}

	// 2. 构建 URL
	endpointPath := tc.EndpointOverride
	if endpointPath == "" {
		endpointPath = schema.Path
	}
	url := strings.TrimRight(route.Endpoint, "/") + endpointPath

	// 3. 构建请求
	bodyBytes, _ := json.Marshal(schema.Body)
	result.RequestSnippet = truncate(string(bodyBytes), 1000)

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, schema.Method, url, bytes.NewReader(bodyBytes))
	if err != nil {
		result.ErrorMessage = "构建请求失败: " + err.Error()
		result.ErrorCategory = "invalid_request"
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	if route.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+route.APIKey)
	}
	for k, v := range schema.Headers {
		req.Header.Set(k, v)
	}

	// 4. 发送
	resp, err := t.httpClient.Do(req)
	latency := int(time.Since(start).Milliseconds())
	result.LatencyMS = latency
	if err != nil {
		result.ErrorMessage = "请求失败: " + err.Error()
		result.ErrorCategory = classifyNetErr(err)
		return result
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	result.UpstreamStatus = resp.StatusCode
	result.ResponseSnippet = truncate(string(respBody), 2000)

	// 5. 构建 TestResponse
	testResp := &TestResponse{
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		BodyBytes:   respBody,
		LatencyMS:   latency,
	}
	// 流式响应：统计 SSE data: 帧数（供 streaming_min_chunks 断言使用）
	if strings.Contains(testResp.ContentType, "event-stream") || strings.Contains(string(respBody), "data:") {
		for _, line := range strings.Split(string(respBody), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "data:") && !strings.Contains(line, "[DONE]") {
				testResp.StreamChunks++
			}
		}
	}
	if strings.Contains(testResp.ContentType, "json") || (len(respBody) > 0 && (respBody[0] == '{' || respBody[0] == '[')) {
		_ = json.Unmarshal(respBody, &testResp.BodyJSON)
	}
	// 错误分类（供 expect_error_category 使用）
	if resp.StatusCode != 200 {
		mcResult := ModelCheckResult{
			StatusCode: resp.StatusCode,
			Error:      string(respBody),
		}
		testResp.ErrorCategory, _ = categorizeCheckError(mcResult)
	}

	// 6. 异步轮询（若包含 async_poll 断言）
	if hasAsyncPoll, pollAssertion := findAsyncPoll(tc.Assertions); hasAsyncPoll {
		trace, finalResp := t.runAsyncPoll(ctx, testResp, route, pollAssertion)
		traceJSON, _ := json.Marshal(trace)
		result.PollTrace = string(traceJSON)
		if finalResp != nil {
			testResp = finalResp
		}
	}

	// 7. 评估断言
	assertResults, allPassed := EvalAssertions(testResp, tc.Assertions)
	arJSON, _ := json.Marshal(assertResults)
	result.AssertionResults = string(arJSON)

	if allPassed {
		result.Status = "passed"
		result.ErrorMessage = ""
	} else {
		result.Status = "failed"
		if result.ErrorCategory == "" {
			result.ErrorCategory = testResp.ErrorCategory
		}
		if result.ErrorCategory == "" {
			result.ErrorCategory = "assertion_failed"
		}
		// 取第一个失败断言的 reason 作为错误消息
		for _, ar := range assertResults {
			if !ar.Passed {
				result.ErrorMessage = fmt.Sprintf("断言失败 [%s]: %s", ar.Name, ar.Reason)
				break
			}
		}
	}

	return result
}

// ============================================================
// 异步轮询（视频生成）
// ============================================================

type PollAttempt struct {
	Timestamp int64  `json:"ts"`
	Status    string `json:"status"`
	Elapsed   int    `json:"elapsed_ms"`
}
type PollTrace struct {
	Attempts []PollAttempt `json:"attempts"`
	Final    string        `json:"final"`
}

type asyncPollSpec struct {
	TaskIDPath      string   `json:"taskIdPath"`
	PollEndpoint    string   `json:"pollEndpoint"`
	PollMethod      string   `json:"pollMethod,omitempty"`
	StatusPath      string   `json:"statusPath"`
	ResultPath      string   `json:"resultPath,omitempty"`
	SuccessValues   []string `json:"successValues"`
	FailValues      []string `json:"failValues"`
	PollIntervalSec int      `json:"pollIntervalSec"`
	TimeoutSec      int      `json:"timeoutSec"`
}

func findAsyncPoll(assertionsJSON string) (bool, asyncPollSpec) {
	var raw []map[string]interface{}
	if json.Unmarshal([]byte(assertionsJSON), &raw) != nil {
		return false, asyncPollSpec{}
	}
	for _, a := range raw {
		if typ, _ := a["type"].(string); typ == "async_poll" {
			spec := asyncPollSpec{
				PollIntervalSec: 10,
				TimeoutSec:      300,
				PollMethod:      "GET",
			}
			b, _ := json.Marshal(a)
			_ = json.Unmarshal(b, &spec)
			return true, spec
		}
	}
	return false, asyncPollSpec{}
}

func (t *CapabilityTester) runAsyncPoll(ctx context.Context, submitResp *TestResponse, route channelRoute, spec asyncPollSpec) (*PollTrace, *TestResponse) {
	trace := &PollTrace{}
	// 提取 task_id
	taskIDVal, ok := jsonPath(submitResp.BodyJSON, spec.TaskIDPath)
	if !ok {
		trace.Final = "no_task_id"
		return trace, nil
	}
	taskID := fmt.Sprintf("%v", taskIDVal)
	pollURL := strings.ReplaceAll(spec.PollEndpoint, "{taskId}", taskID)
	if !strings.HasPrefix(pollURL, "http") {
		pollURL = strings.TrimRight(route.Endpoint, "/") + pollURL
	}

	deadline := time.Now().Add(time.Duration(spec.TimeoutSec) * time.Second)
	interval := time.Duration(spec.PollIntervalSec) * time.Second
	start := time.Now()

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			trace.Final = "cancelled"
			return trace, nil
		case <-time.After(interval):
		}

		req, _ := http.NewRequestWithContext(ctx, spec.PollMethod, pollURL, nil)
		if route.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+route.APIKey)
		}
		resp, err := t.httpClient.Do(req)
		elapsed := int(time.Since(start).Milliseconds())
		if err != nil {
			trace.Attempts = append(trace.Attempts, PollAttempt{
				Timestamp: time.Now().Unix(), Status: "error:" + err.Error(), Elapsed: elapsed,
			})
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
		resp.Body.Close()

		var pollJSON interface{}
		_ = json.Unmarshal(body, &pollJSON)
		status := ""
		if sv, ok := jsonPath(pollJSON, spec.StatusPath); ok {
			status = fmt.Sprintf("%v", sv)
		}
		trace.Attempts = append(trace.Attempts, PollAttempt{
			Timestamp: time.Now().Unix(), Status: status, Elapsed: elapsed,
		})

		if containsStr(spec.SuccessValues, status) {
			trace.Final = "success"
			finalResp := &TestResponse{
				StatusCode:  resp.StatusCode,
				ContentType: resp.Header.Get("Content-Type"),
				BodyBytes:   body,
				BodyJSON:    pollJSON,
				LatencyMS:   elapsed,
			}
			return trace, finalResp
		}
		if containsStr(spec.FailValues, status) {
			trace.Final = "failed"
			finalResp := &TestResponse{
				StatusCode:    resp.StatusCode,
				ContentType:   resp.Header.Get("Content-Type"),
				BodyBytes:     body,
				BodyJSON:      pollJSON,
				LatencyMS:     elapsed,
				ErrorCategory: "upstream_failed",
			}
			return trace, finalResp
		}
	}
	trace.Final = "timeout"
	return trace, &TestResponse{ErrorCategory: "timeout"}
}

// ============================================================
// Helpers
// ============================================================

func (t *CapabilityTester) loadModelsAndCases(ctx context.Context, modelIDs, caseIDs []uint) ([]model.AIModel, []model.CapabilityTestCase, error) {
	var models []model.AIModel
	q := t.db.WithContext(ctx)
	if len(modelIDs) > 0 {
		// 明确指定模型ID时不过滤 is_active，允许测试已停用的模型
		q = q.Where("id IN ?", modelIDs)
	} else {
		q = q.Where("is_active = ?", true)
	}
	if err := q.Find(&models).Error; err != nil {
		return nil, nil, fmt.Errorf("加载模型失败: %w", err)
	}

	var cases []model.CapabilityTestCase
	cq := t.db.WithContext(ctx).Where("enabled = ?", true)
	if len(caseIDs) > 0 {
		cq = t.db.WithContext(ctx).Where("id IN ?", caseIDs)
	}
	if err := cq.Order("priority asc, id asc").Find(&cases).Error; err != nil {
		return nil, nil, fmt.Errorf("加载用例失败: %w", err)
	}

	return models, cases, nil
}

// caseMatchesModel 判断用例是否适用于该模型（保留向后兼容）
func (t *CapabilityTester) caseMatchesModel(c model.CapabilityTestCase, m model.AIModel) bool {
	matched, _ := t.caseMatchesModelV2(c, m, false)
	return matched
}

// caseMatchesModelV2 增强版匹配，返回 (matched bool, skipReason string)
// skipKnownDisabled=true：跳过 Features 中已确认不可用的能力（boundary/baseline 类豁免）
func (t *CapabilityTester) caseMatchesModelV2(c model.CapabilityTestCase, m model.AIModel, skipKnownDisabled bool) (bool, string) {
	// UX 流程用例：与具体模型无关，跳过过滤
	if c.ModelType == "ux_flow" {
		return true, ""
	}

	// model_type 匹配：用例的 model_type vs 模型的实际类型
	modelType := m.ModelType
	if modelType == "" {
		modelType = inferModelTypeByName(m.ModelName)
	}
	modelTypeLower := strings.ToLower(modelType)
	caseTypeLower := strings.ToLower(c.ModelType)

	typeMatch := false
	switch caseTypeLower {
	case "chat":
		// VLM/Reasoning 模型也支持文本对话
		typeMatch = modelTypeLower == "llm" || modelTypeLower == "chat" || modelTypeLower == "" ||
			strings.Contains(modelTypeLower, "reasoning") || strings.Contains(modelTypeLower, "vlm")
	case "vlm", "ocr":
		typeMatch = strings.Contains(modelTypeLower, "vlm") || strings.Contains(modelTypeLower, "vision") || modelTypeLower == "ocr"
	case "image":
		// ImageGeneration 模型匹配 image 类用例
		typeMatch = modelTypeLower == "image" || strings.Contains(modelTypeLower, "imagegeneration") ||
			strings.Contains(modelTypeLower, "image_generation")
	case "video":
		// VideoGeneration 模型匹配 video 类用例
		typeMatch = modelTypeLower == "video" || strings.Contains(modelTypeLower, "videogeneration") ||
			strings.Contains(modelTypeLower, "video_generation")
	case "tts":
		typeMatch = modelTypeLower == "tts" || strings.Contains(modelTypeLower, "tts") ||
			strings.Contains(modelTypeLower, "speech") || strings.Contains(modelTypeLower, "audio")
	case "asr":
		typeMatch = modelTypeLower == "asr" || strings.Contains(modelTypeLower, "asr") ||
			strings.Contains(modelTypeLower, "recognition")
	default:
		typeMatch = caseTypeLower == modelTypeLower
	}
	if !typeMatch {
		return false, "type_mismatch"
	}

	// model_type_filter（细粒度覆盖）
	if c.ModelTypeFilter != "" && !strings.EqualFold(c.ModelTypeFilter, modelType) {
		return false, "model_type_filter"
	}

	// Features 驱动的 provider_filter 覆盖：
	// 若 features[capability]=true（已通过实测确认支持），直接匹配，跳过 provider_filter 启发式限制。
	// 若 features[capability]=false，则视为已知禁用（在 skipKnownDisabled 块处理）。
	if c.Capability != "" && c.Category != "boundary" && c.Category != "baseline" {
		if featJSON := []byte(m.Features); len(featJSON) > 0 {
			var feats map[string]interface{}
			if json.Unmarshal(featJSON, &feats) == nil {
				if val, exists := feats[c.Capability]; exists {
					if b, ok := val.(bool); ok && b {
						return true, "" // Features 明确支持 → 直接匹配，provider_filter 不再限制
					}
				}
			}
		}
	}

	// provider_filter 匹配（启发式，用于 capability 未知时缩小测试范围；逗号分隔，空=全部）
	if c.ProviderFilter != "" {
		providers := strings.Split(strings.ToLower(c.ProviderFilter), ",")
		modelName := strings.ToLower(m.ModelName)
		matched := false
		for _, p := range providers {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if strings.Contains(modelName, p) {
				matched = true
				break
			}
		}
		if !matched {
			return false, "provider_filter"
		}
	}

	// Features 预过滤：已确认不可用的能力跳过（仅 skipKnownDisabled=true，boundary/baseline 类豁免）
	if skipKnownDisabled && c.Capability != "" &&
		c.Category != "boundary" && c.Category != "baseline" {
		if featJSON := []byte(m.Features); len(featJSON) > 0 {
			var feats map[string]interface{}
			if json.Unmarshal(featJSON, &feats) == nil {
				if val, exists := feats[c.Capability]; exists {
					if b, ok := val.(bool); ok && !b {
						return false, "feature_disabled" // 已确认不可用，跳过
					}
				}
			}
		}
	}

	return true, ""
}

// renderTemplate 渲染 request_template（text/template，支持多种变量）
func renderTemplate(tpl string, m model.AIModel, route channelRoute) (string, error) {
	data := map[string]interface{}{
		"ModelName":    route.ActualModel,
		"StandardName": m.ModelName,
		"SupplierName": route.SupplierName,
	}
	if data["ModelName"] == "" {
		data["ModelName"] = m.ModelName
	}
	for k, v := range test_assets.Assets() {
		data[k] = v
	}

	t, err := template.New("req").Parse(tpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (t *CapabilityTester) writeSkippedResult(ctx context.Context, taskID uint, m model.AIModel, c model.CapabilityTestCase, reason string) {
	r := model.CapabilityTestResult{
		TaskID: taskID, ModelID: m.ID, CaseID: c.ID,
		ModelName: m.ModelName, CaseName: c.Name,
		Status:       "skipped",
		ErrorMessage: reason,
	}
	_ = t.db.WithContext(ctx).Create(&r).Error
}

func (t *CapabilityTester) markTaskFailed(ctx context.Context, task *model.CapabilityTestTask, msg string) {
	now := time.Now()
	t.db.WithContext(ctx).Model(&model.CapabilityTestTask{}).
		Where("id = ?", task.ID).
		Updates(map[string]interface{}{
			"status":        "failed",
			"completed_at":  &now,
			"error_message": msg,
		})
}

func (t *CapabilityTester) detectRegression(ctx context.Context, r *model.CapabilityTestResult) bool {
	var baseline model.CapabilityTestBaseline
	if err := t.db.WithContext(ctx).
		Where("model_id = ? AND case_id = ?", r.ModelID, r.CaseID).
		First(&baseline).Error; err != nil {
		return false
	}
	// 基线 pass 但当前 fail → 回归
	if baseline.Outcome == "pass" && r.Status == "failed" {
		return true
	}
	// 延迟恶化 1.5x → 回归
	if baseline.Outcome == "pass" && baseline.LatencyMS > 0 && r.LatencyMS > baseline.LatencyMS*3/2 {
		return true
	}
	return false
}

func (t *CapabilityTester) pushProgress(ch chan<- CapabilityTestProgress, task *model.CapabilityTestTask, total int, passed, failed, skipped, regression *int64) {
	p := atomic.LoadInt64(passed)
	f := atomic.LoadInt64(failed)
	s := atomic.LoadInt64(skipped)
	r := atomic.LoadInt64(regression)
	completed := int(p + f + s + r)
	prog := 0
	if total > 0 {
		prog = completed * 100 / total
	}
	// 异步更新进度到 DB（每 10 个完成才更新一次，避免过于频繁的 DB 写入）
	// 无论是否有 SSE channel，均定期写 DB 确保 UI 可查到实时进度
	if completed%10 == 0 && completed > 0 {
		go t.db.Model(&model.CapabilityTestTask{}).Where("id = ?", task.ID).
			Updates(map[string]interface{}{
				"progress":      prog,
				"passed_count":  int(p),
				"failed_count":  int(f),
				"skipped_count": int(s),
			})
	}
	if ch != nil {
		ch <- CapabilityTestProgress{
			TaskID: task.ID, Total: total, Completed: completed,
			Passed: int(p), Failed: int(f),
			Skipped: int(s), Regression: int(r),
			Stage: "running",
		}
	}
}

// ComputeSuggestionsForTask 公开接口：按需计算建议报告（不缓存到 result_json）
func (t *CapabilityTester) ComputeSuggestionsForTask(ctx context.Context, taskID uint) (*SuggestionReport, error) {
	return t.computeSuggestionsV2(ctx, taskID)
}

// AutoApplySuggestions 无人值守自动应用高置信度建议
// 规则（Phase 2 精准化）：
//   - 仅对 Confidence=="high" 的 enable/disable 建议直接落库
//   - medium/low 置信度、mixed/unverified action 一律跳过，保留人工审核
// 返回：applied=已应用条数，skipped=需人工审查条数，skippedLowConfidence=因置信度不足被跳过条数
func (t *CapabilityTester) AutoApplySuggestions(ctx context.Context, taskID uint) (applied, skipped int, err error) {
	report, err := t.computeSuggestionsV2(ctx, taskID)
	if err != nil {
		return 0, 0, err
	}
	t.logger.Info("AutoApplySuggestions: 计算建议完成",
		zap.Uint("task_id", taskID),
		zap.Int("prioritized", len(report.Prioritized)))
	var firstLookupErr error
	var skippedLowConfidence int
	for _, upd := range report.Prioritized {
		// 过滤非终态建议
		if upd.Action == "mixed" || upd.Action == "unverified" || upd.Action == "" {
			skipped++
			continue
		}
		// Phase 2: 仅应用 high 置信度
		if upd.Confidence != "high" {
			skippedLowConfidence++
			skipped++
			continue
		}
		var m model.AIModel
		lookupErr := t.db.WithContext(ctx).First(&m, upd.ModelID).Error
		if lookupErr != nil {
			if firstLookupErr == nil {
				firstLookupErr = fmt.Errorf("modelID=%d: %w", upd.ModelID, lookupErr)
			}
			continue
		}
		if applyErr := applyCapabilityChange(t.db.WithContext(ctx), &m, upd.Capability, upd.Action == "enable"); applyErr == nil {
			applied++
		}
	}
	if firstLookupErr != nil {
		t.logger.Warn("AutoApplySuggestions: 首次模型查找失败", zap.Error(firstLookupErr))
	}
	t.logger.Info("自动应用建议完成",
		zap.Uint("task_id", taskID),
		zap.Int("applied", applied),
		zap.Int("skipped_total", skipped),
		zap.Int("skipped_low_confidence", skippedLowConfidence))
	return applied, skipped, nil
}

// SyncModelStatusFromBaseline 公开接口：根据 baseline 类用例测试结果更新模型 status
func (t *CapabilityTester) SyncModelStatusFromBaseline(ctx context.Context, taskID uint) {
	t.syncModelStatusFromBaseline(ctx, taskID)
}

// syncModelStatusFromBaseline 根据 baseline 类用例的测试结果更新 ai_models.status
// 逻辑：
//   - baseline 用例至少 1 个 passed  → 设为 online（模型确认可用）
//   - baseline 用例全部 failed       → 设为 offline（模型确认不可用）
//   - baseline 用例未被运行           → 不改动 status
func (t *CapabilityTester) syncModelStatusFromBaseline(ctx context.Context, taskID uint) {
	type row struct {
		ModelID         uint
		PassCount       int
		HardFailCount   int // 排除 rate_limited/timeout 后的真实失败数
	}
	var rows []row
	t.db.WithContext(ctx).Raw(`
		SELECT r.model_id,
		       SUM(CASE WHEN r.status='passed' THEN 1 ELSE 0 END) AS pass_count,
		       SUM(CASE WHEN r.status='failed'
		                 AND r.error_category NOT IN ('rate_limited','timeout','')
		                THEN 1 ELSE 0 END) AS hard_fail_count
		FROM capability_test_results r
		JOIN capability_test_cases c ON c.id = r.case_id
		WHERE r.task_id = ? AND c.category = 'baseline'
		  AND c.name IN ('chat_basic','chat_stream_basic','embedding_basic','rerank_basic')
		GROUP BY r.model_id`, taskID).Scan(&rows)

	for _, row := range rows {
		var newStatus string
		if row.PassCount > 0 {
			newStatus = "online"
		} else if row.HardFailCount > 0 && row.PassCount == 0 {
			// 只有在有确定性失败（非限流/超时）时才标记 offline
			newStatus = "offline"
		} else {
			continue
		}
		t.db.WithContext(ctx).Model(&model.AIModel{}).
			Where("id = ?", row.ModelID).
			Update("status", newStatus)
	}
	t.logger.Info("模型可用状态同步完成",
		zap.Uint("task_id", taskID),
		zap.Int("models", len(rows)))
}

// writeBaselineToCheckLogs 将本次任务中 baseline 类用例的结果回写到 model_check_logs。
// 作用：让「一键检测」和「能力测试」共享同一条可用性证据链，SyncAll 的 isModelCheckFailed
// 判定能同时利用两种数据源。
// 规则：
//   - 每个 (model_id, channel_id) 组合聚合一条 log
//   - baseline 任一 passed → available=true，清零观察窗口
//   - baseline 全 failed（排除 rate_limited/timeout）→ available=false，记录最近一条失败的 error_category / upstream_status
//   - 仅 skipped / rate_limited / timeout → 不写 log（避免污染连续失败计数）
func (t *CapabilityTester) writeBaselineToCheckLogs(ctx context.Context, taskID uint) {
	type row struct {
		ModelID        uint
		ModelName      string
		ChannelID      uint
		Status         string
		ErrorCategory  string
		UpstreamStatus int
		LatencyMS      int
		ErrorMessage   string
	}
	var rows []row
	if err := t.db.WithContext(ctx).Raw(`
		SELECT r.model_id, r.model_name, r.channel_id, r.status,
		       r.error_category, r.upstream_status, r.latency_ms, r.error_message
		FROM capability_test_results r
		JOIN capability_test_cases c ON c.id = r.case_id
		WHERE r.task_id = ? AND c.category = 'baseline'
		  AND c.name IN ('chat_basic','chat_stream_basic','embedding_basic','rerank_basic')
		ORDER BY r.id`, taskID).Scan(&rows).Error; err != nil {
		t.logger.Warn("查询 baseline 结果失败", zap.Error(err))
		return
	}
	if len(rows) == 0 {
		return
	}

	// 按 (model_id, channel_id) 聚合
	type agg struct {
		modelName     string
		channelID     uint
		hasPass       bool
		hardFailCount int
		lastFailErr   string
		lastFailCat   string
		lastFailCode  int
		latencySum    int
		latencyCount  int
	}
	buckets := make(map[string]*agg)
	for _, r := range rows {
		key := fmt.Sprintf("%d:%d", r.ModelID, r.ChannelID)
		a := buckets[key]
		if a == nil {
			a = &agg{modelName: r.ModelName, channelID: r.ChannelID}
			buckets[key] = a
		}
		if r.LatencyMS > 0 {
			a.latencySum += r.LatencyMS
			a.latencyCount++
		}
		switch r.Status {
		case "passed":
			a.hasPass = true
		case "failed", "regression":
			// 排除限流/超时（非确定性失败，不计入硬失败）
			if r.ErrorCategory != "rate_limited" && r.ErrorCategory != "timeout" {
				a.hardFailCount++
				a.lastFailErr = r.ErrorMessage
				a.lastFailCat = r.ErrorCategory
				a.lastFailCode = r.UpstreamStatus
			}
		}
	}

	now := time.Now()
	written := 0
	for key, a := range buckets {
		// 解析 key 得到 modelID
		var modelID uint
		fmt.Sscanf(key, "%d:", &modelID)

		// 跳过既无通过也无硬失败（全是 skipped/limit/timeout）的聚合组
		if !a.hasPass && a.hardFailCount == 0 {
			continue
		}

		available := a.hasPass
		latency := int64(0)
		if a.latencyCount > 0 {
			latency = int64(a.latencySum / a.latencyCount)
		}

		log := &model.ModelCheckLog{
			ModelID:   modelID,
			ModelName: a.modelName,
			ChannelID: a.channelID,
			Available: available,
			LatencyMs: latency,
			CheckedAt: now,
		}
		if available {
			log.StatusCode = 200
		} else {
			log.StatusCode = a.lastFailCode
			log.Error = truncate(a.lastFailErr, 500)
			log.ErrorCategory = a.lastFailCat
			// 连续失败计数：复用 ModelChecker 的统计口径
			if t.mc != nil {
				log.ConsecutiveFailures = t.mc.countRecentFailures(ctx, modelID)
			}
		}
		if err := t.db.WithContext(ctx).Create(log).Error; err != nil {
			t.logger.Warn("回写 model_check_logs 失败",
				zap.Uint("model_id", modelID), zap.Error(err))
			continue
		}
		written++
	}

	t.logger.Info("baseline 结果回写完成",
		zap.Uint("task_id", taskID),
		zap.Int("logs_written", written))
}

// containsStr 查找字符串是否在切片中
func containsStr(arr []string, s string) bool {
	for _, a := range arr {
		if a == s {
			return true
		}
	}
	return false
}

func classifyNetErr(err error) string {
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline"):
		return "timeout"
	case strings.Contains(s, "connection refused") || strings.Contains(s, "no such host"):
		return "connection_error"
	case strings.Contains(s, "eof") || strings.Contains(s, "reset"):
		return "connection_error"
	default:
		return "unknown"
	}
}
