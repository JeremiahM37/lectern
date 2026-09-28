package helpers_test

// Parity tests for the agent-hook helpers: each case runs the Python script a
// target without lectern still runs and the Go helper that replaces it, in
// the same fixture, and compares what a caller can observe — stdout, the
// exit status, stderr where an agent reads it, the requests sent to Lectern
// and the files left behind.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/helpers/helperstest"
	"github.com/JeremiahM37/lectern/v2/internal/hooks"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

func TestHelperProcess(t *testing.T) { helperstest.Serve() }

// requireParity: these start real processes (python3 and the helper), so they
// run in the reviewed isolated runner like every other real-process test.
func requireParity(t *testing.T) {
	t.Helper()
	testutil.RequireIsolated(t)
	helperstest.RequirePython(t)
}

// plane is a fake Lectern that records every request and answers with reply.
type plane struct {
	*httptest.Server
	mu    sync.Mutex
	log   []string
	polls int
}

func newPlane(t *testing.T, reply func(p *plane, r *http.Request) (int, string)) *plane {
	t.Helper()
	p := &plane{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.log = append(p.log, fmt.Sprintf("%s %s auth=%q type=%q body=%q", r.Method, r.URL.Path,
			r.Header.Get("Authorization"), r.Header.Get("Content-Type"), body))
		if strings.HasSuffix(r.URL.Path, "/decision") {
			p.polls++
		}
		code, out := reply(p, r)
		p.mu.Unlock()
		w.WriteHeader(code)
		io.WriteString(w, out)
	}))
	t.Cleanup(p.Close)
	return p
}

