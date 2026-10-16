package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// After a server renames a symbol, the text sweep still runs over the files
// the server did not change, so a remaining code reference is renamed too and
// the result says so.
func TestRenameSymbolRunsTextSweepAfterServer(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/demo\n\ngo 1.22\n",
		"a.go":   "package demo\n\nfunc Greet() string {\n\treturn \"hi\"\n}\n",
		"b.go":   "package demo\n\nfunc Use() string {\n\treturn Greet()\n}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(dir)
	abs := filepath.Join(dir, "a.go")
	// The server renames only the definition in a.go, and misses the call.
	edit := WorkspaceEdit{Changes: map[string][]TextEdit{
		pathToURI(abs): {{
			Range:   Range{Start: Position{Line: 2, Character: 5}, End: Position{Line: 2, Character: 10}},
			NewText: "Hello",
		}},
	}}
	startFakeLSP(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/rename": func(json.RawMessage) any { return edit },
	})
	reg := tools.NewRegistry()
	Register(reg, m)
	tool, _ := reg.Get("rename_symbol")
	args, _ := json.Marshal(map[string]any{"path": "a.go", "line": 3, "column": 6, "new_name": "Hello"})
	res := tool.Run(context.Background(), &tools.Env{Workdir: dir, Files: newReadTracker()}, args)
	if res.IsError {
		t.Fatalf("rename failed: %s", res.Output)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "b.go"))
	if !strings.Contains(string(b), "return Hello()") {
		t.Fatalf("sweep did not rename the remaining call:\n%s\n%s", b, res.Output)
	}
	if !strings.Contains(res.Output, "Text sweep also renamed 'Greet' by name in 1 more file(s)") {
		t.Fatalf("result should say the sweep ran: %s", res.Output)
	}
}
