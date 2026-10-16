package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// readTracker is a FileTracker that remembers what was read and written.
type readTracker struct {
	mu      sync.Mutex
	read    map[string]bool
	written []string
}

func newReadTracker() *readTracker { return &readTracker{read: map[string]bool{}} }

func (r *readTracker) BeforeWrite(abs string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.written = append(r.written, abs)
}
func (r *readTracker) MarkRead(abs string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.read[abs] = true
}
func (r *readTracker) StaleSinceRead(string) bool { return false }
func (r *readTracker) WasRead(abs string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.read[abs]
}

// fakeLSP is a language server that answers requests from handlers, keyed by
// method. A method without a handler gets null. It counts every request.
type fakeLSP struct {
	mu    sync.Mutex
	calls map[string]int
}

func (f *fakeLSP) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

// startFakeLSP installs a scripted Go server for abs's workspace on m, so the
// manager uses it instead of starting one.
func startFakeLSP(t *testing.T, m *Manager, abs string, handlers map[string]func(params json.RawMessage) any) *fakeLSP {
	t.Helper()
	fk := &fakeLSP{calls: map[string]int{}}
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
			fk.mu.Lock()
			fk.calls[method]++
			fk.mu.Unlock()
			var result any
			if h, ok := handlers[method]; ok {
				result = h(msg["params"])
			}
			p.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
		}
	}()
	return fk
}

// runTool calls one registered language-server tool.
func runTool(t *testing.T, dir string, env *tools.Env, name string, args map[string]any) tools.Result {
	t.Helper()
	m := NewManager(dir)
	reg := newRegistry(m)
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("%s not registered", name)
	}
	if env == nil {
		env = &tools.Env{Workdir: dir}
	}
	b, _ := json.Marshal(args)
	return tool.Run(context.Background(), env, b)
}

// greetEdit is a workspace edit that renames Greet to Hello in a.go.
func greetEdit(dir string) map[string]any {
	return map[string]any{"changes": map[string]any{
		pathToURI(filepath.Join(dir, "a.go")): []any{map[string]any{
			"range":   map[string]any{"start": map[string]any{"line": 3, "character": 5}, "end": map[string]any{"line": 3, "character": 10}},
			"newText": "Hello",
		}},
	}}
}

func actionsReply(actions ...any) func(json.RawMessage) any {
	return func(json.RawMessage) any { return actions }
}

func TestCodeActionListsAndMarksServerCommands(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	m := NewManager(dir)
	startFakeLSP(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/codeAction": actionsReply(
			map[string]any{"title": "Rename Greet", "kind": "quickfix", "edit": greetEdit(dir)},
			map[string]any{"title": "Run go generate", "kind": "source", "command": map[string]any{"title": "x", "command": "gen"}},
		),
	})
	reg := newRegistry(m)
	tool, _ := reg.Get("code_action")
	b, _ := json.Marshal(map[string]any{"path": "a.go", "line": 4})
	res := tool.Run(context.Background(), &tools.Env{Workdir: dir}, b)
	if res.IsError {
		t.Fatalf("listing failed: %s", res.Output)
	}
	for _, want := range []string{"1. Rename Greet [quickfix]", "2. Run go generate [source] (runs a server command; cannot be applied here)"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("list missing %q:\n%s", want, res.Output)
		}
	}
}

