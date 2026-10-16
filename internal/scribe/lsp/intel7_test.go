package lsp

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/symedit"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// noServers empties PATH and HOME so no language server can be found.
func noServers(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

// goProject writes a small Go module and returns its root.
func goProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/demo\n\ngo 1.22\n",
		"a.go": "package demo\n\n// Greet returns a greeting.\nfunc Greet(name string) string {\n" +
			"\treturn \"hi \" + name\n}\n\ntype Box struct{}\n\nfunc (b Box) Open() {}\n",
		"b.go": "package demo\n\nfunc Use() string {\n\treturn Greet(\"bob\")\n}\n",
	}
	for name, content := range files {
		mustWrite(t, filepath.Join(dir, name), content)
	}
	return dir
}

func runNav(t *testing.T, dir string, args map[string]any) tools.Result {
	t.Helper()
	reg := newRegistry(NewManager(dir))
	tool, ok := reg.Get("navigate")
	if !ok {
		t.Fatal("navigate not registered")
	}
	b, _ := json.Marshal(args)
	return tool.Run(context.Background(), &tools.Env{Workdir: dir}, b)
}

func TestNavigateDefinitionFallsBackToTextTier(t *testing.T) {
	noServers(t)
	dir := goProject(t)
	res := runNav(t, dir, map[string]any{"action": "definition", "symbol": "Greet"})
	if res.IsError {
		t.Fatalf("definition should fall back, not fail: %s", res.Output)
	}
	if !strings.Contains(res.Output, "a.go:4:6") || !strings.Contains(res.Output, "text search") {
		t.Fatalf("fallback output wrong:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "No language server is installed") {
		t.Fatalf("fallback should say why:\n%s", res.Output)
	}
}

func TestNavigateDefinitionByPositionUsesIdentifier(t *testing.T) {
	noServers(t)
	dir := goProject(t)
	// Line 4, column 9 of b.go is the call to Greet.
	res := runNav(t, dir, map[string]any{"action": "definition", "path": "b.go", "line": 4, "column": 9})
	if res.IsError || !strings.Contains(res.Output, "a.go:4:6") {
		t.Fatalf("position-based fallback wrong: %s", res.Output)
	}
}

func TestNavigateDefinitionWithNothingFoundSaysSo(t *testing.T) {
	noServers(t)
	dir := goProject(t)
	res := runNav(t, dir, map[string]any{"action": "definition", "symbol": "NoSuchThing"})
	if !res.IsError || !strings.Contains(res.Output, "could not find the symbol") {
		t.Fatalf("a name found nowhere should say so: %s", res.Output)
	}
}

func TestNavigateSymbolsFallsBackToOutline(t *testing.T) {
	noServers(t)
	dir := goProject(t)
	res := runNav(t, dir, map[string]any{"action": "symbols", "path": "a.go"})
	if res.IsError {
		t.Fatalf("outline should fall back: %s", res.Output)
	}
	for _, want := range []string{"function Greet", "line 4", "No language server is installed"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("outline missing %q:\n%s", want, res.Output)
		}
	}
}

func TestNavigateSymbolsNestsMethodsUnderType(t *testing.T) {
	noServers(t)
	dir := goProject(t)
	res := runNav(t, dir, map[string]any{"action": "symbols", "path": "a.go"})
	// Open is a method of Box, so it is listed under Box, indented.
	if !strings.Contains(res.Output, "  method Open") && !strings.Contains(res.Output, "  function Open") {
		t.Fatalf("method not nested under its type:\n%s", res.Output)
	}
}

