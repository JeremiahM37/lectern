package nvim

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// run calls the editor tool with args and returns the result.
func run(t *testing.T, env *tools.Env, args map[string]any) tools.Result {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return Tool().Run(context.Background(), env, b)
}

// envFor is the tool environment for a project at dir.
func envFor(dir string) *tools.Env { return &tools.Env{Workdir: dir} }

func TestToolSpecIsValid(t *testing.T) {
	spec := Tool().Spec()
	if spec.Name != "editor" {
		t.Fatalf("name %q", spec.Name)
	}
	var schema map[string]any
	if err := json.Unmarshal(spec.Schema, &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	for _, a := range actions {
		if !validAction(a) {
			t.Fatalf("%s not valid", a)
		}
	}
	if Tool().ReadOnly() {
		t.Fatal("the editor tool can edit, so it is not read-only as a whole")
	}
	if !Tool().(tool).ReadOnlyCall(json.RawMessage(`{"action":"diagnostics"}`)) {
		t.Fatal("diagnostics should be a read-only call")
	}
	if Tool().(tool).ReadOnlyCall(json.RawMessage(`{"action":"edit"}`)) {
		t.Fatal("edit must not be a read-only call")
	}
}

func TestToolReadReturnsNumberedRange(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "r.txt")
	os.WriteFile(file, []byte("alpha\nbeta\ngamma\ndelta\n"), 0o644)
	s := startNvim(t, file)
	_ = s
	res := run(t, envFor(dir), map[string]any{"action": "read", "startLine": 2, "endLine": 3})
	if res.IsError {
		t.Fatalf("read failed: %s", res.Output)
	}
	if !strings.Contains(res.Output, "2\tbeta") || !strings.Contains(res.Output, "3\tgamma") || strings.Contains(res.Output, "alpha") {
		t.Fatalf("range not returned:\n%s", res.Output)
	}
}

func TestToolEditSavesAndReportsTheFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "e.txt")
	os.WriteFile(file, []byte("one\ntwo\nthree\n"), 0o644)
	s := startNvim(t, file)
	got := make(chan string, 4)
	SetWriteHook(func(abs string) { got <- abs })
	t.Cleanup(func() { SetWriteHook(nil) })

	repl := "TWO\nTWO-B"
	res := run(t, envFor(dir), map[string]any{"action": "edit", "startLine": 2, "endLine": 2, "replacement": repl})
	if res.IsError {
		t.Fatalf("edit failed: %s", res.Output)
	}
	if res.Output != "Replaced lines 2-2 with 2 line(s) (saved)" {
		t.Fatalf("output %q", res.Output)
	}
	if len(res.Files) != 1 || res.Files[0] != "e.txt" {
		t.Fatalf("files %v", res.Files)
	}
	disk, _ := os.ReadFile(file)
	if string(disk) != "one\nTWO\nTWO-B\nthree\n" {
		t.Fatalf("disk content %q", disk)
	}
	select {
	case p := <-got:
		t.Fatalf("the agent's own save was reported as a user save: %s", p)
	case <-time.After(300 * time.Millisecond):
	}
	_ = s
}

func TestToolEditRejectsBadRange(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "b.txt")
	os.WriteFile(file, []byte("only\n"), 0o644)
	startNvim(t, file)
	res := run(t, envFor(dir), map[string]any{"action": "edit", "startLine": 9, "replacement": "x"})
	if !res.IsError || !strings.Contains(res.Output, "outside") {
		t.Fatalf("bad range accepted: %+v", res)
	}
	disk, _ := os.ReadFile(file)
	if string(disk) != "only\n" {
		t.Fatalf("file changed on a refused edit: %q", disk)
	}
}

func TestToolNavigateCursorContextAndSelection(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "n.txt")
	os.WriteFile(file, []byte("a\nb\nfind me\nd\n"), 0o644)
	startNvim(t, file)
	env := envFor(dir)

	res := run(t, env, map[string]any{"action": "navigate", "search": "find"})
	if res.IsError || !strings.HasPrefix(res.Output, "n.txt:3:") {
		t.Fatalf("navigate by search: %+v", res)
	}
	res = run(t, env, map[string]any{"action": "goto_cursor", "line": 4, "col": 1})
	if res.IsError || !strings.HasPrefix(res.Output, "n.txt:4:1") {
		t.Fatalf("goto_cursor: %+v", res)
	}
	if res := run(t, env, map[string]any{"action": "navigate", "search": "nothing-here"}); !res.IsError {
		t.Fatal("a missing pattern was not reported")
	}
	run(t, env, map[string]any{"action": "select", "startLine": 1, "endLine": 2})
	res = run(t, env, map[string]any{"action": "cursor_context"})
	if !strings.Contains(res.Output, "file: n.txt") || !strings.Contains(res.Output, "selection: lines 1-2") {
		t.Fatalf("cursor_context:\n%s", res.Output)
	}
}

func TestToolYankWritesRegister(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "y.txt")
	os.WriteFile(file, []byte("y\n"), 0o644)
	s := startNvim(t, file)
	res := run(t, envFor(dir), map[string]any{"action": "yank", "text": "copied text", "register": "x"})
	if res.IsError {
		t.Fatalf("yank: %s", res.Output)
	}
	if v := eval(t, s, "getreg('x')"); v != "copied text" {
		t.Fatalf("register holds %v", v)
	}
}

