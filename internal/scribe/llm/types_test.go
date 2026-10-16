package llm

import (
	"encoding/json"
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
