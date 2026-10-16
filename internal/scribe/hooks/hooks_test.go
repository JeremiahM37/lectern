package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func skipWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook tests use sh")
	}
}

// newTestRunner builds a runner with only the given config hooks, a
// nonexistent user settings file and a temp project root.
func newTestRunner(t *testing.T, hs []config.Hook) (*Runner, string) {
	t.Helper()
	root := t.TempDir()
	r := New(Options{
		Root:         root,
		SessionID:    "sess-1",
		ConfigGlobal: hs,
		UserSettings: filepath.Join(t.TempDir(), "none.json"),
		Trusted:      true,
	})
	return r, root
}

func bashArgs(cmd string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return b
}

func TestPreToolBlockExit2(t *testing.T) {
	skipWindows(t)
	r, _ := newTestRunner(t, []config.Hook{{
		Event: "PreToolUse", Matcher: "shell",
		Command: `cat >/dev/null; echo "no rm -rf here" >&2; exit 2`,
	}})
	got := r.PreTool(context.Background(), "shell", bashArgs("rm -rf /"))
	if got != "no rm -rf here" {
		t.Fatalf("block reason = %q, want stderr text", got)
	}
}

func TestPreToolAllowedWhenExit0(t *testing.T) {
	skipWindows(t)
	r, _ := newTestRunner(t, []config.Hook{{
		Event: "PreToolUse", Command: `cat >/dev/null; exit 0`,
	}})
	if got := r.PreTool(context.Background(), "shell", bashArgs("ls")); got != "" {
		t.Fatalf("exit 0 blocked: %q", got)
	}
	if w := r.Warnings(); len(w) != 0 {
		t.Fatalf("unexpected warnings: %v", w)
	}
}

func TestPreToolJSONDecisionBlock(t *testing.T) {
	skipWindows(t)
	r, _ := newTestRunner(t, []config.Hook{{
		Event: "PreToolUse", Matcher: "Bash",
		Command: `cat >/dev/null; echo '{"decision":"block","reason":"use git instead"}'`,
	}})
	if got := r.PreTool(context.Background(), "shell", bashArgs("ls")); got != "use git instead" {
		t.Fatalf("got %q", got)
	}
}

func TestPreToolHookSpecificDeny(t *testing.T) {
	skipWindows(t)
	r, _ := newTestRunner(t, []config.Hook{{
		Event:   "PreToolUse",
		Command: `cat >/dev/null; echo '{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"frozen"}}'`,
	}})
	if got := r.PreTool(context.Background(), "edit_file", json.RawMessage(`{}`)); got != "frozen" {
		t.Fatalf("got %q", got)
	}
}

func TestPreToolFirstBlockStopsChain(t *testing.T) {
	skipWindows(t)
	marker := filepath.Join(t.TempDir(), "second-ran")
	r, _ := newTestRunner(t, []config.Hook{
		{Event: "PreToolUse", Command: `cat >/dev/null; echo first >&2; exit 2`},
		{Event: "PreToolUse", Command: `touch ` + marker + `; exit 0`},
	})
	if got := r.PreTool(context.Background(), "shell", bashArgs("x")); got != "first" {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("second PreToolUse hook ran after a block")
	}
}

func TestPostToolExtraContext(t *testing.T) {
	skipWindows(t)
	r, _ := newTestRunner(t, []config.Hook{{
		Event: "PostToolUse", Matcher: "edit_file|write_file",
		Command: `cat >/dev/null; echo "gofmt: fixed 2 lines"`,
	}})
	res := tools.Result{Output: "wrote a.go"}
	if got := r.PostTool(context.Background(), "edit_file", json.RawMessage(`{}`), res); got != "gofmt: fixed 2 lines" {
		t.Fatalf("extra = %q", got)
	}
	if got := r.PostTool(context.Background(), "shell", json.RawMessage(`{}`), res); got != "" {
		t.Fatalf("matcher did not exclude shell: %q", got)
	}
}

func TestPromptContextAndBlock(t *testing.T) {
	skipWindows(t)
	r, _ := newTestRunner(t, []config.Hook{
		{Event: "UserPromptSubmit", Command: `cat >/dev/null; echo "branch is main"`},
		{Event: "UserPromptSubmit", Command: `p=$(cat); case "$p" in *DROP*) echo "no DROP" >&2; exit 2;; esac`},
	})
	extra, block := r.Prompt(context.Background(), "hello")
	if extra != "branch is main" || block != "" {
		t.Fatalf("hello: extra=%q block=%q", extra, block)
	}
	_, block = r.Prompt(context.Background(), "DROP TABLE users")
	if block != "no DROP" {
		t.Fatalf("block = %q", block)
	}
}

