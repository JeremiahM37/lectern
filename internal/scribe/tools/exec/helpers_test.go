package exec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// fakeHost is a tools.Host that records what it was asked and answers
// approvals with a fixed decision. It is the only fake in these tests; every
// command runs for real.
type fakeHost struct {
	decision tools.Decision

	mu        sync.Mutex
	approvals []tools.Approval
	progress  []string
}

func (h *fakeHost) Approve(_ context.Context, a tools.Approval) tools.Decision {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.approvals = append(h.approvals, a)
	return h.decision
}

func (h *fakeHost) Ask(context.Context, tools.Question) (string, error) {
	return "", errors.New("fakeHost does not answer questions")
}

func (h *fakeHost) Progress(_, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.progress = append(h.progress, text)
}

func (h *fakeHost) approvalCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.approvals)
}

func (h *fakeHost) progressLines() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.progress...)
}

// newTestRegistry registers the exec tools with opts and returns the jobs.
func newTestRegistry(t *testing.T, opts Options) (*tools.Registry, *Jobs) {
	t.Helper()
	r := tools.NewRegistry()
	jobs := NewJobs(opts)
	Register(r, jobs)
	t.Cleanup(jobs.KillAll)
	return r, jobs
}

func newTestEnv(dir string, host tools.Host) *tools.Env {
	return &tools.Env{Workdir: dir, CallID: "call-1", Host: host, Mode: "default"}
}

// runTool looks up name in r and runs it with args marshalled as JSON.
func runTool(t *testing.T, r *tools.Registry, name string, env *tools.Env, args any) tools.Result {
	t.Helper()
	tool, ok := r.Get(name)
	if !ok {
		t.Fatalf("tool %q is not registered", name)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Run(context.Background(), env, raw)
}

// initRepo creates a git repository with a local identity in a temp dir.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.name", "Scribe Test")
	gitIn(t, dir, "config", "user.email", "scribe@test.invalid")
	gitIn(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

// gitIn runs git in dir for test setup and fails the test on error.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// skipOnWindows skips tests that rely on POSIX shells and process groups.
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell behaviour")
	}
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", d, what)
}

// hasLine reports whether any element of lines contains sub.
func containsAny(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}
