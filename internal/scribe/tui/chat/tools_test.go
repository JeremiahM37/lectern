package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func m(kv ...any) map[string]any {
	out := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func TestReadArgsRangeSymbolAndFull(t *testing.T) {
	v := &View{}
	cases := []struct {
		args map[string]any
		want string
	}{
		{m("path", "a.go", "lines", "10-20"), "a.go (L10-20)"},
		{m("path", "a.go", "lines", "120-"), "a.go (L120-)"},
		{m("path", "a.go"), "a.go (full)"},
		{m("path", "a.go", "symbol", "Parse"), "Parse in a.go"},
		{m("paths", []any{"a.go", "b.go"}), "2 files"},
		{m("path", "a.go", "offset", 5.0, "limit", 3.0), "a.go (L5-7)"},
	}
	for _, c := range cases {
		if got := readArgs(v, c.args); got != c.want {
			t.Errorf("readArgs(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestDispatchArgsFormat(t *testing.T) {
	one := m("tasks", []any{m("role", "explore", "task", "find where sessions are saved")})
	if got := dispatchArgs(nil, one); got != "[explore] find where sessions are saved" {
		t.Errorf("single dispatch = %q", got)
	}
	long := strings.Repeat("x", 80)
	one = m("tasks", []any{m("role", "code", "task", long)})
	if got := dispatchArgs(nil, one); got != "[code] "+strings.Repeat("x", 42)+"..." {
		t.Errorf("long task not cut to 45: %q", got)
	}
	multi := m("tasks", []any{m("role", "explore", "task", "map it"), m("role", "code", "task", "fix it")})
	if got := dispatchArgs(nil, multi); got != "[explore/code] map it" {
		t.Errorf("multi dispatch = %q", got)
	}
}

func TestMemoryArgsPerAction(t *testing.T) {
	cases := []struct {
		args map[string]any
		want string
	}{
		{m("action", "save", "category", "decision", "title", "Use WAL"), "[decision] Use WAL"},
		{m("action", "search", "query", "sessions"), "sessions"},
		{m("action", "list", "category", "bug", "scope", "project"), "bug project"},
		{m("action", "delete", "id", "0123456789abcdef"), "01234567"},
		{m("action", "pin", "id", "abc"), "abc"},
	}
	for _, c := range cases {
		if got := memoryArgs(nil, c.args); got != c.want {
			t.Errorf("memoryArgs(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestGitAndSkillsArgs(t *testing.T) {
	if got := gitArgs(nil, m("action", "commit", "message", "fix the parser\n\nbody")); got != "fix the parser" {
		t.Errorf("commit = %q", got)
	}
	if got := gitArgs(nil, m("action", "log", "args", []any{"-n", "5"})); got != "log last 5" {
		t.Errorf("log = %q", got)
	}
	if got := gitArgs(nil, m("action", "diff", "staged", true)); got != "diff staged" {
		t.Errorf("diff = %q", got)
	}
	if got := gitArgs(nil, m("action", "stash")); got != "stash push" {
		t.Errorf("stash = %q", got)
	}
	if got := gitArgs(nil, m("action", "branch")); got != "branch list" {
		t.Errorf("branch = %q", got)
	}
	if got := skillsArgs(nil, m("action", "load", "name", "review")); got != "load: review" {
		t.Errorf("skills load = %q", got)
	}
}

func TestRenameAndProjectArgs(t *testing.T) {
	if got := renameSymbolArgs(nil, m("symbol", "old", "new_name", "new")); got != "old → new" {
		t.Errorf("rename_symbol = %q", got)
	}
	if got := moveSymbolArgs(nil, m("symbol", "parseConfig", "to", "cfg.go")); got != "parseConfig → cfg.go" {
		t.Errorf("move_symbol = %q", got)
	}
	if got := renameFileArgs(nil, m("from", "a.go", "to", "b.go")); got != "a.go → b.go" {
		t.Errorf("rename_file = %q", got)
	}
	if got := projectArgs(nil, m("action", "test", "target", "./internal/x")); got != "test ./internal/x" {
		t.Errorf("project = %q", got)
	}
}

func TestInlineCodeShowsAsCodeExecution(t *testing.T) {
	rt, code, ok := inlineCode("node -e 'console.log(1)'")
	if !ok || rt != "node" || code != "'console.log(1)'" {
		t.Fatalf("node -e = %q %q %v", rt, code, ok)
	}
	if got := codeRowArgs(rt, code); got != "node: console.log(1)" {
		t.Errorf("codeRowArgs = %q", got)
	}
	if rt, _, ok := inlineCode(`/usr/bin/python3 -c 'print(1)\nprint(2)'`); !ok || rt != "python3" {
		t.Errorf("python3 -c through a path = %q %v", rt, ok)
	}
	if got := codeRowArgs("python3", `'print(1)\nprint(2)'`); got != "python3: print(1)" {
		t.Errorf("first line split on literal \\n = %q", got)
	}
	for _, cmd := range []string{"ls -la", "node server.js", "python3 -c"} {
		if _, _, ok := inlineCode(cmd); ok {
			t.Errorf("%q must not show as code execution", cmd)
		}
	}
}

func TestLocationBadge(t *testing.T) {
	work := t.TempDir()
	if got := locationBadge(work, filepath.Join(work, "a.go"), "b.go"); got != "" {
		t.Errorf("inside paths got badge %q", got)
	}
	if got := locationBadge(work, "/definitely/not/here/x.go"); got != "outside" {
		t.Errorf("outside = %q", got)
	}
	if got := locationBadge(work, filepath.Join(os.TempDir(), "scratch.txt")); got != "tmp" {
		t.Errorf("tmp = %q", got)
	}
	if cfg, err := os.UserConfigDir(); err == nil && cfg != os.TempDir() {
		if got := locationBadge(work, filepath.Join(cfg, "tool", "x.toml")); got != "config" {
			t.Errorf("config = %q", got)
		}
	}
}

func TestOutsideReadsShowLastTwoSegments(t *testing.T) {
	v := &View{opt: Options{Workdir: "/work/proj"}}
	if got := v.outsideTreePath("/opt/lib/deep/x.go"); got != "deep/x.go" {
		t.Errorf("outside tree path = %q", got)
	}
	if got := v.outsideTreePath("/work/proj/src/a.go"); got != "src/a.go" {
		t.Errorf("inside tree path = %q", got)
	}
}

func TestResultSuffixFallsBackToFirstLine(t *testing.T) {
	v := &View{}
	c := &Call{Tool: "shell", Status: "done", Result: &tools.Result{Output: "first line\nsecond"}}
	if got := v.resultSuffix(c); got != "first line" {
		t.Errorf("first-line fallback = %q", got)
	}
	c.Result.Summary = "exit 0"
	if got := v.resultSuffix(c); got != "exit 0" {
		t.Errorf("tool summary should win, got %q", got)
	}
}

func TestEditSuccessRowShowsOwnOutput(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	tool(v, "e1", "edit_file", map[string]any{"path": "a.go", "old_string": "x", "new_string": "y"}, c)
	result(v, "e1", "edit_file", tools.Result{Output: "Wrote 3 lines to a.go"}, time.Second, c)
	finish(v, c, time.Second)
	out := plain(v)
	if !strings.Contains(out, "Wrote 3 lines to a.go") {
		t.Fatalf("edit row should show its first output line:\n%s", out)
	}
	if strings.Contains(out, "Edited") {
		t.Errorf("edit row still shows the verb:\n%s", out)
	}
	checkShape(t, v)
}

func TestFinishedRowHasNoDuration(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	tool(v, "g1", "grep", map[string]any{"pattern": "TODO"}, c)
	result(v, "g1", "grep", tools.Result{Output: "a.go:1: TODO", Summary: "1 match"}, 5*time.Second, c)
	finish(v, c, time.Second)
	out := plain(v)
	if strings.Contains(out, "· 5s") {
		t.Errorf("finished row still shows its duration:\n%s", out)
	}
	if !strings.Contains(out, "→ 1 match") {
		t.Errorf("summary missing:\n%s", out)
	}
}

func TestCategoryAndBackendTags(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	tool(v, "w1", "web_search", map[string]any{"query": "go generics"}, c)
	result(v, "w1", "web_search", tools.Result{Output: "3 results", Meta: map[string]any{"backend": "brave"}}, time.Second, c)
	finish(v, c, time.Second)
	out := plain(v)
	for _, want := range []string{"[web]", "[brave]"} {
		if !strings.Contains(out, want) {
			t.Errorf("row missing %s:\n%s", want, out)
		}
	}
}

func TestShellFullOutputAndResultHeader(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	var lines []string
	for i := 1; i <= 25; i++ {
		lines = append(lines, "line-"+string(rune('a'+i-1)))
	}
	tool(v, "s1", "shell", map[string]any{"command": "make"}, c)
	result(v, "s1", "shell", tools.Result{Output: strings.Join(lines, "\n")}, time.Second, c)
	finish(v, c, time.Second)
	v.ToggleExpand()
	out := plain(v)
	if !strings.Contains(out, "╰─ result ── click to collapse") {
		t.Fatalf("expanded result header missing:\n%s", out)
	}
	if !strings.Contains(out, "line-y") {
		t.Errorf("finished shell output capped instead of full:\n%s", out)
	}
}

func TestFinishedDispatchCollapsesUntilExpanded(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	tool(v, "d1", "dispatch", map[string]any{"tasks": []any{map[string]any{"task": "map it", "role": "explore"}}}, c)
	v.Apply(agent.Event{Kind: agent.EvSubagent, CallID: "d1", At: at(c), Agent: &agent.AgentInfo{
		ID: "a1", Task: "map it", Role: "explore", Status: "done", Model: "anthropic/claude-haiku-4-5",
	}})
	result(v, "d1", "dispatch", tools.Result{Output: "ok"}, 2*time.Second, c)
	finish(v, c, time.Second)
	out := plain(v)
	if strings.Contains(out, "a1 explore") {
		t.Errorf("finished agent child still listed:\n%s", out)
	}
	if !strings.Contains(out, "1/1 agents") {
		t.Errorf("dispatch done suffix missing:\n%s", out)
	}
	v.ToggleExpand()
	if !strings.Contains(plain(v), "a1 explore") {
		t.Errorf("expanded dispatch should list its agents")
	}
}

func TestRunningPlanAwaitsReview(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	tool(v, "p1", "plan", map[string]any{"title": "Ship it"}, c)
	if !strings.Contains(plain(v), "◎ Awaiting review — select below") {
		t.Errorf("running plan should await review:\n%s", plain(v))
	}
}

func TestFinishedPlanNoLongerAwaits(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	tool(v, "p1", "plan", map[string]any{"title": "Ship it"}, c)
	result(v, "p1", "plan", tools.Result{Output: "planned"}, time.Second, c)
	finish(v, c, time.Second)
	if strings.Contains(plain(v), "Awaiting review") {
		t.Errorf("finished plan still awaits review")
	}
}
