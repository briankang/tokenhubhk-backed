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
	"text/template"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// ============================================================
// UXFlowTester: 执行多步脚本测试（用户体验流程）
// 每步可：
//   - 设置 method / endpoint / body_template / headers
//   - 从响应提取变量（extract_vars：{key: "$.jsonpath"}）
//   - 供后续步骤引用（body_template/endpoint/headers 支持 {{.vars.xxx}}）
//   - 并发压测（parallel: N）
//   - 中断流式（abort_after_ms）
// ============================================================

type UXFlowTester struct {
	db         *gorm.DB
	httpClient *http.Client
	logger     *zap.Logger
	baseURL    string // 平台自身的 base URL（如 http://backend:8080/api/v1）
	authToken  string // 可选：admin JWT（用于需要鉴权的流程步骤）
}

func NewUXFlowTester(db *gorm.DB, baseURL, authToken string) *UXFlowTester {
	return &UXFlowTester{
		db:        db,
		baseURL:   baseURL,
		authToken: authToken,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		logger: logger.L.With(zap.String("module", "ux_flow_tester")),
	}
}

// FlowStep UX 流程单步定义
type FlowStep struct {
	Name         string                 `json:"name"`
	Method       string                 `json:"method"`
	Endpoint     string                 `json:"endpoint"`
	BodyTemplate string                 `json:"body_template,omitempty"`
	Headers      map[string]string      `json:"headers,omitempty"`
	Assertions   []map[string]interface{} `json:"assertions,omitempty"`
	ExtractVars  map[string]string      `json:"extract_vars,omitempty"`
	SleepMS      int                    `json:"sleep_ms,omitempty"`
	AbortAfterMS int                    `json:"abort_after_ms,omitempty"`
	Parallel     int                    `json:"parallel,omitempty"`
}

// FlowStepTrace 单步轨迹（写入 result.flow_trace）
type FlowStepTrace struct {
	Name       string                    `json:"name"`
	Method     string                    `json:"method"`
	Endpoint   string                    `json:"endpoint"`
	Status     int                       `json:"status"`
	LatencyMS  int                       `json:"latency_ms"`
	Assertions []AssertionResult         `json:"assertions,omitempty"`
	Passed     bool                      `json:"passed"`
	Error      string                    `json:"error,omitempty"`
	Parallel   *ParallelAggregate        `json:"parallel,omitempty"`
}

type ParallelAggregate struct {
	Total      int   `json:"total"`
	StatusBag  map[int]int `json:"status_bag"`
	AnyFailed  bool  `json:"any_failed"`
}

// RunFlow 执行一个 UX 流程测试（一个 CapabilityTestCase.flow_steps）
func (u *UXFlowTester) RunFlow(ctx context.Context, tc model.CapabilityTestCase) model.CapabilityTestResult {
	result := model.CapabilityTestResult{
		CaseID:   tc.ID,
		CaseName: tc.Name,
		Status:   "failed",
	}

	if tc.FlowSteps == "" {
		result.ErrorMessage = "flow_steps 为空"
		return result
	}

	var steps []FlowStep
	if err := json.Unmarshal([]byte(tc.FlowSteps), &steps); err != nil {
		result.ErrorMessage = "flow_steps 解析失败: " + err.Error()
		return result
	}

	vars := make(map[string]interface{})
	traces := make([]FlowStepTrace, 0, len(steps))
	allPassed := true

	start := time.Now()
	for _, step := range steps {
		if step.SleepMS > 0 {
			time.Sleep(time.Duration(step.SleepMS) * time.Millisecond)
		}
		trace := u.executeStep(ctx, step, vars)
		traces = append(traces, trace)
		if !trace.Passed {
			allPassed = false
			if result.ErrorMessage == "" {
				result.ErrorMessage = fmt.Sprintf("步骤 %s 失败: %s", step.Name, trace.Error)
			}
		}
	}
	result.LatencyMS = int(time.Since(start).Milliseconds())

	tracesJSON, _ := json.Marshal(traces)
	result.FlowTrace = string(tracesJSON)

	if allPassed {
		result.Status = "passed"
		result.ErrorMessage = ""
	}
	return result
}

