package fs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// testFiles is a FileTracker that records read stamps (size and mtime) the way
// the agent's checkpoint tracker does.
type testFiles struct {
	mu     sync.Mutex
	stamps map[string]stamp
	writes []string
}

type stamp struct {
	size  int64
	mtime time.Time
}

func newTestFiles() *testFiles { return &testFiles{stamps: map[string]stamp{}} }

func (f *testFiles) BeforeWrite(abs string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, abs)
}

func (f *testFiles) MarkRead(abs string) {
	fi, err := os.Stat(abs)
	if err != nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stamps[abs] = stamp{fi.Size(), fi.ModTime()}
}

func (f *testFiles) WasRead(abs string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.stamps[abs]
	return ok
}

func (f *testFiles) StaleSinceRead(abs string) bool {
	f.mu.Lock()
	s, ok := f.stamps[abs]
	f.mu.Unlock()
	if !ok {
		return false
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return true
	}
	return fi.Size() != s.size || !fi.ModTime().Equal(s.mtime)
}

// testHost answers approvals with a fixed decision and records them.
type testHost struct {
	mu        sync.Mutex
	decision  tools.Decision
	approvals []tools.Approval
}

func (h *testHost) Approve(_ context.Context, a tools.Approval) tools.Decision {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.approvals = append(h.approvals, a)
	return h.decision
}

func (h *testHost) Ask(context.Context, tools.Question) (string, error) { return "", nil }
func (h *testHost) Progress(string, string)                             {}

func (h *testHost) kinds() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, a := range h.approvals {
		out = append(out, a.Kind)
	}
	return out
}

// newEnv makes an Env over a fresh temp project directory.
func newEnv(t *testing.T) (*tools.Env, string, *testFiles, *testHost) {
	t.Helper()
	root := t.TempDir()
	// Resolve symlinks in the temp root itself (macOS /var -> /private/var).
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	files := newTestFiles()
	host := &testHost{decision: tools.Deny}
	env := &tools.Env{Workdir: root, Host: host, Files: files}
	return env, root, files, host
}

// call runs tool with args marshalled from v.
func call(t *testing.T, tool tools.Tool, env *tools.Env, v any) tools.Result {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Run(context.Background(), env, b)
}

// write creates a file under root with the given content, making dirs.
func write(t *testing.T, root, rel, content string) string {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return abs
}

func readFile(t *testing.T, abs string) string {
	t.Helper()
	b, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// bump gives a file a clearly later mtime, so stale checks see a change even
// on filesystems with coarse timestamps.
func bump(t *testing.T, abs string) {
	t.Helper()
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(abs, later, later); err != nil {
		t.Fatal(err)
	}
}

func mustOK(t *testing.T, r tools.Result) tools.Result {
	t.Helper()
	if r.IsError {
		t.Fatalf("unexpected error: %s", r.Output)
	}
	return r
}

func mustErr(t *testing.T, r tools.Result, contains string) {
	t.Helper()
	if !r.IsError {
		t.Fatalf("expected error containing %q, got ok: %s", contains, r.Output)
	}
	if !strings.Contains(r.Output, contains) {
		t.Fatalf("error %q does not contain %q", r.Output, contains)
	}
}

func TestReadNumbersLinesAndPages(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.txt", "one\ntwo\r\nthree\n")
	r := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "a.txt"}))
	want := "     1  one\n     2  two\n     3  three"
	if r.Output != want {
		t.Fatalf("output:\n%q\nwant:\n%q", r.Output, want)
	}
	if r.Summary != "3 lines" {
		t.Fatalf("summary %q", r.Summary)
	}
	r = mustOK(t, call(t, NewRead(), env, map[string]any{"path": "a.txt", "offset": 2, "limit": 1}))
	if !strings.HasPrefix(r.Output, "     2  two") || !strings.Contains(r.Output, "1 more lines; call read with offset=3") {
		t.Fatalf("paged output: %q", r.Output)
	}
	if r.Summary != "1 of 3 lines" {
		t.Fatalf("paged summary %q", r.Summary)
	}
}

func TestReadMultipleFilesHeaders(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.txt", "alpha\n")
	write(t, root, "b.txt", "beta\n")
	r := mustOK(t, call(t, NewRead(), env, map[string]any{"paths": []string{"a.txt", "b.txt"}}))
	if !strings.Contains(r.Output, "── a.txt ──\n     1  alpha") || !strings.Contains(r.Output, "── b.txt ──\n     1  beta") {
		t.Fatalf("multi output: %q", r.Output)
	}
}

func TestReadCapsLinesWithContinuationHint(t *testing.T) {
	env, root, _, _ := newEnv(t)
	var sb strings.Builder
	for i := 0; i < 2500; i++ {
		sb.WriteString("x\n")
	}
	write(t, root, "big.txt", sb.String())
	r := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "big.txt"}))
	if !strings.Contains(r.Output, "… 500 more lines; call read with offset=2001 to continue.") {
		t.Fatalf("missing continuation hint; tail: %q", r.Output[len(r.Output)-200:])
	}
}