// requests is the recorded traffic with repeated polls collapsed: how many
// times a poll loop spins depends on timing, not on the implementation.
func (p *plane) requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, l := range p.log {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

func sameRequests(t *testing.T, what string, py, got []string) {
	t.Helper()
	if strings.Join(py, "\n") != strings.Join(got, "\n") {
		t.Errorf("%s: requests differ\npython: %q\ngo:     %q", what, py, got)
	}
}

func TestApprovalHookParity(t *testing.T) {
	requireParity(t)
	dir := t.TempDir()
	script := filepath.Join(dir, ".lectern", "hook.py")
	os.MkdirAll(filepath.Dir(script), 0o755)
	if err := os.WriteFile(script, hooks.Hook, 0o755); err != nil {
		t.Fatal(err)
	}
	decide := func(status, note string, pending int) func(p *plane, r *http.Request) (int, string) {
		return func(p *plane, r *http.Request) (int, string) {
			if r.Method == "POST" {
				return 201, `{"id": 7}`
			}
			if p.polls <= pending {
				return 200, `{"status": "pending"}`
			}
			return 200, fmt.Sprintf(`{"status": %q, "note": %s}`, status, note)
		}
	}
	fixed := func(code int, body string) func(p *plane, r *http.Request) (int, string) {
		return func(*plane, *http.Request) (int, string) { return code, body }
	}
	call := `{"tool_name": "Bash", "tool_input": {"command": "rm -rf /", "z": 1.50, "n": 1e400,
		"big": 123456789012345678901234567890, "u": "caf\u00e9 \ud83d\ude00 <&>", "lone": "\udc00", "dup": 1, "dup": 2}}`
	cases := []struct {
		name  string
		stdin string
		env   []string
		reply func(p *plane, r *http.Request) (int, string)
		url   string // overrides the plane's URL
	}{
		{name: "approved after polling", stdin: call, reply: decide("approved", `""`, 1)},
		{name: "denied with a reason", stdin: call, reply: decide("denied", `"not safe, find another way"`, 0)},
		{name: "expired without a note", stdin: call, reply: decide("expired", `""`, 0)},
		{name: "denied with a falsy note", stdin: call, reply: decide("denied", `0`, 0)},
		{name: "denied with a list note", stdin: call, reply: decide("denied", `["x", 1.0, null, true]`, 0)},
		{name: "missing tool fields", stdin: `{"other": 1}`, reply: decide("approved", `""`, 0)},
		{name: "float id", stdin: call, reply: func(p *plane, r *http.Request) (int, string) {
			if r.Method == "POST" {
				return 201, `{"id": 7.0}`
			}
			return 200, `{"status": "approved"}`
		}},
		{name: "unconfigured", stdin: call, reply: decide("denied", `""`, 0), url: "-"},
		{name: "garbage on stdin", stdin: "not json", reply: decide("denied", `""`, 0)},
		{name: "a JSON array on stdin", stdin: "[1]", reply: decide("denied", `""`, 0)},
		{name: "server error", stdin: call, reply: fixed(500, "boom")},
		{name: "not JSON back", stdin: call, reply: fixed(200, "<html>")},
		{name: "no id back", stdin: call, reply: fixed(201, "{}")},
		{name: "unreachable", stdin: call, reply: fixed(200, "{}"), url: "http://127.0.0.1:1"},
		{name: "not a URL", stdin: call, reply: fixed(200, "{}"), url: "notaurl"},
		{name: "trailing slashes", stdin: call, reply: decide("approved", `""`, 0), url: "+///"},
		{name: "no time to decide", stdin: call, reply: decide("approved", `""`, 0),
			env: []string{"LECTERN_APPROVAL_TIMEOUT=0"}},
		{name: "unknown status until timeout", stdin: call, reply: decide("maybe", `""`, 0),
			env: []string{"LECTERN_APPROVAL_TIMEOUT= 1.5 "}},
		{name: "unparseable timeout", stdin: call, reply: decide("approved", `""`, 0),
			env: []string{"LECTERN_APPROVAL_TIMEOUT=soon"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := func(goHelper bool) (helperstest.Result, []string) {
				p := newPlane(t, c.reply)
				env := helperstest.Env(dir, append([]string{"LECTERN_TOKEN=tok"}, c.env...)...)
				switch {
				case c.url == "-":
				case c.url == "+///":
					env = append(env, "LECTERN_URL="+p.URL+"///")
				case c.url != "":
					env = append(env, "LECTERN_URL="+c.url)
				default:
					env = append(env, "LECTERN_URL="+p.URL)
				}
				proc := helperstest.Proc{Dir: dir, Env: env, Stdin: []byte(c.stdin)}
				if goHelper {
					return helperstest.Go(t, proc, "approval-hook"), p.requests()
				}
				return helperstest.Run(t, proc, "python3", ".lectern/hook.py"), p.requests()
			}
			py, pyReqs := run(false)
			got, goReqs := run(true)
			helperstest.Same(t, c.name, py, got)
			// Exit 2's stderr is the reason the agent is shown.
			if py.Code != 1 && py.Stderr != got.Stderr {
				t.Errorf("stderr differs\npython: %q\ngo:     %q", py.Stderr, got.Stderr)
			}
			sameRequests(t, c.name, pyReqs, goReqs)
		})
	}
}

func TestLecParity(t *testing.T) {
	requireParity(t)
	ok := func(p *plane, r *http.Request) (int, string) { return 201, `{"task_id": 12, "note_id": 34}` }
	cases := []struct {
		name  string
		args  []string
		env   string // .lectern/env; "-" for none, URL for the plane
		reply func(p *plane, r *http.Request) (int, string)
	}{
		{"task dispatched", []string{"add-task", "add tests", "write tests for health()", "--dispatch"}, "ADK_URL=URL\nADK_TOKEN=tok-abc\n", ok},
		{"task to backlog, title only", []string{"add-task", "caf\u00e9 \"quoted\""}, "ADK_URL=URL/\r\nADK_TOKEN=tok\r\n", ok},
		{"dispatch first", []string{"--dispatch", "add-task", "t", "p", "extra"}, "ADK_URL=URL\nADK_TOKEN=tok\n", ok},
		{"note", []string{"add-note", "auth uses bcrypt"}, "junk\nADK_URL=URL\n  ADK_TOKEN=a=b  \n", ok},
		{"padded keys do not count", []string{"add-note", "x"}, "ADK_URL = URL\nADK_TOKEN=tok\n", ok},
		{"no env file", []string{"add-note", "x"}, "-", ok},
		{"unknown command", []string{"remove", "x"}, "ADK_URL=URL\nADK_TOKEN=tok\n", ok},
		{"too few arguments", []string{"add-note"}, "ADK_URL=URL\nADK_TOKEN=tok\n", ok},
		{"server error", []string{"add-note", "x"}, "ADK_URL=URL\nADK_TOKEN=tok\n",
			func(*plane, *http.Request) (int, string) { return 500, "no" }},
		{"no id back", []string{"add-task", "x"}, "ADK_URL=URL\nADK_TOKEN=tok\n",
			func(*plane, *http.Request) (int, string) { return 201, `{"note_id": 1}` }},
		{"odd ids", []string{"add-task", "x"}, "ADK_URL=URL\nADK_TOKEN=tok\n",
			func(*plane, *http.Request) (int, string) { return 201, `{"task_id": 12.0}` }},
		{"unreachable", []string{"add-note", "x"}, "ADK_URL=http://127.0.0.1:1\nADK_TOKEN=tok\n", ok},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := func(goHelper bool) (helperstest.Result, []string) {
				dir := t.TempDir()
				rt := filepath.Join(dir, ".lectern")
				os.MkdirAll(rt, 0o755)
				os.WriteFile(filepath.Join(rt, "lec.py"), hooks.ADK, 0o755)
				p := newPlane(t, c.reply)
				if c.env != "-" {
					os.WriteFile(filepath.Join(rt, "env"), []byte(strings.ReplaceAll(c.env, "URL", p.URL)), 0o644)
				}
				proc := helperstest.Proc{Dir: dir, Env: helperstest.Env(dir)}
				var res helperstest.Result
				if goHelper {
					res = helperstest.Go(t, proc, "lec", c.args...)
					res.Stderr = strings.ReplaceAll(res.Stderr, "'"+os.Args[0]+"' helper lec", "KIT")
					res.Stderr = strings.ReplaceAll(res.Stderr, os.Args[0]+" helper lec", "KIT")
				} else {
					res = helperstest.Run(t, proc, "python3", append([]string{".lectern/lec.py"}, c.args...)...)
					res.Stderr = strings.ReplaceAll(res.Stderr, "python3 .lectern/lec.py", "KIT")
				}
				return res, p.requests()
			}
			py, pyReqs := run(false)
			got, goReqs := run(true)
			helperstest.Same(t, c.name, py, got)
			if py.Code != 1 && py.Stderr != got.Stderr {
				t.Errorf("stderr differs\npython: %q\ngo:     %q", py.Stderr, got.Stderr)
			}
			if c.name == "server error" || c.name == "unreachable" {
				if py.Stderr != got.Stderr {
					t.Errorf("stderr differs\npython: %q\ngo:     %q", py.Stderr, got.Stderr)
				}
			}
			sameRequests(t, c.name, pyReqs, goReqs)
		})
	}
}

