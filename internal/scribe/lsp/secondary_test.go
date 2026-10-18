package lsp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startScriptedFor is startScripted for any spec: it installs a scripted
// server under the key sessionForSpec would use, so the manager treats it as
// already running. A secondary is installed the same way.
func startScriptedFor(t *testing.T, m *Manager, spec *serverSpec, abs string,
	handlers map[string]func(json.RawMessage) any) *scriptedServer {
	t.Helper()
	fk := &scriptedServer{calls: map[string]int{}, params: map[string][]json.RawMessage{}}
	clientIn, serverOut := io.Pipe()
	serverIn, clientOut := io.Pipe()
	t.Cleanup(func() { _ = clientIn.Close(); _ = serverIn.Close(); _ = serverOut.Close(); _ = clientOut.Close() })
	client := NewClient(clientIn, clientOut)
	p := newPeer(t, serverIn, serverOut)
	root := m.rootFor(serverSpecs["typescript"], abs)
	s := &session{spec: spec, root: root, client: client, docs: map[string]*docState{}, stderr: &tailBuffer{max: 4096}}
	m.mu.Lock()
	m.sessions[spec.key(root)] = s
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
			p.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
		}
	}()
	return fk
}

// tsFile writes a TypeScript file for the workspace and returns its path.
func tsFile(t *testing.T, dir string) string {
	t.Helper()
	abs := filepath.Join(dir, "a.ts")
	if err := os.WriteFile(abs, []byte("export const x = 1\nexport const y = 2\nexport const z = 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return abs
}

// lspDiag is a published diagnostic on line (0-based) with the given source.
func lspDiag(line int, source, msg string) map[string]any {
	return map[string]any{
		"range": map[string]any{
			"start": map[string]any{"line": line, "character": 0},
			"end":   map[string]any{"line": line, "character": 5},
		},
		"severity": 1,
		"source":   source,
		"message":  msg,
	}
}

// mergedDiagnostics is what a caller gets: Diagnostics merges the
// secondary's findings itself.
func mergedDiagnostics(ctx context.Context, m *Manager, abs string) ([]FileDiagnostic, error) {
	return m.Diagnostics(ctx, abs)
}

// onlyPATH points PATH at dir alone and HOME at an empty directory, so no
// language server can be found except what dir holds.
func onlyPATH(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir)
	t.Setenv("HOME", t.TempDir())
}

// fakeBiome writes a biome executable that exits at once, so starting it
// fails during initialize.
func fakeBiome(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "biome")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestSecondaryDiagnosticsMergeAndKeepTheirSource(t *testing.T) {
	dir := t.TempDir()
	abs := tsFile(t, dir)
	m := NewManager(dir)
	prim := startScriptedFor(t, m, serverSpecs["typescript"], abs, nil)
	setDiagnostics(prim, lspDiag(0, "tsc", "cannot find name"))
	lint := startScriptedFor(t, m, lintSpecs["typescript"], abs, nil)
	setDiagnostics(lint, lspDiag(1, "", "unused variable"), lspDiag(2, "biome", "use const"))

	got, err := mergedDiagnostics(context.Background(), m, abs)
	if err != nil {
		t.Fatalf("merged diagnostics: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 diagnostics (1 primary, 2 secondary), got %d: %+v", len(got), got)
	}
	if got[0].Source != "tsc" || got[0].Message != "cannot find name" {
		t.Errorf("primary diagnostic first and labelled by its source, got %+v", got[0])
	}
	// A secondary diagnostic with no source is labelled with the server, not
	// left blank, so merged output still says where it came from.
	if got[1].Source != "typescript lint" || got[1].Line != 2 {
		t.Errorf("unlabelled secondary diagnostic: got source %q line %d", got[1].Source, got[1].Line)
	}
	if got[2].Source != "biome" {
		t.Errorf("secondary source kept as published, got %q", got[2].Source)
	}
}

func TestNavigationStaysOnThePrimary(t *testing.T) {
	dir := t.TempDir()
	abs := tsFile(t, dir)
	m := NewManager(dir)
	prim := startScriptedFor(t, m, serverSpecs["typescript"], abs, map[string]func(json.RawMessage) any{
		"textDocument/definition": func(json.RawMessage) any { return []any{} },
	})
	lint := startScriptedFor(t, m, lintSpecs["typescript"], abs, nil)

	if _, err := m.Definition(context.Background(), abs, Point{Line: 1, Column: 14}); err != nil {
		t.Fatalf("definition: %v", err)
	}
	if n := prim.count("textDocument/definition"); n != 1 {
		t.Errorf("primary got %d definition requests, want 1", n)
	}
	if n := lint.count("textDocument/definition"); n != 0 {
		t.Errorf("secondary must not answer navigation, got %d definition requests", n)
	}
}

func TestSecondaryIsNotUsedWhenItsBinaryIsMissing(t *testing.T) {
	dir := t.TempDir()
	abs := tsFile(t, dir)
	onlyPATH(t, t.TempDir())
	m := NewManager(dir)
	prim := startScriptedFor(t, m, serverSpecs["typescript"], abs, nil)
	setDiagnostics(prim, lspDiag(0, "tsc", "cannot find name"))

	if _, _, ok := m.lintSpecFor(abs); ok {
		t.Fatal("no biome or eslint on PATH, so no secondary should be chosen")
	}
	got, err := mergedDiagnostics(context.Background(), m, abs)
	if err != nil || len(got) != 1 {
		t.Fatalf("want the primary diagnostic alone, got %+v, %v", got, err)
	}
	if n := len(m.Sessions()); n != 0 {
		t.Errorf("a missing secondary must not be started or listed as failed, got %d sessions", n)
	}
	if _, err := m.lintDiagnostics(context.Background(), abs); err != errNoSecondary {
		t.Errorf("lintDiagnostics with no secondary: got %v, want errNoSecondary", err)
	}
}

func TestSecondaryStartFailureDoesNotHidePrimaryDiagnostics(t *testing.T) {
	dir := t.TempDir()
	abs := tsFile(t, dir)
	bin := fakeBiome(t)
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	m := NewManager(dir)
	prim := startScriptedFor(t, m, serverSpecs["typescript"], abs, nil)
	setDiagnostics(prim, lspDiag(0, "tsc", "cannot find name"))

	start := time.Now()
	got, err := mergedDiagnostics(context.Background(), m, abs)
	if err != nil {
		t.Fatalf("a failing secondary must not fail the call: %v", err)
	}
	if len(got) != 1 || got[0].Source != "tsc" {
		t.Fatalf("want the primary diagnostic alone, got %+v", got)
	}
	// The second call is held back by the failure cooldown, not started again.
	if _, err := mergedDiagnostics(context.Background(), m, abs); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("failing secondary took %s; the exit should be seen at once", elapsed)
	}

	var failed []SessionInfo
	for _, s := range m.Sessions() {
		if s.State == StateFailed && s.Language == "typescript" {
			failed = append(failed, s)
		}
	}
	if len(failed) != 1 || !strings.Contains(failed[0].LastError, "biome") {
		t.Fatalf("want the failed biome secondary listed, got %+v", m.Sessions())
	}
	// The primary's health record is its own; the secondary's failure left it
	// alone.
	if h := m.status.health[serverSpecs["typescript"].key(m.rootFor(serverSpecs["typescript"], abs))]; h != nil {
		t.Errorf("primary health was touched by the secondary: %+v", h)
	}
}

