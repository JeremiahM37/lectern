package tui

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------- skills

func TestSkillsBrowserListsSkillsAndWhereToAddThem(t *testing.T) {
	m := modalModel(t, 140, 36, newRecBackend())
	m.runCommand("/skills")
	gitPump(m, nil)
	skillsTabs(m, "Installed")
	if topTitle(m) != "Skills" {
		t.Fatalf("/skills should open the skills browser, top=%q", topTitle(m))
	}
	s := screen(m)
	mustContain(t, s, "release-notes", "db-migrations", "k8s-debug", ".scribe/skills", "~/.claude/skills", "Project skills", "Global skills")
	mustContain(t, s, "problem:")
	mustContain(t, s, "Write release notes")
}

func TestSkillsCtrlSOpensBrowser(t *testing.T) {
	m := modalModel(t, 100, 30, newRecBackend())
	send(m, "ctrl+s")
	if topTitle(m) != "Skills" {
		t.Fatalf("ctrl+s should open the skills browser, top=%q", topTitle(m))
	}
}

func TestSkillsEnterShowsInstructionsAndEscReturns(t *testing.T) {
	m := modalModel(t, 100, 30, newRecBackend())
	send(m, "ctrl+s")
	skillsTabs(m, "Installed")
	send(m, "ctrl+o")
	if topTitle(m) != "db-migrations" {
		t.Fatalf("enter should open the first skill's instructions, top=%q", topTitle(m))
	}
	mustContain(t, screen(m), "Follow the steps in this skill")
	send(m, "esc")
	if topTitle(m) != "Skills" {
		t.Fatalf("esc should return to the list, top=%q", topTitle(m))
	}
}

func TestSkillsWithoutCatalogFallBackToNames(t *testing.T) {
	m := modalModel(t, 100, 30, plainBackend{newRecBackend()})
	send(m, "ctrl+s")
	mustContain(t, screen(m), "release-notes", "db-migrations")
	send(m, "ctrl+o")
	mustContain(t, screen(m), "not available from this backend")
}

func TestSkillsWidths(t *testing.T) {
	for _, w := range []int{80, 140} {
		m := modalModel(t, w, 30, newRecBackend())
		send(m, "ctrl+s")
		checkWidths(t, m, w, 30)
		skillsTabs(m, "Installed")
		checkWidths(t, m, w, 30)
		send(m, "ctrl+o")
		checkWidths(t, m, w, 30)
	}
}

// ---------------------------------------------------------------- tools

func toolsFixture() *recBackend {
	b := newRecBackend()
	b.tools = []ToolInfo{
		{Name: "read_file", Description: "read a file", Enabled: true, ReadOnly: true, Source: "builtin"},
		{Name: "edit_file", Description: "replace text in a file", Enabled: true, Source: "builtin"},
		{Name: "shell", Description: "run a shell command", Enabled: true, Source: "builtin"},
		{Name: "mcp__github__search", Description: "search issues", Enabled: false, ReadOnly: true, Source: "mcp:github"},
	}
	return b
}

func TestToolsGroupedWithToggles(t *testing.T) {
	m := modalModel(t, 140, 36, toolsFixture())
	m.runCommand("/tools")
	if topTitle(m) != "Tools" {
		t.Fatalf("/tools should open the tools modal, top=%q", topTitle(m))
	}
	s := screen(m)
	mustContain(t, s, "Edit", "Run", "Read", "MCP: github", "✓ read_file", "✗ mcp__github__search", "3 / 4 enabled")
	mustContain(t, s, "read-only")
}

func TestToolsSpaceTogglesSelectedTool(t *testing.T) {
	b := toolsFixture()
	m := modalModel(t, 140, 36, b)
	m.runCommand("/tools")
	// the list is grouped: Read, Edit, Run, MCP: github. Filter to shell.
	send(m, "/")
	typeText(m, "shell")
	send(m, "enter") // stop filtering, keep selection
	send(m, " ")
	if on := enabledOf(b, "shell"); on {
		t.Fatalf("space should disable shell")
	}
	mustContain(t, screen(m), "2 / 4 enabled")
	send(m, " ")
	if on := enabledOf(b, "shell"); !on {
		t.Fatalf("second space should enable shell again")
	}
}

func TestToolsSpaceTypesWhileFiltering(t *testing.T) {
	b := toolsFixture()
	m := modalModel(t, 140, 36, b)
	m.runCommand("/tools")
	send(m, "/")
	typeText(m, "a b")
	if !strings.Contains(screen(m), "a b") {
		t.Fatalf("space while filtering should go into the filter")
	}
	if !enabledOf(b, "shell") {
		t.Fatalf("space while filtering must not toggle a tool")
	}
}

func TestToolsDescriptionInPreview(t *testing.T) {
	m := modalModel(t, 140, 36, toolsFixture())
	m.runCommand("/tools")
	mustContain(t, screen(m), "run a shell command")
}

// enabledOf reads a tool's state back through the backend.
func enabledOf(b *recBackend, name string) bool {
	for _, tl := range b.Tools() {
		if tl.Name == name {
			return tl.Enabled
		}
	}
	return false
}

func TestToolsWidths(t *testing.T) {
	for _, w := range []int{80, 140} {
		m := modalModel(t, w, 30, toolsFixture())
		m.runCommand("/tools")
		checkWidths(t, m, w, 30)
	}
}