// fixture writes files (relative path → content) under a fresh root.
func fixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(root, 0o755)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(files[name]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaudeSettingsInstallParity(t *testing.T) {
	requireParity(t)
	home := filepath.Join(t.TempDir(), "home")
	withStatus := map[string]string{".claude/settings.json": `{"statusLine": {"type": "command", "command": "~/bin/line --x 'y'"}}`}
	cases := []struct {
		name          string
		files         map[string]string
		env           []string
		tmux, hookURL string
		ask           bool
	}{
		{name: "fresh", tmux: "lec-3", hookURL: "http://127.0.0.1:9110/api/hook/session/3"},
		{name: "ask", tmux: "lec-3", hookURL: "http://h:1/api/hook/session/3", ask: true},
		{name: "operator status line", files: withStatus, tmux: "lec-4", hookURL: "http://h/x'y z"},
		{name: "config dir", files: map[string]string{"alt/settings.json": `{"statusLine": {"type": "command", "command": "echo \u00e9"}}`},
			env: []string{"CLAUDE_CONFIG_DIR=~/alt"}, tmux: "lec-5", hookURL: "http://h"},
		{name: "not a command status line", files: map[string]string{".claude/settings.json": `{"statusLine": {"type": "static", "command": "x"}}`},
			tmux: "lec-6", hookURL: "http://h"},
		{name: "non-string command", files: map[string]string{".claude/settings.json": `{"statusLine": {"type": "command", "command": 5}}`},
			tmux: "lec-7", hookURL: "http://h"},
		{name: "unreadable settings", files: map[string]string{".claude/settings.json": `{"statusLine": `},
			tmux: "lec-8", hookURL: "http://h"},
		{name: "settings not an object", files: map[string]string{".claude/settings.json": `[1]`},
			tmux: "lec-9", hookURL: "http://h"},
		{name: "existing hooks dir", files: map[string]string{".lectern/hooks/lec-10.json": "old"},
			tmux: "lec-10", hookURL: "http://h"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ask := "0"
			if c.ask {
				ask = "1"
			}
			proc := helperstest.Proc{Dir: t.TempDir(), Env: helperstest.Env(home, c.env...)}
			fixture(t, home, c.files)
			py := helperstest.Run(t, proc, "bash", "-c", agentevents.ClaudeSettingsInstallCommand(c.tmux, c.hookURL, c.ask))
			pyTree := helperstest.Tree(t, home)
			fixture(t, home, c.files)
			got := helperstest.Go(t, proc, "claude-settings-install", c.tmux, c.hookURL, ask)
			helperstest.Same(t, c.name, py, got)
			helperstest.SameTree(t, c.name, pyTree, helperstest.Tree(t, home))
		})
	}
}