func TestWarmStartsAnInstalledSecondary(t *testing.T) {
	dir := t.TempDir()
	onlyPATH(t, fakeBiome(t))
	m := NewManager(dir)

	m.Warm(context.Background(), []string{"typescript"})

	var sawBiome bool
	for _, s := range m.Sessions() {
		if strings.Contains(s.LastError, "biome") {
			sawBiome = true
		}
	}
	if !sawBiome {
		t.Fatalf("Warm should try the installed biome secondary, sessions: %+v", m.Sessions())
	}
}

func TestSecondaryCodeActionsAreMergedAndSentItsOwnDiagnostics(t *testing.T) {
	dir := t.TempDir()
	abs := tsFile(t, dir)
	m := NewManager(dir)
	action := func(title, kind string) func(json.RawMessage) any {
		return func(json.RawMessage) any {
			return []any{map[string]any{"title": title, "kind": kind}}
		}
	}
	startScriptedFor(t, m, serverSpecs["typescript"], abs, map[string]func(json.RawMessage) any{
		"textDocument/codeAction": action("Organize imports", "source.organizeImports"),
	})
	lint := startScriptedFor(t, m, lintSpecs["typescript"], abs, map[string]func(json.RawMessage) any{
		"textDocument/codeAction": action("Biome: fix this", "quickfix"),
	})
	setDiagnostics(lint, lspDiag(0, "biome", "use const"), lspDiag(2, "biome", "far away"))

	ctx := context.Background()
	// Let both servers publish first, as a real session does before asking.
	if _, err := m.Diagnostics(ctx, abs); err != nil {
		t.Fatal(err)
	}
	got, err := m.Actions(ctx, abs, 1, 1, nil, "") // merges the secondary's actions
	if err != nil {
		t.Fatalf("actions: %v", err)
	}
	titles := map[string]bool{}
	for _, a := range got {
		titles[a.Title] = true
	}
	if !titles["Organize imports"] || !titles["Biome: fix this"] || len(got) != 2 {
		t.Fatalf("want primary and secondary actions merged, got %+v", got)
	}

	// Quick fixes are only offered for diagnostics the server published, and
	// only those on the requested lines are sent.
	sent := lint.recorded("textDocument/codeAction")
	if len(sent) == 0 {
		t.Fatal("secondary was never asked for code actions")
	}
	var params struct {
		Context struct {
			Diagnostics []Diagnostic `json:"diagnostics"`
		} `json:"context"`
	}
	if err := json.Unmarshal(sent[len(sent)-1], &params); err != nil {
		t.Fatal(err)
	}
	if len(params.Context.Diagnostics) != 1 || params.Context.Diagnostics[0].Message != "use const" {
		t.Errorf("want only the line-1 biome diagnostic in the context, got %+v", params.Context.Diagnostics)
	}
}