// ---------------------------------------------------------------- servers

func serversFixture() *recBackend {
	b := newRecBackend()
	b.servers = []ServerStatus{
		{Name: "github", Kind: "mcp", State: "ready", Tools: 12},
		{Name: "postgres", Kind: "mcp", State: "failed", Error: "connection refused while dialling the database host on port 5432 after three retries"},
		{Name: "fs", Kind: "mcp", State: "starting"},
		{Name: "gopls", Kind: "lsp", State: "ready", Tools: 3},
	}
	return b
}

func TestMCPViewShowsOnlyMCPServers(t *testing.T) {
	m := modalModel(t, 140, 36, serversFixture())
	m.runCommand("/mcp")
	if topTitle(m) != "MCP servers" {
		t.Fatalf("/mcp should open the MCP view, top=%q", topTitle(m))
	}
	s := screen(m)
	mustContain(t, s, "● ", "github", "12 tools", "✗ ", "postgres", "connection refused", "◐ ", "fs")
	mustNotContain(t, s, "gopls")
	mustContain(t, s, ".scribe/config.json", ".mcp.json")
}

func TestLSPViewShowsLanguageServersAndMCP(t *testing.T) {
	m := modalModel(t, 140, 36, serversFixture())
	m.runCommand("/lsp")
	s := screen(m)
	mustContain(t, s, "Language servers", "gopls", "postgres")
}

func TestLSPViewEmpty(t *testing.T) {
	m := modalModel(t, 100, 30, newRecBackend())
	m.runCommand("/lsp")
	mustContain(t, screen(m), "No language servers have started yet.")
}

func TestServersRestartsSelectedMCPServer(t *testing.T) {
	b := serversFixture()
	m := modalModel(t, 140, 36, b)
	m.runCommand("/servers")
	send(m, "down") // postgres
	cmd := send(m, "r")
	if cmd == nil {
		t.Fatalf("r should return a restart command")
	}
	msg := cmd()
	fm, ok := msg.(flashMsg)
	if !ok || fm.kind != "ok" {
		t.Fatalf("restart should report ok, got %#v", msg)
	}
	if len(b.restarted) != 1 || b.restarted[0] != "postgres" {
		t.Fatalf("expected restart of postgres, got %v", b.restarted)
	}
}

func TestServersDoNotRestartLanguageServers(t *testing.T) {
	b := serversFixture()
	m := modalModel(t, 140, 36, b)
	m.runCommand("/lsp")
	for range 3 { // github, postgres, fs, then gopls
		send(m, "down")
	}
	send(m, "r")
	if len(b.restarted) != 0 {
		t.Fatalf("language servers must not be restarted from here")
	}
	mustContain(t, screen(m), "Only MCP servers can be restarted")
}

func TestServersWidths(t *testing.T) {
	for _, w := range []int{80, 140} {
		m := modalModel(t, w, 30, serversFixture())
		m.runCommand("/servers")
		checkWidths(t, m, w, 30)
	}
}

// ---------------------------------------------------------------- code map

func mapFixture() *recBackend {
	b := newRecBackend()
	b.stats = MapStats{Files: 214, Symbols: 3920, Edges: 11840, Languages: map[string]int{"go": 180, "ts": 34}}
	return b
}

func TestMapShowsCountsAndLanguageBars(t *testing.T) {
	m := modalModel(t, 140, 36, mapFixture())
	m.runCommand("/map")
	if topTitle(m) != "Code map" {
		t.Fatalf("/map should open the code map, top=%q", topTitle(m))
	}
	s := screen(m)
	mustContain(t, s, "214", "3,920", "11,840", "Languages", "go", "ts", "84%", "15%", "█")
}

func TestMapRebuildKey(t *testing.T) {
	b := mapFixture()
	m := modalModel(t, 140, 36, b)
	m.runCommand("/map")
	send(m, "r")
	if b.rebuilds != 1 {
		t.Fatalf("r should rebuild once, got %d", b.rebuilds)
	}
}

func TestMapRebuildRefusedWhileBuilding(t *testing.T) {
	b := mapFixture()
	b.stats.Building, b.stats.Progress = true, 0.42
	m := modalModel(t, 140, 36, b)
	m.runCommand("/map")
	mustContain(t, screen(m), "42%")
	send(m, "r")
	if b.rebuilds != 0 {
		t.Fatalf("no second rebuild while building")
	}
	mustContain(t, screen(m), "Already building.")
}

func TestMapRebuildCommandArgument(t *testing.T) {
	b := mapFixture()
	m := modalModel(t, 140, 36, b)
	cmd := m.runCommand("/map rebuild")
	runCmd(m, cmd)
	if b.rebuilds != 1 {
		t.Fatalf("/map rebuild should rebuild, got %d", b.rebuilds)
	}
	if topTitle(m) == "Code map" {
		t.Fatalf("/map rebuild should not open the view")
	}
}

func TestMapEmptyState(t *testing.T) {
	m := modalModel(t, 100, 30, newRecBackend())
	m.runCommand("/map")
	mustContain(t, screen(m), "The code map is empty. Press r to build it.")
}

func TestMapWidths(t *testing.T) {
	for _, w := range []int{80, 140} {
		m := modalModel(t, w, 30, mapFixture())
		m.runCommand("/map")
		checkWidths(t, m, w, 30)
	}
}
