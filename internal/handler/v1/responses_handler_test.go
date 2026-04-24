package v1

import "testing"

func TestResponsesInputText_String(t *testing.T) {
	if got := responsesInputText("hello"); got != "hello" {
		t.Fatalf("expected hello, got %q", got)
	}
}

func TestResponsesInputText_ArrayContent(t *testing.T) {
	got := responsesInputText([]interface{}{
		map[string]interface{}{"content": "first"},
		map[string]interface{}{"text": "second"},
	})
	if got != "first\nsecond" {
		t.Fatalf("unexpected text: %q", got)
	}
}
