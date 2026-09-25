package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

// MockDiff is the patch every mock attempt "produces".
const MockDiff = `diff --git a/app.py b/app.py
index 83db48f..bf2f3f4 100644
--- a/app.py
+++ b/app.py
@@ -1,4 +1,7 @@
 def main():
-    print("hello")
+    print("hello, lectern")
+
+def health():
+    return {"ok": True}
`

// MockNumstat is the matching --numstat output.
const MockNumstat = "4\t1\tapp.py\n"

// Mock is a scripted fake target. It powers the whole hermetic test suite and
// LECTERN_MOCK=1 demo mode, and emits REAL claude stream-json shapes so the
// parser is exercised end to end rather than stubbed out.
//
// Scenario markers in the prompt:
//
//	[mock:approval]        agent requests a hook approval mid-run (real HTTP)
//	[mock:fail]            agent exits non-zero
//	[mock:slow]            agent takes ~3x longer
//	[mock:subtask]         agent files a follow-up card through the task hook
//	[mock:note]            agent leaves a project note
//	[mock:approve-verdict] agent ends with VERDICT: APPROVE
//	[mock:reject-verdict]  agent ends with VERDICT: REQUEST_CHANGES
//	[mock:judge:N]         agent ends with JUDGE: attempt N (for judge-task tests)
//	[mock:eval-judge:match|no-match]  agent ends with EVAL_JUDGE: MATCH|NO_MATCH (replay eval judge tests)
type Mock struct {
	mu       sync.Mutex
	fs       map[string][]byte
	cmdLog   []string
	agents   map[string]*mockAgent
	scratchN int
	panes    map[string]*mockPane

	// Delay paces the fake agent between events.
	Delay time.Duration
	// HTTP is the client the fake agent uses for hook callbacks. Tests point it
	// at their httptest server's transport; production demo mode uses the default.
	HTTP *http.Client
	// Intercept, when set, runs synchronously at the top of every Run call.
	// It exists purely as a test seam for controlling timing — e.g. blocking
	// on a specific command to deterministically create the overlap a
	// coalescing test needs — and is never set in production code.
	Intercept func(cmd string)
}

type mockAgent struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewMock builds a mock executor.
func NewMock(delay time.Duration) *Mock {
	if delay <= 0 {
		delay = 400 * time.Millisecond
	}
	return &Mock{
		fs:     map[string][]byte{},
		agents: map[string]*mockAgent{},
		// an agent the operator started themselves, weeks ago, that lectern
		// knows nothing about — the case session discovery exists for
		panes: map[string]*mockPane{"legacy-claude": {
			workdir: "/mock/demo-app",
			psArgs:  "claude --continue --model opus",
			lines:   []string{"...", "❯ "},
		}},
		Delay: delay,
		HTTP:  &http.Client{Timeout: 30 * time.Second},
	}
}

// CmdLog returns a copy of every command this executor was asked to run.
// scratchDirName pulls the slug out of the scratch-creation command, so two
// scratch sessions get two directories here exactly as they would on a real host.
func scratchDirName(cmd string) string {
	if m := scratchRe.FindStringSubmatch(cmd); len(m) > 1 {
		return m[1]
	}
	return "scratch"
}

func (m *Mock) CmdLog() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.cmdLog...)
}

