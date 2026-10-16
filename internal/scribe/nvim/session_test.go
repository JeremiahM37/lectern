package nvim

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestRPCTalksToRealNeovim(t *testing.T) {
	file := writeFile(t, "a.txt", "one", "two")
	s := startNvim(t, file)
	if v := eval(t, s, "1+1"); v != int64(2) {
		t.Fatalf("1+1 = %#v", v)
	}
	if got := eval(t, s, "expand('%:p')"); got != file {
		t.Fatalf("current file %v, want %s", got, file)
	}
}

func TestFollowOpensAndJumpsToTheLine(t *testing.T) {
	file := writeFile(t, "f.txt", "l1", "l2", "l3", "l4", "l5", "l6")
	other := writeFile(t, "g.txt", "x")
	s := startNvim(t, other)
	if res := s.Follow(file, 4); res != "followed" {
		t.Fatalf("follow = %q", res)
	}
	if got := eval(t, s, "expand('%:p')"); got != file {
		t.Fatalf("editor shows %v, want %s", got, file)
	}
	if line := eval(t, s, "line('.')"); line != int64(4) {
		t.Fatalf("cursor on line %v, want 4", line)
	}
}

func TestFollowReloadsAnUnmodifiedBuffer(t *testing.T) {
	file := writeFile(t, "r.txt", "old")
	s := startNvim(t, file)
	if err := os.WriteFile(file, []byte("new line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := s.Follow(file, 0); res != "followed" {
		t.Fatalf("follow = %q", res)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	v, err := s.Lua(ctx, `return vim.api.nvim_buf_get_lines(0, 0, 1, false)[1]`)
	if err != nil {
		t.Fatal(err)
	}
	if v != "new line" {
		t.Fatalf("buffer shows %q after the agent's write", v)
	}
}

func TestFollowKeepsUnsavedUserEdits(t *testing.T) {
	file := writeFile(t, "u.txt", "disk")
	s := startNvim(t, file)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.Lua(ctx, `vim.api.nvim_buf_set_lines(0, 0, -1, false, {"typed"})`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := s.Follow(file, 1); res != "modified" {
		t.Fatalf("follow = %q, want modified", res)
	}
	v, _ := s.Lua(ctx, `return vim.api.nvim_buf_get_lines(0, 0, 1, false)[1]`)
	if v != "typed" {
		t.Fatalf("the user's unsaved edit was lost: buffer shows %q", v)
	}
}

func TestFollowLeavesAnUnsavedCurrentBufferAlone(t *testing.T) {
	cur := writeFile(t, "cur.txt", "c")
	other := writeFile(t, "other.txt", "o")
	s := startNvim(t, cur)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.Lua(ctx, `vim.api.nvim_buf_set_lines(0, 0, -1, false, {"dirty"})`); err != nil {
		t.Fatal(err)
	}
	if res := s.Follow(other, 1); res != "busy" {
		t.Fatalf("follow = %q, want busy", res)
	}
	if got := eval(t, s, "expand('%:p')"); got != cur {
		t.Fatalf("editor left %v for %s", got, other)
	}
}

func TestUserSaveReachesTheWriteHook(t *testing.T) {
	file := writeFile(t, "w.txt", "x")
	s := startNvim(t, file)
	got := make(chan string, 4)
	SetWriteHook(func(abs string) { got <- abs })
	t.Cleanup(func() { SetWriteHook(nil) })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.Lua(ctx, `vim.api.nvim_buf_set_lines(0, 0, -1, false, {"saved"}); vim.cmd("write")`); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-got:
		if p != file {
			t.Fatalf("hook got %s, want %s", p, file)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the user's save did not reach the write hook")
	}
}

func TestAgentWriteIsNotReportedAsUserSave(t *testing.T) {
	file := writeFile(t, "t.txt", "x")
	s := startNvim(t, file)
	got := make(chan string, 4)
	SetWriteHook(func(abs string) { got <- abs })
	t.Cleanup(func() { SetWriteHook(nil) })

	s.markToolWrite(file)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.Lua(ctx, `vim.cmd("write")`); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-got:
		t.Fatalf("agent write reported as a user save: %s", p)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestCloseRemovesSocketAndClearsCurrent(t *testing.T) {
	file := writeFile(t, "c.txt", "x")
	s := startNvim(t, file)
	sock := s.socket
	if Current() != s {
		t.Fatal("started editor is not the current one")
	}
	s.Close()
	if Current() == s {
		t.Fatal("closed editor is still current")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket %s not removed: %v", sock, err)
	}
	if _, err := s.Call(context.Background(), "nvim_eval", "1"); err == nil {
		t.Fatal("call on a closed editor succeeded")
	}
}

func TestNonNeovimEditorHasNoRPC(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("cat missing")
	}
	s, err := Start([]string{"cat"}, t.TempDir(), 40, 10)
	if err != nil {
		t.Skipf("no pseudo-terminal here: %v", err)
	}
	t.Cleanup(s.Close)
	if s.socket != "" {
		t.Fatal("a non-Neovim editor got a socket")
	}
	if _, err := s.Call(context.Background(), "nvim_eval", "1"); err == nil {
		t.Fatal("RPC on a non-Neovim editor succeeded")
	}
	if !IsNvim([]string{filepath.Join("/usr/bin", "nvim")}) || IsNvim([]string{"vim"}) {
		t.Fatal("IsNvim misclassifies the editor")
	}
}

func TestSweepRemovesOnlyDeadOwnersSockets(t *testing.T) {
	dead := filepath.Join(os.TempDir(), "scribe-nvim-999999999-1-x.sock")
	live := filepath.Join(os.TempDir(), "scribe-nvim-"+strconv.Itoa(os.Getpid())+"-1-x.sock")
	for _, p := range []string{dead, live} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(p) })
	}
	sweepStale()
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatal("a socket of a dead Scribe was kept")
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatal("a socket of a live process was removed")
	}
}

// TestFollowThenReportsUnsavedEdits: the app warns the user when the agent
// changed a file they are editing, so FollowThen must hand back "modified".
func TestFollowThenReportsUnsavedEdits(t *testing.T) {
	file := writeFile(t, "w.txt", "disk")
	s := startNvim(t, file)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.Lua(ctx, `vim.api.nvim_buf_set_lines(0, 0, -1, false, {"typed"})`); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	FollowThen(file, 0, func(res string) { got <- res })
	select {
	case res := <-got:
		if res != "modified" {
			t.Fatalf("FollowThen reported %q, want modified", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("FollowThen never reported")
	}
}
