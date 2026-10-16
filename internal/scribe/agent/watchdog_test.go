package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// patientHost answers an approval only after a while, the way a person does.
type patientHost struct{ wait time.Duration }

func (h patientHost) Approve(context.Context, tools.Approval) tools.Decision {
	time.Sleep(h.wait)
	return tools.AllowOnce
}
func (h patientHost) Ask(context.Context, tools.Question) (string, error) {
	time.Sleep(h.wait)
	return "yes", nil
}
func (h patientHost) Progress(string, string) {}

// approvingTool asks for approval and then returns, so it only finishes when
// the host answers.
func approvingTool() tools.Tool {
	return fnTool{name: "approve", run: func(ctx context.Context, env *tools.Env, _ json.RawMessage) tools.Result {
		if env.Host.Approve(ctx, tools.Approval{Tool: "approve", Kind: "test", Title: "t"}) == tools.Deny {
			return tools.Errorf("denied")
		}
		return tools.Result{Output: "approved"}
	}}
}

// The tool watchdog must not expire while the tool waits on a person: the
// approval takes four times the tool timeout here and the call still succeeds.
func TestToolWatchdogPausesWhileApprovalPending(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(approvingTool())
	a, _ := rsAgent(t, &scriptProv{}, reg, func(o *Options) {
		o.ToolTimeout = 150 * time.Millisecond
		o.Env.Host = patientHost{wait: 600 * time.Millisecond}
	})
	res := a.execTool(context.Background(), toolCall("c1", "approve"))
	if res.IsError || res.Output != "approved" {
		t.Fatalf("approval wait counted against the tool timeout: %+v", res)
	}
}

// A tool that is not waiting on anyone still times out.
func TestToolWatchdogStillFiresWithoutAWait(t *testing.T) {
	reg := tools.NewRegistry()
	var n atomic.Int32
	reg.Register(fnTool{name: "busy", run: func(ctx context.Context, _ *tools.Env, _ json.RawMessage) tools.Result {
		n.Add(1)
		time.Sleep(3 * time.Second)
		return tools.Result{Output: "late"}
	}})
	a, _ := rsAgent(t, &scriptProv{}, reg, func(o *Options) { o.ToolTimeout = 150 * time.Millisecond })
	res := a.execTool(context.Background(), toolCall("c1", "busy"))
	if !res.IsError || !strings.Contains(res.Output, "did not finish") {
		t.Fatalf("busy tool was not timed out: %+v", res)
	}
}

func toolCall(id, name string) llm.Part {
	return llm.Part{Type: llm.PartToolCall, ToolCallID: id, ToolName: name, Args: json.RawMessage(`{}`)}
}
