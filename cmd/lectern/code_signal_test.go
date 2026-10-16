//go:build !windows

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/session"
)

// TestSigtermSavesSessionAndContinues runs a real build of `lectern code` in a
// private tmux server. A prompt goes to a model that never answers, so the
// turn is still running when SIGTERM arrives. The session file must then be
// readable and announced by the exit banner, and `lectern code --continue`
// must finish the turn without sending the prompt a second time.
func TestSigtermSavesSessionAndContinues(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary and drives tmux")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	work := t.TempDir()
	bin := buildLecternBinary(t, work)
	repo := newScribeRepo(t, filepath.Join(work, "repo"))
	stall := newStallModel(t)
	home := filepath.Join(work, "home")
	writeScribeConfig(t, home, stall.URL)

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + work,
		"SCRIBE_HOME=" + home,
		"STALL_KEY=test-key",
		"TERM=xterm-256color",
		"LANG=C.UTF-8",
	}
	sock := fmt.Sprintf("scribe-sig-%d-%d", os.Getpid(), time.Now().UnixNano())

	// First run: a prompt, then SIGTERM while the model is still silent.
	first := startLecternPane(t, sock, "first", work, env,
		bin, "code", "--cwd", repo, "write the greeting")
	pollUntil(t, 60*time.Second, "the model to receive the prompt", func() bool { return stall.count() >= 1 })
	stopLectern(t, first)

	sessions := session.NewStore(repo).List()
	if len(sessions) != 1 {
		t.Fatalf("want one saved session, have %d", len(sessions))
	}
	id := sessions[0].ID
	banner := "Session saved: " + id + " — resume with: lectern code -s " + id
	pollUntil(t, 10*time.Second, "the exit banner", func() bool {
		return strings.Contains(readFile(t, first.out), banner)
	})
	if !strings.Contains(sessions[0].Title, "greeting") {
		t.Errorf("session title %q, want the prompt", sessions[0].Title)
	}
	msgs, err := session.NewStore(repo).LoadMessages(id)
	if err != nil {
		t.Fatalf("session file unreadable: %v", err)
	}
	if n := countUserMessages(msgs, "greeting"); n != 1 {
		t.Fatalf("the prompt should be saved once, found %d", n)
	}
	if len(msgs) == 0 {
		t.Fatal("the session saved no messages")
	}
	if last := msgs[len(msgs)-1]; last.Role != llm.RoleUser {
		t.Fatalf("the interrupted turn should end on the prompt, ends with %s", last.Role)
	}

	// Second run: --continue re-runs the interrupted turn from the saved history.
	second := startLecternPane(t, sock, "second", work, env,
		bin, "code", "--continue", "--cwd", repo)
	pollUntil(t, 60*time.Second, "the continued turn to reach the model", func() bool { return stall.count() >= 2 })
	stopLectern(t, second)

	if got := stall.userMessagesWithText(t, 2, "greeting"); got != 1 {
		t.Fatalf("the continued request carries the prompt %d times, want 1", got)
	}
	if n := len(session.NewStore(repo).List()); n != 1 {
		t.Fatalf("continue must reuse the session, have %d sessions", n)
	}
	pollUntil(t, 10*time.Second, "the second exit banner", func() bool {
		return strings.Contains(readFile(t, second.out), banner)
	})
}

// buildLecternBinary compiles this package into dir.
func buildLecternBinary(t *testing.T, dir string) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		goBin = "/usr/local/go/bin/go"
	}
	bin := filepath.Join(dir, "lectern")
	if out, err := exec.Command(goBin, "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build lectern: %v\n%s", err, out)
	}
	return bin
}

