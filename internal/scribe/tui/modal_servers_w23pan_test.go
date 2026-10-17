package tui

import "testing"

// The language server row carries its process id and open document count.
// A server that has not started has no process to show, only its count.
func TestLSPDetailTailShowsPIDAndOpenCount(t *testing.T) {
	v := &serversView{kind: "lsp", be: lspFixture()}
	v.servers()
	if got := v.lspDetail("go lectern"); got != "pid 4242 · 2 open" {
		t.Fatalf("running server tail = %q", got)
	}
	if got := v.lspDetail("rust crate"); got != "0 open" {
		t.Fatalf("failed server tail = %q, want no pid", got)
	}
	if got := v.lspDetail("no such server"); got != "" {
		t.Fatalf("unknown server tail = %q, want empty", got)
	}
}

// The details popup names the process and the open document count, so a
// stuck server can be matched to a process in ps.
func TestLSPDetailPaneShowsProcessAndOpenDocuments(t *testing.T) {
	m := modalModel(t, 140, 36, lspFixture())
	m.runCommand("/lsp")
	for range 4 { // github, postgres, fs, gopls, then go lectern
		send(m, "down")
	}
	send(m, "enter")
	mustContain(t, screen(m), "Process", "4242", "Open documents")
}
