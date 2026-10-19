package config

import (
	"os"
	"path/filepath"
	"testing"
)

// w40Preset is a project or global preset carrying one hook and one server.
func w40Preset(project bool) Preset {
	return Preset{
		Name:    "guard",
		Project: project,
		Config: Config{
			Hooks: []Hook{{Event: "PreToolUse", Matcher: "shell", Command: "echo preset"}},
			MCP:   []MCPServer{{Name: "docs", Command: "docs-mcp"}},
		},
	}
}

// applyW40 does what the /preset confirm does: save, then record.
func applyW40(t *testing.T, root string, p Preset) {
	t.Helper()
	before, err := LoadPresetScope(root, p)
	if err != nil {
		t.Fatal(err)
	}
	added, err := SavePresetCommands(root, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordPresetCommands(root, p, before, added); err != nil {
		t.Fatal(err)
	}
}

func TestRevertRemovesOnlyWhatThePresetAdded(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	root := t.TempDir()
	p := w40Preset(true)
	// The user's own hook and server are in the file before the preset runs.
	userHook := Hook{Event: "Stop", Command: "echo mine"}
	userMCP := MCPServer{Name: "mine", Command: "mine-mcp"}
	if err := SaveProject(root, Config{Hooks: []Hook{userHook}, MCP: []MCPServer{userMCP}}); err != nil {
		t.Fatal(err)
	}

	applyW40(t, root, p)
	got, err := LoadFile(ProjectPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hooks) != 2 || len(got.MCP) != 2 {
		t.Fatalf("apply should add one hook and one server: %+v", got)
	}

	reverted, err := RevertPresetCommands(root, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(reverted.Hooks) != 1 || len(reverted.MCP) != 1 {
		t.Fatalf("revert should report one hook and one server, got %+v", reverted)
	}
	got, err = LoadFile(ProjectPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hooks) != 1 || got.Hooks[0] != userHook {
		t.Fatalf("the user's hook must stay and the preset's must go: %+v", got.Hooks)
	}
	if len(got.MCP) != 1 || got.MCP[0].Name != "mine" {
		t.Fatalf("the user's server must stay and the preset's must go: %+v", got.MCP)
	}
	if _, err := os.Stat(filepath.Join(ProjectDir(root), appliedDirName, "guard.json")); !os.IsNotExist(err) {
		t.Fatalf("the record should be deleted after a revert, stat err %v", err)
	}
}

func TestRevertRestoresAServerThePresetReplaced(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	root := t.TempDir()
	p := w40Preset(false)
	orig := MCPServer{Name: "docs", Command: "old-docs-mcp"}
	if err := SaveGlobal(Config{MCP: []MCPServer{orig}}); err != nil {
		t.Fatal(err)
	}

	applyW40(t, root, p)
	got, _ := LoadFile(GlobalPath())
	if len(got.MCP) != 1 || got.MCP[0].Command != "docs-mcp" {
		t.Fatalf("apply should overwrite the server of the same name: %+v", got.MCP)
	}

	reverted, err := RevertPresetCommands(root, p)
	if err != nil {
		t.Fatal(err)
	}
	if !reverted.Replaced["docs"] {
		t.Fatalf("the replaced server should be reported as restored: %+v", reverted)
	}
	got, _ = LoadFile(GlobalPath())
	if len(got.MCP) != 1 || !sameMCP(got.MCP[0], orig) {
		t.Fatalf("the original server should be back: %+v", got.MCP)
	}
}

func TestRevertLeavesAnEditedServerAlone(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	root := t.TempDir()
	p := w40Preset(true)
	applyW40(t, root, p)

	// The user changed the server after applying it; removal must not
	// delete a definition it did not write.
	cur, _ := LoadFile(ProjectPath(root))
	cur.MCP[0].Command = "edited-docs-mcp"
	if err := SaveProject(root, cur); err != nil {
		t.Fatal(err)
	}
	if _, err := RevertPresetCommands(root, p); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadFile(ProjectPath(root))
	if len(got.MCP) != 1 || got.MCP[0].Command != "edited-docs-mcp" {
		t.Fatalf("an edited server must stay: %+v", got.MCP)
	}
	if len(got.Hooks) != 0 {
		t.Fatalf("the preset's hook should still be removed: %+v", got.Hooks)
	}
}

func TestRevertWithoutRecordDoesNothing(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	root := t.TempDir()
	if err := SaveProject(root, Config{Hooks: []Hook{{Event: "Stop", Command: "keep"}}}); err != nil {
		t.Fatal(err)
	}
	reverted, err := RevertPresetCommands(root, w40Preset(true))
	if err != nil {
		t.Fatal(err)
	}
	if !reverted.Empty() {
		t.Fatalf("no record means nothing to take back: %+v", reverted)
	}
	got, _ := LoadFile(ProjectPath(root))
	if len(got.Hooks) != 1 {
		t.Fatalf("hooks the preset never wrote must stay: %+v", got.Hooks)
	}
}

func TestReapplyKeepsTheFirstOriginal(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	root := t.TempDir()
	p := w40Preset(false)
	orig := MCPServer{Name: "docs", Command: "old-docs-mcp"}
	if err := SaveGlobal(Config{MCP: []MCPServer{orig}}); err != nil {
		t.Fatal(err)
	}
	applyW40(t, root, p)
	applyW40(t, root, p) // a second apply sees the preset's own server, not the original

	if _, err := RevertPresetCommands(root, p); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadFile(GlobalPath())
	if len(got.MCP) != 1 || !sameMCP(got.MCP[0], orig) {
		t.Fatalf("the first original must survive a second apply: %+v", got.MCP)
	}
}