func TestTimeoutKillsProcessGroup(t *testing.T) {
	skipWindows(t)
	// The grandchild holds stdout open; the timeout must kill it too or the
	// call would hang until the sleep ends.
	r, _ := newTestRunner(t, []config.Hook{{
		Event: "PreToolUse", Command: `cat >/dev/null; sleep 20; exit 2`, Timeout: 1,
	}})
	start := time.Now()
	got := r.PreTool(context.Background(), "shell", bashArgs("x"))
	if el := time.Since(start); el > 8*time.Second {
		t.Fatalf("timeout took %s", el)
	}
	if got != "" {
		t.Fatalf("timed-out hook blocked: %q", got)
	}
	w := r.Warnings()
	if len(w) != 1 || !strings.Contains(w[0], "timed out") {
		t.Fatalf("warnings = %v", w)
	}
}

func TestNonZeroExitIsWarningNotBlock(t *testing.T) {
	skipWindows(t)
	r, _ := newTestRunner(t, []config.Hook{{
		Event: "PreToolUse", Command: `cat >/dev/null; echo broken >&2; exit 1`,
	}})
	if got := r.PreTool(context.Background(), "shell", bashArgs("x")); got != "" {
		t.Fatalf("exit 1 blocked: %q", got)
	}
	w := r.Warnings()
	if len(w) != 1 || !strings.Contains(w[0], "exited 1") {
		t.Fatalf("warnings = %v", w)
	}
	if len(r.Warnings()) != 0 {
		t.Fatal("Warnings did not clear")
	}
}

func TestMatcherSemantics(t *testing.T) {
	cases := []struct {
		matcher string
		tool    string
		want    bool
	}{
		{"", "shell", true},
		{"*", "grep", true},
		{"Bash", "shell", true},
		{"Bash", "grep", false},
		{"Bash|Edit", "edit_file", true},
		{"Edit", "multi_edit", true},
		{"Bash|Edit", "read", false},
		{"^Ba", "shell", true},
		{"^Ba", "edit_file", false},
		{"Edit|Write", "write_file", true},
		{"mcp__.*", "mcp__srv__tool", true},
		{"Bash", "mcp__srv__Bash", false},
	}
	for _, c := range cases {
		e, err := newEntry("PreToolUse", c.matcher, "true", 0, "t")
		if err != nil {
			t.Fatal(err)
		}
		if got := e.matches(toolNames(c.tool)); got != c.want {
			t.Errorf("matcher %q vs %q = %v, want %v", c.matcher, c.tool, got, c.want)
		}
	}
	if _, err := newEntry("PreToolUse", "([", "true", 0, "t"); err == nil {
		t.Error("invalid regex accepted")
	}
}

func TestClaudeSettingsFormat(t *testing.T) {
	skipWindows(t)
	root := t.TempDir()
	user := filepath.Join(t.TempDir(), "settings.json")
	writeFile(t, user, `{
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash|Edit", "hooks": [
        {"type": "command", "command": "cat >/dev/null; echo blocked-by-user >&2; exit 2", "timeout": 5},
        {"type": "http", "command": "ignored"}
      ]}
    ],
    "Stop": [{"hooks": [{"type": "command", "command": "echo done > stop.txt"}]}]
  }
}`)
	r := New(Options{Root: root, SessionID: "s", UserSettings: user, Trusted: false})
	if r.Count() != 2 {
		t.Fatalf("entries = %d, want 2 (http hook must be skipped)", r.Count())
	}
	if got := r.PreTool(context.Background(), "edit_file", json.RawMessage(`{}`)); got != "blocked-by-user" {
		t.Fatalf("Claude Edit matcher: got %q", got)
	}
	if got := r.PreTool(context.Background(), "read", json.RawMessage(`{}`)); got != "" {
		t.Fatalf("Read should not match: %q", got)
	}
	r.Stop(context.Background(), agent.TurnStats{})
	if _, err := os.Stat(filepath.Join(root, "stop.txt")); err != nil {
		t.Fatalf("user Stop hook did not run in project dir: %v", err)
	}
}

