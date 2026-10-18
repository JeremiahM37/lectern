package lsp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

const importFixtureSrc = "package demo\n\nfunc Use() string {\n\treturn strings.ToUpper(\"x\")\n}\n"

// missingStringsDiag is what a server reports for the unimported strings package.
var missingStringsDiag = map[string]any{
	"range":    map[string]any{"start": map[string]any{"line": 3, "character": 8}, "end": map[string]any{"line": 3, "character": 15}},
	"severity": 1,
	"source":   "compiler",
	"message":  "undefined: strings",
}

// importEdit is a workspace edit for uri that adds an import of strings.
func importEdit(uri string) map[string]any {
	return map[string]any{"changes": map[string]any{uri: []any{map[string]any{
		"range":   map[string]any{"start": map[string]any{"line": 1, "character": 0}, "end": map[string]any{"line": 1, "character": 0}},
		"newText": "\nimport \"strings\"\n",
	}}}}
}

// importFixture writes the file and a scripted server that reports the missing
// import and answers code actions with actions.
func importFixture(t *testing.T, actions func(uri string) []any) (string, *Manager, *scriptedServer, *readTracker) {
	t.Helper()
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	mustWrite(t, abs, importFixtureSrc)
	m := NewManager(dir)
	fk := startScripted(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/codeAction": func(json.RawMessage) any { return actions(pathToURI(abs)) },
	})
	setDiagnostics(fk, missingStringsDiag)
	trk := newReadTracker()
	trk.MarkRead(abs)
	return abs, m, fk, trk
}

func TestFixMissingImportsAppliesTheSourceAction(t *testing.T) {
	abs, m, fk, trk := importFixture(t, func(uri string) []any {
		return []any{map[string]any{"title": "Organize Imports", "kind": "source.organizeImports", "edit": importEdit(uri)}}
	})
	title, changes, err := m.FixMissingImports(context.Background(), &tools.Env{Workdir: m.Workdir(), Files: trk}, abs, true)
	if err != nil {
		t.Fatalf("FixMissingImports: %v", err)
	}
	if title != "Organize Imports" || len(changes) != 1 {
		t.Fatalf("title %q, %d changes", title, len(changes))
	}
	if got := mustRead(t, abs); !strings.Contains(got, `import "strings"`) {
		t.Fatalf("import not written:\n%s", got)
	}
	// The request asked for the import actions, and carried the missing-name error.
	req := fk.recorded("textDocument/codeAction")
	if len(req) == 0 {
		t.Fatal("no codeAction request was sent")
	}
	var p struct {
		Context struct {
			Diagnostics []json.RawMessage `json:"diagnostics"`
			Only        []string          `json:"only"`
		} `json:"context"`
	}
	if err := json.Unmarshal(req[0], &p); err != nil {
		t.Fatal(err)
	}
	if !contains(strings.Join(p.Context.Only, " "), "source.addMissingImports") || len(p.Context.Diagnostics) != 1 {
		t.Fatalf("codeAction context = %+v", p.Context)
	}
}

func TestFixMissingImportsPrefersAddMissingImports(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	mustWrite(t, abs, importFixtureSrc)
	uri := pathToURI(abs)
	m := NewManager(dir)
	fk := startScripted(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/codeAction": func(json.RawMessage) any {
			return []any{
				map[string]any{"title": "Organize Imports", "kind": "source.organizeImports", "edit": importEdit(uri)},
				map[string]any{"title": "Add all missing imports", "kind": "source.addMissingImports", "edit": importEdit(uri)},
			}
		},
	})
	setDiagnostics(fk, missingStringsDiag)
	trk := newReadTracker()
	trk.MarkRead(abs)
	title, _, err := m.FixMissingImports(context.Background(), &tools.Env{Workdir: dir, Files: trk}, abs, true)
	if err != nil || title != "Add all missing imports" {
		t.Fatalf("title %q, err %v; want the addMissingImports action", title, err)
	}
}

func TestFixMissingImportsWithNoMissingNameLeavesFileAlone(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	mustWrite(t, abs, importFixtureSrc)
	m := NewManager(dir)
	fk := startScripted(t, m, abs, map[string]func(json.RawMessage) any{})
	trk := newReadTracker()
	trk.MarkRead(abs)
	title, changes, err := m.FixMissingImports(context.Background(), &tools.Env{Workdir: dir, Files: trk}, abs, true)
	if err != nil || title != "" || len(changes) != 0 {
		t.Fatalf("no missing names: title %q, %d changes, err %v", title, len(changes), err)
	}
	if fk.count("textDocument/codeAction") != 0 {
		t.Fatal("asked for code actions with no missing name reported")
	}
	if mustRead(t, abs) != importFixtureSrc {
		t.Fatal("file changed")
	}
}

