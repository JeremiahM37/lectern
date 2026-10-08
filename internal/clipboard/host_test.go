package clipboard_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/clipboard"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// The machine-side half end to end, as a launch and a mirror use it: Prepare
// installs the shims and starts the headless clipboard through the real
// binary, Mirror fills it, and the shim path and the native X path both see it.
func TestPrepareAndMirrorOnALocalMachine(t *testing.T) {
	if _, err := exec.LookPath("Xvfb"); err != nil {
		t.Skip("Xvfb is not installed")
	}
	bin := filepath.Join(t.TempDir(), "lectern")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/JeremiahM37/lectern/v2/cmd/lectern").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	rt, err := os.MkdirTemp("", "lcb")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", rt)
	t.Cleanup(func() { exec.Command("pkill", "-f", rt).Run(); os.RemoveAll(rt) })

	ex := executor.NewLocal()
	env, err := clipboard.Prepare(context.Background(), ex, bin)
	if err != nil || env.Display == "" || env.Shims == "" {
		t.Fatalf("prepare: %+v %v", env, err)
	}
	for _, tool := range []string{"wl-paste", "xclip"} {
		if st, err := os.Stat(filepath.Join(env.Shims, tool)); err != nil || st.Mode()&0o111 == 0 {
			t.Fatalf("%s shim: %v", tool, err)
		}
	}
	if a := env.EnvAssignments(); !strings.Contains(a, "DISPLAY=") || !strings.Contains(a, `:"$PATH"`) {
		t.Fatalf("assignments: %s", a)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte("pixels"), 40000)...)
	if err := clipboard.Mirror(context.Background(), ex, bin, "image/png", png); err != nil {
		t.Fatal(err)
	}
	// a native X reader finds it where the session's DISPLAY points (xclip
	// stands in for a toolkit/arboard-style reader when it is installed)
	if x, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.Command(x, "-selection", "clipboard", "-t", "image/png", "-o")
		cmd.Env = append(os.Environ(), "DISPLAY="+env.Display, "XAUTHORITY="+env.Xauthority)
		got, err := cmd.Output()
		if err != nil || !bytes.Equal(got, png) {
			t.Fatalf("native read: %v len=%d", err, len(got))
		}
	}
}
