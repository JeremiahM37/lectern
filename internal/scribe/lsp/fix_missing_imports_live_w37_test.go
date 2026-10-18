package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// TestLiveW37FixMissingImportsWithGopls runs FixMissingImports against the real
// gopls binary. Guarded by SCRIBE_LIVE_GOPLS=1 because it starts gopls and
// needs the Go toolchain. Nothing outside a t.TempDir is written, and no
// process is killed by name.
func TestLiveW37FixMissingImportsWithGopls(t *testing.T) {
	if os.Getenv("SCRIBE_LIVE_GOPLS") != "1" {
		t.Skip("set SCRIBE_LIVE_GOPLS=1 to run against the real gopls")
	}
	const goplsDir = "/home/admin/go/bin"
	if _, err := os.Stat(filepath.Join(goplsDir, "gopls")); err != nil {
		t.Skipf("gopls not at %s: %v", goplsDir, err)
	}
	t.Setenv("PATH", goplsDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/w37live\n\ngo 1.22\n")
	abs := filepath.Join(dir, "demo.go")
	src := "package demo\n\nfunc Run() {\n\tfmt.Println(strings.ToUpper(\"x\"))\n}\n"
	mustWrite(t, abs, src)

	m := NewManager(dir)
	defer m.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	trk := newReadTracker()
	trk.MarkRead(abs)
	env := &tools.Env{Workdir: dir, Files: trk}

	title, changes, err := m.FixMissingImports(ctx, env, abs, true)
	if err != nil {
		t.Fatalf("FixMissingImports: %v", err)
	}
	if title == "" || len(changes) == 0 {
		t.Fatalf("no fix applied: title %q, %d changes", title, len(changes))
	}
	t.Logf("fix chosen: %q, %d file change(s)", title, len(changes))

	got := mustRead(t, abs)
	if !strings.Contains(got, `"fmt"`) || !strings.Contains(got, `"strings"`) {
		t.Fatalf("imports not added:\n%s", got)
	}

	build := exec.CommandContext(ctx, "/usr/local/go/bin/go", "build", "./...")
	build.Dir = dir
	build.Env = append(os.Environ(), "PATH="+goplsDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s\nfile:\n%s", err, out, mustRead(t, abs))
	}

	// A successful fix must leave the server healthy, not degraded.
	for _, s := range m.Sessions() {
		if s.Language == "go" && s.State == StateDegraded {
			t.Fatalf("gopls degraded after a successful fix: %+v", s)
		}
	}
	t.Logf("sessions after fix: %+v", m.Sessions())
}
