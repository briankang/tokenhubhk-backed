package channel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"tokenhub-server/internal/model"
)

// CustomParamField describes one supported channel custom_params field.
type CustomParamField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Sensitive   bool   `json:"sensitive,omitempty"`
	Description string `json:"description"`
	Example     string `json:"example,omitempty"`
}

// CustomParamSchema describes the accepted custom_params shape for a supplier.
type CustomParamSchema struct {
	SupplierCode string             `json:"supplier_code"`
	Fields       []CustomParamField `json:"fields"`
	AllowsExtra  bool               `json:"allows_extra"`
	Example      map[string]any     `json:"example"`
}

// GetCustomParamSchema returns the runtime schema used to validate channel custom_params.
func GetCustomParamSchema(supplierCode string) CustomParamSchema {
	supplierCode = strings.ToLower(strings.TrimSpace(supplierCode))
	fields := []CustomParamField{
		{Name: "headers", Type: "object<string,string>", Description: "HTTP headers merged into upstream requests.", Example: `{"X-Custom": "value"}`},
		{Name: "extra_body", Type: "object", Description: "JSON object merged into the upstream request body.", Example: `{"metadata": {"tenant": "default"}}`},
	}
	example := map[string]any{
		"headers":    map[string]string{"X-Custom": "value"},
		"extra_body": map[string]any{"metadata": map[string]string{"source": "tokenhub"}},
	}
	if supplierNeedsClientSecret(supplierCode) {
		fields = append(fields, CustomParamField{
			Name:        "client_secret",
			Type:        "string",
			Required:    true,
			Sensitive:   true,
			Description: "OAuth client secret. channel.api_key stores the paired client_id.",
			Example:     "your-client-secret",
		})
		example["client_secret"] = "your-client-secret"
	}
	return CustomParamSchema{SupplierCode: supplierCode, Fields: fields, AllowsExtra: true, Example: example}
}

// NormalizeCustomParams accepts a JSON string, object, or raw JSON and returns a canonical JSON object.
func NormalizeCustomParams(input any) (model.JSON, error) {
	if input == nil {
		return nil, nil
	}

	var raw []byte
	switch v := input.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, nil
		}
		raw = []byte(v)
	case []byte:
		if len(bytes.TrimSpace(v)) == 0 {
			return nil, nil
		}
		raw = v
	case json.RawMessage:
		if len(bytes.TrimSpace(v)) == 0 || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, nil
		}
		if len(bytes.TrimSpace(v)) > 0 && bytes.TrimSpace(v)[0] == '"' {
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return nil, fmt.Errorf("custom_params must be a JSON object or JSON string: %w", err)
			}
			return NormalizeCustomParams(s)
		}
		raw = v
	default:
		marshaled, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("custom_params is not JSON serializable: %w", err)
		}
		raw = marshaled
	}

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("custom_params must be valid JSON: %w", err)
	}
	if obj == nil {
		return nil, nil
	}
	normalized, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return model.JSON(normalized), nil
}

// ValidateCustomParams checks reserved fields and supplier-required fields.
func ValidateCustomParams(supplierCode string, params model.JSON) error {
	if len(bytes.TrimSpace(params)) == 0 {
		if supplierNeedsClientSecret(supplierCode) {
			return fmt.Errorf("custom_params.client_secret is required for supplier %s", supplierCode)
		}
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal(params, &obj); err != nil {
		return fmt.Errorf("custom_params must be valid JSON object: %w", err)
	}
	if headers, ok := obj["headers"]; ok {
		headerMap, ok := headers.(map[string]any)
		if !ok {
			return fmt.Errorf("custom_params.headers must be an object")
		}
		for k, v := range headerMap {
			if strings.TrimSpace(k) == "" {
				return fmt.Errorf("custom_params.headers contains an empty header name")
			}
			if _, ok := v.(string); !ok {
				return fmt.Errorf("custom_params.headers.%s must be a string", k)
			}
		}
	}
	if extraBody, ok := obj["extra_body"]; ok {
		if _, ok := extraBody.(map[string]any); !ok {
			return fmt.Errorf("custom_params.extra_body must be an object")
		}
	}
	if supplierNeedsClientSecret(supplierCode) {
		secret, ok := obj["client_secret"].(string)
		if !ok || strings.TrimSpace(secret) == "" {
			return fmt.Errorf("custom_params.client_secret is required for supplier %s", supplierCode)
		}
	}
	return nil
}

func supplierNeedsClientSecret(supplierCode string) bool {
	switch strings.ToLower(strings.TrimSpace(supplierCode)) {
	case "baidu_qianfan", "wenxin", "baidu_wenxin":
		return true
	default:
		return false
	}
}

func flattenCustomBodyParams(params map[string]any) map[string]any {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]any)
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "headers" || k == "extra_body" {
			continue
		}
		out[k] = params[k]
	}
	if extra, ok := params["extra_body"].(map[string]any); ok {
		extraKeys := make([]string, 0, len(extra))
		for k := range extra {
			extraKeys = append(extraKeys, k)
		}
		sort.Strings(extraKeys)
		for _, k := range extraKeys {
			out[k] = extra[k]
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