// With a lectern binary the only difference is the default status line,
// which runs the Go helper instead of python3.
func TestClaudeSettingsInstallWithLectern(t *testing.T) {
	requireParity(t)
	home := filepath.Join(t.TempDir(), "home")
	proc := helperstest.Proc{Dir: t.TempDir(), Env: helperstest.Env(home)}
	fixture(t, home, nil)
	py := helperstest.Run(t, proc, "bash", "-c", agentevents.ClaudeSettingsInstallCommand("lec-1", "http://h", false))
	pyTree := helperstest.Tree(t, home)
	fixture(t, home, nil)
	got := helperstest.Go(t, proc, "claude-settings-install", "lec-1", "http://h", "0", "/opt/my lectern/lectern")
	helperstest.Same(t, "with lectern", py, got)
	goTree := helperstest.Tree(t, home)
	settings := filepath.Join(".lectern", "hooks", "lec-1.json")
	if pyTree[settings] != goTree[settings] {
		t.Errorf("settings differ:\n%s\n%s", pyTree[settings], goTree[settings])
	}
	line := goTree[filepath.Join(".lectern", "hooks", "lec-1-statusline.sh")]
	if !strings.Contains(line, `printf %s "$INPUT" | '/opt/my lectern/lectern' helper claude-statusline`+"\n") ||
		strings.Contains(line, "python3") {
		t.Errorf("status line does not run the Go helper:\n%s", line)
	}
}

