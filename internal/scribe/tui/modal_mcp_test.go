package tui

import (
	"reflect"
	"testing"
)

// mcpBacked returns a model on the servers fixture, so the editor has data.
func mcpBacked(t *testing.T) (*Model, *recBackend) {
	t.Helper()
	b := serversFixture()
	m := modalModel(t, 140, 36, b)
	m.runCommand("/mcp")
	return m, b
}

func findSpec(t *testing.T, be Backend, name string) (MCPServerSpec, bool) {
	t.Helper()
	for _, s := range be.MCPServers() {
		if s.Name == name {
			return s, true
		}
	}
	return MCPServerSpec{}, false
}

func TestParseKVReadsLinesAndSkipsComments(t *testing.T) {
	got, err := parseKV("A=1\n# a comment\n\n  B = x=y  ")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "x=y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseKV = %v, want %v", got, want)
	}
	if _, err := parseKV("TOKEN"); err == nil {
		t.Fatal("a line with no = should fail")
	}
	if _, err := parseKV("=value"); err == nil {
		t.Fatal("a line with no key should fail")
	}
	if got, _ := parseKV("\n\n"); got != nil {
		t.Fatalf("no entries should be nil, got %v", got)
	}
}

func TestMCPFormAddsStdioServer(t *testing.T) {
	m, b := mcpBacked(t)
	send(m, "a")
	if topTitle(m) != "Add MCP server" {
		t.Fatalf("a should open the add form, top=%q", topTitle(m))
	}
	typeText(m, "tools")
	send(m, "tab") // transport
	send(m, "tab") // command
	typeText(m, "npx")
	send(m, "tab") // arguments
	typeText(m, "-y pkg")
	send(m, "tab") // environment
	typeText(m, "TOKEN=abc")
	send(m, "ctrl+s")

	if topTitle(m) != "MCP servers" {
		t.Fatalf("a valid save should close the form, top=%q", topTitle(m))
	}
	got, ok := findSpec(t, b, "tools")
	if !ok {
		t.Fatalf("server not saved; have %+v", b.MCPServers())
	}
	if got.Transport != "stdio" || got.Command != "npx" || !reflect.DeepEqual(got.Args, []string{"-y", "pkg"}) ||
		got.Env["TOKEN"] != "abc" || got.Scope != "global" {
		t.Fatalf("saved server = %+v", got)
	}
}

func TestMCPFormHttpServerUsesURLAndHeaders(t *testing.T) {
	m, b := mcpBacked(t)
	send(m, "a")
	typeText(m, "remote")
	send(m, "tab")
	send(m, "ctrl+t") // http: the transport toggle keeps focus on it
	send(m, "tab")    // URL
	typeText(m, "https://mcp.example.invalid/mcp")
	send(m, "tab") // headers
	typeText(m, "Authorization=Bearer x")
	send(m, "ctrl+g") // project scope
	send(m, "ctrl+s")

	got, ok := findSpec(t, b, "remote")
	if !ok {
		t.Fatalf("http server not saved: %+v", b.MCPServers())
	}
	if got.Transport != "http" || got.URL != "https://mcp.example.invalid/mcp" ||
		got.Headers["Authorization"] != "Bearer x" || got.Scope != "project" {
		t.Fatalf("saved http server = %+v", got)
	}
}

func TestMCPFormShowsHintsAndKeepsOpen(t *testing.T) {
	m, b := mcpBacked(t)
	send(m, "a")
	send(m, "ctrl+s")
	if topTitle(m) != "Add MCP server" {
		t.Fatalf("an empty form must stay open, top=%q", topTitle(m))
	}
	mustContain(t, screen(m), "A name is needed.")

	typeText(m, "bad")
	send(m, "tab")
	send(m, "tab")
	send(m, "ctrl+s")
	mustContain(t, screen(m), "A command is needed.")

	// A malformed environment line is reported with its line number.
	typeText(m, "run")
	send(m, "tab")
	send(m, "tab") // environment
	typeText(m, "NOTAPAIR")
	send(m, "ctrl+s")
	mustContain(t, screen(m), "line 1 needs KEY=VALUE")

	if _, ok := findSpec(t, b, "bad"); ok {
		t.Fatal("an invalid form must not save")
	}
}

func TestMCPFormEditPrefillsAndRenames(t *testing.T) {
	m, b := mcpBacked(t)
	send(m, "e") // github, the first row
	if topTitle(m) != "Edit MCP server" {
		t.Fatalf("e should open the edit form, top=%q", topTitle(m))
	}
	mustContain(t, screen(m), "github", "npx")

	// Rename: the old name goes away and the new one replaces it in place.
	for range "github" {
		send(m, "backspace")
	}
	typeText(m, "gh")
	send(m, "ctrl+s")
	if _, ok := findSpec(t, b, "github"); ok {
		t.Fatal("renamed server still under its old name")
	}
	if got, ok := findSpec(t, b, "gh"); !ok || got.Command != "npx" {
		t.Fatalf("renamed server = %+v ok=%v", got, ok)
	}
}

func TestServersToggleDisablesAndEnables(t *testing.T) {
	m, b := mcpBacked(t)
	runCmd(m, send(m, "t")) // github is enabled, so this disables it
	if s, _ := findSpec(t, b, "github"); !s.Disabled {
		t.Fatalf("t should disable github: %+v", s)
	}
	send(m, "t")
	if s, _ := findSpec(t, b, "github"); s.Disabled {
		t.Fatalf("second t should enable github: %+v", s)
	}
}

func TestServersRemoveAsksFirst(t *testing.T) {
	m, b := mcpBacked(t)
	send(m, "d")
	if topTitle(m) != "Remove MCP server" {
		t.Fatalf("d should ask first, top=%q", topTitle(m))
	}
	send(m, "n")
	if _, ok := findSpec(t, b, "github"); !ok {
		t.Fatal("cancelling the confirm removed the server")
	}

	send(m, "d")
	send(m, "y")
	if _, ok := findSpec(t, b, "github"); ok {
		t.Fatal("confirmed remove did not remove the server")
	}
}

func TestServersLanguageViewHasNoMCPEditing(t *testing.T) {
	m := modalModel(t, 140, 36, serversFixture())
	m.runCommand("/lsp")
	send(m, "a")
	send(m, "t")
	if topTitle(m) != "Language servers" {
		t.Fatalf("MCP keys must not open anything in the language view, top=%q", topTitle(m))
	}
}
