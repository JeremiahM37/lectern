package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// verifyOutcome is what the post-phase check found after code tasks changed
// files (spec 06 §9.5).
type verifyOutcome struct {
	status  string // not run, skipped, passed, passed after fix, failing
	text    string // body of the Verification section
	failing bool
	edited  []string // files changed by the fix-up agent
}

const fixTaskFormat = `The project check failed after other agents edited these files: %s

Fix only what the check output reports, with the smallest edits that make it pass. Do not refactor or touch anything else. Then stop.

Check output (end of the output):
%s`

// projectCheck runs the project tool's check action from reg. ran is false
// when the registry has no project tool; ok is false when the check failed.
func projectCheck(ctx context.Context, env *tools.Env, reg *tools.Registry) (ran, ok bool, output string) {
	if reg == nil {
		return false, false, ""
	}
	t, found := reg.Get("project")
	if !found {
		return false, false, ""
	}
	e := *env
	e.CallID = env.CallID + "/check"
	res := t.Run(ctx, &e, json.RawMessage(`{"action":"check"}`))
	return true, !res.IsError, res.Output
}

// verify runs the project check on the files code tasks changed. A failing
// check gets one short code subagent with the output and the file list; the
// check then runs again so the report can say how it ended.
func (r *dispatchRun) verify(ctx context.Context, env *tools.Env, reg *tools.Registry, changed []string) verifyOutcome {
	if ctx.Err() != nil {
		return verifyOutcome{status: "not run", text: "not run: the dispatch was cancelled"}
	}
	ran, ok, out := projectCheck(ctx, env, reg)
	if !ran {
		return verifyOutcome{status: "skipped", text: "skipped: no project tool is available, so the changed files were not checked"}
	}
	if ok {
		return verifyOutcome{status: "passed", text: "project check passed on the changed files"}
	}

	idx := r.board.add("code", "fix project check failures")
	fix := r.launch(ctx, idx, "code", PurposeFix, fmt.Sprintf(fixTaskFormat, strings.Join(changed, ", "), tailOf(out, 3000)))
	if fix.err != nil {
		return verifyOutcome{
			status:  "failing",
			failing: true,
			text:    fmt.Sprintf("project check failed and the fix agent did not finish: %v\n%s", fix.err, tailOf(out, 1500)),
			edited:  fix.edited,
		}
	}
	if ran2, ok2, out2 := projectCheck(ctx, env, reg); ran2 && ok2 {
		return verifyOutcome{
			status: "passed after fix",
			text:   "project check failed; one follow-up code agent fixed it and the check passes now",
			edited: fix.edited,
		}
	} else if !ran2 {
		return verifyOutcome{status: "failing", failing: true, text: "project check failed and is no longer available to re-run", edited: fix.edited}
	} else {
		return verifyOutcome{
			status:  "failing",
			failing: true,
			text:    "project check still fails after one fix attempt:\n" + tailOf(out2, 1500),
			edited:  fix.edited,
		}
	}
}

// tailOf returns the last n bytes of s, starting on a rune boundary.
func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	k := len(s) - n
	for k < len(s) && !utf8.RuneStart(s[k]) {
		k++
	}
	return "..." + s[k:]
}