func TestCodeActionApplyWritesThroughSafePath(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	m := NewManager(dir)
	startFakeLSP(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/codeAction": actionsReply(map[string]any{"title": "Rename Greet", "kind": "quickfix", "edit": greetEdit(dir)}),
	})
	trk := newReadTracker()
	trk.MarkRead(abs)
	env := &tools.Env{Workdir: dir, Files: trk}
	reg := newRegistry(m)
	tool, _ := reg.Get("code_action")
	b, _ := json.Marshal(map[string]any{"path": "a.go", "line": 4, "choose": "rename"})
	res := tool.Run(context.Background(), env, b)
	if res.IsError {
		t.Fatalf("apply failed: %s", res.Output)
	}
	if !strings.Contains(mustRead(t, abs), "func Hello(name string)") {
		t.Fatalf("edit not written:\n%s", mustRead(t, abs))
	}
	if len(trk.written) != 1 || trk.written[0] != abs {
		t.Fatalf("checkpoint hook not called for the changed file: %v", trk.written)
	}
	if !trk.WasRead(abs) {
		t.Fatal("file should be recorded as read at its new content")
	}
	if !strings.Contains(res.Diff, "+func Hello(name string) string {") {
		t.Fatalf("diff missing: %q", res.Diff)
	}
}

func TestCodeActionRefusesUnreadFileAndWritesNothing(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	before := mustRead(t, abs)
	m := NewManager(dir)
	startFakeLSP(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/codeAction": actionsReply(map[string]any{"title": "Rename Greet", "kind": "quickfix", "edit": greetEdit(dir)}),
	})
	trk := newReadTracker()
	env := &tools.Env{Workdir: dir, Files: trk}
	tool, _ := newRegistry(m).Get("code_action")
	b, _ := json.Marshal(map[string]any{"path": "a.go", "line": 4, "choose": 1})
	res := tool.Run(context.Background(), env, b)
	if !res.IsError || !strings.Contains(res.Output, "has not been read this session") {
		t.Fatalf("unread file should be refused: %s", res.Output)
	}
	if mustRead(t, abs) != before || len(trk.written) != 0 {
		t.Fatal("refused apply must not write")
	}
}

func TestCodeActionPreviewWritesNothing(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	before := mustRead(t, abs)
	m := NewManager(dir)
	startFakeLSP(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/codeAction": actionsReply(map[string]any{"title": "Rename Greet", "kind": "quickfix", "edit": greetEdit(dir)}),
	})
	env := &tools.Env{Workdir: dir, Files: newReadTracker()}
	tool, _ := newRegistry(m).Get("code_action")
	b, _ := json.Marshal(map[string]any{"path": "a.go", "line": 4, "choose": 1, "apply": false})
	res := tool.Run(context.Background(), env, b)
	if res.IsError || !strings.Contains(res.Output, "Preview, not written") || !strings.Contains(res.Diff, "+func Hello") {
		t.Fatalf("preview wrong: %s", res.Output)
	}
	if mustRead(t, abs) != before {
		t.Fatal("preview wrote the file")
	}
}

func TestCodeActionResolvesActionsWithoutEdit(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	m := NewManager(dir)
	fk := startFakeLSP(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/codeAction": actionsReply(map[string]any{"title": "Rename Greet", "kind": "quickfix", "data": map[string]any{"id": 7}}),
		"codeAction/resolve": func(params json.RawMessage) any {
			var got struct {
				Data struct {
					ID int `json:"id"`
				} `json:"data"`
			}
			_ = json.Unmarshal(params, &got)
			if got.Data.ID != 7 {
				return map[string]any{"title": "wrong data"}
			}
			return map[string]any{"title": "Rename Greet", "kind": "quickfix", "edit": greetEdit(dir)}
		},
	})
	trk := newReadTracker()
	trk.MarkRead(abs)
	tool, _ := newRegistry(m).Get("code_action")
	b, _ := json.Marshal(map[string]any{"path": "a.go", "line": 4, "choose": 1})
	res := tool.Run(context.Background(), &tools.Env{Workdir: dir, Files: trk}, b)
	if res.IsError {
		t.Fatalf("resolved action should apply: %s", res.Output)
	}
	if fk.count("codeAction/resolve") != 1 {
		t.Fatalf("resolve called %d times, want 1", fk.count("codeAction/resolve"))
	}
	if !strings.Contains(mustRead(t, abs), "func Hello(") {
		t.Fatal("resolved edit not written")
	}
}

