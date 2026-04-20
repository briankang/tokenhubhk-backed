package aimodel

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ============================================================
// 能力测试断言引擎
// 14 种断言类型，用于在 capability_test_cases 中声明期望
// ============================================================

// Assertion 单条断言定义（从 capability_test_cases.Assertions JSON 解析）
type Assertion struct {
	Type   string                 `json:"type"`
	Path   string                 `json:"path,omitempty"`
	Value  interface{}            `json:"value,omitempty"`
	Params map[string]interface{} `json:"-"` // 原始 JSON 对象（扩展参数）
}

// AssertionResult 断言评估结果
type AssertionResult struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason,omitempty"`
}

// TestResponse HTTP 响应摘要（供断言使用）
type TestResponse struct {
	StatusCode    int
	ContentType   string
	BodyBytes     []byte
	BodyJSON      interface{} // 若 body 是 JSON 则解析，否则 nil
	LatencyMS     int
	StreamChunks  int    // 流式模式下的 SSE 帧数
	ErrorCategory string // 由 categorizeCheckError 推断
}

// EvalAssertions 依次评估所有断言，全部通过返回 true
func EvalAssertions(resp *TestResponse, assertionsJSON string) ([]AssertionResult, bool) {
	var raw []map[string]interface{}
	if err := json.Unmarshal([]byte(assertionsJSON), &raw); err != nil {
		return []AssertionResult{{
			Name: "parse_assertions", Type: "parse", Passed: false,
			Reason: "断言 JSON 解析失败: " + err.Error(),
		}}, false
	}

	results := make([]AssertionResult, 0, len(raw))
	allPassed := true
	for i, a := range raw {
		typ, _ := a["type"].(string)
		name := typ
		if n, ok := a["name"].(string); ok && n != "" {
			name = n
		}
		if typ == "" {
			results = append(results, AssertionResult{
				Name: fmt.Sprintf("assert_%d", i), Type: "unknown", Passed: false,
				Reason: "断言 type 字段为空",
			})
			allPassed = false
			continue
		}
		passed, reason := evalSingle(resp, typ, a)
		results = append(results, AssertionResult{
			Name: name, Type: typ, Passed: passed, Reason: reason,
		})
		if !passed {
			allPassed = false
		}
	}
	return results, allPassed
}

