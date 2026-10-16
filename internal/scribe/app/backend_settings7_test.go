package app

import (
	"os"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/registry"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

func TestSetField7WritesTheScopeAndReloads(t *testing.T) {
	be, _ := testBackend(t)
	f := be.(tui.ConfigSetter7)
	if err := f.SetField7("diff_style", "compact", false); err != nil {
		t.Fatal(err)
	}
	if err := f.SetField7("agent_features.verify_edits", true, true); err != nil {
		t.Fatal(err)
	}
	merged := be.(tui.ConfigFields7).MergedConfig7()
	if merged.DiffStyle != "compact" || !merged.AgentFeatures.Enabled("verify_edits") {
		t.Fatalf("merged settings missing the writes: %+v", merged)
	}
	global, err := config.LoadFile(config.GlobalPath())
	if err != nil || global.DiffStyle != "compact" || global.AgentFeatures != nil {
		t.Fatalf("global file should hold only diff_style: %+v %v", global, err)
	}
	project, err := config.LoadFile(config.ProjectPath(be.Workdir()))
	if err != nil || project.AgentFeatures == nil || !project.AgentFeatures.Enabled("verify_edits") {
		t.Fatalf("project file should hold the feature: %+v %v", project, err)
	}
}

func TestSetField7RefusesUnknownKeysWithoutWriting(t *testing.T) {
	be, _ := testBackend(t)
	f := be.(tui.ConfigSetter7)
	if err := f.SetField7("no_such_key", "x", false); err == nil {
		t.Fatal("an unknown key must be an error")
	}
	if err := f.SetField7("diff_style", "wide", false); err == nil {
		t.Fatal("a bad value must be an error")
	}
	global, _ := config.LoadFile(config.GlobalPath())
	if global.DiffStyle != "" {
		t.Fatalf("a refused write must not change the file: %+v", global)
	}
}

func TestMoveField7MovesBetweenScopes(t *testing.T) {
	be, _ := testBackend(t)
	f := be.(tui.ConfigSetter7)
	if err := f.SetField7("tool_timeout", "4", false); err != nil {
		t.Fatal(err)
	}
	if err := f.MoveField7("tool_timeout", 4.0, true); err != nil {
		t.Fatal(err)
	}
	global, _ := config.LoadFile(config.GlobalPath())
	project, _ := config.LoadFile(config.ProjectPath(be.Workdir()))
	if global.ToolTimeout != nil {
		t.Fatal("the global file must lose the key after a move")
	}
	if project.ToolTimeout == nil || *project.ToolTimeout != 4 {
		t.Fatalf("the project file must hold the key: %+v", project.ToolTimeout)
	}
	if err := f.MoveField7("verbose", true, true); err == nil {
		t.Fatal("a key that is not a flat key cannot be moved here")
	}
}

func TestSearchKeysAreStoredNotWrittenToConfig(t *testing.T) {
	be, _ := testBackend(t)
	st := be.(tui.SecretStore7)
	const secret = "BSA-test-key-9876"
	if err := st.SetSecret(registry.BraveKeyID, secret); err != nil {
		t.Fatal(err)
	}
	if got := st.SecretSource(registry.BraveKeyID); got != "store" {
		t.Fatalf("source = %q, want store", got)
	}
	if err := st.SetSecret("openai", "nope"); err == nil {
		t.Fatal("only the search keys can be set here")
	}
	raw, err := os.ReadFile(config.GlobalPath())
	if err == nil && strings.Contains(string(raw), secret) {
		t.Fatal("the key value must never be written to config.json")
	}
	if err := st.RemoveSecret(registry.BraveKeyID); err != nil {
		t.Fatal(err)
	}
	if got := st.SecretSource(registry.BraveKeyID); got != "" {
		t.Fatalf("after removal the source should be empty, got %q", got)
	}
}
