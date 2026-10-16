// Command sample prints a static render of a rich transcript at width 100, so
// the chat layout can be checked by eye: go run ./internal/scribe/tui/chat/cmd/sample
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/chat"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// Usage: sample [verbose] [live] [bubble] [expand] [reasoning]
//
//	verbose    original order, every row
//	live       the turn is still running (spinner header, thinking row)
//	bubble     bubble-style user messages
//	expand     Ctrl+O: tool details and reasoning expanded
//	reasoning  keep reasoning segments (they are off by default)
func main() {
	args := map[string]bool{}
	for _, a := range os.Args[1:] {
		args[a] = true
	}
	now := time.Date(2026, 10, 16, 15, 4, 0, 0, time.UTC)
	clock := now
	style := "accent"
	if args["bubble"] {
		style = "bubble"
	}
	v := chat.New(chat.Options{
		Theme:         theme.Default(),
		Workdir:       "/home/admin/projects/lectern",
		ChatStyle:     style,
		Verbose:       args["verbose"],
		ShowReasoning: args["reasoning"],
		Now:           func() time.Time { return clock },
	})
	v.SetSize(100, 64)
	ev := func(e agent.Event) { e.At = clock; v.Apply(e) }
	call := func(id, tool string, args map[string]any, res *tools.Result, d time.Duration) {
		raw, _ := json.Marshal(args)
		ev(agent.Event{Kind: agent.EvToolStart, CallID: id, Tool: tool})
		ev(agent.Event{Kind: agent.EvToolRun, CallID: id, Tool: tool, Args: raw})
		if res != nil {
			ev(agent.Event{Kind: agent.EvToolResult, CallID: id, Tool: tool, Result: res, Duration: d})
		}
	}

	v.AddUser("Why do sessions vanish after a restart? Please check the store and fix it.", 0, now, false)
	ev(agent.Event{Kind: agent.EvTurnStart, Prompt: "why do sessions vanish"})
	ev(agent.Event{Kind: agent.EvReasoning, Text: "**Plan**: look at the session store first.\nThen check where it is written.\nFinally confirm with a test."})
	if args["expand"] {
		v.ToggleExpand()
	}
	ev(agent.Event{Kind: agent.EvText, Text: "I'll look at how sessions are stored before changing anything."})

	for i, p := range []string{"internal/scribe/session/store.go", "internal/scribe/session/paths.go", "internal/scribe/session/store_test.go"} {
		call(fmt.Sprintf("r%d", i), "read", map[string]any{"path": "/home/admin/projects/lectern/" + p},
			&tools.Result{Output: "package session", Summary: "120 lines"}, time.Second)
	}
	call("g1", "grep", map[string]any{"pattern": "os.TempDir"}, &tools.Result{Output: "x", Summary: "2 hits in 2 files"}, 0)
	call("g2", "grep", map[string]any{"pattern": "sessionDir"}, &tools.Result{Output: "x", Summary: "5 hits in 3 files"}, 0)
	diff := "--- a/internal/scribe/session/paths.go\n+++ b/internal/scribe/session/paths.go\n@@ -10,4 +10,5 @@\n func Dir() string {\n-\treturn os.TempDir()\n+\thome, _ := os.UserHomeDir()\n+\treturn filepath.Join(home, \".scribe\", \"sessions\")\n }\n"
	call("e1", "edit_file", map[string]any{"path": "/home/admin/projects/lectern/internal/scribe/session/paths.go"},
		&tools.Result{Output: "ok", Summary: "+2 -1", Diff: diff, Files: []string{"internal/scribe/session/paths.go"}}, 0)
	call("e2", "edit_file", map[string]any{"path": "/home/admin/projects/lectern/internal/scribe/session/paths.go"},
		&tools.Result{Output: "no match", IsError: true}, 0)
	call("s1", "shell", map[string]any{"command": "go test ./internal/scribe/session/..."},
		&tools.Result{Output: "ok  session", Summary: "exit 0"}, 3*time.Second)

	ev(agent.Event{Kind: agent.EvPlan, Plan: &agent.Plan{Title: "Fix session location", Steps: []agent.PlanStep{
		{ID: "1", Text: "Find where sessions are written", Status: "done"},
		{ID: "2", Text: "Move the store under the home directory", Status: "active"},
		{ID: "3", Text: "Add a regression test", Status: "pending"},
	}}})
	ev(agent.Event{Kind: agent.EvSubagent, CallID: "d1", Agent: &agent.AgentInfo{ID: "a1", Task: "check the migration notes for session paths", Role: "explore", Status: "done", Model: "anthropic/claude-haiku-4-5-20251001", Step: 4, Tokens: llm.Usage{InputTokens: 9100, OutputTokens: 3200}}})
	ev(agent.Event{Kind: agent.EvText, Text: "Sessions were written under the system temp directory, which is cleared on reboot. They now live under `~/.scribe/sessions`, and the test covers it."})
	if !args["live"] {
		ev(agent.Event{Kind: agent.EvTurnDone, Stats: &agent.TurnStats{Duration: 65 * time.Second}})
	}

	v.AddUser("thanks, also rename the flag", 1, now.Add(time.Minute), false)
	ev(agent.Event{Kind: agent.EvTurnStart, Prompt: "rename the flag"})
	ev(agent.Event{Kind: agent.EvRetry, Category: llm.CatRateLimited, Attempt: 2, MaxAttempts: 3, Delay: 4 * time.Second})
	ev(agent.Event{Kind: agent.EvError, Category: llm.CatOverloaded, Text: "Overloaded (529) https://status.example.com/incident"})
	if args["live"] {
		clock = clock.Add(13 * time.Second)
		v.Tick(clock)
		fmt.Println(v.View())
		return
	}
	ev(agent.Event{Kind: agent.EvTurnDone, Stats: &agent.TurnStats{Duration: 9 * time.Second}})
	clock = clock.Add(2 * time.Second) // let the newest system row finish typing
	v.Tick(clock)
	fmt.Println(v.View())
}
