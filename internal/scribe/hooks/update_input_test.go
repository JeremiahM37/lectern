package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// A PreToolUse hook that returns updatedInput changes the input the call runs
// with, and the next hook sees the changed input.
func TestPreToolUpdateReplacesInputForTheCallAndLaterHooks(t *testing.T) {
	skipWindows(t)
	r, _ := newTestRunner(t, []config.Hook{
		{Event: "PreToolUse", Matcher: "shell",
			Command: `cat >/dev/null; printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"command":"echo safe"}}}'`},
		{Event: "PreToolUse", Matcher: "shell",
			Command: `cat | grep -q '"echo safe"' && exit 0; echo "second hook saw the old input" >&2; exit 2`},
	})
	got, block := r.PreToolUpdate(context.Background(), "shell", bashArgs("rm -rf build"))
	if block != "" {
		t.Fatalf("unexpected block: %s", block)
	}
	var in map[string]string
	if err := json.Unmarshal(got, &in); err != nil || in["command"] != "echo safe" {
		t.Fatalf("input = %s, %v", got, err)
	}
}

// Without updatedInput the input is passed through unchanged.
func TestPreToolUpdateKeepsInputWhenNoUpdate(t *testing.T) {
	skipWindows(t)
	r, _ := newTestRunner(t, []config.Hook{{Event: "PreToolUse", Command: `cat >/dev/null; exit 0`}})
	args := bashArgs("ls")
	got, block := r.PreToolUpdate(context.Background(), "shell", args)
	if block != "" || string(got) != string(args) {
		t.Fatalf("got %s, %q; want the original input", got, block)
	}
}

// A block refuses the call and returns the reason, as PreTool does.
func TestPreToolUpdateBlockRefuses(t *testing.T) {
	skipWindows(t)
	r, _ := newTestRunner(t, []config.Hook{{Event: "PreToolUse",
		Command: `cat >/dev/null; echo "not today" >&2; exit 2`}})
	_, block := r.PreToolUpdate(context.Background(), "shell", bashArgs("ls"))
	if block != "not today" {
		t.Fatalf("block = %q", block)
	}
}

// updatedInput that is not an object is ignored, and the input is kept.
func TestPreToolUpdateIgnoresNonObjectUpdate(t *testing.T) {
	skipWindows(t)
	r, _ := newTestRunner(t, []config.Hook{{Event: "PreToolUse",
		Command: `cat >/dev/null; printf '%s' '{"hookSpecificOutput":{"updatedInput":"just a string"}}'`}})
	args := bashArgs("ls")
	got, block := r.PreToolUpdate(context.Background(), "shell", args)
	if block != "" || string(got) != string(args) {
		t.Fatalf("got %s, %q", got, block)
	}
	if !strings.Contains(strings.Join(r.Warnings(), "\n"), "not a JSON object") {
		t.Fatalf("no warning recorded: %v", r.Warnings())
	}
}

func TestIsJSONObject(t *testing.T) {
	for in, want := range map[string]bool{`{"a":1}`: true, `  {}  `: true, `[1]`: false, `"x"`: false, `{"a":`: false, ``: false} {
		if got := isJSONObject([]byte(in)); got != want {
			t.Errorf("isJSONObject(%q) = %v, want %v", in, got, want)
		}
	}
}
