package lsp

import (
	"strings"
	"testing"
)

func TestMonikerHintNamesARealSymbol(t *testing.T) {
	src := "package p\n\ntype Server struct{}\n\nfunc (s *Server) Start() {}\n"
	h := monikerHint("pkg/p.go", src)
	if !strings.Contains(h, "pkg/p.go#") || !strings.Contains(h, "ast_edit") {
		t.Fatalf("hint = %q", h)
	}
	if monikerHint("notes.txt", "hello") != "" {
		t.Fatal("a file without moniker support got a hint")
	}
}