func TestNavigateReadSymbolPrintsDefinition(t *testing.T) {
	noServers(t)
	dir := goProject(t)
	res := runNav(t, dir, map[string]any{"action": "read_symbol", "symbol": "Greet"})
	if res.IsError {
		t.Fatalf("read_symbol failed: %s", res.Output)
	}
	// The span starts at the doc comment, which is part of the definition.
	if !strings.Contains(res.Output, "a.go:3-6 (text search)") || !strings.Contains(res.Output, "     4  func Greet(name string) string {") {
		t.Fatalf("definition not printed with line numbers:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "return \"hi \" + name") || strings.Contains(res.Output, "type Box") {
		t.Fatalf("wrong span printed:\n%s", res.Output)
	}
}

func TestNavigateReadSymbolNeedsNameAndKnowsMissing(t *testing.T) {
	noServers(t)
	dir := goProject(t)
	if res := runNav(t, dir, map[string]any{"action": "read_symbol"}); !res.IsError {
		t.Fatal("read_symbol without symbol should fail")
	}
	res := runNav(t, dir, map[string]any{"action": "read_symbol", "path": "a.go", "symbol": "Nope"})
	if !res.IsError || !strings.Contains(res.Output, "could not find the definition") {
		t.Fatalf("missing symbol message: %s", res.Output)
	}
}

func TestNavigateImplementationNeedsServerAndIsAction(t *testing.T) {
	noServers(t)
	dir := goProject(t)
	res := runNav(t, dir, map[string]any{"action": "implementation", "path": "a.go", "line": 4, "column": 6})
	if !res.IsError || !strings.Contains(res.Output, "installed") {
		t.Fatalf("implementation without a server should say so: %+v", res)
	}
	bad := runNav(t, dir, map[string]any{"action": "nonsense"})
	if !bad.IsError || !strings.Contains(bad.Output, "read_symbol") || !strings.Contains(bad.Output, "implementation") {
		t.Fatalf("unknown-action message should list the actions: %s", bad.Output)
	}
}

func TestNestDefinitionsPutsMembersUnderParents(t *testing.T) {
	defs := []symedit.Definition{
		{Name: "Box", Kind: "class", Line: 1, Column: 6, EndLine: 1},
		{Name: "Open", Parent: "Box", Kind: "method", Line: 2, Column: 9, EndLine: 2},
		{Name: "Loose", Parent: "Missing", Kind: "method", Line: 3, Column: 1, EndLine: 3},
	}
	out := nestDefinitions(defs)
	if len(out) != 2 || len(out[0].Children) != 1 || out[0].Children[0].Name != "Open" {
		t.Fatalf("nesting wrong: %+v", out)
	}
	if out[1].Name != "Loose" {
		t.Fatalf("orphan member should stay top level: %+v", out)
	}
}

func TestDetectLanguagesFromMarkersAndSources(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module x\n")
	mustWrite(t, filepath.Join(dir, "web", "tsconfig.json"), "{}")
	mustWrite(t, filepath.Join(dir, "tools", "a.py"), "x = 1\n")
	mustWrite(t, filepath.Join(dir, "tools", "b.py"), "x = 2\n")
	mustWrite(t, filepath.Join(dir, "tools", "c.py"), "x = 3\n")
	mustWrite(t, filepath.Join(dir, "scripts", "one.rb"), "puts 1\n")
	mustWrite(t, filepath.Join(dir, ".hidden", "Cargo.toml"), "[package]\n")
	mustWrite(t, filepath.Join(dir, "node_modules", "pkg", "Cargo.toml"), "[package]\n")
	got := DetectLanguages(dir)
	want := map[string]bool{"go": true, "typescript": true, "python": true}
	for _, l := range got {
		if !want[l] {
			t.Fatalf("unexpected language %q in %v", l, got)
		}
		delete(want, l)
	}
	if len(want) != 0 {
		t.Fatalf("missing %v, got %v", want, got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("not sorted: %v", got)
		}
	}
}

func TestDetectLanguagesEmptyDirectory(t *testing.T) {
	if got := DetectLanguages(t.TempDir()); len(got) != 0 {
		t.Fatalf("empty project detected %v", got)
	}
}

func TestFormatFileRangeAndOrganizeArguments(t *testing.T) {
	noServers(t)
	dir := goProject(t)
	reg := newRegistry(NewManager(dir))
	tool, _ := reg.Get("format_file")
	run := func(args map[string]any) tools.Result {
		b, _ := json.Marshal(args)
		return tool.Run(context.Background(), &tools.Env{Workdir: dir}, b)
	}
	if res := run(map[string]any{"path": "a.go", "start_line": 9, "end_line": 2}); !res.IsError || !strings.Contains(res.Output, "start_line") {
		t.Fatalf("inverted range should be refused: %s", res.Output)
	}
	if res := run(map[string]any{"path": "a.go", "start_line": 1, "end_line": 3}); !res.IsError || !strings.Contains(res.Output, "installed") {
		t.Fatalf("range format without a server should explain: %s", res.Output)
	}
	if res := run(map[string]any{"path": "a.go", "organize_imports": true}); !res.IsError || !strings.Contains(res.Output, "installed") {
		t.Fatalf("organize without a server should explain: %s", res.Output)
	}
}

// fakeServerSession serves rename requests from a scripted peer. The first
// rename answers null, as a server still indexing does; the second returns
// an edit that changes one word.
func fakeServerSession(t *testing.T, m *Manager, abs string, renameReplies []string) *int {
	t.Helper()
	clientIn, serverOut := io.Pipe()
	serverIn, clientOut := io.Pipe()
	t.Cleanup(func() { _ = clientIn.Close(); _ = serverIn.Close(); _ = serverOut.Close(); _ = clientOut.Close() })
	client := NewClient(clientIn, clientOut)
	p := newPeer(t, serverIn, serverOut)
	spec := serverSpecs["go"]
	root := m.rootFor(spec, abs)
	s := &session{spec: spec, root: root, client: client, docs: map[string]*docState{}, stderr: &tailBuffer{max: 4096}}
	m.mu.Lock()
	m.sessions[spec.Language+"\x00"+root] = s
	m.mu.Unlock()

	calls := 0
	go func() {
		for {
			msg := p.readOrStop()
			if msg == nil {
				return
			}
			var method string
			_ = json.Unmarshal(msg["method"], &method)
			id, hasID := msg["id"]
			if !hasID {
				continue
			}
			var reply any
			if method == "textDocument/rename" {
				text := "null"
				if calls < len(renameReplies) {
					text = renameReplies[calls]
				}
				calls++
				var result any
				_ = json.Unmarshal([]byte(text), &result)
				reply = map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result}
			} else {
				reply = map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": nil}
			}
			p.write(reply)
		}
	}()
	return &calls
}

