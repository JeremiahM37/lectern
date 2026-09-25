package onboard

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakePath builds a directory containing an executable stub for each given
// name and points PATH at it (and only it), restoring the real PATH after
// the test. This is how "detect installed agent CLIs" is tested without
// depending on what's actually installed on the machine running the suite.
func fakePath(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		bin := name
		if runtime.GOOS == "windows" {
			bin += ".bat"
		}
		path := filepath.Join(dir, bin)
		script := "#!/bin/sh\nexit 0\n"
		if runtime.GOOS == "windows" {
			script = "@echo off\r\n"
		}
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := os.Getenv("PATH")
	if err := os.Setenv("PATH", dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", old) })
	return dir
}

func TestDetectAgentsFindsWhatsOnPath(t *testing.T) {
	fakePath(t, "claude", "aider")
	agents := DetectAgents("", "", "")
	found := map[string]AgentCheck{}
	for _, a := range agents {
		found[a.Name] = a
	}
	if !found["claude"].Found {
		t.Errorf("claude should be found: %+v", found["claude"])
	}
	if !found["claude"].Builtin {
		t.Errorf("claude must be reported builtin")
	}
	if found["codex"].Found {
		t.Errorf("codex should not be found on an empty-but-for-claude/aider PATH")
	}
	if !found["aider"].Found {
		t.Errorf("aider should be found: %+v", found["aider"])
	}
	if found["aider"].Builtin {
		t.Errorf("aider must not be reported builtin")
	}
}

func TestDetectAgentsHonorsBinOverride(t *testing.T) {
	dir := fakePath(t, "my-claude-fork")
	agents := DetectAgents(filepath.Join(dir, "my-claude-fork"), "", "")
	for _, a := range agents {
		if a.Name == "claude" {
			if !a.Found {
				t.Fatalf("configured claude bin override should resolve: %+v", a)
			}
			return
		}
	}
	t.Fatal("claude missing from report")
}

func TestDetectAgentsNoneFound(t *testing.T) {
	fakePath(t) // empty PATH dir
	for _, a := range DetectAgents("", "", "") {
		if a.Found {
			t.Errorf("%s should not be found on an empty PATH", a.Name)
		}
	}
}

func TestCheckTmuxAndGit(t *testing.T) {
	fakePath(t, "tmux", "git")
	if tmux := CheckTmux(); !tmux.OK {
		t.Errorf("tmux should be OK: %+v", tmux)
	}
	if git := CheckGit(); !git.OK {
		t.Errorf("git should be OK: %+v", git)
	}
	fakePath(t) // now neither is on PATH
	if tmux := CheckTmux(); tmux.OK || tmux.Fix == "" {
		t.Errorf("missing tmux should be reported not-OK with a fix: %+v", tmux)
	}
	if git := CheckGit(); git.OK || git.Fix == "" {
		t.Errorf("missing git should be reported not-OK with a fix: %+v", git)
	}
}
