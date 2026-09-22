package sessions

// This deliberately uses real tmux and a compiled executable. Mock executors
// cannot prove that native identity belongs to the interactive process rather
// than a shell, a sidecar, or a subagent.
import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

func TestCaptureNativeIDUsesInteractiveVSCodeTranscriptFD(t *testing.T) {
	testutil.RequireIsolated(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	root := t.TempDir()
	// Go tests are sometimes launched from a Lectern tmux pane.  An
	// inherited TMUX value makes tmux address the caller's client/server before
	// TMUX_TMPDIR is considered, so clear it before creating the fixture.
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", filepath.Join(root, "tmux"))
	if err := os.MkdirAll(filepath.Join(root, "tmux"), 0700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "workspace")
	home := filepath.Join(root, "codex")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", home)
	src := filepath.Join(root, "fake.go")
	if err := os.WriteFile(src, []byte(`package main
import("encoding/json";"os";"path/filepath";"time")
func main(){ cid:="11111111-1111-4111-8111-111111111111"; cwd:=os.Getenv("FAKE_CWD"); p:=filepath.Join(os.Getenv("CODEX_HOME"),"sessions","fixture.jsonl"); f,_:=os.OpenFile(p,os.O_CREATE|os.O_WRONLY|os.O_APPEND,0600); defer f.Close(); b,_:=json.Marshal(map[string]any{"type":"session_meta","payload":map[string]any{"id":cid,"cwd":cwd,"source":"vscode"}}); f.Write(append(b,'\n')); for { time.Sleep(time.Second) } }
`), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "codex")
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build fake codex: %v: %s", err, out)
	}
	name := "lec-native-fixture"
	ex := executor.NewLocal()
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", "="+name).Run() })
	cmd := "FAKE_CWD=" + shellQuoteForTest(work) + " CODEX_HOME=" + shellQuoteForTest(home) + " " + shellQuoteForTest(bin)
	if out, err := exec.Command("tmux", "-f", "/dev/null", "new-session", "-d", "-s", name, "-c", work, "--", "bash", "-c", cmd).CombinedOutput(); err != nil {
		t.Fatalf("tmux: %v: %s", err, out)
	}
	ctx := context.Background()
	identity := captureTrackingIdentity(ctx, ex, name)
	if identity == "" {
		t.Fatal("could not establish tmux tracking identity")
	}
	got := CaptureNativeID(ctx, ex, "codex", work, home, name, identity)
	if got != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("native identity = %q", got)
	}
	// Killing the fixture process must make the identity unavailable; a stale
	// transcript on disk alone cannot authorize recovery.
	if out, err := exec.Command("tmux", "kill-session", "-t", "="+name).CombinedOutput(); err != nil {
		t.Fatalf("kill tmux: %v: %s", err, out)
	}
	if got := CaptureNativeID(ctx, ex, "codex", work, home, name, identity); got != "" {
		t.Fatalf("identity survived process loss: %q", got)
	}
}

func shellQuoteForTest(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
