package tui

import "testing"

func TestServerToolsListsRegisteredNames(t *testing.T) {
	b := serversFixture()
	b.toolsOf = map[string][]string{"github": {"mcp__github__search", "mcp__github__create_issue"}}
	m := modalModel(t, 140, 36, b)
	m.runCommand("/mcp")
	send(m, "v")
	if topTitle(m) != "Tools · github" {
		t.Fatalf("v on a connected server should open its tools, top=%q", topTitle(m))
	}
	mustContain(t, screen(m), "mcp__github__search", "mcp__github__create_issue")
	send(m, "esc")
	if topTitle(m) != "MCP servers" {
		t.Fatalf("esc should close the tools list and return to the servers view, top=%q", topTitle(m))
	}
}

func TestServerToolsNoToolsAndLspRows(t *testing.T) {
	b := serversFixture()
	b.toolsOf = map[string][]string{"fs": nil}
	m := modalModel(t, 140, 36, b)
	m.runCommand("/mcp")
	send(m, "j") // postgres (failed)
	send(m, "j") // fs (starting)
	send(m, "v")
	mustContain(t, screen(m), "fs has no tools (state: starting).")

	lsp := modalModel(t, 140, 36, serversFixture())
	lsp.runCommand("/lsp")
	for range 3 { // gopls is the first language server row
		send(lsp, "down")
	}
	send(lsp, "v")
	mustContain(t, screen(lsp), "Select an MCP server first.")
}

func lspFixture() *recBackend {
	b := serversFixture()
	b.lsp = []LSPSession{
		{Language: "go", Command: "gopls", Root: "/work/lectern", State: "running", PID: 4242, OpenDocs: 2},
		{Language: "rust", Root: "/work/crate", State: "failed", LastError: "rust-analyzer: not found on PATH"},
	}
	return b
}

func TestLSPSessionsListedBesideMCPInLspView(t *testing.T) {
	m := modalModel(t, 140, 36, lspFixture())
	m.runCommand("/lsp")
	s := screen(m)
	mustContain(t, s, "go lectern", "pid 4242 · 2 open", "rust crate", "github")
}

func TestLSPDetailsPopupShowsCommandAndError(t *testing.T) {
	m := modalModel(t, 140, 36, lspFixture())
	m.runCommand("/lsp")
	for range 4 { // github, postgres, fs, gopls, then go lectern
		send(m, "down")
	}
	send(m, "enter")
	if topTitle(m) != "Language server · go" {
		t.Fatalf("enter on a language server should open its details, top=%q", topTitle(m))
	}
	mustContain(t, screen(m), "Command", "gopls", "/work/lectern", "Process")
	send(m, "esc")
	send(m, "down") // rust crate
	send(m, "enter")
	mustContain(t, screen(m), "rust-analyzer: not found on PATH")
}

func TestLSPRowsAbsentFromMCPView(t *testing.T) {
	m := modalModel(t, 140, 36, lspFixture())
	m.runCommand("/mcp")
	mustNotContain(t, screen(m), "pid 4242")
}