func evalSingle(resp *TestResponse, typ string, a map[string]interface{}) (bool, string) {
	switch typ {
	case "status_eq":
		want := toInt(a["value"])
		if resp.StatusCode == want {
			return true, ""
		}
		return false, fmt.Sprintf("status=%d want %d", resp.StatusCode, want)

	case "status_in":
		wants := toIntSlice(a["value"])
		for _, w := range wants {
			if resp.StatusCode == w {
				return true, ""
			}
		}
		return false, fmt.Sprintf("status=%d not in %v", resp.StatusCode, wants)

	case "jsonpath_exists":
		path, _ := a["path"].(string)
		v, ok := jsonPath(resp.BodyJSON, path)
		if ok && v != nil {
			return true, ""
		}
		return false, fmt.Sprintf("path %s not found", path)

	case "jsonpath_eq":
		path, _ := a["path"].(string)
		v, ok := jsonPath(resp.BodyJSON, path)
		if !ok {
			return false, fmt.Sprintf("path %s not found", path)
		}
		if fmt.Sprintf("%v", v) == fmt.Sprintf("%v", a["value"]) {
			return true, ""
		}
		return false, fmt.Sprintf("path %s = %v want %v", path, v, a["value"])

	case "jsonpath_contains":
		path, _ := a["path"].(string)
		v, ok := jsonPath(resp.BodyJSON, path)
		if !ok {
			return false, fmt.Sprintf("path %s not found", path)
		}
		want, _ := a["value"].(string)
		if strings.Contains(fmt.Sprintf("%v", v), want) {
			return true, ""
		}
		return false, fmt.Sprintf("path %s does not contain %q", path, want)

	case "response_is_valid_json":
		path, _ := a["path"].(string)
		var target interface{} = resp.BodyJSON
		if path != "" && path != "$" {
			v, ok := jsonPath(resp.BodyJSON, path)
			if !ok {
				return false, fmt.Sprintf("path %s not found", path)
			}
			target = v
		}
		// 若字段是字符串，尝试二次解析
		if s, ok := target.(string); ok {
			var tmp interface{}
			if json.Unmarshal([]byte(s), &tmp) != nil {
				return false, "field is not valid JSON"
			}
			return true, ""
		}
		if target != nil {
			return true, ""
		}
		return false, "target is nil"

	case "no_error_code":
		if resp.BodyJSON == nil {
			return true, ""
		}
		if m, ok := resp.BodyJSON.(map[string]interface{}); ok {
			if err, has := m["error"]; has && err != nil {
				return false, fmt.Sprintf("response has error: %v", err)
			}
			// code != 0 / != 200 视为错误
			if code, has := m["code"]; has {
				ci := toInt(code)
				if ci != 0 && ci != 200 {
					return false, fmt.Sprintf("code=%v", code)
				}
			}
		}
		return true, ""

	case "field_nonempty":
		path, _ := a["path"].(string)
		v, ok := jsonPath(resp.BodyJSON, path)
		if !ok || v == nil {
			return false, fmt.Sprintf("path %s not found", path)
		}
		switch x := v.(type) {
		case string:
			if x != "" {
				return true, ""
			}
			return false, "empty string"
		case []interface{}:
			if len(x) > 0 {
				return true, ""
			}
			return false, "empty array"
		default:
			return true, ""
		}

	case "expect_error_category":
		want, _ := a["category"].(string)
		if want == "" {
			want, _ = a["value"].(string)
		}
		if resp.ErrorCategory == want {
			return true, ""
		}
		return false, fmt.Sprintf("category=%q want %q", resp.ErrorCategory, want)

	case "latency_lt_ms":
		th := toInt(a["threshold"])
		if th == 0 {
			th = toInt(a["value"])
		}
		if resp.LatencyMS < th {
			return true, ""
		}
		return false, fmt.Sprintf("latency=%dms >= %dms", resp.LatencyMS, th)

	case "response_size_gt_bytes":
		th := toInt(a["min_bytes"])
		if th == 0 {
			th = toInt(a["value"])
		}
		if len(resp.BodyBytes) > th {
			return true, ""
		}
		return false, fmt.Sprintf("body size=%d <= %d", len(resp.BodyBytes), th)

	case "content_type_matches":
		pat, _ := a["pattern"].(string)
		if pat == "" {
			pat, _ = a["value"].(string)
		}
		// 支持简单通配：audio/* → ^audio/
		regex := strings.ReplaceAll(regexp.QuoteMeta(pat), `\*`, `.*`)
		re, err := regexp.Compile("^" + regex)
		if err != nil {
			return false, "invalid pattern: " + err.Error()
		}
		if re.MatchString(resp.ContentType) {
			return true, ""
		}
		return false, fmt.Sprintf("content-type=%q !~ %q", resp.ContentType, pat)

	case "streaming_min_chunks":
		th := toInt(a["min_chunks"])
		if th == 0 {
			th = toInt(a["value"])
		}
		if resp.StreamChunks >= th {
			return true, ""
		}
		return false, fmt.Sprintf("chunks=%d < %d", resp.StreamChunks, th)

	case "async_poll":
		// async_poll 在 runOne 层面处理（轮询期间生成 PollTrace），
		// 走到断言评估阶段意味着轮询已完成；若响应仍 != 成功则失败。
		// 业务上 runOne 会把终态响应注入 resp，再走其他断言（如 field_nonempty $.result.video_url）
		return true, ""

	default:
		return false, "unknown assertion type: " + typ
	}
}

// ============================================================
// JSONPath（最小实现）
// 支持：$, $.a.b, $.a[0], $.a.b[2].c
// ============================================================

var jpTokenRe = regexp.MustCompile(`^([a-zA-Z_][\w-]*)(.*)$`)
var jpArrayRe = regexp.MustCompile(`^\[(\d+)\](.*)$`)

func jsonPath(root interface{}, path string) (interface{}, bool) {
	if root == nil {
		return nil, false
	}
	path = strings.TrimSpace(path)
	if path == "" || path == "$" {
		return root, true
	}
	path = strings.TrimPrefix(path, "$")
	path = strings.TrimPrefix(path, ".")

	current := root
	remaining := path
	for len(remaining) > 0 {
		if strings.HasPrefix(remaining, ".") {
			remaining = remaining[1:]
			continue
		}
		if strings.HasPrefix(remaining, "[") {
			m := jpArrayRe.FindStringSubmatch(remaining)
			if m == nil {
				return nil, false
			}
			idx, _ := strconv.Atoi(m[1])
			arr, ok := current.([]interface{})
			if !ok || idx >= len(arr) {
				return nil, false
			}
			current = arr[idx]
			remaining = m[2]
			continue
		}
		m := jpTokenRe.FindStringSubmatch(remaining)
		if m == nil {
			return nil, false
		}
		key := m[1]
		obj, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		v, has := obj[key]
		if !has {
			return nil, false
		}
		current = v
		remaining = m[2]
	}
	return current, true
}

// ============================================================
// Helpers
// ============================================================

func toInt(v interface{}) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}

func toIntSlice(v interface{}) []int {
	if arr, ok := v.([]interface{}); ok {
		out := make([]int, 0, len(arr))
		for _, e := range arr {
			out = append(out, toInt(e))
		}
		return out
	}
	return nil
}
