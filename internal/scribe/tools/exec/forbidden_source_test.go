package exec

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The shell refuses forbidden paths from the same sources the file tools use:
// the project forbidden.json and the session list in Services["forbidden"].
func TestShellHonoursProjectAndSessionForbiddenPatterns(t *testing.T) {
	skipOnWindows(t)
	t.Setenv("SCRIBE_HOME", t.TempDir())
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".scribe", "forbidden.json"), `{"patterns": ["*.log"]}`)
	writeFile(t, filepath.Join(dir, "notes", "plan.md"), "x\n")
	r, _ := newTestRegistry(t, Options{})

	env := newTestEnv(dir, nil)
	res := runTool(t, r, "shell", env, map[string]any{"command": "cat build.log"})
	if !res.IsError || !strings.Contains(res.Output, `matches forbidden pattern "*.log"`) {
		t.Fatalf("project forbidden.json should refuse build.log: %+v", res)
	}

	env.Services = map[string]any{"forbidden": []string{"notes/**"}}
	res = runTool(t, r, "shell", env, map[string]any{"command": "cat notes/plan.md"})
	if !res.IsError || !strings.Contains(res.Output, `forbidden pattern "notes/**"`) {
		t.Fatalf("a session pattern should refuse notes/plan.md: %+v", res)
	}
}

// A forbidden.json that cannot be parsed stops shell commands rather than
// letting them run past the rules.
func TestShellFailsClosedOnMalformedForbiddenFile(t *testing.T) {
	skipOnWindows(t)
	t.Setenv("SCRIBE_HOME", t.TempDir())
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".scribe", "forbidden.json"), `{oops`)
	r, _ := newTestRegistry(t, Options{})
	res := runTool(t, r, "shell", newTestEnv(dir, nil), map[string]any{"command": "echo hi"})
	if !res.IsError || !strings.Contains(res.Output, "not valid JSON") {
		t.Fatalf("expected fail-closed refusal: %+v", res)
	}
}

// The configured tool timeout is the default for a shell command that names
// none, and a zero config value means no limit.
func TestDefaultTimeoutFromOptions(t *testing.T) {
	j := NewJobs(Options{ToolTimeout: 3 * time.Minute})
	if got := j.defaultTimeout(defaultShellTimeout); got != 3*time.Minute {
		t.Fatalf("configured timeout = %s, want 3m", got)
	}
	if got := NewJobs(Options{}).defaultTimeout(defaultShellTimeout); got != defaultShellTimeout {
		t.Fatalf("unset timeout = %s, want the built-in default", got)
	}
	if got := NewJobs(Options{ToolTimeout: NoTimeout}).defaultTimeout(defaultShellTimeout); got != NoTimeout {
		t.Fatalf("no-limit timeout = %s", got)
	}
}