func TestCodeActionChooseErrorsListTheChoices(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	m := NewManager(dir)
	startFakeLSP(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/codeAction": actionsReply(
			map[string]any{"title": "Extract to function", "kind": "refactor.extract", "edit": greetEdit(dir)},
			map[string]any{"title": "Extract to variable", "kind": "refactor.extract", "edit": greetEdit(dir)},
		),
	})
	tool, _ := newRegistry(m).Get("code_action")
	try := func(choose any) tools.Result {
		b, _ := json.Marshal(map[string]any{"path": "a.go", "line": 4, "choose": choose})
		return tool.Run(context.Background(), &tools.Env{Workdir: dir, Files: newReadTracker()}, b)
	}
	if res := try(9); !res.IsError || !strings.Contains(res.Output, "from 1 to 2") {
		t.Fatalf("out-of-range choice: %s", res.Output)
	}
	if res := try("extract"); !res.IsError || !strings.Contains(res.Output, "matches 2 actions") {
		t.Fatalf("ambiguous title: %s", res.Output)
	}
	if res := try("nothing like this"); !res.IsError || !strings.Contains(res.Output, "no action title contains") {
		t.Fatalf("unknown title: %s", res.Output)
	}
}

func TestCodeActionNeedsPathAndLine(t *testing.T) {
	dir := goProject(t)
	res := runTool(t, dir, nil, "code_action", map[string]any{"path": "a.go"})
	if !res.IsError || !strings.Contains(res.Output, "needs line") {
		t.Fatalf("missing line: %s", res.Output)
	}
}

func TestRefactorExtractFunctionAppliesServerEdit(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	m := NewManager(dir)
	extract := map[string]any{"changes": map[string]any{
		pathToURI(abs): []any{map[string]any{
			"range":   map[string]any{"start": map[string]any{"line": 4, "character": 0}, "end": map[string]any{"line": 4, "character": 20}},
			"newText": "\treturn newFunction(name)",
		}},
	}}
	fk := startFakeLSP(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/codeAction": func(params json.RawMessage) any {
			var got struct {
				Context struct {
					Only []string `json:"only"`
				} `json:"context"`
			}
			_ = json.Unmarshal(params, &got)
			if len(got.Context.Only) == 1 && got.Context.Only[0] == "refactor.extract.function" {
				return []any{map[string]any{"title": "Extract to function", "kind": "refactor.extract.function", "edit": extract}}
			}
			return []any{}
		},
	})
	trk := newReadTracker()
	trk.MarkRead(abs)
	tool, _ := newRegistry(m).Get("refactor")
	b, _ := json.Marshal(map[string]any{"action": "extract_function", "path": "a.go", "start_line": 5})
	res := tool.Run(context.Background(), &tools.Env{Workdir: dir, Files: trk}, b)
	if res.IsError {
		t.Fatalf("extract failed: %s", res.Output)
	}
	if !strings.Contains(mustRead(t, abs), "return newFunction(name)") {
		t.Fatalf("extract not applied:\n%s", mustRead(t, abs))
	}
	if fk.count("textDocument/codeAction") != 1 {
		t.Fatalf("expected one code action request, got %d", fk.count("textDocument/codeAction"))
	}
}

func TestRefactorSaysUnsupportedWhenServerOffersNothing(t *testing.T) {
	dir := goProject(t)
	m := NewManager(dir)
	startFakeLSP(t, m, filepath.Join(dir, "a.go"), map[string]func(json.RawMessage) any{
		"textDocument/codeAction": actionsReply(),
	})
	tool, _ := newRegistry(m).Get("refactor")
	b, _ := json.Marshal(map[string]any{"action": "extract_variable", "path": "a.go", "start_line": 5})
	res := tool.Run(context.Background(), &tools.Env{Workdir: dir, Files: newReadTracker()}, b)
	if !res.IsError || !strings.Contains(res.Output, "extract_variable is not supported for go") {
		t.Fatalf("expected a not-supported error: %s", res.Output)
	}
}

