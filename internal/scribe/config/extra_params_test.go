package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtraParamsDecodeFromJSON(t *testing.T) {
	var c Config
	raw := `{"reasoning_by_provider":{"myproxy":{"extra_params":{"top_k":5,"enable_thinking":true}}}}`
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	ep := c.ReasoningByProvider["myproxy"].ExtraParams
	if ep == nil || len(*ep) != 2 {
		t.Fatalf("extra_params = %v, want two keys", ep)
	}
	if string((*ep)["top_k"]) != "5" || string((*ep)["enable_thinking"]) != "true" {
		t.Fatalf("values not kept verbatim: %v", *ep)
	}
	if c.ReasoningByProvider["myproxy"].IsZero() {
		t.Fatal("a block with only extra_params must not read as zero")
	}
}

func TestExtraParamsMergeOverlaysKeyByKey(t *testing.T) {
	base := map[string]Reasoning{"p": {Effort: "low", ExtraParams: &ExtraParamMap{
		"a": json.RawMessage(`1`),
		"b": json.RawMessage(`"base"`),
	}}}
	over := map[string]Reasoning{"p": {ExtraParams: &ExtraParamMap{
		"b": json.RawMessage(`"over"`),
		"c": json.RawMessage(`3`),
	}}}
	out := mergeReasoning(base, over)["p"]
	if out.Effort != "low" {
		t.Errorf("effort = %q, want the base value kept", out.Effort)
	}
	want := map[string]string{"a": "1", "b": `"over"`, "c": "3"}
	if len(*out.ExtraParams) != len(want) {
		t.Fatalf("merged keys = %v, want %v", *out.ExtraParams, want)
	}
	for k, v := range want {
		if got := string((*out.ExtraParams)[k]); got != v {
			t.Errorf("%s = %s, want %s", k, got, v)
		}
	}
	// The base map is still the user's original, not the merged copy.
	if len(*base["p"].ExtraParams) != 2 || string((*base["p"].ExtraParams)["b"]) != `"base"` {
		t.Errorf("merge changed the base provider's map: %v", *base["p"].ExtraParams)
	}
}

func TestExtraParamsMergeKeepsBaseWhenOverlayHasNone(t *testing.T) {
	base := map[string]Reasoning{"p": {ExtraParams: &ExtraParamMap{"a": json.RawMessage(`true`)}}}
	out := mergeReasoning(base, map[string]Reasoning{"p": {Effort: "high"}})["p"]
	if out.ExtraParams == nil || string((*out.ExtraParams)["a"]) != "true" {
		t.Fatalf("base extra_params lost on merge: %v", out.ExtraParams)
	}
}

func TestProtectedExtraParamsAreRefusedWithWarning(t *testing.T) {
	all := map[string]Reasoning{
		"zeta": {ExtraParams: &ExtraParamMap{"stream": json.RawMessage(`false`), "top_k": json.RawMessage(`5`)}},
		"alfa": {ExtraParams: &ExtraParamMap{"model": json.RawMessage(`"x"`), "messages": json.RawMessage(`[]`), "tools": json.RawMessage(`[]`)}},
	}
	got := ExtraParamWarnings(all)
	if len(got) != 4 {
		t.Fatalf("warnings = %d, want 4 (one per protected key): %v", len(got), got)
	}
	// Provider order is sorted, so alfa's three come before zeta's one.
	if !strings.Contains(got[0], "reasoning_by_provider.alfa.extra_params") || !strings.Contains(got[0], `"messages"`) {
		t.Errorf("first warning = %q, want alfa.messages", got[0])
	}
	for _, w := range got {
		if !strings.Contains(w, "cannot be overridden") || !strings.Contains(w, "ignored") {
			t.Errorf("warning is not plain: %q", w)
		}
		if strings.Contains(w, "top_k") {
			t.Errorf("an allowed key was warned about: %q", w)
		}
	}
}

func TestAllowedExtraParamsGiveNoWarning(t *testing.T) {
	all := map[string]Reasoning{"p": {ExtraParams: &ExtraParamMap{"top_k": json.RawMessage(`5`)}}}
	if got := ExtraParamWarnings(all); got != nil {
		t.Fatalf("warnings = %v, want none", got)
	}
}

func TestIsProtectedExtraParamIsExact(t *testing.T) {
	for _, k := range []string{"model", "messages", "tools", "stream"} {
		if !IsProtectedExtraParam(k) {
			t.Errorf("%s should be protected", k)
		}
	}
	for _, k := range []string{"Model", "stream_options", "temperature", "tool_choice"} {
		if IsProtectedExtraParam(k) {
			t.Errorf("%s should not be protected", k)
		}
	}
}