func TestClaudeStatuslineParity(t *testing.T) {
	requireParity(t)
	home := t.TempDir()
	proc := helperstest.Proc{Dir: home, Env: helperstest.Env(home)}
	// The Python status line is whatever the installer writes.
	if r := helperstest.Run(t, proc, "bash", "-c", agentevents.ClaudeSettingsInstallCommand("s", "http://127.0.0.1:1", false)); r.Code != 0 {
		t.Fatalf("install: %+v", r)
	}
	script := filepath.Join(home, ".lectern", "hooks", "s-statusline.sh")
	inputs := []string{
		`{"model": {"display_name": "Opus", "id": "o"}, "context_window": {"used_percentage": 42}, "cost": {"total_cost_usd": 1.234}}`,
		`{"model": {"id": "claude-x"}, "context_window": {"used_percentage": 12.5}, "cost": {"total_cost_usd": 0.005}}`,
		`{"model": {"display_name": ""}, "context_window": {"used_percentage": 12.0}, "cost": {"total_cost_usd": 2.675}}`,
		`{}`,
		`{"model": null, "context_window": {}, "cost": {"total_cost_usd": 3}}`,
		`{"model": {"display_name": "\u03a9\u03bc\u03ad\u03b3\u03b1"}, "context_window": {"used_percentage": true}}`,
		`{"context_window": {"used_percentage": "x"}, "cost": {"total_cost_usd": -0.0}}`,
		`{"context_window": {"used_percentage": [1, "a", null, {"k": 1e-7}]}}`,
		`{"context_window": {"used_percentage": 1e16}, "cost": {"total_cost_usd": 1e300}}`,
		`{"context_window": {"used_percentage": null}, "cost": {"total_cost_usd": null}}`,
		`{"cost": {"total_cost_usd": NaN}}`,
		`{"cost": {"total_cost_usd": 12345678901234567890123}}`,
		`{"cost": {"total_cost_usd": 1` + strings.Repeat("0", 400) + `}}`,
		`{"cost": {"total_cost_usd": "1.00"}}`,
		`{"cost": {"total_cost_usd": true}}`,
		`{"model": "opus"}`,
		`{"model": {"display_name": 5}}`,
		`{"model": 0, "cost": 0, "context_window": ""}`,
		`{"cost": [1]}`,
		`[1]`,
		`not json`,
		"",
		"\xff",
	}
	for _, in := range inputs {
		p := proc
		p.Stdin = []byte(in)
		py := helperstest.Run(t, p, "sh", script)
		got := helperstest.Go(t, p, "claude-statusline")
		helperstest.Same(t, in, py, got)
	}
}

func TestCodexNotifyParity(t *testing.T) {
	requireParity(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "notify.py")
	os.WriteFile(script, []byte(agentevents.CodexNotifyScript), 0o700)
	cases := []struct {
		name string
		args []string
		env  bool
	}{
		{"turn complete", []string{`{"type": "agent-turn-complete", "turn-id": "t1", "input-messages": ["hi \u00e9"], "n": 1.0}`}, true},
		{"last argument wins", []string{"first", `[1, 2]`}, true},
		{"not JSON", []string{"{"}, true},
		{"no argument", nil, true},
		{"unconfigured", []string{"{}"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := func(goHelper bool) (helperstest.Result, []string) {
				p := newPlane(t, func(*plane, *http.Request) (int, string) { return 200, "{}" })
				var env []string
				if c.env {
					env = []string{"LECTERN_HOOK_TOKEN=tok", "LECTERN_HOOK_URL=" + p.URL + "/api/hook/session/4/"}
				}
				proc := helperstest.Proc{Dir: dir, Env: helperstest.Env(dir, env...)}
				if goHelper {
					return helperstest.Go(t, proc, "codex-notify", c.args...), p.requests()
				}
				return helperstest.Run(t, proc, "python3", append([]string{script}, c.args...)...), p.requests()
			}
			py, pyReqs := run(false)
			got, goReqs := run(true)
			helperstest.Same(t, c.name, py, got)
			sameRequests(t, c.name, pyReqs, goReqs)
		})
	}
}

