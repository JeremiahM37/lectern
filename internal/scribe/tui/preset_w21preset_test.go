package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// presetProject makes a project root with a .scribe/presets directory and
// points the fake backend at it, so /preset sees a project.
func presetProject(t *testing.T, fb *FakeBackend) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".scribe", "presets"), 0o700); err != nil {
		t.Fatal(err)
	}
	fb.workdir = root
	return root
}

func writeProjectPreset(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".scribe", "presets", name+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readGlobalConfig(t *testing.T) config.Config {
	t.Helper()
	c, err := config.LoadFile(config.GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A hook that runs on every tool call, and an MCP server that runs a command.
const presetWithCommands = `{
  "hooks": [{"event": "PreToolUse", "command": "echo audit >> /tmp/x"}],
  "mcp": [{"name": "docs", "command": "npx", "args": ["-y", "docs-mcp"], "env": {"TOKEN": "t"}, "timeout": 30}]
}`

// Nothing is added until the user says yes: the dialog is shown and the
// config file is untouched.
func TestPresetCommandsWaitForConfirmation(t *testing.T) {
	home := presetHome(t)
	writePreset(t, home, "tools", presetWithCommands)
	m, _ := newTestModel(t, 100, 40)

	m.runCommand("/preset tools")

	if len(m.modals) != 1 {
		t.Fatalf("applying a preset with hooks and MCP servers must ask first, got %d modals", len(m.modals))
	}
	if _, err := os.Stat(config.GlobalPath()); err == nil {
		t.Fatalf("the config file must not change before the user answers")
	}
}

func TestPresetCommandsAddedOnYes(t *testing.T) {
	home := presetHome(t)
	writePreset(t, home, "tools", presetWithCommands)
	m, _ := newTestModel(t, 100, 40)
	m.runCommand("/preset tools")

	m.Update(key("y"))

	c := readGlobalConfig(t)
	if len(c.Hooks) != 1 || c.Hooks[0].Event != "PreToolUse" || c.Hooks[0].Command != "echo audit >> /tmp/x" {
		t.Fatalf("the confirmed hook was not written: %+v", c.Hooks)
	}
	if len(c.MCP) != 1 {
		t.Fatalf("the confirmed MCP server was not written: %+v", c.MCP)
	}
	srv := c.MCP[0]
	if srv.Name != "docs" || srv.Command != "npx" || len(srv.Args) != 2 || srv.Env["TOKEN"] != "t" || srv.Timeout != 30 {
		t.Fatalf("the MCP server lost fields on the way to disk: %+v", srv)
	}
	advanceChat(m)
	if !strings.Contains(screen(m), "Added 1 hook and 1 MCP server from preset tools") {
		t.Fatalf("the result should be reported:\n%s", screen(m))
	}
}

func TestPresetCommandsSkippedOnNo(t *testing.T) {
	home := presetHome(t)
	writePreset(t, home, "tools", presetWithCommands)
	m, _ := newTestModel(t, 100, 40)
	m.runCommand("/preset tools")

	m.Update(key("esc"))

	if _, err := os.Stat(config.GlobalPath()); err == nil {
		t.Fatalf("declining must write nothing")
	}
	advanceChat(m)
	if !strings.Contains(screen(m), "Skipped 1 hook and 1 MCP server from preset tools") {
		t.Fatalf("a skipped preset should say what was skipped:\n%s", screen(m))
	}
}

// Settings from the same preset still apply without a dialog; only the
// commands wait.
func TestPresetCommandsDoNotBlockOtherSettings(t *testing.T) {
	home := presetHome(t)
	writePreset(t, home, "mix", `{"mode":"plan","hooks":[{"event":"Stop","command":"true"}]}`)
	m, fb := newTestModel(t, 100, 40)

	m.runCommand("/preset mix")

	if !containsWrite(fb.ConfigWrites(), ConfigWrite{Key: "mode", Value: "plan", Project: false}) {
		t.Fatalf("the mode should still apply: %+v", fb.ConfigWrites())
	}
	if len(m.modals) != 1 {
		t.Fatalf("the hook should still ask, got %d modals", len(m.modals))
	}
}

// Entries the config already has exactly as the preset has them are not new,
// so there is nothing to ask about.
func TestPresetCommandsIdenticalEntriesAskNothing(t *testing.T) {
	home := presetHome(t)
	writePreset(t, home, "tools", presetWithCommands)
	existing := config.Config{
		Hooks: []config.Hook{{Event: "PreToolUse", Command: "echo audit >> /tmp/x"}},
		MCP:   []config.MCPServer{{Name: "docs", Command: "npx", Args: []string{"-y", "docs-mcp"}, Env: map[string]string{"TOKEN": "t"}, Timeout: 30}},
	}
	if err := config.SaveGlobal(existing); err != nil {
		t.Fatal(err)
	}
	m, _ := newTestModel(t, 100, 40)

	m.runCommand("/preset tools")

	if len(m.modals) != 0 {
		t.Fatalf("a preset whose commands are already there should not ask again, got %d modals", len(m.modals))
	}
	if c := readGlobalConfig(t); len(c.Hooks) != 1 || len(c.MCP) != 1 {
		t.Fatalf("nothing should be duplicated: hooks %+v mcp %+v", c.Hooks, c.MCP)
	}
}

// A server with the same name but a different command is a replacement, and
// the dialog says so. Other servers and the existing hook stay.
func TestPresetCommandsReplaceChangedServer(t *testing.T) {
	home := presetHome(t)
	writePreset(t, home, "tools", `{"mcp":[{"name":"docs","command":"new-docs"}]}`)
	existing := config.Config{
		Theme: "scribe",
		Hooks: []config.Hook{{Event: "Stop", Command: "keep-me"}},
		MCP: []config.MCPServer{
			{Name: "docs", Command: "old-docs"},
			{Name: "other", Command: "other-mcp"},
		},
	}
	if err := config.SaveGlobal(existing); err != nil {
		t.Fatal(err)
	}
	m, _ := newTestModel(t, 100, 40)
	m.runCommand("/preset tools")

	if len(m.modals) != 1 {
		t.Fatalf("a changed server must ask, got %d modals", len(m.modals))
	}
	m.Update(key("y"))

	c := readGlobalConfig(t)
	if c.Theme != "scribe" {
		t.Fatalf("keys the preset did not touch must survive, theme is %q", c.Theme)
	}
	if len(c.Hooks) != 1 || c.Hooks[0].Command != "keep-me" {
		t.Fatalf("existing hooks must be kept: %+v", c.Hooks)
	}
	if len(c.MCP) != 2 || c.MCP[0].Name != "docs" || c.MCP[0].Command != "new-docs" || c.MCP[1].Command != "other-mcp" {
		t.Fatalf("the server with the same name should be replaced and the other kept: %+v", c.MCP)
	}
}

// A project preset writes its commands to the project config, and the global
// file is left alone.
func TestPresetCommandsProjectScopeWritesProjectFile(t *testing.T) {
	presetHome(t)
	m, fb := newTestModel(t, 100, 40)
	root := presetProject(t, fb)
	writeProjectPreset(t, root, "local", `{"hooks":[{"event":"Stop","command":"make check"}]}`)

	m.runCommand("/preset local")
	m.Update(key("y"))

	proj, err := config.LoadFile(config.ProjectPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(proj.Hooks) != 1 || proj.Hooks[0].Command != "make check" {
		t.Fatalf("the project hook was not written to the project config: %+v", proj.Hooks)
	}
	if _, err := os.Stat(config.GlobalPath()); err == nil {
		t.Fatalf("a project preset must not write the global config")
	}
}

// A malformed command entry is refused before anything is written, and says
// which preset and server it was.
func TestPresetCommandsRefuseBrokenServer(t *testing.T) {
	home := presetHome(t)
	writePreset(t, home, "broken", `{"mcp":[{"name":"neither"}]}`)
	m, _ := newTestModel(t, 100, 40)

	m.runCommand("/preset broken")
	advanceChat(m)

	if len(m.modals) != 0 {
		t.Fatalf("a broken server must not raise a dialog")
	}
	if !strings.Contains(screen(m), "neither") {
		t.Fatalf("the error should name the server:\n%s", screen(m))
	}
	if _, err := os.Stat(config.GlobalPath()); err == nil {
		t.Fatalf("nothing should be written for a broken server")
	}
}

// The dialog lists every hook that runs on every tool call, and says so in
// the intro. A matched hook is listed by its matcher instead.
func TestPresetCommandLinesFlagEveryToolCall(t *testing.T) {
	c := presetCommands(
		config.Hook{Event: "PreToolUse", Command: "audit"},
		config.Hook{Event: "PostToolUse", Matcher: "Bash", Command: "log"},
	)
	lines := presetCommandLines(c)
	if len(lines) != 2 {
		t.Fatalf("one line per hook, got %v", lines)
	}
	if !strings.Contains(lines[0], "every tool call") || !strings.Contains(lines[0], "audit") {
		t.Fatalf("an unmatched tool hook must say it runs on every call: %q", lines[0])
	}
	if strings.Contains(lines[1], "every tool call") || !strings.Contains(lines[1], "matching Bash") {
		t.Fatalf("a matched hook should show its matcher: %q", lines[1])
	}
	if !presetHasEveryToolHook(c) {
		t.Fatalf("the intro warning should fire for the unmatched hook")
	}
	if presetHasEveryToolHook(presetCommands(config.Hook{Event: "Stop", Command: "x"})) {
		t.Fatalf("a Stop hook is not a tool hook")
	}
}

func TestPresetCommandLinesDescribeServers(t *testing.T) {
	c := config.PresetCommands{
		MCP: []config.MCPServer{
			{Name: "a", Command: "npx", Args: []string{"-y", "x"}},
			{Name: "b", URL: "https://mcp.example/x", Disabled: true},
		},
		Replaced: map[string]bool{"a": true},
	}
	lines := presetCommandLines(c)
	if !strings.Contains(lines[0], "runs npx -y x") || !strings.Contains(lines[0], "replaces the one already there") {
		t.Fatalf("stdio server line: %q", lines[0])
	}
	if !strings.Contains(lines[1], "connects to https://mcp.example/x") || !strings.Contains(lines[1], "[disabled]") {
		t.Fatalf("http server line: %q", lines[1])
	}
}

func TestPresetCountsAreSingularAndPlural(t *testing.T) {
	one := presetCommands(config.Hook{Event: "Stop", Command: "x"})
	one.MCP = []config.MCPServer{{Name: "a", Command: "a"}}
	if got := presetCommandCounts(one); got != "1 hook and 1 MCP server" {
		t.Fatalf("got %q", got)
	}
	many := config.PresetCommands{Hooks: make([]config.Hook, 3), MCP: make([]config.MCPServer, 2)}
	if got := presetCommandCounts(many); got != "3 hooks and 2 MCP servers" {
		t.Fatalf("got %q", got)
	}
}

// presetCommands builds a PresetCommands holding the given hooks, for the
// line-formatting tests.
func presetCommands(hooks ...config.Hook) config.PresetCommands {
	return config.PresetCommands{Hooks: hooks, Replaced: map[string]bool{}}
}