func TestRefactorWithoutAnyLanguageServerIsNotSupported(t *testing.T) {
	noServers(t)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "tool.py"), "def f(x):\n    return x\n")
	res := runTool(t, dir, nil, "refactor", map[string]any{"action": "extract_function", "path": "tool.py", "start_line": 2})
	if !res.IsError || !strings.Contains(res.Output, "extract_function is not supported for python") {
		t.Fatalf("no server should be a clear not-supported error: %s", res.Output)
	}
}

func TestRefactorRejectsUnknownAction(t *testing.T) {
	dir := goProject(t)
	res := runTool(t, dir, nil, "refactor", map[string]any{"action": "inline", "path": "a.go", "start_line": 4})
	if !res.IsError || !strings.Contains(res.Output, "extract_function or extract_variable") {
		t.Fatalf("unknown refactor action: %s", res.Output)
	}
}

func hierarchyFixture(dir string) map[string]func(json.RawMessage) any {
	aURI := pathToURI(filepath.Join(dir, "a.go"))
	bURI := pathToURI(filepath.Join(dir, "b.go"))
	greet := map[string]any{
		"name": "Greet", "kind": 12, "uri": aURI,
		"range":          map[string]any{"start": map[string]any{"line": 2, "character": 0}, "end": map[string]any{"line": 5, "character": 1}},
		"selectionRange": map[string]any{"start": map[string]any{"line": 3, "character": 5}, "end": map[string]any{"line": 3, "character": 10}},
		"data":           map[string]any{"id": "greet"},
	}
	use := map[string]any{
		"name": "Use", "kind": 12, "uri": bURI,
		"range":          map[string]any{"start": map[string]any{"line": 2, "character": 0}, "end": map[string]any{"line": 4, "character": 1}},
		"selectionRange": map[string]any{"start": map[string]any{"line": 2, "character": 5}, "end": map[string]any{"line": 2, "character": 8}},
	}
	box := map[string]any{
		"name": "Box", "kind": 23, "uri": aURI,
		"range":          map[string]any{"start": map[string]any{"line": 7, "character": 0}, "end": map[string]any{"line": 7, "character": 16}},
		"selectionRange": map[string]any{"start": map[string]any{"line": 7, "character": 5}, "end": map[string]any{"line": 7, "character": 8}},
	}
	child := map[string]any{
		"name": "Child", "kind": 23, "uri": bURI,
		"range":          map[string]any{"start": map[string]any{"line": 2, "character": 0}, "end": map[string]any{"line": 2, "character": 16}},
		"selectionRange": map[string]any{"start": map[string]any{"line": 2, "character": 5}, "end": map[string]any{"line": 2, "character": 10}},
	}
	callSite := []any{map[string]any{"start": map[string]any{"line": 3, "character": 9}, "end": map[string]any{"line": 3, "character": 14}}}
	return map[string]func(json.RawMessage) any{
		"textDocument/prepareCallHierarchy": func(json.RawMessage) any {
			return []any{greet}
		},
		"callHierarchy/incomingCalls": func(params json.RawMessage) any {
			var got struct {
				Item struct {
					Data struct {
						ID string `json:"id"`
					} `json:"data"`
				} `json:"item"`
			}
			_ = json.Unmarshal(params, &got)
			if got.Item.Data.ID != "greet" {
				return []any{} // the server must receive the item it returned, data included
			}
			return []any{map[string]any{"from": use, "fromRanges": callSite}}
		},
		"callHierarchy/outgoingCalls": func(json.RawMessage) any {
			return []any{map[string]any{"to": box, "fromRanges": callSite}}
		},
		"textDocument/prepareTypeHierarchy": func(json.RawMessage) any {
			return []any{box}
		},
		"typeHierarchy/supertypes": func(json.RawMessage) any { return []any{} },
		"typeHierarchy/subtypes":   func(json.RawMessage) any { return []any{child} },
	}
}