func TestReadErrors(t *testing.T) {
	env, root, _, _ := newEnv(t)
	mustErr(t, call(t, NewRead(), env, map[string]any{"path": "nope.txt"}), "File not found")
	bin := filepath.Join(root, "x.bin")
	os.WriteFile(bin, []byte{1, 2, 0, 3}, 0o644)
	mustErr(t, call(t, NewRead(), env, map[string]any{"path": "x.bin"}), "binary")
	write(t, root, "sniff.dat", "abc\x00def")
	mustErr(t, call(t, NewRead(), env, map[string]any{"path": "sniff.dat"}), "binary")
	write(t, root, "two.txt", "a\nb\n")
	mustErr(t, call(t, NewRead(), env, map[string]any{"path": "two.txt", "offset": 9}), "past the end")
	mustErr(t, call(t, NewRead(), env, map[string]any{}), "needs path")
}

func TestReadDirectoryListsAndImageIsMeta(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "d/z.go", "package z\n")
	os.MkdirAll(filepath.Join(root, "d", "sub"), 0o755)
	r := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "d"}))
	if !strings.HasPrefix(r.Output, "d/\n") || !strings.Contains(r.Output, "sub/") || !strings.Contains(r.Output, "z.go") {
		t.Fatalf("listing: %q", r.Output)
	}
	os.WriteFile(filepath.Join(root, "pic.png"), []byte("not really a png"), 0o644)
	r = mustOK(t, call(t, NewRead(), env, map[string]any{"path": "pic.png"}))
	if r.Meta["image"] != "pic.png" {
		t.Fatalf("Meta[image] = %v", r.Meta["image"])
	}
}

func TestReadMarksFileRead(t *testing.T) {
	env, root, files, _ := newEnv(t)
	abs := write(t, root, "m.txt", "x\n")
	if files.WasRead(abs) {
		t.Fatal("read before any read")
	}
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "m.txt"}))
	if !files.WasRead(abs) {
		t.Fatal("read did not call MarkRead")
	}
}

func TestSecretReadNeedsApproval(t *testing.T) {
	env, root, _, host := newEnv(t)
	write(t, root, ".env", "TOKEN=abc\n")
	mustErr(t, call(t, NewRead(), env, map[string]any{"path": ".env"}), "Denied: the user chose Deny when asked to read a file that looks like secrets")
	if got := host.kinds(); len(got) != 1 || got[0] != "sensitive" {
		t.Fatalf("approvals %v", got)
	}
	host.decision = tools.AllowOnce
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": ".env"}))
}

func TestSecretReadWithoutHostRefuses(t *testing.T) {
	env, root, _, _ := newEnv(t)
	env.Host = nil
	write(t, root, "id_rsa", "key")
	mustErr(t, call(t, NewRead(), env, map[string]any{"path": "id_rsa"}), "looks like secrets")
}

func TestSymlinkToSecretIsJudgedByTarget(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "keys/server.pem", "pem")
	if err := os.Symlink(filepath.Join(root, "keys", "server.pem"), filepath.Join(root, "innocent.txt")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	mustErr(t, call(t, NewRead(), env, map[string]any{"path": "innocent.txt"}), "looks like secrets")
}

func TestIsSecretName(t *testing.T) {
	yes := []string{".env", ".env.local", "server.pem", "x.key", "id_rsa", "id_ed25519.pub", "credentials.json", "secrets.yaml", "service-account-prod.json", ".npmrc"}
	no := []string{"main.go", "environment.go", "keyboard.txt", "README.md", "secret_sauce.go"}
	for _, n := range yes {
		if !isSecretName(n) {
			t.Errorf("%s should be secret", n)
		}
	}
	for _, n := range no {
		if isSecretName(n) {
			t.Errorf("%s should not be secret", n)
		}
	}
}

func TestWriteOutsideNeedsApprovalAndAutoSkipsIt(t *testing.T) {
	env, _, _, host := newEnv(t)
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	mustErr(t, call(t, NewWriteFile(nil), env, map[string]any{"path": outside, "content": "x"}), "Denied")
	if got := host.kinds(); len(got) != 1 || got[0] != "write_outside" {
		t.Fatalf("approvals %v", got)
	}
	env.Mode = "auto"
	mustOK(t, call(t, NewWriteFile(nil), env, map[string]any{"path": outside, "content": "x"}))
	if readFile(t, outside) != "x" {
		t.Fatal("auto mode did not write outside file")
	}
}

func TestWriteInsideGitRefused(t *testing.T) {
	env, root, _, _ := newEnv(t)
	mustErr(t, call(t, NewWriteFile(nil), env, map[string]any{"path": ".git/config", "content": "x"}), "inside .git")
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		t.Fatal(".git was created")
	}
	write(t, root, ".git/HEAD", "ref")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": ".git/HEAD"}))
}

func TestWriteSensitiveNeedsApproval(t *testing.T) {
	env, _, _, host := newEnv(t)
	mustErr(t, call(t, NewWriteFile(nil), env, map[string]any{"path": ".github/workflows/ci.yml", "content": "x"}), "Denied: the user chose Deny when asked to edit sensitive file")
	if got := host.kinds(); len(got) != 1 || got[0] != "sensitive" {
		t.Fatalf("approvals %v", got)
	}
}
