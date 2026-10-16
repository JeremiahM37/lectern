package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestDeclaredEventsIncludeTheNewOnes(t *testing.T) {
	have := map[string]bool{}
	for _, e := range Events {
		have[e] = true
	}
	for _, want := range []string{"PostToolUseFailure", "StopFailure", "PostCompact", "SubagentStart", "SubagentStop"} {
		if !have[want] {
			t.Errorf("event %s is not declared", want)
		}
	}
}

func TestPostToolFailureFiresOnlyForFailedCalls(t *testing.T) {
	skipWindows(t)
	root := t.TempDir()
	r := New(Options{Root: root, UserSettings: filepath.Join(t.TempDir(), "x.json"), Trusted: true,
		ConfigGlobal: []config.Hook{{Event: "PostToolUseFailure", Command: `cat > fail.json`}}})
	r.PostTool(context.Background(), "shell", bashArgs("false"), tools.Result{Output: "exit 1", IsError: false})
	if _, err := os.Stat(filepath.Join(root, "fail.json")); err == nil {
		t.Fatal("PostToolUseFailure fired for a successful call")
	}
	r.PostTool(context.Background(), "shell", bashArgs("false"), tools.Result{Output: "exit 1", IsError: true})
	b, err := os.ReadFile(filepath.Join(root, "fail.json"))
	if err != nil {
		t.Fatalf("PostToolUseFailure did not fire: %v", err)
	}
	if !strings.Contains(string(b), `"error":"exit 1"`) || !strings.Contains(string(b), `"tool_name":"Bash"`) {
		t.Fatalf("payload = %s", b)
	}
}

func TestStopFailureFiresOnErrorNotAbort(t *testing.T) {
	skipWindows(t)
	root := t.TempDir()
	r := New(Options{Root: root, UserSettings: filepath.Join(t.TempDir(), "x.json"), Trusted: true,
		ConfigGlobal: []config.Hook{{Event: "StopFailure", Command: `echo failed > sf.txt`}}})
	r.Stop(context.Background(), agent.TurnStats{Aborted: true})
	if _, err := os.Stat(filepath.Join(root, "sf.txt")); err == nil {
		t.Fatal("StopFailure fired for an aborted turn")
	}
	r.Stop(context.Background(), agent.TurnStats{Error: "provider down"})
	if _, err := os.Stat(filepath.Join(root, "sf.txt")); err != nil {
		t.Fatalf("StopFailure did not fire: %v", err)
	}
}

func TestSubagentAndPostCompactHooksFire(t *testing.T) {
	skipWindows(t)
	root := t.TempDir()
	r := New(Options{Root: root, UserSettings: filepath.Join(t.TempDir(), "x.json"), Trusted: true,
		ConfigGlobal: []config.Hook{
			{Event: "SubagentStart", Command: `cat > start.json`},
			{Event: "SubagentStop", Command: `cat > stop.json`},
			{Event: "PostCompact", Command: `echo done > pc.txt`},
		}})
	r.SubagentStart(context.Background(), "explore", "find the parser")
	r.SubagentStop(context.Background(), "explore", "find the parser", "boom")
	r.PostCompact(context.Background())
	for f, want := range map[string]string{"start.json": `"agent_type":"explore"`, "stop.json": `"error":"boom"`} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil || !strings.Contains(string(b), want) {
			t.Fatalf("%s = %s (%v), want %s", f, b, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "pc.txt")); err != nil {
		t.Fatalf("PostCompact did not fire: %v", err)
	}
}

func TestStatusMessageIsShownWhileTheHookRuns(t *testing.T) {
	skipWindows(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	b, _ := json.Marshal(map[string]any{"hooks": map[string][]map[string]any{
		"Stop": {{"hooks": []map[string]any{{"type": "command", "command": "true", "statusMessage": "Checking the build"}}}},
	}})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(Options{Root: t.TempDir(), SessionID: "s", UserSettings: path, Trusted: true})
	var shown []string
	r.SetStatus(func(s string) { shown = append(shown, s) })
	r.Stop(context.Background(), agent.TurnStats{})
	if len(shown) != 1 || shown[0] != "Checking the build" {
		t.Fatalf("status shown = %v", shown)
	}
	// Without a status sink nothing breaks.
	r.SetStatus(nil)
	r.Stop(context.Background(), agent.TurnStats{})
}
