package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// finalResponseName is the model-controlled end-of-turn tool. A turn that
// used two or more tools ends with a call to it, so the renderer can tell
// where the answer starts (spec 03 §7.2, 03 §14).
const finalResponseName = "final_response"

// finalTool carries the final answer in its argument. The loop reads the
// answer and ends the turn once the call succeeds (see finalAnswer). It is
// registered on the main agent only: subagents have their own registries.
//
// It is not read-only: restricted modes (plan, architect, socratic,
// challenge) refuse it through the usual mode gate, and the answer is given
// as text there, as the spec's restricted sets do not include it.
type finalTool struct{}

func (finalTool) ReadOnly() bool { return false }

func (finalTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: finalResponseName,
		Description: "Deliver your final answer and end the turn. Use it as the last tool call of a turn that used tools. " +
			"Put the complete answer in answer, written the way you write any reply to the user. " +
			"Nothing after this call runs, so do not call it while work is still left.",
		Schema: tools.Schema(map[string]any{
			"answer": tools.Str("The complete final answer for the user. Plain text, in the same voice as your other replies."),
		}, "answer"),
	}
}

func (finalTool) Run(_ context.Context, _ *tools.Env, args json.RawMessage) tools.Result {
	var in struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(args, &in); err != nil || strings.TrimSpace(in.Answer) == "" {
		// Refused, so the turn goes on and the model can send the answer.
		return tools.Errorf("final_response needs the answer text in answer. If the task is not finished, keep working; otherwise call it again with the full answer.")
	}
	return tools.Result{Output: "Final answer delivered. The turn ends here.", Summary: "answer delivered"}
}

// finalAnswer returns the answer of the last final_response call in calls
// that succeeded. A refused call (empty answer, restricted mode, plan
// execution) returns false, so the turn carries on. results lines up with
// calls.
func finalAnswer(calls, results []llm.Part) (string, bool) {
	answer, found := "", false
	for i, c := range calls {
		if c.ToolName != finalResponseName || i >= len(results) || results[i].IsError {
			continue
		}
		var in struct {
			Answer string `json:"answer"`
		}
		if json.Unmarshal(c.Args, &in) != nil || strings.TrimSpace(in.Answer) == "" {
			continue
		}
		answer, found = in.Answer, true
	}
	return answer, found
}
