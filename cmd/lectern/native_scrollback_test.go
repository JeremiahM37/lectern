package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/console"
)

func TestNativeWrapScrollbackBindings(t *testing.T) {
	plan := testWrapPlan(t, []string{"tmux", "attach", "-t", "agent"}, "")
	for _, want := range []string{
		"bind-key -n WheelUpPane",
		// A program that asked for the mouse, or a pane already scrolled,
		// gets the event itself. The wheel is never made into Up/Down keys.
		"#{||:#{mouse_any_flag},#{pane_in_mode}}",
		"{ send-keys -M }",
		"#{@lectern_agent}",
		"bind-key -T prefix [ run-shell -b",
		plan.scrollScript,
	} {
		if !strings.Contains(plan.conf, want) {
			t.Errorf("config is missing %q:\n%s", want, plan.conf)
		}
	}
	if !strings.Contains(plan.scrollBody, terminalScrollbackFlag+" session 17 http://127.0.0.1:9110") ||
		!strings.Contains(plan.scrollBody, insertSocketEnv+"="+plan.socket) ||
		!strings.Contains(plan.scrollBody, linkDirEnv+"="+plan.dir) {
		t.Errorf("scrollback script: %q", plan.scrollBody)
	}
	if strings.Contains(plan.scrollBody, "scoped-secret") {
		t.Errorf("scrollback script carries the credential: %q", plan.scrollBody)
	}
	if !strings.Contains(plan.attachMenu(), plan.scrollScript) {
		t.Errorf("the Ctrl+] ? menu's Scroll back does not use the agent's history")
	}
}

// The private server accepts the generated bindings and marks the agent pane,
// so the wheel pages history there and nowhere else.
func TestNativeWrapScrollbackBindingsLoadOnRealTmux(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	plan := testWrapPlan(t, []string{"sh", "-c", "sleep 30"}, "")
	if err := plan.write(); err != nil {
		t.Fatal(err)
	}
	if err := plan.start(tmuxPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command(tmuxPath, "-S", plan.socket, "kill-server").Run() })
	run := func(args ...string) string {
		out, err := exec.Command(tmuxPath, append([]string{"-S", plan.socket}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v %s", args, err, out)
		}
		return string(out)
	}
	if got := strings.TrimSpace(run("show-options", "-pv", "-t", plan.session+":", agentPaneOption)); got != "1" {
		t.Errorf("agent pane is not marked: %q", got)
	}
	for _, table := range []struct{ name, key string }{{"root", "WheelUpPane"}, {"prefix", "["}} {
		if keys := run("list-keys", "-T", table.name); !strings.Contains(keys, plan.scrollScript) {
			t.Errorf("%s %s does not open the agent's history:\n%s", table.name, table.key, keys)
		}
	}
}

// The reported bug. An Ink-style agent erases and rewrites a live region and
// finishes lines faster than the agent's tmux client repaints, so the wrapper
// sees a screen at a time and loses what scrolled past between repaints. The
// agent's own pane has every line; the history the wheel now reads must too,
// complete and in order.
func TestScrollbackIsTheAgentPanesFullHistoryNotTheWrappersCopy(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	const lines = 300
	dir := t.TempDir()
	inner := filepath.Join(dir, "inner.sock")
	agent := fmt.Sprintf(`i=1; while [ $i -le %d ]; do
  [ $i -gt 1 ] && printf '\033[4A\r\033[J'
  printf 'committed line %%03d\n  live row a %%03d\n  live row b %%03d\n> prompt\n' $i $i $i
  i=$((i+1)); done; echo ALL-DONE; sleep 60`, lines)
	tm := func(sock string, args ...string) string {
		out, _ := exec.Command(tmuxPath, append([]string{"-S", sock}, args...)...).CombinedOutput()
		return string(out)
	}
	// The wrapper's tmux, exactly as the attachment configures it, around a
	// client of the agent's tmux.
	plan := testWrapPlan(t, []string{"sh", "-c", "sleep 1"}, "")
	if err := plan.write(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tm(plan.socket, "kill-server"); tm(inner, "kill-server") })
	// The agent's tmux must be up and its pane running before a client
	// attaches, as in real use; the output then arrives while attached.
	tm(inner, "-f", "/dev/null", "new-session", "-d", "-x", "100", "-y", "30", "-s", "agent", "sleep 1000")
	tm(inner, "respawn-pane", "-k", "-t", "agent:", "sh", "-c", agent)
	_ = exec.Command(tmuxPath, "-S", plan.socket, "-f", plan.confPath, "new-session", "-d", "-x", "100", "-y", "30",
		"-s", plan.session, "env -u TMUX "+tmuxPath+" -S "+inner+" attach -t agent").Run()
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(tm(inner, "capture-pane", "-p", "-t", "agent:"), "ALL-DONE") {
		if time.Now().After(deadline) {
			t.Fatal("the test agent never finished")
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)

	committed := regexp.MustCompile(`committed line (\d+)`)
	distinct := func(text string) map[string]bool {
		seen := map[string]bool{}
		for _, m := range committed.FindAllStringSubmatch(text, -1) {
			seen[m[1]] = true
		}
		return seen
	}
	wrapper := distinct(tm(plan.socket, "capture-pane", "-p", "-J", "-S", "-", "-t", plan.session+":"))
	t.Logf("wrapper history holds %d of %d lines", len(wrapper), lines)

	// The control plane answers the way the real endpoint does: the agent
	// pane's capture, joined, whole history.
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		text := tm(inner, "capture-pane", "-p", "-J", "-S", "-", "-t", "agent:")
		_ = json.NewEncoder(w).Encode(map[string]any{"text": text, "app_screen": false})
	}))
	defer srv.Close()
	text, ok := readScrollback(console.New(srv.URL, ""), "session", "17")
	if !ok {
		t.Fatal("no scrollback from the control plane")
	}
	if asked != "/api/term/session/17/history" {
		t.Errorf("asked %q", asked)
	}
	got := distinct(text)
	if len(got) != lines {
		t.Fatalf("scrollback has %d of %d lines (the wrapper's copy had %d)", len(got), lines, len(wrapper))
	}
	// Continuous and in order: committed lines are 001..300 with nothing
	// missing between them.
	last := 0
	for _, m := range committed.FindAllStringSubmatch(text, -1) {
		var n int
		fmt.Sscanf(m[1], "%d", &n)
		if n < last {
			t.Fatalf("line %d came after %d", n, last)
		}
		last = n
	}
	if len(wrapper) >= lines {
		t.Logf("this tmux kept every line in the wrapper too; the gap needs a slower terminal to show")
	}
}

// Anything but a usable history leaves the wheel on the wrapper's own copy
// mode, as it was before.
func TestReadScrollbackFallsBackWhenThereIsNoUsableHistory(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"older server": func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"empty":        func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"text":"  \n"}`)) },
		"owns screen": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"text":"one\ntwo","app_screen":true}`))
		},
		"not json": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`oops`)) },
	} {
		srv := httptest.NewServer(handler)
		if text, ok := readScrollback(console.New(srv.URL, ""), "session", "1"); ok {
			t.Errorf("%s: got history %q", name, text)
		}
		srv.Close()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"text":"one\ntwo\n\n\n","app_screen":false}`))
	}))
	defer srv.Close()
	if text, ok := readScrollback(console.New(srv.URL, ""), "session", "1"); !ok || text != "one\ntwo\n" {
		t.Errorf("history %q %v", text, ok)
	}
}
