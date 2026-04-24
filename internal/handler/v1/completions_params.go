package v1

import (
	"encoding/json"
	"strings"
)

var chatRequestHandledFields = map[string]bool{
	"model": true, "messages": true, "max_tokens": true, "temperature": true,
	"top_p": true, "stream": true, "stream_options": true, "stop": true,
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