// Files returns a copy of the fake filesystem, for assertions about what was
// actually staged onto the target.
func (m *Mock) Files() map[string][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string][]byte, len(m.fs))
	for k, v := range m.fs {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

var (
	sessionRe = regexp.MustCompile(`-s (\S+)`)
	targetRe  = regexp.MustCompile(`-t (\S+)`)
	cdRe      = regexp.MustCompile(`cd (\S+) &&`)
	urlRe     = regexp.MustCompile(`LECTERN_URL=(\S+)`)
	tokenRe   = regexp.MustCompile(`LECTERN_TOKEN=(\S+)`)
)

// Run interprets the command against the scripted target.
func (m *Mock) Run(ctx context.Context, cmd string, opts RunOpts) (Result, error) {
	m.mu.Lock()
	m.cmdLog = append(m.cmdLog, cmd)
	m.mu.Unlock()
	if m.Intercept != nil {
		m.Intercept(cmd)
	}

	switch {
	case strings.HasPrefix(cmd, "python3 -c ") && strings.Contains(cmd, "os.O_NOFOLLOW"):
		// MCP runtime publication is a target-side helper in real executors. The
		// mock only needs to return the absolute private-state path that the
		// launcher would receive; it must not pretend the Git worktree owns it.
		return Result{0, "/tmp/lectern-mcp-state/lectern/mcp/mock/mcp.json\n", ""}, nil
	case strings.HasPrefix(cmd, "test \"$(wc -c < ") && strings.Contains(cmd, " && mv -- "):
		return m.publishUpload(cmd), nil
	case strings.HasPrefix(cmd, "sudo pvesh get /cluster/nextid"):
		return Result{0, "9001\n", ""}, nil
	case hasAnyPrefix(cmd, "sudo pct clone", "sudo pct start", "sudo pct stop",
		"sudo pct destroy", "sudo pct push", "sudo pct exec"):
		return Result{0, "", ""}, nil
	case strings.HasPrefix(cmd, "test -d ") && strings.Contains(cmd, "missingdir"):
		// A test sentinel: the path a project claims does not exist on the
		// target. A project shell must fail rather than fall back elsewhere.
		return Result{1, "", ""}, nil
	case strings.HasPrefix(cmd, "git clone"):
		return Result{0, "", ""}, nil
	case strings.Contains(cmd, "lectern-scratch") && strings.Contains(cmd, "mktemp -d"):
		// a scratch directory is created by the target's own shell and its path
		// read back from `pwd`; mktemp's uniqueness is modelled by a counter, so
		// a test sees the same "never the same directory twice" guarantee
		m.mu.Lock()
		m.scratchN++
		n := m.scratchN
		m.mu.Unlock()
		return Result{0, fmt.Sprintf("/mock/home/lectern-scratch/%s-%06d\n",
			scratchDirName(cmd), n), ""}, nil
	case strings.Contains(cmd, "symbolic-ref --short HEAD"):
		return Result{0, "main\n", ""}, nil
	case strings.HasPrefix(cmd, "rm -f "):
		m.mu.Lock()
		for _, tok := range strings.Fields(cmd)[2:] {
			delete(m.fs, tok)
		}
		m.mu.Unlock()
		return Result{0, "", ""}, nil
	case strings.HasPrefix(cmd, "tmux new-session"):
		sess := strings.Trim(firstGroup(sessionRe, cmd), "'")
		wt := firstGroup(cdRe, cmd)
		if wt == "" {
			wt = "/mock"
		}
		if interactiveSession(cmd) {
			m.startPane(sess, strings.Trim(wt, "'"))
		} else {
			m.startAgent(sess, wt)
		}
		return Result{0, "", ""}, nil
	case strings.Contains(cmd, "@lectern-tracking-identity"):
		return m.handleTracking(cmd), nil
	case strings.Contains(cmd, "capture-pane"):
		return m.handlePoll(cmd), nil
	case strings.Contains(cmd, "tmux list-panes -a"):
		return m.handleDiscover(), nil
	case strings.Contains(cmd, "tmux load-buffer"):
		return m.handleSendText(cmd), nil
	case sendKeysRe.MatchString(cmd):
		return m.handleSendKey(cmd), nil
	case strings.Contains(cmd, "tmux display-message"):
		name := strings.TrimPrefix(strings.Trim(firstGroup(targetRe, cmd), "'"), "=")
		m.mu.Lock()
		_, known := m.panes[name]
		m.mu.Unlock()
		if !known {
			return Result{1, "", "can't find session"}, nil
		}
		// a session that has been up for a while, so adoption's clock is testable
		now := time.Now().Unix()
		return Result{0, fmt.Sprintf("%d %d\n", now-7200, now-600), ""}, nil
	case strings.HasPrefix(cmd, "tmux has-session"):
		name := strings.TrimPrefix(strings.Trim(firstGroup(targetRe, cmd), "'"), "=")
		if m.alive(name) {
			return Result{0, "", ""}, nil
		}
		m.mu.Lock()
		_, isPane := m.panes[name]
		m.mu.Unlock()
		if isPane {
			return Result{0, "", ""}, nil
		}
		return Result{1, "", ""}, nil
	case strings.HasPrefix(cmd, "tmux kill-session"):
		name := strings.TrimPrefix(strings.Trim(firstGroup(targetRe, cmd), "'"), "=")
		m.killAgent(name)
		m.mu.Lock()
		delete(m.panes, name)
		m.mu.Unlock()
		return Result{0, "", ""}, nil
	case strings.HasPrefix(cmd, "test -f ") && strings.Contains(cmd, "command -v verify"):
		// internal/checks' auto-detect probe (".verify.yaml" present + verify on
		// PATH). Defaults to "not found" so every existing test that doesn't set
		// verify_cmd keeps seeing no auto-verify; write the marker path via
		// WriteFile to opt a test into auto-detection deliberately.
		m.mu.Lock()
		_, present := m.fs[strings.Fields(cmd)[2]]
		m.mu.Unlock()
		if present {
			return Result{0, "", ""}, nil
		}
		return Result{1, "", ""}, nil
	case strings.Contains(cmd, "diff --numstat"):
		return Result{0, MockNumstat, ""}, nil
	case strings.Contains(cmd, "diff --no-color") || gitDiffRe.MatchString(cmd):
		return Result{0, MockDiff, ""}, nil
	case strings.Contains(cmd, "status --porcelain"):
		return Result{0, " M app.py\n", ""}, nil
	case strings.Contains(cmd, "--version") || strings.HasPrefix(cmd, "df "):
		return Result{0, "mock 1.0", ""}, nil
	case strings.HasPrefix(cmd, `claude -p "Reply with exactly: ok"`):
		// the deep probe MUST redirect stdin: `claude -p` reads to EOF and an ssh
		// exec channel never EOFs, so without it the probe hangs until timeout
		if !strings.Contains(cmd, "< /dev/null") {
			return Result{}, Errf("deep probe must redirect stdin (ssh hang): %s", cmd)
		}
		return Result{0, "ok", ""}, nil
	case strings.Contains(cmd, "mockverify-fail"):
		return Result{1, "", "2 failed, 3 passed"}, nil
	case strings.Contains(cmd, "mockverify-pass"):
		return Result{0, "5 passed in 0.1s", ""}, nil
	case strings.Contains(cmd, "mocksetup-fail"):
		return Result{1, "", "setup blew up"}, nil
	case strings.Contains(cmd, "mocksetup-pass"):
		return Result{0, "setup ok", ""}, nil
	case strings.Contains(cmd, "git add -A && git commit"):
		return Result{0, "[lec 1a2b3c4] mock commit", ""}, nil
	case strings.HasPrefix(cmd, "git") && strings.Contains(cmd, " push "):
		return Result{0, "branch pushed (mock)", ""}, nil
	case strings.HasPrefix(cmd, "gh pr create"):
		return Result{0, "https://github.com/mock/repo/pull/7", ""}, nil
	case strings.HasPrefix(cmd, "ls "):
		// Backed by the in-memory fs so evals' "import from repo" (a glob
		// listing followed by ReadFile) has something real to find, the same
		// way a real target's directory would.
		return m.handleLs(cmd), nil
	}
	// git worktree add, mkdir, exclude appends, memory links, …
	return Result{0, "", ""}, nil
}

var scratchRe = regexp.MustCompile(`mktemp -d "\$root/([^"]+)-XXXXXX"`)
var judgeRe = regexp.MustCompile(`\[mock:judge:(\d+)\]`)
var evalJudgeRe = regexp.MustCompile(`\[mock:eval-judge:(match|no-match)\]`)
var gitDiffRe = regexp.MustCompile(`\bgit\b.*\bdiff\b`)

// ReadFile reads from the fake filesystem.
func (m *Mock) ReadFile(_ context.Context, path string, offset int64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data := m.fs[path]
	if offset >= int64(len(data)) {
		return nil, nil
	}
	return append([]byte(nil), data[offset:]...), nil
}

// WriteFile writes to the fake filesystem.
func (m *Mock) WriteFile(_ context.Context, path string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fs[path] = append([]byte(nil), data...)
	return nil
}

// handleLs answers a plain `ls <glob> <glob> ... [2>/dev/null]` against the
// fake filesystem — just enough for evals' "import from repo" (list
// `.lectern/evals/*.yaml`, then ReadFile each) to see files a test wrote
// through WriteFile, the way a real target's directory would.
func (m *Mock) handleLs(cmd string) Result {
	cmd = strings.TrimSuffix(strings.TrimSpace(cmd), "2>/dev/null")
	fields := strings.Fields(cmd)
	if len(fields) < 2 {
		return Result{1, "", "ls: missing operand"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	var matches []string
	for _, pattern := range fields[1:] {
		for name := range m.fs {
			if seen[name] {
				continue
			}
			if ok, _ := path.Match(pattern, name); ok {
				seen[name] = true
				matches = append(matches, name)
			}
		}
	}
	if len(matches) == 0 {
		return Result{1, "", "ls: no such file or directory"}
	}
	sortStrings(matches)
	return Result{0, strings.Join(matches, "\n") + "\n", ""}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// Close cancels every fake agent still running.
func (m *Mock) Close() error {
	m.mu.Lock()
	agents := make([]*mockAgent, 0, len(m.agents))
	for _, a := range m.agents {
		agents = append(agents, a)
	}
	m.agents = map[string]*mockAgent{}
	m.mu.Unlock()
	for _, a := range agents {
		a.cancel()
	}
	return nil
}

// Probe answers the capability check without shelling out.
func (m *Mock) Probe() map[string]any {
	return map[string]any{
		"git": "git version 2.43 (mock)", "tmux": "tmux 3.4 (mock)",
		"claude": "2.0.0 (mock)", "python3": "Python 3.12 (mock)",
		"disk_free": "42G",
	}
}

func (m *Mock) alive(sess string) bool {
	m.mu.Lock()
	a, ok := m.agents[sess]
	m.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case <-a.done:
		return false
	default:
		return true
	}
}

func (m *Mock) killAgent(sess string) {
	m.mu.Lock()
	a, ok := m.agents[sess]
	m.mu.Unlock()
	if ok {
		a.cancel()
		<-a.done
	}
}

func (m *Mock) startAgent(sess, wt string) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &mockAgent{cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	m.agents[sess] = a
	m.mu.Unlock()
	go func() {
		defer close(a.done)
		m.runAgent(ctx, sess, wt)
	}()
}

// ---- the fake agent ---------------------------------------------------------

func (m *Mock) append(path string, line map[string]any) {
	b, _ := json.Marshal(line)
	m.mu.Lock()
	m.fs[path] = append(m.fs[path], append(b, '\n')...)
	m.mu.Unlock()
}

func (m *Mock) read(path string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.fs[path]...)
}

func (m *Mock) runAgent(ctx context.Context, sess, wt string) {
	rt := wt + "/.lectern"
	events := rt + "/events.jsonl"
	prompt := string(m.read(rt + "/prompt.md"))
	pace := m.Delay
	if strings.Contains(prompt, "[mock:slow]") {
		pace *= 3
	}
	sid := "mock-sess-" + sess

	sleep := func() bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(pace):
			return true
		}
	}

	m.append(events, map[string]any{
		"type": "system", "subtype": "init", "session_id": sid,
		"model": "claude-mock", "tools": []string{"Bash", "Edit", "Write"}})
	if !sleep() {
		return
	}
	m.append(events, map[string]any{"type": "assistant", "message": map[string]any{
		"content": []any{map[string]any{"type": "text",
			"text": "Reading the codebase and planning the change."}}}})
	if !sleep() {
		return
	}

	if strings.Contains(prompt, "[mock:subtask]") {
		m.hookPost(ctx, rt, "/api/hook/tasks", map[string]any{
			"title":    "Agent follow-up: add tests",
			"prompt":   "Write tests for the new health() endpoint.",
			"dispatch": false})
	}
	if strings.Contains(prompt, "[mock:note]") {
		m.hookPost(ctx, rt, "/api/hook/notes", map[string]any{
			"note": "Auth uses bcrypt; integration tests live in tests/."})
	}

	if strings.Contains(prompt, "[mock:approval]") {
		if !m.requestApproval(ctx, rt) {
			m.append(events, map[string]any{"type": "assistant", "message": map[string]any{
				"content": []any{map[string]any{"type": "text",
					"text": "Action denied by operator — stopping."}}}})
			m.finish(rt, sid, 0, "Stopped: operator denied the action.")
			return
		}
	}

	m.append(events, map[string]any{"type": "assistant", "message": map[string]any{
		"content": []any{map[string]any{"type": "tool_use", "id": "tu_1", "name": "Edit",
			"input": map[string]any{"file_path": "app.py", "old_string": "hello",
				"new_string": "hello, lectern"}}}}})
	if !sleep() {
		return
	}
	m.append(events, map[string]any{"type": "user", "message": map[string]any{
		"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tu_1",
			"content": "Edit applied"}}}})
	if !sleep() {
		return
	}

	if strings.Contains(prompt, "[mock:fail]") {
		m.append(events, map[string]any{"type": "assistant", "message": map[string]any{
			"content": []any{map[string]any{"type": "text",
				"text": "Hit an unrecoverable error."}}}})
		m.finish(rt, sid, 1, "error")
		return
	}
	result := "Done: updated app.py and added health()."
	switch {
	case strings.Contains(prompt, "[mock:reject-verdict]"):
		result = "Reviewed the diff. VERDICT: REQUEST_CHANGES — rename health() and add a test."
	case strings.Contains(prompt, "[mock:approve-verdict]"):
		result = "Reviewed the diff. VERDICT: APPROVE — clean, focused change."
	case judgeRe.MatchString(prompt):
		n := firstGroup(judgeRe, prompt)
		result = "Compared the attempts. JUDGE: attempt " + n + "\nREASON: it passed its check."
	case evalJudgeRe.MatchString(prompt):
		verdict := strings.ToUpper(strings.ReplaceAll(firstGroup(evalJudgeRe, prompt), "-", "_"))
		result = "Compared against the reference diff. EVAL_JUDGE: " + verdict + "\nREASON: mock verdict."
	}
	m.finish(rt, sid, 0, result)
}