func TestDisableAllHooks(t *testing.T) {
	user := filepath.Join(t.TempDir(), "settings.json")
	writeFile(t, user, `{"disableAllHooks": true, "hooks": {"PreToolUse": [{"hooks": [{"type":"command","command":"exit 2"}]}]}}`)
	r := New(Options{UserSettings: user, Trusted: true})
	if r.Count() != 0 {
		t.Fatalf("disableAllHooks ignored: %d hooks", r.Count())
	}
}

func TestStdinJSONAndEnvironment(t *testing.T) {
	skipWindows(t)
	t.Setenv("SCRIBE_TEST_SECRET_KEY", "hunter2")
	r, root := newTestRunner(t, []config.Hook{{
		Event: "PreToolUse", Matcher: "shell",
		Command: `cat > in.json; printf '%s|%s|%s' "$SCRIBE_TEST_SECRET_KEY" "$CLAUDE_PROJECT_DIR" "$SCRIBE_PROJECT_DIR" > env.txt; exit 0`,
	}})
	r.PreTool(context.Background(), "shell", bashArgs("go test ./..."))
	var in map[string]any
	b, err := os.ReadFile(filepath.Join(root, "in.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatalf("stdin not JSON: %v (%s)", err, b)
	}
	if in["session_id"] != "sess-1" || in["hook_event_name"] != "PreToolUse" || in["tool_name"] != "Bash" {
		t.Fatalf("stdin = %v", in)
	}
	ti := in["tool_input"].(map[string]any)
	if ti["command"] != "go test ./..." {
		t.Fatalf("tool_input = %v", ti)
	}
	env, _ := os.ReadFile(filepath.Join(root, "env.txt"))
	parts := strings.Split(string(env), "|")
	if parts[0] != "" {
		t.Fatal("hook inherited a secret-looking variable")
	}
	if parts[1] != root || parts[2] != root {
		t.Fatalf("project dir env = %q", env)
	}
}

func TestProjectHooksRequireTrust(t *testing.T) {
	skipWindows(t)
	t.Setenv("SCRIBE_HOME", t.TempDir())
	root := t.TempDir()
	marker := filepath.Join(root, "ran.txt")
	writeFile(t, filepath.Join(root, ".claude", "settings.json"),
		`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"touch ran.txt"}]}]}}`)
	if Trusted(root) {
		t.Fatal("fresh root reported trusted")
	}

	untrusted := New(Options{Root: root, UserSettings: filepath.Join(t.TempDir(), "x.json"), Trusted: Trusted(root)})
	untrusted.SessionStart(context.Background())
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("untrusted project hook ran")
	}
	w := untrusted.Warnings()
	if len(w) != 1 || !strings.Contains(w[0], "not trusted") {
		t.Fatalf("warnings = %v", w)
	}

	if err := Trust(root); err != nil {
		t.Fatal(err)
	}
	if !Trusted(root) {
		t.Fatal("Trust did not stick")
	}
	trusted := New(Options{Root: root, UserSettings: filepath.Join(t.TempDir(), "x.json"), Trusted: Trusted(root)})
	trusted.SessionStart(context.Background())
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("trusted project hook did not run: %v", err)
	}

	if err := Untrust(root); err != nil {
		t.Fatal(err)
	}
	if Trusted(root) {
		t.Fatal("Untrust did not stick")
	}
}

func TestTrustFileIsIdempotent(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	root := t.TempDir()
	for i := 0; i < 2; i++ {
		if err := Trust(root); err != nil {
			t.Fatal(err)
		}
	}
	tf, err := readTrust()
	if err != nil || len(tf.Roots) != 1 {
		t.Fatalf("roots = %v, err %v", tf.Roots, err)
	}
	if _, err := os.Stat(TrustPath()); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationAndSessionEventsRun(t *testing.T) {
	skipWindows(t)
	root := t.TempDir()
	r := New(Options{Root: root, UserSettings: filepath.Join(t.TempDir(), "x.json"), Trusted: true,
		ConfigGlobal: []config.Hook{
			{Event: "Notification", Command: `cat > note.json`},
			{Event: "SessionEnd", Command: `echo bye > end.txt`},
			{Event: "PreCompact", Command: `echo compacting > compact.txt`},
		}})
	r.Notification(context.Background(), "approval needed")
	r.SessionEnd(context.Background())
	r.PreCompact(context.Background())
	b, _ := os.ReadFile(filepath.Join(root, "note.json"))
	if !strings.Contains(string(b), `"message":"approval needed"`) {
		t.Fatalf("notification payload = %s", b)
	}
	for _, f := range []string{"end.txt", "compact.txt"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Fatalf("%s missing: %v", f, err)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
