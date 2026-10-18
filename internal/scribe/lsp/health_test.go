package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// scriptedServer is a fake language server. Handlers answer requests by
// method; a handler that returns an *rpcError gets a JSON-RPC error. Every
// request is counted and its params kept. Diagnostics are published for abs on
// each didOpen and didChange.
type scriptedServer struct {
	mu          sync.Mutex
	calls       map[string]int
	params      map[string][]json.RawMessage
	diagnostics []any
}

func (s *scriptedServer) count(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method]
}

func (s *scriptedServer) recorded(method string) []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]json.RawMessage(nil), s.params[method]...)
}

// startScripted installs a scripted Go server for abs's workspace on m.
func startScripted(t *testing.T, m *Manager, abs string, handlers map[string]func(json.RawMessage) any) *scriptedServer {
	t.Helper()
	fk := &scriptedServer{calls: map[string]int{}, params: map[string][]json.RawMessage{}}
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
				if method == "textDocument/didOpen" || method == "textDocument/didChange" {
					fk.mu.Lock()
					diags := fk.diagnostics
					fk.mu.Unlock()
					p.write(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics",
						"params": map[string]any{"uri": pathToURI(abs), "diagnostics": diags}})
				}
				continue
			}
			fk.mu.Lock()
			fk.calls[method]++
			fk.params[method] = append(fk.params[method], msg["params"])
			fk.mu.Unlock()
			var result any
			if h, ok := handlers[method]; ok {
				result = h(msg["params"])
			}
			if e, ok := result.(*rpcError); ok {
				p.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "error": e})
				continue
			}
			p.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
		}
	}()
	return fk
}

func setDiagnostics(fk *scriptedServer, diags ...any) {
	fk.mu.Lock()
	defer fk.mu.Unlock()
	fk.diagnostics = diags
}

// failingDefinition is a server whose definition lookups fail inside the server.
func failingDefinition(json.RawMessage) any {
	return &rpcError{Code: -32603, Message: "internal failure"}
}

// defPos is a position inside the first function of the goProject fixture.
var defPos = Point{Line: 1, Column: 1}

func TestOperationFailuresDegradeTheServer(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	m := NewManager(dir)
	fk := startScripted(t, m, abs, map[string]func(json.RawMessage) any{"textDocument/definition": failingDefinition})
	ctx := context.Background()
	key := serverSpecs["go"].Language + "\x00" + m.rootFor(serverSpecs["go"], abs)

	for i := 1; i < degradeAfter; i++ {
		_, err := m.Definition(ctx, abs, defPos)
		if err == nil || !strings.Contains(err.Error(), "use grep or read instead") {
			t.Fatalf("failure %d: want an error that says to fall back, got %v", i, err)
		}
		if st := m.Sessions(); len(st) > 0 && st[0].State == StateDegraded {
			t.Fatalf("degraded after only %d failures", i)
		}
	}
	if _, err := m.Definition(ctx, abs, defPos); err == nil {
		t.Fatal("third failure: want an error")
	}
	m.status.mu.Lock()
	m.status.info[key] = SessionInfo{Language: "go", Root: m.rootFor(serverSpecs["go"], abs), State: StateRunning}
	m.status.mu.Unlock()
	if got := m.Sessions()[0].State; got != StateDegraded {
		t.Fatalf("after %d failures state = %q, want degraded", degradeAfter, got)
	}
	if last := m.Sessions()[0].LastError; !strings.Contains(last, "internal failure") {
		t.Fatalf("degraded state should carry the last error, got %q", last)
	}

	// While degraded the server is not asked: the call fails at once.
	before := fk.count("textDocument/definition")
	_, err := m.Definition(ctx, abs, defPos)
	if err == nil || !strings.Contains(err.Error(), "not asked again") {
		t.Fatalf("degraded call: want a skip error, got %v", err)
	}
	if fk.count("textDocument/definition") != before {
		t.Fatal("a degraded server was still sent the request")
	}

	// After the cooling-off period one probe goes through. Its success clears
	// the degraded state. The probe runs against a healthy server.
	m.status.mu.Lock()
	m.status.health[key].lastAt = time.Now().Add(-failCooldown - time.Second)
	m.status.mu.Unlock()
	fixed := startScripted(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/definition": func(json.RawMessage) any { return []any{} },
	})
	if _, err := m.Definition(ctx, abs, defPos); err != nil {
		t.Fatalf("probe after cooldown: %v", err)
	}
	if fixed.count("textDocument/definition") != 1 {
		t.Fatal("probe was not sent after the cooldown")
	}
	if got := m.Sessions()[0].State; got == StateDegraded {
		t.Fatal("a successful probe should clear the degraded state")
	}
}

