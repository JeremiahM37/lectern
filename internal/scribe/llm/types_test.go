package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolArgs(t *testing.T) {
	cases := []struct {
		raw     string
		want    string
		malform bool
	}{
		{"", `{}`, false},
		{"  null ", `{}`, false},
		{`{"a":1}`, `{"a":1}`, false},
		{` {"a": 1} `, `{"a": 1}`, false},
		{`{"a": 1`, `"{\"a\": 1"`, true},
		{`[1,2]`, `"[1,2]"`, true},
	}
	for _, c := range cases {
		got := ToolArgs(c.raw)
		if string(got) != c.want {
			t.Errorf("ToolArgs(%q) = %s, want %s", c.raw, got, c.want)
		}
		if !json.Valid(got) {
			t.Errorf("ToolArgs(%q) = %s is not valid JSON", c.raw, got)
		}
		text, bad := UnparsedArgs(got)
		if bad != c.malform {
			t.Errorf("UnparsedArgs(%s) flagged = %v, want %v", got, bad, c.malform)
		}
		if bad && text != c.raw {
			t.Errorf("raw text = %q, want %q", text, c.raw)
		}
	}
}

func TestStreamErrorIsRetryableAndNamesNoStatus(t *testing.T) {
	err := &APIError{Provider: "ollama", Type: StreamErrorType, Message: "XML syntax error on line 21"}
	if cat, retry := Classify(err); cat != CatStream || !retry {
		t.Fatalf("Classify = %q, %v; want a retryable stream error", cat, retry)
	}
	if got := err.Error(); strings.Contains(got, "HTTP 0") || !strings.Contains(got, "XML syntax error") {
		t.Fatalf("Error() = %q", got)
	}
}