func TestToolBuffersDiagnosticsQuickfixAndNoLSP(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "d.txt")
	os.WriteFile(file, []byte("x\n"), 0o644)
	startNvim(t, file)
	env := envFor(dir)

	if res := run(t, env, map[string]any{"action": "buffers"}); !strings.Contains(res.Output, "d.txt [current]") {
		t.Fatalf("buffers: %s", res.Output)
	}
	if res := run(t, env, map[string]any{"action": "diagnostics"}); res.IsError || !strings.Contains(res.Output, "No diagnostics") {
		t.Fatalf("diagnostics: %+v", res)
	}
	if res := run(t, env, map[string]any{"action": "quickfix"}); res.IsError || !strings.Contains(res.Output, "empty") {
		t.Fatalf("quickfix: %+v", res)
	}
	res := run(t, env, map[string]any{"action": "hover", "line": 1, "col": 1})
	if !res.IsError || res.Output != "no LSP client attached for this buffer" {
		t.Fatalf("hover without a server: %+v", res)
	}
	if res := run(t, env, map[string]any{"action": "terminal_output"}); !res.IsError {
		t.Fatal("terminal_output with no terminal should say so")
	}
}

func TestToolSwitchOpensFileAndKeepsUnsavedBuffer(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "cur.txt")
	other := filepath.Join(dir, "other.txt")
	os.WriteFile(cur, []byte("c\n"), 0o644)
	os.WriteFile(other, []byte("o\n"), 0o644)
	s := startNvim(t, cur)
	env := envFor(dir)

	if res := run(t, env, map[string]any{"action": "navigate", "file": "other.txt"}); res.IsError || !strings.HasPrefix(res.Output, "other.txt:") {
		t.Fatalf("switch to other: %+v", res)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.Lua(ctx, `vim.api.nvim_buf_set_lines(0, 0, -1, false, {"dirty"})`); err != nil {
		t.Fatal(err)
	}
	res := run(t, env, map[string]any{"action": "navigate", "file": "cur.txt"})
	if !res.IsError || !strings.Contains(res.Output, "unsaved changes") {
		t.Fatalf("switch away from an unsaved buffer was allowed: %+v", res)
	}
}

func TestToolRefusesForbiddenFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secret.env")
	os.WriteFile(file, []byte("KEY=1\n"), 0o644)
	startNvim(t, file)
	env := &tools.Env{Workdir: dir, Services: map[string]any{"forbidden": []string{"*.env"}}}
	res := run(t, env, map[string]any{"action": "read", "startLine": 1, "endLine": 1})
	if !res.IsError || res.Tag != "forbidden" {
		t.Fatalf("forbidden current buffer was read: %+v", res)
	}
	res = run(t, env, map[string]any{"action": "read", "file": "secret.env"})
	if !res.IsError || res.Tag != "forbidden" {
		t.Fatalf("forbidden file switch was allowed: %+v", res)
	}
}

func TestToolAccessGate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "g.txt")
	os.WriteFile(file, []byte("g\n"), 0o644)
	t.Cleanup(func() { SetAccess(AccessOn) })

	SetAccess(AccessOff)
	if res := run(t, envFor(dir), map[string]any{"action": "read"}); !res.IsError || res.Output != "Editor tool is disabled." || res.Tag != tagEditorDisabled {
		t.Fatalf("off: %+v", res)
	}

	SetAccess(AccessWhenOpen)
	res := run(t, envFor(dir), map[string]any{"action": "read"})
	if !res.IsError || res.Tag != tagEditorNotOpen || !strings.Contains(res.Output, "requires the editor panel to be open") {
		t.Fatalf("when-open with no editor: %+v", res)
	}
	startNvim(t, file)
	if res := run(t, envFor(dir), map[string]any{"action": "buffers"}); res.IsError {
		t.Fatalf("when-open with an editor open refused: %s", res.Output)
	}

	SetAccess(AccessOn)
	if res := run(t, envFor(dir), map[string]any{"action": "buffers"}); res.IsError {
		t.Fatalf("on with an editor open refused: %s", res.Output)
	}
}

func TestToolWithoutEditorSaysSo(t *testing.T) {
	mu.Lock()
	prev := current
	current = nil
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); current = prev; mu.Unlock() })
	res := run(t, envFor(t.TempDir()), map[string]any{"action": "read"})
	if !res.IsError || res.Tag != tagEditorNotOpen {
		t.Fatalf("no editor: %+v", res)
	}
}

func TestParseAccess(t *testing.T) {
	cases := map[string]Access{"on": AccessOn, "Always": AccessOn, "when-open": AccessWhenOpen, "never": AccessOff, "off": AccessOff}
	for in, want := range cases {
		got, err := ParseAccess(in)
		if err != nil || got != want {
			t.Fatalf("ParseAccess(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseAccess("sometimes"); err == nil {
		t.Fatal("bad value accepted")
	}
	if AccessWhenOpen.String() != "when-open" || AccessOff.Label() != "Never" {
		t.Fatal("names do not round-trip")
	}
}

func TestToolRejectsUnknownAction(t *testing.T) {
	res := run(t, envFor(t.TempDir()), map[string]any{"action": "explode"})
	if !res.IsError || !strings.Contains(res.Output, "unknown action") {
		t.Fatalf("unknown action: %+v", res)
	}
}

// Code actions need a server: listing and applying without one say so, and
// listing is read-only (plan mode allows it), applying is not.
func TestToolCodeActionsWithoutServerAndClassification(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "c.txt")
	os.WriteFile(file, []byte("x\n"), 0o644)
	startNvim(t, file)
	env := envFor(dir)

	res := run(t, env, map[string]any{"action": "code_actions"})
	if !res.IsError || res.Output != "no LSP client attached for this buffer" {
		t.Fatalf("code_actions without a server: %+v", res)
	}
	res = run(t, env, map[string]any{"action": "apply_code_action", "index": 1})
	if !res.IsError {
		t.Fatalf("apply_code_action without a server must fail: %+v", res)
	}
	if !readOnlyActions["code_actions"] || readOnlyActions["apply_code_action"] {
		t.Fatal("code_actions must be read-only and apply_code_action must not be")
	}
}
