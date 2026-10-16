package lsp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// TestGoplsHierarchyAndExtract runs the wave 8 tools against a real gopls. It
// is skipped when gopls is not installed.
func TestGoplsHierarchyAndExtract(t *testing.T) {
	dir := goplsFixture(t)
	requireGopls(t, dir)
	m := NewManager(dir)
	defer m.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	trk := newReadTracker()
	trk.MarkRead(filepath.Join(dir, "a.go"))
	trk.MarkRead(filepath.Join(dir, "b.go"))
	env := &tools.Env{Workdir: dir, Files: trk}
	reg := newRegistry(m)
	call := func(name string, args map[string]any) tools.Result {
		t.Helper()
		tool, _ := reg.Get(name)
		b, _ := json.Marshal(args)
		return tool.Run(ctx, env, b)
	}

	t.Run("incoming calls of Greet include Use", func(t *testing.T) {
		res := call("navigate", map[string]any{"action": "incoming_calls", "path": "a.go", "line": 4, "column": 6})
		if res.IsError || !strings.Contains(res.Output, "Use") || !strings.Contains(res.Output, "b.go") {
			t.Fatalf("incoming_calls: %s", res.Output)
		}
	})

	t.Run("code actions list on a line", func(t *testing.T) {
		res := call("code_action", map[string]any{"path": "b.go", "line": 4})
		if res.IsError {
			t.Fatalf("code_action list: %s", res.Output)
		}
	})

	t.Run("extract_function applies the server's edit", func(t *testing.T) {
		ext := filepath.Join(dir, "ext.go")
		mustWrite(t, ext, "package demo\n\nfunc Work(n int) int {\n\ta := n + 1\n\tb := a * 2\n\treturn b\n}\n")
		trk.MarkRead(ext)
		preview := call("refactor", map[string]any{"action": "extract_function", "path": "ext.go", "start_line": 4, "end_line": 5, "apply": false})
		if preview.IsError || !strings.Contains(preview.Output, "Preview, not written") {
			t.Fatalf("preview: %s", preview.Output)
		}
		if strings.Contains(mustRead(t, ext), "newFunction") {
			t.Fatal("preview wrote the file")
		}
		res := call("refactor", map[string]any{"action": "extract_function", "path": "ext.go", "start_line": 4, "end_line": 5})
		if res.IsError {
			t.Fatalf("extract_function: %s", res.Output)
		}
		if got := mustRead(t, ext); !strings.Contains(got, "newFunction(") {
			t.Fatalf("extract not written:\n%s", got)
		}
	})

	t.Run("extract_variable previews or says it is unsupported", func(t *testing.T) {
		res := call("refactor", map[string]any{"action": "extract_variable", "path": "b.go", "start_line": 4, "apply": false})
		if res.IsError {
			if !strings.Contains(res.Output, "not supported") {
				t.Fatalf("unexpected refactor error: %s", res.Output)
			}
			return
		}
		if !strings.Contains(res.Output, "Preview, not written") {
			t.Fatalf("preview should not write: %s", res.Output)
		}
	})
}
