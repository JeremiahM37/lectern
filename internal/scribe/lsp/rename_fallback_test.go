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

// With no language server installed, rename_symbol falls back to the
// comment- and string-aware text sweep and says so.
func TestRenameSymbolWithoutLanguageServerSweepsText(t *testing.T) {
	// Nothing on PATH or in HOME, so gopls cannot be found.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/demo\n\ngo 1.22\n",
		"a.go":   "package demo\n\n// Greet returns a greeting.\nfunc Greet(name string) string {\n\treturn \"hi \" + name\n}\n",
		"b.go":   "package demo\n\nfunc Use() string {\n\treturn Greet(\"bob\")\n}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	reg := tools.NewRegistry()
	Register(reg, NewManager(dir))
	tool, ok := reg.Get("rename_symbol")
	if !ok {
		t.Fatal("rename_symbol not registered")
	}
	args, _ := json.Marshal(map[string]any{"symbol": "Greet", "new_name": "Hello"})
	env := &tools.Env{Workdir: dir}
	res := tool.Run(context.Background(), env, args)
	if res.IsError {
		t.Fatalf("rename failed: %s", res.Output)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "b.go"))
	if !strings.Contains(string(b), `return Hello("bob")`) {
		t.Fatalf("call site not renamed:\n%s", b)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	if !strings.Contains(string(a), "func Hello(name string)") || !strings.Contains(string(a), "// Greet returns a greeting.") || !strings.Contains(string(a), `"hi " + name`) {
		t.Fatalf("definition wrong or comment/string touched:\n%s", a)
	}
	if !strings.Contains(res.Output, "text-only") || !strings.Contains(res.Output, "b.go:4") {
		t.Fatalf("result should report the text-only sites:\n%s", res.Output)
	}
}
