package api_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// A split of a native attachment is a shell on the session's target in the
// directory its agent pane is in now (docs/terminal-client.md).
func TestTerminalSplitOpensAShellWhereTheAgentIs(t *testing.T) {
	f := newFileRig(t)
	sub := filepath.Join(f.root, "sub dir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	var session struct{ TmuxSession string }
	code, raw, _ := f.get("/info")
	if code != 200 {
		t.Fatalf("info: %d %s", code, raw)
	}
	json.Unmarshal(raw, &struct {
		T *string `json:"tmux_session"`
	}{&session.TmuxSession})
	mustRun(t, f.root, "tmux", "send-keys", "-t", "="+session.TmuxSession+":", "cd '"+sub+"'", "Enter")
	real, _ := filepath.EvalSymlinks(sub)
	var out struct {
		Dir  string   `json:"dir"`
		Argv []string `json:"attach_argv"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, raw, _ = f.get("/split")
		json.Unmarshal(raw, &out)
		if code == 200 && out.Dir == real {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("split: %d %s, want dir %s", code, raw, real)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The command is a shell there.
	cmd := exec.Command(out.Argv[0], out.Argv[1:]...)
	cmd.Env = append(os.Environ(), "SHELL=/bin/sh")
	cmd.Stdin = strings.NewReader("pwd\nexit\n")
	shown, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(shown), real) {
		t.Fatalf("shell: %v %q", err, shown)
	}
}

// When the pane cannot be asked (its tmux session is gone), the shell opens
// in the session's workdir.
func TestTerminalSplitFallsBackToTheWorkdir(t *testing.T) {
	f := newFileRig(t)
	target, _ := f.h.App.DB.InsertTarget(&store.Target{Name: "gone", Kind: "local"})
	session, err := f.h.App.DB.InsertSession(&store.Session{TargetID: target.ID, Name: "gone", Workdir: f.root, TmuxSession: "no-such-session-anywhere", Status: "idle"})
	if err != nil {
		t.Fatal(err)
	}
	code, raw := f.h.request("GET", "/api/term/session/"+itoa(session.ID)+"/split", nil, nil)
	var out struct {
		Dir string `json:"dir"`
	}
	json.Unmarshal(raw, &out)
	if code != 200 || out.Dir != f.root {
		t.Fatalf("fallback: %d %s", code, raw)
	}
}
