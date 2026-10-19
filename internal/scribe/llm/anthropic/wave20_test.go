package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func boolp(b bool) *bool { return &b }

// Anthropic reads the session id from metadata.user_id. Without a session the
// body has no metadata at all.
func TestSessionIDSentAsMetadataUserID(t *testing.T) {
	body := bodyFor(t, llm.Request{Model: "claude-opus-5-5", SessionID: "chat-1234"})
	md, ok := body["metadata"].(map[string]any)
	if !ok || md["user_id"] != "chat-1234" {
		t.Fatalf("metadata = %v", body["metadata"])
	}
	body = bodyFor(t, llm.Request{Model: "claude-opus-5-5"})
	if _, ok := body["metadata"]; ok {
		t.Fatalf("metadata sent without a session: %v", body["metadata"])
	}
}

func TestSpeedFastOnlyOnFastModels(t *testing.T) {
	for _, m := range []string{"claude-opus-5", "claude-opus-5-5", "claude-opus-4-8"} {
		body := decodeBody(t, llm.Request{Model: m, Speed: "fast"})
		if body["speed"] != "fast" {
			t.Errorf("%s: speed = %v, want fast", m, body["speed"])
		}
	}
	// Opus 4.7 returns an error for speed fast, and other models have no
	// fast mode. Both must send the request unchanged.
	for _, m := range []string{"claude-opus-4-7", "claude-sonnet-5-5", "claude-haiku-5-5"} {
		body := decodeBody(t, llm.Request{Model: m, Speed: "fast"})
		if _, ok := body["speed"]; ok {
			t.Errorf("%s: speed sent on a model without fast mode", m)
		}
	}
}

func TestSpeedUnsetOrStandardSendsNothing(t *testing.T) {
	for _, s := range []string{"", "standard"} {
		body := decodeBody(t, llm.Request{Model: "claude-opus-5-5", Speed: s})
		if _, ok := body["speed"]; ok {
			t.Errorf("speed %q sent", s)
		}
	}
}

// The fast-mode beta flag goes on the wire only with speed fast on a model
// that takes it.
func TestFastModeBetaHeader(t *testing.T) {
	p, c := captureServer(t)
	mustStream(t, p, llm.Request{Model: "claude-opus-5-5", Speed: "fast", Messages: []llm.Message{llm.UserText("x")}})
	c.await(t)
	if got := c.headers.Get("anthropic-beta"); got != fastModeBeta {
		t.Fatalf("anthropic-beta = %q, want %q", got, fastModeBeta)
	}

	p, c = captureServer(t)
	mustStream(t, p, llm.Request{Model: "claude-opus-4-7", Speed: "fast", Messages: []llm.Message{llm.UserText("x")}})
	c.await(t)
	if got := c.headers.Get("anthropic-beta"); got != "" {
		t.Fatalf("beta header sent on unsupported model: %q", got)
	}
}

// A beta flag already configured on the provider must survive, and the fast
// flag must not be repeated.
func TestBetaHeaderMergesWithExisting(t *testing.T) {
	p, c := captureServer(t, func(o *Options) {
		o.ExtraHeaders = map[string]string{"anthropic-beta": "prompt-caching-x, " + fastModeBeta}
	})
	mustStream(t, p, llm.Request{Model: "claude-opus-5-5", Speed: "fast", Messages: []llm.Message{llm.UserText("x")}})
	c.await(t)
	if got := c.headers.Get("anthropic-beta"); got != "prompt-caching-x,"+fastModeBeta {
		t.Fatalf("anthropic-beta = %q", got)
	}
}

// Tool streaming is on unless the setting turns it off: nil and an explicit
// true send eager_input_streaming, an explicit false leaves the tool unchanged.
func TestToolStreamingDefaultsOnAndOffLeavesToolsUnchanged(t *testing.T) {
	tools := []llm.ToolSpec{{Name: "read", Schema: []byte(`{"type":"object"}`)}}
	for _, c := range []struct {
		ts   *bool
		want bool
	}{{nil, true}, {boolp(true), true}, {boolp(false), false}} {
		body := decodeBody(t, llm.Request{Model: "claude-opus-5-5", Tools: tools, ToolStreaming: c.ts})
		tool := body["tools"].([]any)[0].(map[string]any)
		_, got := tool["eager_input_streaming"]
		if got != c.want {
			t.Errorf("ToolStreaming=%v: eager_input_streaming sent = %v, want %v", c.ts, got, c.want)
		}
	}
}

// Fine-grained tool streaming is a per-tool field with no beta header, so the
// test checks the field and that no legacy header appears.
func TestToolStreamingOnSetsEagerInputPerTool(t *testing.T) {
	tools := []llm.ToolSpec{
		{Name: "read", Schema: []byte(`{"type":"object"}`)},
		{Name: "write", Schema: []byte(`{"type":"object"}`)},
	}
	p, c := captureServer(t)
	mustStream(t, p, llm.Request{Model: "claude-opus-5-5", Tools: tools, ToolStreaming: boolp(true), Messages: []llm.Message{llm.UserText("x")}})
	c.await(t)
	for _, raw := range c.body["tools"].([]any) {
		tool := raw.(map[string]any)
		if tool["eager_input_streaming"] != true {
			t.Errorf("%v: eager_input_streaming = %v", tool["name"], tool["eager_input_streaming"])
		}
	}
	if got := c.headers.Get("anthropic-beta"); got != "" {
		t.Errorf("tool streaming added a beta header: %q", got)
	}
}

