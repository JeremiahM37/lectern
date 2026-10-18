package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// contextEditsOf returns the edits in a body's context_management, or nil
// when the key is absent.
func contextEditsOf(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["context_management"]
	if !ok {
		return nil
	}
	cm, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("context_management is %T, want object", raw)
	}
	var out []map[string]any
	for _, e := range cm["edits"].([]any) {
		out = append(out, e.(map[string]any))
	}
	return out
}

// thinkingReq is a budget-style request with thinking on.
var thinkingReq = llm.Request{Model: "claude-sonnet-4-5", ThinkingBudget: 4000}

func TestContextManagementOffByDefault(t *testing.T) {
	body := bodyFor(t, thinkingReq)
	if _, ok := body["context_management"]; ok {
		t.Fatalf("context_management sent with both edits off: %v", body["context_management"])
	}
	plain := bodyFor(t, llm.Request{Model: "claude-sonnet-4-5", ContextWindow: 200000})
	if _, ok := plain["context_management"]; ok {
		t.Fatalf("context_management sent without a request for it: %v", plain["context_management"])
	}
	if got := betaFeatures(thinkingReq); len(got) != 0 {
		t.Fatalf("beta flags with both edits off: %v", got)
	}
}

func TestClearThinkingKeepsAllWithThinkingOn(t *testing.T) {
	req := thinkingReq
	req.ClearThinking = true
	edits := contextEditsOf(t, bodyFor(t, req))
	if len(edits) != 1 {
		t.Fatalf("edits = %v, want one clear_thinking edit", edits)
	}
	if edits[0]["type"] != "clear_thinking_20251015" || edits[0]["keep"] != "all" {
		t.Fatalf("clear_thinking edit = %v, want keep all", edits[0])
	}
	if got := betaFeatures(req); len(got) != 1 || got[0] != contextManagementBeta {
		t.Fatalf("beta flags = %v, want %s", got, contextManagementBeta)
	}
}

func TestClearThinkingNeedsThinking(t *testing.T) {
	// No Reasoning and no budget: the request has no thinking block, so the
	// preservation edit has nothing to keep and is not sent.
	req := llm.Request{Model: "claude-sonnet-4-5", ClearThinking: true}
	body := bodyFor(t, req)
	if _, ok := body["thinking"]; ok {
		t.Fatalf("test premise broken: thinking sent: %v", body["thinking"])
	}
	if _, ok := body["context_management"]; ok {
		t.Fatalf("clear_thinking sent with thinking off: %v", body["context_management"])
	}
	if got := betaFeatures(req); len(got) != 0 {
		t.Fatalf("beta flag sent with no edit in the body: %v", got)
	}
}

func TestClearToolUsesThresholdsScaleWithWindow(t *testing.T) {
	cases := []struct {
		name    string
		window  int
		trigger float64
		atLeast float64
	}{
		{"no window uses the floors", 0, 120000, 40000},
		{"200k window", 200000, 130000, 40000},
		{"1M window scales both", 1000000, 650000, 200000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := llm.Request{Model: "claude-sonnet-4-5", ClearToolUses: true, ContextWindow: c.window}
			edits := contextEditsOf(t, bodyFor(t, req))
			if len(edits) != 1 {
				t.Fatalf("edits = %v, want one clear_tool_uses edit", edits)
			}
			e := edits[0]
			if e["type"] != "clear_tool_uses_20250919" {
				t.Fatalf("type = %v", e["type"])
			}
			if got := e["trigger"].(map[string]any)["value"]; got != c.trigger {
				t.Errorf("trigger = %v, want %v", got, c.trigger)
			}
			if got := e["clear_at_least"].(map[string]any)["value"]; got != c.atLeast {
				t.Errorf("clear_at_least = %v, want %v", got, c.atLeast)
			}
			keep := e["keep"].(map[string]any)
			if keep["type"] != "tool_uses" || keep["value"] != float64(5) {
				t.Errorf("keep = %v, want the last 5 tool uses", keep)
			}
		})
	}
}

func TestBothEditsInFixedOrder(t *testing.T) {
	req := thinkingReq
	req.ClearThinking = true
	req.ClearToolUses = true
	edits := contextEditsOf(t, bodyFor(t, req))
	if len(edits) != 2 {
		t.Fatalf("edits = %v, want two", edits)
	}
	if edits[0]["type"] != "clear_thinking_20251015" || edits[1]["type"] != "clear_tool_uses_20250919" {
		t.Fatalf("order = %v, %v; want clear_thinking then clear_tool_uses", edits[0]["type"], edits[1]["type"])
	}
}

// w27ccCaptured is what a test server saw on one request.
type w27ccCaptured struct {
	beta string
	body map[string]any
}

// streamCapture sends req through a provider whose server records the beta
// header and body. The response is an error, which is enough to inspect the
// request.
func streamCapture(t *testing.T, req llm.Request, opts ...func(*Options)) w27ccCaptured {
	t.Helper()
	var got w27ccCaptured
	handler := func(w http.ResponseWriter, r *http.Request) {
		got.beta = r.Header.Get("anthropic-beta")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got.body)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"stop"}}`)
	}
	p := newTestServer(t, handler, opts...)
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	return got
}

func TestStreamSendsContextBetaWhenEditSent(t *testing.T) {
	req := thinkingReq
	req.ClearThinking = true
	got := streamCapture(t, req)
	if !strings.Contains(got.beta, contextManagementBeta) {
		t.Fatalf("anthropic-beta = %q, want %s", got.beta, contextManagementBeta)
	}
	if len(contextEditsOf(t, got.body)) != 1 {
		t.Fatalf("body edits = %v", got.body["context_management"])
	}
}

func TestStreamOffSendsNoContextBetaOrBody(t *testing.T) {
	got := streamCapture(t, thinkingReq)
	if strings.Contains(got.beta, contextManagementBeta) {
		t.Fatalf("anthropic-beta = %q with both edits off", got.beta)
	}
	if _, ok := got.body["context_management"]; ok {
		t.Fatalf("context_management sent with both edits off")
	}
}

func TestStreamKeepsExistingBetaFlags(t *testing.T) {
	req := thinkingReq
	req.ClearToolUses = true
	got := streamCapture(t, req, func(o *Options) {
		o.ExtraHeaders = map[string]string{"anthropic-beta": "some-other-2025-01-01"}
	})
	if !strings.Contains(got.beta, "some-other-2025-01-01") || !strings.Contains(got.beta, contextManagementBeta) {
		t.Fatalf("anthropic-beta = %q, want both the provider flag and the context flag", got.beta)
	}
}