func TestCodexHookParity(t *testing.T) {
	requireParity(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "lectern-codex-hook.py")
	os.WriteFile(script, []byte(agentevents.CodexHookScript), 0o700)
	answer := func(code int, body string) func(*plane, *http.Request) (int, string) {
		return func(*plane, *http.Request) (int, string) { return code, body }
	}
	cases := []struct {
		name  string
		args  []string
		stdin string
		reply func(*plane, *http.Request) (int, string)
		url   string
	}{
		{"answer", []string{"SessionStart", "8"}, `{"hook_event_name": "SessionStart", "x": "\u00e9"}`,
			answer(200, `{"hookSpecificOutput": {"additionalContext": "hi"}}`+"\n"), ""},
		{"empty answer", []string{"PreToolUse", "3"}, `{}`, answer(200, ""), ""},
		{"padded answer", []string{"Stop", "8"}, "raw \xff bytes", answer(200, " \t padded \u2028\n\n"), ""},
		{"invalid UTF-8 back", []string{"Stop", "8"}, `{}`, answer(200, "a\xe2\x82Ab\xffc\xf0\x9f\x98"), ""},
		{"error status", []string{"Stop", "8"}, `{}`, answer(500, "oops"), ""},
		{"redirect is not followed", []string{"Stop", "8"}, `{}`, answer(302, "moved"), ""},
		{"bad timeout", []string{"Stop", "soon"}, `{}`, answer(200, `{"a": 1}`), ""},
		{"no timeout", []string{"Stop"}, `{}`, answer(200, `{"a": 1}`), ""},
		{"no event", nil, `{}`, answer(200, `{"a": 1}`), ""},
		{"unconfigured", []string{"Stop", "8"}, `{}`, answer(200, `{"a": 1}`), "-"},
		{"unreachable", []string{"Stop", "2"}, `{}`, answer(200, `{"a": 1}`), "http://127.0.0.1:1"},
		{"negative timeout", []string{"Stop", "-5"}, `{}`, answer(200, `{"a": 1}`), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := func(goHelper bool) (helperstest.Result, []string) {
				p := newPlane(t, c.reply)
				env := []string{"LECTERN_HOOK_TOKEN=tok"}
				switch c.url {
				case "-":
				case "":
					env = append(env, "LECTERN_HOOK_URL="+p.URL+"/api/hook/session/9")
				default:
					env = append(env, "LECTERN_HOOK_URL="+c.url)
				}
				proc := helperstest.Proc{Dir: dir, Env: helperstest.Env(dir, env...), Stdin: []byte(c.stdin)}
				if goHelper {
					return helperstest.Go(t, proc, "codex-hook", c.args...), p.requests()
				}
				return helperstest.Run(t, proc, "python3", append([]string{script}, c.args...)...), p.requests()
			}
			py, pyReqs := run(false)
			got, goReqs := run(true)
			helperstest.Same(t, c.name, py, got)
			sameRequests(t, c.name, pyReqs, goReqs)
		})
	}
}

const aoeHooks = `{
  "version": 3,
  "hooks": {
    "Stop": [
      {"hooks": [{"type": "command", "command": "aoe hook stop"}]},
      {"hooks": [{"type": "command", "command": "python3 HOOK Stop 8"}]}
    ],
    "PermissionRequest": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "python3 HOOK PermissionRequest 130"}]}
    ],
    "Custom": [{"hooks": []}],
    "PreToolUse": [
      "not a group",
      {"hooks": "a string"},
      {"hooks": {"type": "command"}},
      {"hooks": [{"type": "http", "command": "HOOK"}, 5, {"type": "command", "command": ["x", "HOOK"]}]},
      {"hooks": [{"type": "command", "command": 0}]},
      {"hooks": [{"type": "command", "command": {"HOOK": 1}}]}
    ]
  },
  "tail": {"k": [1.5, 1e22, "\u00e9"]}
}`

