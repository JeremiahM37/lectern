package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// failureHooks records PostToolUseFailure calls and the PostTool calls.
type failureHooks struct {
	failures []string
	posts    []string
}

func (h *failureHooks) PreTool(context.Context, string, json.RawMessage) string { return "" }
func (h *failureHooks) PostTool(_ context.Context, tool string, _ json.RawMessage, _ tools.Result) string {
	h.posts = append(h.posts, tool)
	return ""
}
func (h *failureHooks) PostToolFailure(_ context.Context, tool string, _ json.RawMessage, res tools.Result) string {
	h.failures = append(h.failures, tool+": "+res.Output)
	return "failure hook saw it"
}
func (h *failureHooks) Prompt(context.Context, string) (string, string) { return "", "" }
func (h *failureHooks) Stop(context.Context, TurnStats)                 {}

// A call refused before the tool runs (here an unknown tool) still fires the
// failure hook once, and the hook's text reaches the model.
func TestRefusedCallFiresPostToolFailureOnce(t *testing.T) {
	h := &failureHooks{}
	reg := tools.NewRegistry()
	a, _ := rsAgent(t, &scriptProv{}, reg, func(o *Options) { o.Hooks = h })
	res := a.runToolCall(context.Background(), toolCall("c1", "nope"))
	if !res.IsError {
		t.Fatalf("unknown tool should fail: %+v", res)
	}
	if len(h.failures) != 1 || !strings.HasPrefix(h.failures[0], "nope: ") {
		t.Fatalf("failure hook calls = %v, want one for nope", h.failures)
	}
	if !strings.Contains(res.Output, "failure hook saw it") {
		t.Fatalf("hook text missing from the result: %q", res.Output)
	}
	if len(h.posts) != 0 {
		t.Fatalf("PostTool should not run for a refused call: %v", h.posts)
	}
}

// A call that reaches the tool and fails reports through PostTool only, so
// the failure hook is not fired twice.
func TestToolFailureReachingTheToolFiresOnce(t *testing.T) {
	h := &failureHooks{}
	reg := tools.NewRegistry()
	reg.Register(fnTool{name: "bad", run: func(context.Context, *tools.Env, json.RawMessage) tools.Result {
		return tools.Errorf("boom")
	}})
	a, _ := rsAgent(t, &scriptProv{}, reg, func(o *Options) { o.Hooks = h })
	a.runToolCall(context.Background(), toolCall("c1", "bad"))
	if len(h.posts) != 1 {
		t.Fatalf("PostTool calls = %v", h.posts)
	}
	if len(h.failures) != 0 {
		t.Fatalf("the failure hook fired from the agent as well as PostTool: %v", h.failures)
	}
}

// Approval notes come back to the model at the top of the result. The tool
// below does what a caller does: it asks with a reason slot, and notes an
// approval that was not a denial.
func TestApprovalNoteReachesTheResult(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(fnTool{name: "guarded", run: func(ctx context.Context, env *tools.Env, _ json.RawMessage) tools.Result {
		rctx, reason := tools.WithReasonSlot(ctx)
		if env.Host.Approve(rctx, tools.Approval{Tool: "guarded", Kind: "test", Title: "t"}) == tools.Deny {
			return tools.Errorf("denied")
		}
		tools.NoteApproval(ctx, reason())
		return tools.Result{Output: "did the thing"}
	}})
	a, _ := rsAgent(t, &scriptProv{}, reg, func(o *Options) { o.Env.Host = noteHost{} })
	res := a.runToolCall(context.Background(), toolCall("c1", "guarded"))
	if res.IsError {
		t.Fatalf("approved call failed: %+v", res)
	}
	if !strings.HasPrefix(res.Output, "The user approved this with a note: only the test directory") {
		t.Fatalf("note missing from the result: %q", res.Output)
	}
	if !strings.HasSuffix(res.Output, "did the thing") {
		t.Fatalf("tool output lost: %q", res.Output)
	}
}

// noteHost approves and attaches a reason, the way a phone answer does.
type noteHost struct{}

func (noteHost) Approve(ctx context.Context, _ tools.Approval) tools.Decision {
	tools.SetApprovalReason(ctx, "only the test directory")
	return tools.AllowOnce
}
func (noteHost) Ask(context.Context, tools.Question) (string, error) { return "", nil }
func (noteHost) Progress(string, string)                             {}
