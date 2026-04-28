package channel

import (
	"encoding/json"
	"testing"

	"tokenhub-server/internal/model"
)

func TestNormalizeCustomParamsAcceptsJSONString(t *testing.T) {
	got, err := NormalizeCustomParams(`{"headers":{"X-Test":"ok"},"extra_body":{"seed":1}}`)
	if err != nil {
		t.Fatalf("NormalizeCustomParams returned error: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("normalized JSON is invalid: %v", err)
	}
	if _, ok := obj["headers"]; !ok {
		t.Fatalf("headers missing from normalized params: %s", string(got))
	}
}

func TestValidateCustomParamsRejectsInvalidReservedFields(t *testing.T) {
	if err := ValidateCustomParams("openai", model.JSON(`{"headers":{"X-Number":1}}`)); err == nil {
		t.Fatal("headers values must be strings")
	}
	if err := ValidateCustomParams("openai", model.JSON(`{"extra_body":"bad"}`)); err == nil {
		t.Fatal("extra_body must be an object")
	}
}

func TestValidateCustomParamsRequiresQianfanClientSecret(t *testing.T) {
	if err := ValidateCustomParams("baidu_qianfan", model.JSON(`{"extra_body":{"foo":"bar"}}`)); err == nil {
		t.Fatal("baidu_qianfan should require client_secret")
	}
	if err := ValidateCustomParams("baidu_qianfan", model.JSON(`{"client_secret":"secret"}`)); err != nil {
		t.Fatalf("client_secret should satisfy baidu_qianfan schema: %v", err)
	}
}

func TestGetCustomBodyParamsFlattensExtraBody(t *testing.T) {
	ch := &model.Channel{CustomParams: model.JSON(`{
		"headers": {"X-Test": "ok"},
		"client_secret": "secret",
		"extra_body": {"metadata": {"source": "tokenhub"}, "temperature": 0.2}
	}`)}
	got := GetCustomBodyParams(ch)
	if _, ok := got["headers"]; ok {
		t.Fatalf("headers should not be returned as body params: %+v", got)
	}
	if _, ok := got["extra_body"]; ok {
		t.Fatalf("extra_body should be flattened: %+v", got)
	}
	if got["temperature"] != 0.2 {
		t.Fatalf("temperature not flattened from extra_body: %+v", got)
	}
	if got["client_secret"] != "secret" {
		t.Fatalf("top-level custom field should be preserved: %+v", got)
	}
}