func TestCodexHooksInstallParity(t *testing.T) {
	requireParity(t)
	home := filepath.Join(t.TempDir(), "home")
	hook := filepath.Join(home, ".lectern", "hooks", "lectern-codex-hook.py")
	withHook := func(s string) string { return strings.ReplaceAll(s, "HOOK", hook) }
	cases := []struct {
		name  string
		files map[string]string
		env   []string
		ask   bool
	}{
		{name: "fresh"},
		{name: "fresh with ask", ask: true},
		{name: "merge", files: map[string]string{".codex/hooks.json": withHook(aoeHooks)}},
		{name: "merge with ask", files: map[string]string{".codex/hooks.json": withHook(aoeHooks)}, ask: true},
		{name: "only ours", files: map[string]string{".codex/hooks.json": withHook(`{"hooks": {"PermissionRequest": [{"hooks": [{"type": "command", "command": "python3 HOOK x"}]}]}}`)}},
		{name: "not an object", files: map[string]string{".codex/hooks.json": `[1, 2]`}},
		{name: "hooks not an object", files: map[string]string{".codex/hooks.json": `{"a": 1, "hooks": [1]}`}},
		{name: "event not a list", files: map[string]string{".codex/hooks.json": `{"hooks": {"Stop": {"x": 1}}}`}},
		{name: "unreadable", files: map[string]string{".codex/hooks.json": `{"hooks": `}},
		{name: "null hooks list", files: map[string]string{".codex/hooks.json": `{"hooks": {"Stop": [{"hooks": null}]}}`}},
		{name: "numeric command", files: map[string]string{".codex/hooks.json": `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": 7}]}]}}`}},
		{name: "CODEX_HOME", env: []string{"CODEX_HOME=" + filepath.Join(home, "elsewhere", "codex")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ask := "0"
			if c.ask {
				ask = "1"
			}
			proc := helperstest.Proc{Dir: t.TempDir(), Env: helperstest.Env(home, c.env...)}
			files := map[string]string{".lectern/hooks/.keep": ""}
			for k, v := range c.files {
				files[k] = v
			}
			fixture(t, home, files)
			py := helperstest.Run(t, proc, "bash", "-c", agentevents.CodexHooksInstallCommand(c.ask))
			os.Remove(hook) // written by the Python install's shell step
			pyTree := helperstest.Tree(t, home)
			fixture(t, home, files)
			got := helperstest.Go(t, proc, "codex-hooks-install", ask, hook)
			helperstest.Same(t, c.name, py, got)
			helperstest.SameTree(t, c.name, pyTree, helperstest.Tree(t, home))
		})
	}
}

// With a lectern binary the entries run the Go handler, and replace both the
// Python handler's entries and Go entries naming an older binary.
func TestCodexHooksInstallWithLectern(t *testing.T) {
	requireParity(t)
	home := filepath.Join(t.TempDir(), "home")
	hook := filepath.Join(home, ".lectern", "hooks", "lectern-codex-hook.py")
	old := `'/old/lectern' helper codex-hook Stop 8`
	fixture(t, home, map[string]string{".codex/hooks.json": strings.ReplaceAll(strings.ReplaceAll(aoeHooks, "HOOK", hook),
		`python3 `+hook+` Stop 8`, old)})
	proc := helperstest.Proc{Dir: t.TempDir(), Env: helperstest.Env(home)}
	got := helperstest.Go(t, proc, "codex-hooks-install", "0", hook, "/opt/lectern")
	if got.Code != 0 || got.Stdout != filepath.Join(home, ".codex", "hooks.json")+"\n" {
		t.Fatalf("install: %+v", got)
	}
	first, _ := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	doc := string(first)
	for _, want := range []string{`"/opt/lectern helper codex-hook SessionStart 8"`, `"aoe hook stop"`,
		`"/opt/lectern helper codex-hook Stop 8"`, `"version": 3`} {
		if !strings.Contains(doc, want) {
			t.Errorf("hooks.json lacks %s:\n%s", want, doc)
		}
	}
	for _, gone := range []string{old, "python3 " + hook, "PermissionRequest"} {
		if strings.Contains(doc, gone) {
			t.Errorf("hooks.json still has %s:\n%s", gone, doc)
		}
	}
	if got := helperstest.Go(t, proc, "codex-hooks-install", "0", hook, "/opt/lectern"); got.Code != 0 {
		t.Fatalf("second install: %+v", got)
	}
	if second, _ := os.ReadFile(filepath.Join(home, ".codex", "hooks.json")); string(second) != doc {
		t.Errorf("a second install changed hooks.json:\n%s", second)
	}
}