func TestPrimaryActionsSurviveAFailedSecondary(t *testing.T) {
	onlyPATH(t, t.TempDir())
	primary := []CodeAction{{Title: "Organize imports"}}
	m := NewManager(t.TempDir())
	got, err := m.withSecondaryActions(context.Background(), filepath.Join(m.workdir, "a.ts"), 1, 1, nil, primary, nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("no secondary installed: want the primary actions, got %+v, %v", got, err)
	}
}

func TestDisablingTheLanguageAlsoStopsItsSecondary(t *testing.T) {
	dir := t.TempDir()
	abs := tsFile(t, dir)
	m := NewManager(dir)
	startScriptedFor(t, m, lintSpecs["typescript"], abs, nil)
	m.SetDisabled([]string{"typescript"})

	if _, err := m.lintDiagnostics(context.Background(), abs); err != errNoSecondary {
		t.Fatalf("disabled language: secondary should not be used, got %v", err)
	}
}

func TestSecondaryCatalogueIsAddedBesideThePrimary(t *testing.T) {
	if got := serverSpecs["go"].key("/w"); got != "go\x00/w" {
		t.Errorf("primary key format changed: %q", got)
	}
	for lang, lint := range lintSpecs {
		prim, ok := serverSpecs[lang]
		if !ok {
			t.Errorf("%s has a secondary but no primary server", lang)
			continue
		}
		if lint.Role != roleLint || lint.Language != lang {
			t.Errorf("%s secondary has role %q, language %q", lang, lint.Role, lint.Language)
		}
		if len(lint.Candidates) == 0 {
			t.Errorf("%s secondary has no candidate binary", lang)
		}
		primaryNames := map[string]bool{}
		for _, c := range prim.Candidates {
			primaryNames[c.name] = true
		}
		for _, c := range lint.Candidates {
			if primaryNames[c.name] {
				t.Errorf("%s: %s is both primary and secondary", lang, c.name)
			}
		}
		if lint.key("/w") == prim.key("/w") {
			t.Errorf("%s: secondary and primary share a session key", lang)
		}
	}
}