// executeStep 执行单步
func (u *UXFlowTester) executeStep(ctx context.Context, step FlowStep, vars map[string]interface{}) FlowStepTrace {
	trace := FlowStepTrace{
		Name: step.Name, Method: step.Method, Endpoint: step.Endpoint,
	}
	if step.Method == "" {
		step.Method = "POST"
	}

	// 并发模式
	if step.Parallel > 1 {
		return u.executeParallel(ctx, step, vars)
	}

	// 渲染 endpoint / body / headers 中的 {{.vars.xxx}}
	renderedEP, err := renderWithVars(step.Endpoint, vars)
	if err != nil {
		trace.Error = "渲染 endpoint 失败: " + err.Error()
		return trace
	}
	renderedBody, err := renderWithVars(step.BodyTemplate, vars)
	if err != nil {
		trace.Error = "渲染 body 失败: " + err.Error()
		return trace
	}

	url := u.baseURL + renderedEP

	start := time.Now()
	reqCtx := ctx
	var cancel context.CancelFunc
	if step.AbortAfterMS > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, time.Duration(step.AbortAfterMS)*time.Millisecond)
		defer cancel()
	}

	var body io.Reader
	if renderedBody != "" {
		body = strings.NewReader(renderedBody)
	}
	req, err := http.NewRequestWithContext(reqCtx, step.Method, url, body)
	if err != nil {
		trace.Error = "构建请求失败: " + err.Error()
		return trace
	}
	req.Header.Set("Content-Type", "application/json")
	if u.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+u.authToken)
	}
	for k, v := range step.Headers {
		rv, _ := renderWithVars(v, vars)
		req.Header.Set(k, rv)
	}

	resp, err := u.httpClient.Do(req)
	trace.LatencyMS = int(time.Since(start).Milliseconds())
	if err != nil {
		// abort 是预期行为
		if step.AbortAfterMS > 0 && strings.Contains(err.Error(), "context deadline") {
			trace.Status = -1
			trace.Passed = true
			return trace
		}
		trace.Error = err.Error()
		return trace
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	trace.Status = resp.StatusCode

	var bodyJSON interface{}
	_ = json.Unmarshal(respBody, &bodyJSON)

	// 提取变量
	for k, path := range step.ExtractVars {
		if v, ok := jsonPath(bodyJSON, path); ok {
			vars[k] = v
		}
	}

	// 评估断言
	testResp := &TestResponse{
		StatusCode: resp.StatusCode, BodyBytes: respBody,
		BodyJSON: bodyJSON, ContentType: resp.Header.Get("Content-Type"),
		LatencyMS: trace.LatencyMS,
		// 流式 chunks：简单估算按行数
		StreamChunks: countLines(respBody),
	}
	if len(step.Assertions) > 0 {
		assertionsJSON, _ := json.Marshal(step.Assertions)
		results, passed := EvalAssertions(testResp, string(assertionsJSON))
		trace.Assertions = results
		trace.Passed = passed
		if !passed {
			for _, r := range results {
				if !r.Passed {
					trace.Error = fmt.Sprintf("断言 [%s]: %s", r.Name, r.Reason)
					break
				}
			}
		}
	} else {
		trace.Passed = resp.StatusCode >= 200 && resp.StatusCode < 400
	}

	return trace
}

// executeParallel 并发压测模式（for rate_limit 测试）
func (u *UXFlowTester) executeParallel(ctx context.Context, step FlowStep, vars map[string]interface{}) FlowStepTrace {
	trace := FlowStepTrace{
		Name: step.Name, Method: step.Method, Endpoint: step.Endpoint,
	}
	agg := &ParallelAggregate{
		Total:     step.Parallel,
		StatusBag: make(map[int]int),
	}

	renderedEP, _ := renderWithVars(step.Endpoint, vars)
	renderedBody, _ := renderWithVars(step.BodyTemplate, vars)
	url := u.baseURL + renderedEP

	var mu sync.Mutex
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < step.Parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequestWithContext(ctx, step.Method, url, strings.NewReader(renderedBody))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if u.authToken != "" {
				req.Header.Set("Authorization", "Bearer "+u.authToken)
			}
			resp, err := u.httpClient.Do(req)
			if err != nil {
				mu.Lock()
				agg.StatusBag[-1]++
				mu.Unlock()
				return
			}
			resp.Body.Close()
			mu.Lock()
			agg.StatusBag[resp.StatusCode]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	trace.LatencyMS = int(time.Since(start).Milliseconds())

	// 评估断言（针对每个响应状态码）
	// 简化：若任一断言期望 status_in [..429..] 则需要 429 出现
	passed := true
	for _, a := range step.Assertions {
		if typ, _ := a["type"].(string); typ == "status_in" {
			if wants, ok := a["value"].([]interface{}); ok {
				hasExpected := false
				for _, w := range wants {
					wi := toInt(w)
					if agg.StatusBag[wi] > 0 {
						hasExpected = true
						break
					}
				}
				if !hasExpected {
					passed = false
					trace.Error = fmt.Sprintf("并发 %d 请求未出现期望状态码", step.Parallel)
				}
			}
		}
	}
	trace.Passed = passed
	trace.Parallel = agg
	return trace
}

// renderWithVars 渲染模板，支持 {{.vars.xxx}} 和 {{.ModelName}}
func renderWithVars(tpl string, vars map[string]interface{}) (string, error) {
	if tpl == "" {
		return "", nil
	}
	if !strings.Contains(tpl, "{{") {
		return tpl, nil
	}
	t, err := template.New("step").Parse(tpl)
	if err != nil {
		return "", err
	}
	data := map[string]interface{}{
		"vars": vars,
	}
	// 将 vars 里的变量平铺到顶层（便于写 {{.ModelName}}）
	for k, v := range vars {
		if _, conflict := data[k]; !conflict {
			data[k] = v
		}
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func countLines(b []byte) int {
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}