func TestNavigateCallHierarchyIncomingAndOutgoing(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	m := NewManager(dir)
	startFakeLSP(t, m, abs, hierarchyFixture(dir))
	tool, _ := newRegistry(m).Get("navigate")
	run := func(args map[string]any) tools.Result {
		b, _ := json.Marshal(args)
		return tool.Run(context.Background(), &tools.Env{Workdir: dir}, b)
	}
	in := run(map[string]any{"action": "incoming_calls", "path": "a.go", "line": 4, "column": 6})
	if in.IsError {
		t.Fatalf("incoming failed: %s", in.Output)
	}
	for _, want := range []string{"Callers of Greet (function) at a.go:4:6", "Use (function) b.go:3:6", "calls at line 4"} {
		if !strings.Contains(in.Output, want) {
			t.Fatalf("incoming missing %q:\n%s", want, in.Output)
		}
	}
	out := run(map[string]any{"action": "callees", "symbol": "Greet"})
	if out.IsError || !strings.Contains(out.Output, "Calls made by Greet") || !strings.Contains(out.Output, "Box (struct) a.go:8:6") {
		t.Fatalf("outgoing wrong: %s", out.Output)
	}
}

func TestNavigateTypeHierarchySuperAndSub(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	m := NewManager(dir)
	startFakeLSP(t, m, abs, hierarchyFixture(dir))
	tool, _ := newRegistry(m).Get("navigate")
	run := func(args map[string]any) tools.Result {
		b, _ := json.Marshal(args)
		return tool.Run(context.Background(), &tools.Env{Workdir: dir}, b)
	}
	sup := run(map[string]any{"action": "super", "path": "a.go", "line": 8, "column": 6})
	if sup.IsError || !strings.Contains(sup.Output, "No supertypes of Box") {
		t.Fatalf("supertypes wrong: %s", sup.Output)
	}
	sub := run(map[string]any{"action": "subtypes", "path": "a.go", "line": 8, "column": 6})
	if sub.IsError || !strings.Contains(sub.Output, "Subtypes of Box") || !strings.Contains(sub.Output, "Child (struct) b.go:3:6") {
		t.Fatalf("subtypes wrong: %s", sub.Output)
	}
}

func TestNavigateHierarchyWithoutServerSaysToInstall(t *testing.T) {
	noServers(t)
	dir := goProject(t)
	res := runNav(t, dir, map[string]any{"action": "incoming_calls", "path": "a.go", "line": 4, "column": 6})
	if !res.IsError || !strings.Contains(res.Output, "installed") {
		t.Fatalf("hierarchy needs a server and should say so: %s", res.Output)
	}
}

func TestNavigateActionAliases(t *testing.T) {
	cases := map[string]string{
		"callers": hierIncoming, "Incoming-Calls": hierIncoming, "callees": hierOutgoing,
		"super": hierSuper, "subtype": hierSub, "definition": "definition",
	}
	for in, want := range cases {
		if got := normalizeNavAction(in); got != want {
			t.Errorf("normalizeNavAction(%q) = %q, want %q", in, got, want)
		}
	}
}

// Sanity check that the item sent back to the server is the one it returned.
func TestHierarchyItemKeepsServerData(t *testing.T) {
	raws := []json.RawMessage{json.RawMessage(`{"name":"F","kind":12,"uri":"file:///x.go","selectionRange":{"start":{"line":1,"character":2},"end":{"line":1,"character":3}},"data":{"k":"v"}}`)}
	items := decodeHierarchyItems(raws)
	if len(items) != 1 {
		t.Fatalf("decoded %d items", len(items))
	}
	if got := fmt.Sprint(string(items[0].rawValue())); !strings.Contains(got, `"data":{"k":"v"}`) {
		t.Fatalf("data not preserved: %s", got)
	}
}
