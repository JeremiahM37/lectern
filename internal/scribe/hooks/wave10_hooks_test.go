package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// PostToolFailure runs the failure hooks on its own, for a call the agent
// refused before it ran, and returns their text.
func TestPostToolFailureRunsWithoutPostTool(t *testing.T) {
	skipWindows(t)
	root := t.TempDir()
	r := New(Options{Root: root, UserSettings: filepath.Join(t.TempDir(), "x.json"), Trusted: true,
		ConfigGlobal: []config.Hook{
			{Event: "PostToolUseFailure", Command: `cat > fail.json; echo seen`},
			{Event: "PostToolUse", Command: `echo post > post.txt`},
		}})
	extra := r.PostToolFailure(context.Background(), "shell", bashArgs("false"), tools.Result{Output: "not available", IsError: true})
	if !strings.Contains(extra, "seen") {
		t.Fatalf("extra = %q, want the hook's text", extra)
	}
	b, err := os.ReadFile(filepath.Join(root, "fail.json"))
	if err != nil || !strings.Contains(string(b), `"error":"not available"`) {
		t.Fatalf("failure payload = %s, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(root, "post.txt")); err == nil {
		t.Fatal("PostToolUse fired from PostToolFailure")
	}
}