// Sending reasoning is the default and keeps the signed thinking blocks the
// API needs for tool use on adaptive models.
func TestSendReasoningDefaultKeepsThinking(t *testing.T) {
	msgs := reasoningHistory()
	for _, sr := range []*bool{nil, boolp(true)} {
		body := decodeBody(t, llm.Request{Model: "claude-opus-5-5", Messages: msgs, SendReasoning: sr})
		if !hasBlockType(body, "thinking") {
			t.Errorf("thinking dropped with SendReasoning=%v", sr)
		}
	}
}

func TestSendReasoningFalseDropsThinking(t *testing.T) {
	body := decodeBody(t, llm.Request{Model: "claude-opus-5-5", Messages: reasoningHistory(), SendReasoning: boolp(false)})
	if hasBlockType(body, "thinking") {
		t.Fatal("thinking still sent with SendReasoning=false")
	}
	// The user turns on either side of a thinking-only assistant turn must
	// merge into one, because same-role neighbours are rejected.
	msgs := body["messages"].([]any)
	for i := 1; i < len(msgs); i++ {
		a := msgs[i-1].(map[string]any)["role"]
		b := msgs[i].(map[string]any)["role"]
		if a == b {
			t.Errorf("messages %d and %d both have role %v", i-1, i, a)
		}
	}
}

// reasoningHistory is user, assistant with signed thinking then text, user.
func reasoningHistory() []llm.Message {
	return []llm.Message{
		llm.UserText("first"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{
			{Type: llm.PartReasoning, Text: "hmm", Signature: "sig"},
			{Type: llm.PartText, Text: "answer"},
		}},
		llm.UserText("second"),
	}
}

func hasBlockType(body map[string]any, typ string) bool {
	for _, m := range body["messages"].([]any) {
		for _, b := range m.(map[string]any)["content"].([]any) {
			if b.(map[string]any)["type"] == typ {
				return true
			}
		}
	}
	return false
}

func TestEffortClampedPerModel(t *testing.T) {
	cases := []struct {
		model, effort, want string
	}{
		{"claude-opus-5-5", "xhigh", "xhigh"}, // all five levels
		{"claude-opus-5-5", "max", "max"},
		{"claude-opus-4-6", "xhigh", "high"}, // no xhigh: nearest below
		{"claude-opus-4-6", "max", "max"},
		{"claude-sonnet-4-6", "xhigh", "high"},
		{"claude-opus-4-5", "max", "high"}, // only low, medium, high
		{"claude-opus-4-5", "xhigh", "high"},
		{"claude-opus-4-5-20251101", "low", "low"},
		{"claude-sonnet-4-5", "high", ""},  // takes no effort control
		{"claude-unknown-9", "max", "max"}, // unknown ids are not clamped
		{"claude-opus-5-5", "", ""},
	}
	for _, c := range cases {
		if got := clampEffort(c.model, c.effort); got != c.want {
			t.Errorf("clampEffort(%s, %q) = %q, want %q", c.model, c.effort, got, c.want)
		}
	}
}

// The adaptive path sends the clamped level on the wire. Every adaptive model
// takes xhigh, so this checks that the clamp does not alter a supported level.
func TestEffortClampReachesOutputConfig(t *testing.T) {
	body := decodeBody(t, llm.Request{Model: "claude-opus-5-5", Reasoning: "xhigh"})
	oc := body["output_config"].(map[string]any)
	if oc["effort"] != "xhigh" {
		t.Fatalf("effort = %v, want xhigh on Opus 5.5", oc["effort"])
	}
}

// Budget-style models map effort to a token budget and are not clamped, so
// their thinking budget is the same as before the clamp existed.
func TestBudgetModelsKeepEffortBudgetMapping(t *testing.T) {
	body := decodeBody(t, llm.Request{Model: "claude-sonnet-4-5", Reasoning: "high"})
	th := body["thinking"].(map[string]any)
	if th["budget_tokens"] != float64(thinkingBudgets["high"]) {
		t.Fatalf("budget = %v, want %d", th["budget_tokens"], thinkingBudgets["high"])
	}
}

// Defaults are unchanged: no new field appears on a plain request.
func TestPlainRequestHasNoNewFields(t *testing.T) {
	body := decodeBody(t, llm.Request{Model: "claude-opus-5-5", Messages: []llm.Message{llm.UserText("x")}})
	for _, k := range []string{"metadata", "speed"} {
		if _, ok := body[k]; ok {
			t.Errorf("%s present on a plain request", k)
		}
	}
}

// With send-reasoning off, the turn in progress keeps its thinking: the API
// wants it sent back with the tool results of that turn.
func TestSendReasoningFalseKeepsTheCurrentTurnsThinking(t *testing.T) {
	msgs := append(reasoningHistory(),
		llm.UserText("next task"),
		llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
			{Type: llm.PartReasoning, Text: "current-turn thought", Signature: "sig2"},
			{Type: llm.PartToolCall, ToolCallID: "t9", ToolName: "read", Args: []byte(`{}`)},
		}},
		llm.Message{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartToolResult, ToolCallID: "t9", ToolName: "read", Result: "ok"}}},
	)
	raw, err := json.Marshal(decodeBody(t, llm.Request{Model: "claude-opus-5-5", Messages: msgs, SendReasoning: boolp(false)}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "current-turn thought") {
		t.Fatalf("current turn's thinking was dropped: %s", raw)
	}
}