func TestFixMissingImportsWithSeveralCandidatesGuessesNothing(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	mustWrite(t, abs, importFixtureSrc)
	uri := pathToURI(abs)
	m := NewManager(dir)
	fk := startScripted(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/codeAction": func(json.RawMessage) any {
			return []any{
				map[string]any{"title": "Add import from \"strings\"", "kind": "quickfix", "edit": importEdit(uri)},
				map[string]any{"title": "Add import from \"strconv\"", "kind": "quickfix", "edit": importEdit(uri)},
			}
		},
	})
	setDiagnostics(fk, missingStringsDiag)
	trk := newReadTracker()
	trk.MarkRead(abs)
	_, _, err := m.FixMissingImports(context.Background(), &tools.Env{Workdir: dir, Files: trk}, abs, true)
	if err == nil || !strings.Contains(err.Error(), "choose one") {
		t.Fatalf("two import candidates: want a request to choose, got %v", err)
	}
	if mustRead(t, abs) != importFixtureSrc {
		t.Fatal("a guessed import was written")
	}
}

func TestFixMissingImportsTakesTheSingleImportQuickFix(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	mustWrite(t, abs, importFixtureSrc)
	uri := pathToURI(abs)
	m := NewManager(dir)
	fk := startScripted(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/codeAction": func(json.RawMessage) any {
			return []any{
				map[string]any{"title": "Remove unused import \"os\"", "kind": "quickfix", "edit": importEdit(uri)},
				map[string]any{"title": "Add import from \"strings\"", "kind": "quickfix", "edit": importEdit(uri)},
			}
		},
	})
	setDiagnostics(fk, missingStringsDiag)
	trk := newReadTracker()
	trk.MarkRead(abs)
	title, _, err := m.FixMissingImports(context.Background(), &tools.Env{Workdir: dir, Files: trk}, abs, true)
	if err != nil || title != "Add import from \"strings\"" {
		t.Fatalf("title %q, err %v; want the single import quick fix", title, err)
	}
}

func TestFixMissingImportsPreviewWritesNothing(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	mustWrite(t, abs, importFixtureSrc)
	uri := pathToURI(abs)
	m := NewManager(dir)
	fk := startScripted(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/codeAction": func(json.RawMessage) any {
			return []any{map[string]any{"title": "Organize Imports", "kind": "source.organizeImports", "edit": importEdit(uri)}}
		},
	})
	setDiagnostics(fk, missingStringsDiag)
	trk := newReadTracker()
	trk.MarkRead(abs)
	_, changes, err := m.FixMissingImports(context.Background(), &tools.Env{Workdir: dir, Files: trk}, abs, false)
	if err != nil || len(changes) != 1 {
		t.Fatalf("preview: %d changes, err %v", len(changes), err)
	}
	if mustRead(t, abs) != importFixtureSrc {
		t.Fatal("preview wrote the file")
	}
}

func TestFixMissingImportsWithNoServerFixSaysSo(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	mustWrite(t, abs, importFixtureSrc)
	m := NewManager(dir)
	fk := startScripted(t, m, abs, map[string]func(json.RawMessage) any{})
	setDiagnostics(fk, missingStringsDiag)
	trk := newReadTracker()
	trk.MarkRead(abs)
	_, _, err := m.FixMissingImports(context.Background(), &tools.Env{Workdir: dir, Files: trk}, abs, true)
	if err == nil || !strings.Contains(err.Error(), "no import fix") {
		t.Fatalf("want no import fix, got %v", err)
	}
	if mustRead(t, abs) != importFixtureSrc {
		t.Fatal("file changed")
	}
}

func TestRefactorFixMissingImportsTool(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	mustWrite(t, abs, importFixtureSrc)
	uri := pathToURI(abs)
	m := NewManager(dir)
	fk := startScripted(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/codeAction": func(json.RawMessage) any {
			return []any{map[string]any{"title": "Organize Imports", "kind": "source.organizeImports", "edit": importEdit(uri)}}
		},
	})
	setDiagnostics(fk, missingStringsDiag)
	trk := newReadTracker()
	trk.MarkRead(abs)
	env := &tools.Env{Workdir: dir, Files: trk}
	tool, ok := newRegistry(m).Get("refactor")
	if !ok {
		t.Fatal("refactor not registered")
	}
	b, _ := json.Marshal(map[string]any{"action": "fix_missing_imports", "path": "a.go"})
	res := tool.Run(context.Background(), env, b)
	if res.IsError || !strings.Contains(res.Output, "Organize Imports") {
		t.Fatalf("fix_missing_imports: %s", res.Output)
	}
	if !strings.Contains(mustRead(t, abs), `import "strings"`) {
		t.Fatal("import not written")
	}

	// A clean file reports that nothing needed doing.
	mustWrite(t, abs, importFixtureSrc)
	setDiagnostics(fk)
	res = tool.Run(context.Background(), env, b)
	if res.IsError || !strings.Contains(res.Output, "no missing names") {
		t.Fatalf("clean file: %s", res.Output)
	}
	if mustRead(t, abs) != importFixtureSrc {
		t.Fatal("clean file changed")
	}
}