// newScribeRepo makes a git repository with one commit at dir.
func newScribeRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "scribe@example.test"},
		{"config", "user.name", "Scribe Test"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// writeScribeConfig points a user provider at base, so the model is local.
func writeScribeConfig(t *testing.T, home, base string) {
	t.Helper()
	cfg := map[string]any{
		"model": "stall/fake-model",
		"providers": []any{map[string]any{
			"id": "stall", "name": "Stall", "base_url": base + "/v1",
			"api_key_env": "STALL_KEY", "models": []string{"fake-model"},
		}},
	}
	b, _ := json.Marshal(cfg)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// stallModel is an OpenAI-compatible server that records each request and
// never answers, so a turn stays in progress until it is stopped.
type stallModel struct {
	URL     string
	mu      sync.Mutex
	bodies  []string
	release chan struct{}
}

func newStallModel(t *testing.T) *stallModel {
	t.Helper()
	s := &stallModel{release: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only model calls count. Scribe also lists models at start (a GET),
		// which must not be mistaken for the prompt arriving.
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, string(b))
		s.mu.Unlock()
		select {
		case <-s.release:
		case <-r.Context().Done():
		}
	}))
	s.URL = srv.URL
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(s.release) }) // runs first, so Close does not wait on a handler
	return s
}

func (s *stallModel) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

// userMessagesWithText counts the user messages of the nth request (1-based)
// that contain text.
func (s *stallModel) userMessagesWithText(t *testing.T, nth int, text string) int {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) < nth {
		t.Fatalf("only %d requests reached the model", len(s.bodies))
	}
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(s.bodies[nth-1]), &req); err != nil {
		t.Fatalf("request %d is not JSON: %v", nth, err)
	}
	n := 0
	for _, m := range req.Messages {
		raw, _ := json.Marshal(m.Content)
		if m.Role == "user" && strings.Contains(string(raw), text) {
			n++
		}
	}
	return n
}

// lecternPane is one `lectern code` process in a tmux pane.
type lecternPane struct {
	sock, name string
	pid        int
	out        string // everything the pane printed
}

// startLecternPane runs argv in a new pane of the private tmux server sock.
// The process is exec'd, so its pid is the pane's pid, and its output goes to
// a file the test can read.
func startLecternPane(t *testing.T, sock, name, work string, env []string, argv ...string) *lecternPane {
	t.Helper()
	quoted := []string{"exec", "env", "-i"}
	for _, kv := range env {
		quoted = append(quoted, tmuxQuote(kv))
	}
	for _, a := range argv {
		quoted = append(quoted, tmuxQuote(a))
	}
	tmuxCmd(t, sock, "new-session", "-d", "-s", name, "-x", "120", "-y", "40", strings.Join(quoted, " "))
	p := &lecternPane{sock: sock, name: name, out: filepath.Join(work, name+".out")}
	tmuxCmd(t, sock, "pipe-pane", "-t", name, "-o", "cat >> "+tmuxQuote(p.out))
	pid, err := strconv.Atoi(tmuxCmd(t, sock, "display", "-p", "-t", name, "#{pane_pid}"))
	if err != nil {
		t.Fatalf("pane pid: %v", err)
	}
	p.pid = pid
	t.Cleanup(func() {
		if syscall.Kill(p.pid, 0) == nil {
			_ = syscall.Kill(p.pid, syscall.SIGKILL)
		}
	})
	return p
}

// stopLectern sends SIGTERM, the way a terminal going away or a service
// manager stopping the process does, and waits for the process to exit.
func stopLectern(t *testing.T, p *lecternPane) {
	t.Helper()
	if err := syscall.Kill(p.pid, syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM %d: %v", p.pid, err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for syscall.Kill(p.pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("lectern did not exit after SIGTERM; pane output:\n%s", readFile(t, p.out))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// tmuxCmd runs tmux on the private socket and returns its trimmed output.
func tmuxCmd(t *testing.T, sock string, args ...string) string {
	t.Helper()
	out, err := exec.Command("tmux", append([]string{"-L", sock, "-f", "/dev/null"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func tmuxQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func countUserMessages(msgs []llm.Message, text string) int {
	n := 0
	for _, m := range msgs {
		if m.Role == llm.RoleUser && strings.Contains(m.Text(), text) {
			n++
		}
	}
	return n
}

// pollUntil polls cond until it holds or the timeout passes.
func pollUntil(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