func TestRequestErrorsDoNotDegrade(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	m := NewManager(dir)
	fk := startScripted(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/definition": func(json.RawMessage) any { return &rpcError{Code: invalidParams, Message: "bad position"} },
	})
	for i := 0; i < degradeAfter+2; i++ {
		if _, err := m.Definition(context.Background(), abs, defPos); err == nil {
			t.Fatal("want the server's error")
		}
	}
	if fk.count("textDocument/definition") != degradeAfter+2 {
		t.Fatalf("invalid-params errors should never skip the server; sent %d requests", fk.count("textDocument/definition"))
	}
}

func TestSuccessResetsTheFailureCount(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	m := NewManager(dir)
	fail := true
	var mu sync.Mutex
	startScripted(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/definition": func(json.RawMessage) any {
			mu.Lock()
			defer mu.Unlock()
			if fail {
				return &rpcError{Code: -32603, Message: "flaky"}
			}
			return []any{}
		},
	})
	setFail := func(v bool) { mu.Lock(); fail = v; mu.Unlock() }
	ctx := context.Background()
	for i := 0; i < degradeAfter-1; i++ {
		_, _ = m.Definition(ctx, abs, defPos)
	}
	setFail(false)
	if _, err := m.Definition(ctx, abs, defPos); err != nil {
		t.Fatalf("success: %v", err)
	}
	setFail(true)
	for i := 0; i < degradeAfter-1; i++ {
		_, _ = m.Definition(ctx, abs, defPos)
	}
	key := serverSpecs["go"].Language + "\x00" + m.rootFor(serverSpecs["go"], abs)
	m.status.mu.Lock()
	h := m.status.health[key]
	m.status.mu.Unlock()
	if h != nil && h.failures >= degradeAfter {
		t.Fatalf("failures should restart from zero after a success, got %d", h.failures)
	}
}

func TestCallerCancelDoesNotCount(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	m := NewManager(dir)
	startScripted(t, m, abs, map[string]func(json.RawMessage) any{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < degradeAfter+1; i++ {
		_, _ = m.Definition(ctx, abs, defPos)
	}
	key := serverSpecs["go"].Language + "\x00" + m.rootFor(serverSpecs["go"], abs)
	m.status.mu.Lock()
	defer m.status.mu.Unlock()
	if h := m.status.health[key]; h != nil && h.failures > 0 {
		t.Fatalf("a cancelled caller counted %d failures against the server", h.failures)
	}
}

func TestServerFailureClassification(t *testing.T) {
	if !countsAgainstServer(ErrServerExited) {
		t.Fatal("an exited server counts")
	}
	if !countsAgainstServer(&rpcError{Code: -32603}) {
		t.Fatal("an internal error counts")
	}
	if countsAgainstServer(&rpcError{Code: invalidParams}) {
		t.Fatal("invalid params is the caller's fault")
	}
	if countsAgainstServer(errors.New("file not found")) {
		t.Fatal("a local error is not the server's fault")
	}
}

func TestNavigateFallbackSaysToUseTextSearch(t *testing.T) {
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	m := NewManager(dir)
	startScripted(t, m, abs, map[string]func(json.RawMessage) any{"textDocument/definition": failingDefinition})
	tool, ok := newRegistry(m).Get("navigate")
	if !ok {
		t.Fatal("navigate not registered")
	}
	b, _ := json.Marshal(map[string]any{"action": "definition", "path": "a.go", "line": 4, "column": 6})
	res := tool.Run(context.Background(), &tools.Env{Workdir: dir}, b)
	if !strings.Contains(res.Output, "text search") || !strings.Contains(res.Output, "grep or read") {
		t.Fatalf("a failed lookup should fall back and say so: %s", res.Output)
	}
}

func TestDegradedStateIsKeptOutOfDisk(t *testing.T) {
	// Degrading changes only what is asked; the file on disk is untouched.
	dir := goProject(t)
	abs := filepath.Join(dir, "a.go")
	before, _ := os.ReadFile(abs)
	m := NewManager(dir)
	startScripted(t, m, abs, map[string]func(json.RawMessage) any{"textDocument/definition": failingDefinition})
	for i := 0; i < degradeAfter+1; i++ {
		_, _ = m.Definition(context.Background(), abs, defPos)
	}
	after, _ := os.ReadFile(abs)
	if string(before) != string(after) {
		t.Fatal("the file changed")
	}
}
