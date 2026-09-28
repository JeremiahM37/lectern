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

type splitAnswer struct {
	Dir     string   `json:"dir"`
	Argv    []string `json:"attach_argv"`
	Session struct {
		ID          int64  `json:"id"`
		Agent       string `json:"agent"`
		Workdir     string `json:"workdir"`
		TmuxSession string `json:"tmux_session"`
	} `json:"session"`
}

// A split of a native attachment is a new tracked shell session on the
// session's target, in the directory its agent pane is in now
// (docs/terminal-client.md).
func TestTerminalSplitOpensAShellWhereTheAgentIs(t *testing.T) {
	f := newFileRig(t)
	sub := filepath.Join(f.root, "sub dir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	code, raw, _ := f.get("/info")
	var info struct {
		TmuxSession string `json:"tmux_session"`
	}
	if json.Unmarshal(raw, &info); code != 200 || info.TmuxSession == "" {
		t.Fatalf("info: %d %s", code, raw)
	}
	mustRun(t, f.root, "tmux", "send-keys", "-t", "="+info.TmuxSession+":", "cd '"+sub+"'", "Enter")
	real, _ := filepath.EvalSymlinks(sub)
	// Wait until the agent's shell is there.
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, _ := runOut(f.root, "tmux", "display-message", "-p", "-t", "="+info.TmuxSession+":", "#{pane_current_path}")
		if strings.TrimSpace(out) == real {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the agent pane never reached %s: %q", real, out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	code, raw = f.h.request("POST", f.base+"/split", nil, nil)
	var out splitAnswer
	json.Unmarshal(raw, &out)
	if code != 201 || out.Dir != real || out.Session.Agent != "shell" || out.Session.Workdir != real || out.Session.TmuxSession == "" {
		t.Fatalf("split: %d %s", code, raw)
	}
	if got := strings.Join(out.Argv, " "); got != "tmux attach -t "+out.Session.TmuxSession+" ; set-option -w -t ="+out.Session.TmuxSession+": window-size latest" {
		t.Fatalf("attach command: %s", got)
	}
	// The tracked shell is running there.
	cwd, err := runOut(f.root, "tmux", "display-message", "-p", "-t", "="+out.Session.TmuxSession+":", "#{pane_current_path}")
	if err != nil || strings.TrimSpace(cwd) != real {
		t.Fatalf("shell pane: %v %q", err, cwd)
	}
	// ?dir=workdir opens it in the session's workdir instead.
	code, raw = f.h.request("POST", f.base+"/split?dir=workdir", nil, nil)
	json.Unmarshal(raw, &out)
	if code != 201 || out.Dir != f.root {
		t.Fatalf("workdir split: %d %s", code, raw)
	}
	if code, _ = f.h.request("POST", f.base+"/split?dir=elsewhere", nil, nil); code != 400 {
		t.Fatalf("a client-chosen directory was accepted: %d", code)
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
	code, raw := f.h.request("POST", "/api/term/session/"+itoa(session.ID)+"/split", nil, nil)
	var out splitAnswer
	json.Unmarshal(raw, &out)
	if code != 201 || out.Dir != f.root || out.Session.Workdir != f.root {
		t.Fatalf("fallback: %d %s", code, raw)
	}
}

func runOut(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}
