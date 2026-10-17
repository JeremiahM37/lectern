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

// formatServer scripts a server whose formatter removes the doubled space in
// "func  F" and whose organize-imports action adds a comment on line 1.
func formatServer(t *testing.T, m *Manager, abs string) {
	t.Helper()
	uri := pathToURI(abs)
	startFakeLSP(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/formatting": func(json.RawMessage) any {
			return []any{map[string]any{
				"range":   map[string]any{"start": map[string]any{"line": 2, "character": 4}, "end": map[string]any{"line": 2, "character": 6}},
				"newText": " ",
			}}
		},
		"textDocument/codeAction": func(json.RawMessage) any {
			return []any{map[string]any{
				"title": "Organize Imports",
				"kind":  "source.organizeImports",
				"edit": map[string]any{"changes": map[string]any{uri: []any{map[string]any{
					"range":   map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 0}},
					"newText": "// organized\n",
				}}}},
			}}
		},
	})
}

// callTool runs one registered tool against m with the given args.
func callTool(t *testing.T, m *Manager, dir, name string, args map[string]any) tools.Result {
	t.Helper()
	tool, ok := newRegistry(m).Get(name)
	if !ok {
		t.Fatalf("%s not registered", name)
	}
	b, _ := json.Marshal(args)
	return tool.Run(context.Background(), &tools.Env{Workdir: dir}, b)
}

// refactor format and format_file share formatFile, so both change the file
// the same way and say the same thing about it.
func TestRefactorFormatAppliesTheFormatFileSteps(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	mustWrite(t, abs, "package demo\n\nfunc  F() {}\n")
	mustWrite(t, filepath.Join(dir, "b.go"), "package demo\n\nfunc  G() {}\n")
	m := NewManager(dir)
	formatServer(t, m, abs)
	formatServer(t, m, filepath.Join(dir, "b.go"))

	res := callTool(t, m, dir, "refactor", map[string]any{"action": "format", "path": "a.go"})
	if res.IsError || !strings.Contains(res.Output, "formatted 1 edit(s) in a.go") {
		t.Fatalf("refactor format: %s", res.Output)
	}
	if b, _ := os.ReadFile(abs); !strings.Contains(string(b), "func F() {}") {
		t.Fatalf("refactor format did not write the edit: %q", b)
	}

	res = callTool(t, m, dir, "format_file", map[string]any{"path": "b.go"})
	if res.IsError || !strings.Contains(res.Output, "formatted 1 edit(s) in b.go") {
		t.Fatalf("format_file: %s", res.Output)
	}
}

func TestRefactorOrganizeImportsAppliesTheCodeAction(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	mustWrite(t, abs, "package demo\n")
	m := NewManager(dir)
	formatServer(t, m, abs)

	res := callTool(t, m, dir, "refactor", map[string]any{"action": "organize_imports", "path": "a.go"})
	if res.IsError || !strings.Contains(res.Output, "organized imports in a.go") {
		t.Fatalf("organize_imports: %s", res.Output)
	}
	if b, _ := os.ReadFile(abs); !strings.HasPrefix(string(b), "// organized\n") {
		t.Fatalf("organize_imports did not write the edit: %q", b)
	}
}

func TestRefactorFormatRefusesPreview(t *testing.T) {
	dir := goProject(t)
	res := callTool(t, NewManager(dir), dir, "refactor", map[string]any{"action": "format", "path": "a.go", "apply": false})
	if !res.IsError || !strings.Contains(res.Output, "no preview") {
		t.Fatalf("format with apply false should say it has no preview: %s", res.Output)
	}
}

func TestRefactorFormatWithoutServerExplainsInstall(t *testing.T) {
	noServers(t)
	dir := goProject(t)
	m := NewManager(dir)
	for _, action := range []string{"format", "organize_imports"} {
		res := callTool(t, m, dir, "refactor", map[string]any{"action": action, "path": "a.go"})
		if !res.IsError || !strings.Contains(res.Output, "installed") {
			t.Fatalf("%s without a server should explain the install: %s", action, res.Output)
		}
		if strings.Contains(res.Output, "start_line") {
			t.Fatalf("%s must not ask for start_line: %s", action, res.Output)
		}
	}
}

func TestRefactorFormatRangeIsChecked(t *testing.T) {
	dir := goProject(t)
	m := NewManager(dir)
	res := callTool(t, m, dir, "refactor", map[string]any{"action": "format", "path": "a.go", "start_line": 9, "end_line": 2})
	if !res.IsError || !strings.Contains(res.Output, "range") {
		t.Fatalf("inverted format range should be refused: %s", res.Output)
	}
	res = callTool(t, m, dir, "refactor", map[string]any{"action": "organize_imports", "path": "a.go", "start_line": 2})
	if !res.IsError || !strings.Contains(res.Output, "whole file") {
		t.Fatalf("organize_imports on a range should be refused: %s", res.Output)
	}
}

func TestRefactorExtractStillNeedsStartLine(t *testing.T) {
	dir := goProject(t)
	res := callTool(t, NewManager(dir), dir, "refactor", map[string]any{"action": "extract_function", "path": "a.go"})
	if !res.IsError || !strings.Contains(res.Output, "start_line") {
		t.Fatalf("extract_function without start_line should say so: %s", res.Output)
	}
}

func TestRefactorUnknownActionListsFormatKinds(t *testing.T) {
	dir := goProject(t)
	res := callTool(t, NewManager(dir), dir, "refactor", map[string]any{"action": "explode", "path": "a.go", "start_line": 4})
	if !res.IsError || !strings.Contains(res.Output, "format, organize_imports") {
		t.Fatalf("unknown action should list format and organize_imports: %s", res.Output)
	}
}
