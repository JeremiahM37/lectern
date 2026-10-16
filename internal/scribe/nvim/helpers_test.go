package nvim

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requireNvim skips the test when Neovim is not installed.
func requireNvim(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("nvim is not installed")
	}
}

// startNvim runs an isolated Neovim (-u NONE, no shada) on file and waits
// until its RPC connection is up.
func startNvim(t *testing.T, file string) *Session {
	t.Helper()
	requireNvim(t)
	s, err := Start([]string{"nvim", "-u", "NONE", "-i", "NONE", file}, filepath.Dir(file), 80, 24)
	if err != nil {
		t.Skipf("no pseudo-terminal here: %v", err)
	}
	t.Cleanup(s.Close)
	waitReady(t, s)
	return s
}

// waitReady blocks until the RPC connection is up, failing the test if not.
func waitReady(t *testing.T, s *Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := s.wait(ctx); err != nil {
		t.Fatalf("editor did not connect: %v", err)
	}
}

// writeFile writes lines to a fresh file in a temp dir.
func writeFile(t *testing.T, name string, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// eval evaluates a Vim expression in the editor.
func eval(t *testing.T, s *Session, expr string) any {
	t.Helper()
	v, err := s.Call(context.Background(), "nvim_eval", expr)
	if err != nil {
		t.Fatalf("eval %q: %v", expr, err)
	}
	return v
}
