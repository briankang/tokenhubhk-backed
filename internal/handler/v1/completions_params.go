package v1

import (
	"encoding/json"
	"strings"
)

var chatRequestHandledFields = map[string]bool{
	"model": true, "messages": true, "max_tokens": true, "max_completion_tokens": true, "temperature": true,
	"top_p": true, "stream": true, "stream_options": true, "stop": true,
}

// normalizeChatCompletionRequest folds OpenAI's newer max_completion_tokens
// into the platform's internal MaxTokens field. The rest of the billing,
// TPM and provider routing code uses MaxTokens as the single system input.
// If both fields are present, max_tokens keeps backward-compatible priority.
func normalizeChatCompletionRequest(req *chatCompletionRequest, rawMap map[string]json.RawMessage) {
	if req == nil || req.MaxTokens > 0 || rawMap == nil {
		return
	}
	req.MaxTokens = intFromRaw(rawMap["max_completion_tokens"])
}

// extractChatExtraParams returns request fields that are not represented by
// chatCompletionRequest but still need to be forwarded or transformed.
func extractChatExtraParams(rawMap map[string]json.RawMessage) map[string]interface{} {
	extra := make(map[string]interface{})
	for k, v := range rawMap {
		if chatRequestHandledFields[k] {
			continue
		}
		var val interface{}
		if json.Unmarshal(v, &val) == nil {
			extra[k] = val
		}
	}
	normalizeReasoningEffort(extra)
	return extra
}

// extractProviderPassthroughParams removes explicit provider passthrough wrappers
// from platform params. The returned K/V pairs are merged after param mapping, so
// names and values arrive at the upstream provider unchanged.
func extractProviderPassthroughParams(extra map[string]interface{}) map[string]interface{} {
	if extra == nil {
		return nil
	}
	out := make(map[string]interface{})
	for _, key := range []string{"provider_extra", "extra_body", "custom_params"} {
		raw, ok := extra[key]
		if !ok {
			continue
		}
		delete(extra, key)
		obj, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		for k, v := range obj {
			if strings.TrimSpace(k) == "" {
				continue
			}
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeReasoningEffort keeps compatibility with Playground's historical
// {reasoning:{effort:"..."}} shape while the platform mapping key is flat.
func normalizeReasoningEffort(extra map[string]interface{}) {
	if extra == nil {
		return
	}
	if _, exists := extra["reasoning_effort"]; exists {
		return
	}
	reasoning, ok := extra["reasoning"].(map[string]interface{})
	if !ok {
		return
	}
	if effort, ok := reasoning["effort"].(string); ok && strings.TrimSpace(effort) != "" {
		extra["reasoning_effort"] = strings.TrimSpace(effort)
	}
}

func intFromRaw(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int(f)
	}
	return 0
}