// readOrStop reads one message, or returns nil when the pipe closes.
func (p *peer) readOrStop() map[string]json.RawMessage {
	body, err := readFrame(p.r)
	if err != nil {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return nil
	}
	return m
}

func TestRenameWaitsAndAsksAgainWhenNoEditIsReturned(t *testing.T) {
	dir := goProject(t)
	m := NewManager(dir)
	abs := filepath.Join(dir, "a.go")
	edit := `{"changes":{"` + pathToURI(filepath.Join(dir, "b.go")) + `":[{"range":{"start":{"line":3,"character":8},"end":{"line":3,"character":13}},"newText":"Hello"}]}}`
	calls := fakeServerSession(t, m, abs, []string{"null", edit})

	start := time.Now()
	got, err := m.Rename(context.Background(), abs, Point{Line: 4, Column: 6}, "Hello")
	if err != nil {
		t.Fatalf("rename should succeed on the second ask: %v", err)
	}
	if *calls != 2 {
		t.Fatalf("server asked %d time(s), want 2", *calls)
	}
	if time.Since(start) < diagMax {
		t.Fatalf("retry did not wait before asking again")
	}
	if len(got.Changes) != 1 {
		t.Fatalf("edit not returned: %+v", got)
	}
}

func TestRenameGivesUpAfterOneRetry(t *testing.T) {
	dir := goProject(t)
	m := NewManager(dir)
	abs := filepath.Join(dir, "a.go")
	calls := fakeServerSession(t, m, abs, []string{"null", "null"})
	_, err := m.Rename(context.Background(), abs, Point{Line: 4, Column: 6}, "Hello")
	if err == nil || !strings.Contains(err.Error(), "even after waiting") {
		t.Fatalf("expected a clear refusal, got %v", err)
	}
	if *calls != 2 {
		t.Fatalf("server asked %d time(s), want 2", *calls)
	}
}
