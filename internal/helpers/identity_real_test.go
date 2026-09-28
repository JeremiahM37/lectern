package helpers

// Real panes: native identity and configured-home read a live process tree
// below a tmux pane, so these run only in the isolated test runner.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

const fakeClaude = `#!/bin/bash
start=$(sed 's/.*) //' /proc/$$/stat | cut -d' ' -f20)
mkdir -p "$CLAUDE_CONFIG_DIR/sessions"
printf '{"pid":%d,"procStart":"%s","kind":"interactive","entrypoint":"cli","cwd":"%s","sessionId":"%s"}' $$ "$start" "$PWD" "$FAKE_CID" > "$CLAUDE_CONFIG_DIR/sessions/$$.json.tmp"
mv "$CLAUDE_CONFIG_DIR/sessions/$$.json.tmp" "$CLAUDE_CONFIG_DIR/sessions/$$.json"
exec "$FAKE_BIN" 600
`

func tmuxSession(t *testing.T, name, dir, command string) {
	t.Helper()
	if out, err := exec.Command("tmux", "-f", "/dev/null", "new-session", "-d", "-s", name, "-c", dir, "--", "bash", "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("tmux: %v: %s", err, out)
	}
	if out, err := exec.Command("tmux", "set-option", "-t", "="+name+":", "@lectern-tracking-identity", strings.Repeat("ab", 16)).CombinedOutput(); err != nil {
		t.Fatalf("tmux set-option: %v: %s", err, out)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestNativeIdentityRealPaneParity(t *testing.T) {
	testutil.RequireIsolated(t)
	requirePython(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	root := t.TempDir()
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", filepath.Join(root, "tmux"))
	if err := os.MkdirAll(filepath.Join(root, "tmux"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testutil.CleanupTmux(t, filepath.Join(root, "tmux")) })
	work := filepath.Join(root, "work")
	claudeHome := filepath.Join(root, "claude-home")
	codexHome := filepath.Join(root, "codex-home")
	bin := filepath.Join(root, "bin")
	for _, d := range []string{work, claudeHome, filepath.Join(codexHome, "sessions"), bin} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Point the helper's own fallbacks somewhere empty, so only the
	// processes' environments can find the homes.
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")

	// Copies of sleep stand in for the CLIs, so each process's executable
	// has the agent's name.
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep unavailable")
	}
	data, err := os.ReadFile(sleep)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bin, name), data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(bin, "fake-claude")
	if err := os.WriteFile(script, []byte(fakeClaude), 0o700); err != nil {
		t.Fatal(err)
	}
	claudeCID := "11111111-1111-4111-8111-111111111111"
	tmuxSession(t, "lec-claude", work, "CLAUDE_CONFIG_DIR="+shq(claudeHome)+" FAKE_CID="+claudeCID+" FAKE_BIN="+shq(filepath.Join(bin, "claude"))+" exec "+shq(script))

	// The fake codex holds its transcript open, as the real CLI does.
	codexCID := "22222222-2222-4222-8222-222222222222"
	transcript := filepath.Join(codexHome, "sessions", "rollout-"+codexCID+".jsonl")
	writeFile(t, transcript, lines(`{"type":"session_meta","payload":{"id":"`+codexCID+`","cwd":`+jsonStr(work)+`,"source":"vscode"}}`), time.Time{})
	tmuxSession(t, "lec-codex", work, "exec 3<"+shq(transcript)+"; CODEX_HOME="+shq(codexHome)+" exec "+shq(filepath.Join(bin, "codex"))+" 600")

	waitFor(t, "the fake claude session file", func() bool {
		entries, _ := os.ReadDir(filepath.Join(claudeHome, "sessions"))
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				return true
			}
		}
		return false
	})
	waitFor(t, "the fake codex process", func() bool {
		out, _ := exec.Command("tmux", "display-message", "-p", "-t", "=lec-codex:", "#{pane_current_command}").Output()
		return strings.TrimSpace(string(out)) == "codex"
	})

	identity := strings.Repeat("ab", 16)
	type probe struct{ agent, ws, home, name, expected string }
	probes := []probe{
		{"claude", work, "", "lec-claude", identity},
		{"claude", work, claudeHome, "lec-claude", ""},
		{"claude", root, "", "lec-claude", identity},
		{"claude", work, "", "lec-claude", strings.Repeat("cd", 16)},
		{"claude", work, "", "lec-missing", ""},
		{"codex", work, "", "lec-codex", identity},
		{"codex", root, "", "lec-codex", identity},
		{"codex", work, codexHome, "lec-claude", identity},
	}
	identified := 0
	for _, p := range probes {
		for _, discovery := range []bool{false, true} {
			flag, goFlag := "False", ""
			if discovery {
				flag, goFlag = "True", "1"
			}
			out := same(t, p.agent+" "+p.name+" "+flag, identityCall(p.ws, p.home, p.name, p.expected, p.agent, flag), "native-identity",
				nil, []string{p.agent, p.ws, p.home, p.name, p.expected, goFlag})
			if strings.Contains(out, `"identified"`) {
				identified++
			}
		}
	}
	if identified < 4 {
		t.Errorf("only %d probes identified a conversation; the fixture is not exercising the evidence paths", identified)
	}

	homeScript, err := os.ReadFile("../sessions/configured_home.py")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"claude", "lec-claude"}, {"codex", "lec-codex"}, {"codex", "lec-claude"}, {"claude", "lec-missing"}, {"claude"}} {
		same(t, "configured home "+strings.Join(args, " "), string(homeScript), "configured-home", args, args)
	}
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