func (m *Mock) finish(rt, sid string, rc int, result string) {
	subtype := "success"
	if rc != 0 {
		subtype = "error"
	}
	m.append(rt+"/events.jsonl", map[string]any{
		"type": "result", "subtype": subtype, "total_cost_usd": 0.0123,
		"duration_ms": 3456, "num_turns": 3, "result": result, "session_id": sid})
	m.mu.Lock()
	m.fs[rt+"/exit_code"] = []byte(fmt.Sprintf("%d\n", rc))
	m.mu.Unlock()
}

// hookPost simulates the agent using .lectern/lec.py.
func (m *Mock) hookPost(ctx context.Context, rt, path string, payload map[string]any) {
	env := parseEnvFile(string(m.read(rt + "/env")))
	url, token := env["ADK_URL"], env["ADK_TOKEN"]
	if url == "" || token == "" {
		return
	}
	payload["token"] = token
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(url, "/")+path,
		bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.HTTP.Do(req)
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

// requestApproval exercises the REAL hook flow: it reads the generated
// settings.json for its URL and token, posts, then long-polls for a decision.
func (m *Mock) requestApproval(ctx context.Context, rt string) bool {
	var settings struct {
		Hooks struct {
			PreToolUse []struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(m.read(rt+"/settings.json"), &settings); err != nil ||
		len(settings.Hooks.PreToolUse) == 0 || len(settings.Hooks.PreToolUse[0].Hooks) == 0 {
		return true // no hooks configured (permission mode is not 'default')
	}
	hookCmd := settings.Hooks.PreToolUse[0].Hooks[0].Command
	url, token := firstGroup(urlRe, hookCmd), firstGroup(tokenRe, hookCmd)
	if url == "" || token == "" {
		return true
	}
	base := strings.TrimRight(url, "/")

	body, _ := json.Marshal(map[string]any{"token": token, "tool_name": "Bash",
		"tool_input": map[string]any{"command": "rm -rf build/ && make deploy"}})
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/api/hook/approval",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return false
	}
	var created struct {
		ID int64 `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	for i := 0; i < 600; i++ {
		if ctx.Err() != nil {
			return false
		}
		r, err := http.NewRequestWithContext(ctx, "GET",
			fmt.Sprintf("%s/api/hook/approval/%d/decision", base, created.ID), nil)
		if err != nil {
			return false
		}
		dres, err := m.HTTP.Do(r)
		if err != nil {
			return false
		}
		var d struct {
			Status string `json:"status"`
		}
		json.NewDecoder(dres.Body).Decode(&d)
		dres.Body.Close()
		switch d.Status {
		case "approved":
			return true
		case "denied", "expired":
			return false
		}
	}
	return false
}

func parseEnvFile(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			out[k] = v
		}
	}
	return out
}

func firstGroup(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
