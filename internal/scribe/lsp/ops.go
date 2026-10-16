package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Location is a span in a file. Lines and columns are 1-based; columns count
// code points.
type Location struct {
	Path      string
	Line      int
	Column    int
	EndLine   int
	EndColumn int
}

// Symbol is one entry of a document outline.
type Symbol struct {
	Name     string
	Kind     string
	Detail   string
	Line     int
	Column   int
	Children []Symbol
}

// SymbolInfo is a workspace symbol search hit.
type SymbolInfo struct {
	Name      string
	Kind      string
	Container string
	Path      string
	Line      int
	Column    int
}

// FileDiagnostic is a diagnostic with its location resolved.
type FileDiagnostic struct {
	Path     string
	Line     int
	Column   int
	EndLine  int
	EndCol   int
	Severity string
	Source   string
	Code     string
	Message  string
}

// CodeAction is a quick fix or refactor offered by the server.
type CodeAction struct {
	Title string
	Kind  string
	Edit  *WorkspaceEdit
}

// ErrNoServer is returned by workspace-wide operations when no server is
// running yet and no file was named to start one.
var ErrNoServer = errors.New("no language server is running yet; pass a path so one can be started")

// run syncs abs with the disk on its server and calls fn with the synced
// state. If the server died during the call it is restarted once and fn runs
// again.
func (m *Manager) run(ctx context.Context, abs string, fn func(s *session, sr syncResult) error) error {
	abs = m.abs(abs)
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var s *session
		s, err = m.sessionFor(ctx, abs)
		if err != nil {
			return err
		}
		var sr syncResult
		sr, err = s.syncDoc(abs)
		if err == nil {
			err = fn(s, sr)
		}
		if err == nil || !errors.Is(err, ErrServerExited) {
			return err
		}
	}
	return err
}

func textDocumentPosition(uri string, pos Position) map[string]any {
	return map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
	}
}

// converter turns LSP ranges into Locations, reading each file once per call.
type converter struct {
	texts map[string]string
}

func (c *converter) text(path string) string {
	if c.texts == nil {
		c.texts = map[string]string{}
	}
	t, ok := c.texts[path]
	if !ok {
		if data, err := os.ReadFile(path); err == nil {
			t = string(data)
		}
		c.texts[path] = t
	}
	return t
}

func (c *converter) location(uri string, r Range) Location {
	path, isFile := uriToPath(uri)
	if !isFile {
		return Location{Path: path, Line: r.Start.Line + 1, Column: r.Start.Character + 1,
			EndLine: r.End.Line + 1, EndColumn: r.End.Character + 1}
	}
	text := c.text(path)
	st := positionToPoint(text, r.Start)
	en := positionToPoint(text, r.End)
	return Location{Path: path, Line: st.Line, Column: st.Column, EndLine: en.Line, EndColumn: en.Column}
}

type locRef struct {
	uri string
	r   Range
}

// parseLocations accepts null, Location, Location[] and LocationLink[].
func parseLocations(raw json.RawMessage) ([]locRef, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	type item struct {
		URI            string `json:"uri"`
		Range          Range  `json:"range"`
		TargetURI      string `json:"targetUri"`
		TargetSelRange Range  `json:"targetSelectionRange"`
	}
	var items []item
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, err
		}
	} else {
		var one item
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, err
		}
		items = []item{one}
	}
	out := make([]locRef, 0, len(items))
	for _, it := range items {
		if it.URI != "" {
			out = append(out, locRef{it.URI, it.Range})
		} else if it.TargetURI != "" {
			out = append(out, locRef{it.TargetURI, it.TargetSelRange})
		}
	}
	return out, nil
}

// Definition returns where the symbol at p is defined.
func (m *Manager) Definition(ctx context.Context, abs string, p Point) ([]Location, error) {
	return m.locations(ctx, abs, p, "textDocument/definition", nil)
}

// References returns every reference to the symbol at p, including its
// declaration.
func (m *Manager) References(ctx context.Context, abs string, p Point) ([]Location, error) {
	return m.locations(ctx, abs, p, "textDocument/references", map[string]any{
		"context": map[string]any{"includeDeclaration": true},
	})
}

func (m *Manager) locations(ctx context.Context, abs string, p Point, method string, extra map[string]any) ([]Location, error) {
	var out []Location
	err := m.run(ctx, abs, func(s *session, sr syncResult) error {
		params := textDocumentPosition(sr.uri, pointToPosition(sr.text, p))
		for k, v := range extra {
			params[k] = v
		}
		var raw json.RawMessage
		if err := s.call(ctx, method, params, &raw); err != nil {
			return err
		}
		refs, err := parseLocations(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		var c converter
		for _, r := range refs {
			out = append(out, c.location(r.uri, r.r))
		}
		return nil
	})
	return out, err
}

// Hover returns the hover text for the symbol at p, or "" when there is none.
func (m *Manager) Hover(ctx context.Context, abs string, p Point) (string, error) {
	var out string
	err := m.run(ctx, abs, func(s *session, sr syncResult) error {
		var raw json.RawMessage
		if err := s.call(ctx, "textDocument/hover", textDocumentPosition(sr.uri, pointToPosition(sr.text, p)), &raw); err != nil {
			return err
		}
		out = hoverText(raw)
		return nil
	})
	return out, err
}

func hoverText(raw json.RawMessage) string {
	var h struct {
		Contents json.RawMessage `json:"contents"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &h) != nil || len(h.Contents) == 0 {
		return ""
	}
	return markedText(h.Contents)
}

func markedText(raw json.RawMessage) string {
	raw = []byte(strings.TrimSpace(string(raw)))
	switch {
	case len(raw) == 0 || string(raw) == "null":
		return ""
	case raw[0] == '"':
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	case raw[0] == '[':
		var parts []json.RawMessage
		_ = json.Unmarshal(raw, &parts)
		var outs []string
		for _, p := range parts {
			if t := markedText(p); t != "" {
				outs = append(outs, t)
			}
		}
		return strings.Join(outs, "\n\n")
	default:
		var o struct {
			Value string `json:"value"`
		}
		_ = json.Unmarshal(raw, &o)
		return o.Value
	}
}

type rawSymbol struct {
	Name           string      `json:"name"`
	Detail         string      `json:"detail"`
	Kind           int         `json:"kind"`
	Range          *Range      `json:"range"`
	SelectionRange *Range      `json:"selectionRange"`
	Location       *locRefRaw  `json:"location"`
	ContainerName  string      `json:"containerName"`
	Children       []rawSymbol `json:"children"`
}

type locRefRaw struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// startOf is where a symbol's name sits, used as its position.
func (rs rawSymbol) startOf() (Position, bool) {
	switch {
	case rs.SelectionRange != nil:
		return rs.SelectionRange.Start, true
	case rs.Range != nil:
		return rs.Range.Start, true
	case rs.Location != nil:
		return rs.Location.Range.Start, true
	}
	return Position{}, false
}

// DocumentSymbols returns the outline of abs. Columns are taken from the
// symbol's name, using the file's text on disk.
func (m *Manager) DocumentSymbols(ctx context.Context, abs string) ([]Symbol, error) {
	var out []Symbol
	err := m.run(ctx, abs, func(s *session, sr syncResult) error {
		var raw []rawSymbol
		if err := s.call(ctx, "textDocument/documentSymbol", map[string]any{
			"textDocument": map[string]any{"uri": sr.uri},
		}, &raw); err != nil {
			return err
		}
		c := converter{texts: map[string]string{m.abs(abs): sr.text}}
		for _, rs := range raw {
			out = append(out, rs.toSymbolLocal(&c, sr.text))
		}
		return nil
	})
	return out, err
}

// toSymbolLocal converts a symbol from the file whose text is text.
func (rs rawSymbol) toSymbolLocal(c *converter, text string) Symbol {
	sym := Symbol{Name: rs.Name, Kind: symbolKindName(rs.Kind), Detail: rs.Detail}
	if pos, ok := rs.startOf(); ok {
		if rs.Location != nil {
			loc := c.location(rs.Location.URI, Range{Start: pos, End: pos})
			sym.Line, sym.Column = loc.Line, loc.Column
		} else {
			p := positionToPoint(text, pos)
			sym.Line, sym.Column = p.Line, p.Column
		}
	}
	for _, ch := range rs.Children {
		sym.Children = append(sym.Children, ch.toSymbolLocal(c, text))
	}
	return sym
}

// WorkspaceSymbols searches symbols by name. With a path, only that file's
// server is asked; without one, every running server is.
func (m *Manager) WorkspaceSymbols(ctx context.Context, path, query string) ([]SymbolInfo, error) {
	if path != "" {
		var out []SymbolInfo
		err := m.run(ctx, path, func(s *session, sr syncResult) error {
			var err error
			out, err = s.workspaceSymbols(ctx, query)
			return err
		})
		return out, err
	}
	list := m.running()
	if len(list) == 0 {
		return nil, ErrNoServer
	}
	var out []SymbolInfo
	var lastErr error
	for _, s := range list {
		hits, err := s.workspaceSymbols(ctx, query)
		if err != nil {
			lastErr = err
			continue
		}
		out = append(out, hits...)
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

func (s *session) workspaceSymbols(ctx context.Context, query string) ([]SymbolInfo, error) {
	var raw []rawSymbol
	if err := s.call(ctx, "workspace/symbol", map[string]any{"query": query}, &raw); err != nil {
		return nil, err
	}
	c := converter{}
	out := make([]SymbolInfo, 0, len(raw))
	for _, rs := range raw {
		if rs.Location == nil {
			continue
		}
		loc := c.location(rs.Location.URI, rs.Location.Range)
		out = append(out, SymbolInfo{
			Name: rs.Name, Kind: symbolKindName(rs.Kind), Container: rs.ContainerName,
			Path: loc.Path, Line: loc.Line, Column: loc.Column,
		})
	}
	return out, nil
}

// Rename returns the server's workspace edit renaming the symbol at p. It
// does not apply the edit; see ApplyWorkspaceEdit.
func (m *Manager) Rename(ctx context.Context, abs string, p Point, newName string) (WorkspaceEdit, error) {
	var edit WorkspaceEdit
	err := m.run(ctx, abs, func(s *session, sr syncResult) error {
		// Give the server time to index the project from its config, so
		// cross-file references are known before it renames.
		s.client.waitSettled(ctx, sr.uri, sr.seq, diagQuiet, diagMax)
		params := textDocumentPosition(sr.uri, pointToPosition(sr.text, p))
		params["newName"] = newName
		var raw json.RawMessage
		if err := s.call(ctx, "textDocument/rename", params, &raw); err != nil {
			return err
		}
		if len(raw) == 0 || string(raw) == "null" {
			return errors.New("the server returned no rename edit; the position may not be a renamable symbol")
		}
		return json.Unmarshal(raw, &edit)
	})
	return edit, err
}

// Diagnostics returns the diagnostics for abs. It syncs the file and, if
// the text changed (or the server has not published yet), waits for the
// server to settle for up to 3 seconds.
func (m *Manager) Diagnostics(ctx context.Context, abs string) ([]FileDiagnostic, error) {
	var out []FileDiagnostic
	err := m.run(ctx, abs, func(s *session, sr syncResult) error {
		if sr.changed || sr.seq == 0 {
			s.client.waitSettled(ctx, sr.uri, sr.seq, diagQuiet, diagMax)
		}
		for _, d := range s.client.Diagnostics(sr.uri) {
			start := positionToPoint(sr.text, d.Range.Start)
			end := positionToPoint(sr.text, d.Range.End)
			out = append(out, FileDiagnostic{
				Path: m.abs(abs), Line: start.Line, Column: start.Column,
				EndLine: end.Line, EndCol: end.Column,
				Severity: severityName(d.Severity), Source: d.Source,
				Code: fmt.Sprint(d.Code), Message: strings.TrimSpace(d.Message),
			})
		}
		return nil
	})
	return out, err
}

// Format returns the server's formatting edits for abs. Nothing is applied.
func (m *Manager) Format(ctx context.Context, abs string) ([]TextEdit, error) {
	var out []TextEdit
	err := m.run(ctx, abs, func(s *session, sr syncResult) error {
		insertSpaces := s.spec.Language != "go"
		var raw []TextEdit
		if err := s.call(ctx, "textDocument/formatting", map[string]any{
			"textDocument": map[string]any{"uri": sr.uri},
			"options":      map[string]any{"tabSize": 4, "insertSpaces": insertSpaces},
		}, &raw); err != nil {
			return err
		}
		out = raw
		return nil
	})
	return out, err
}

// CodeActions returns the server's code actions for the span from start to end.
func (m *Manager) CodeActions(ctx context.Context, abs string, start, end Point) ([]CodeAction, error) {
	var out []CodeAction
	err := m.run(ctx, abs, func(s *session, sr syncResult) error {
		rng := Range{Start: pointToPosition(sr.text, start), End: pointToPosition(sr.text, end)}
		var raw []struct {
			Title string         `json:"title"`
			Kind  string         `json:"kind"`
			Edit  *WorkspaceEdit `json:"edit"`
		}
		if err := s.call(ctx, "textDocument/codeAction", map[string]any{
			"textDocument": map[string]any{"uri": sr.uri},
			"range":        rng,
			"context":      map[string]any{"diagnostics": []any{}},
		}, &raw); err != nil {
			return err
		}
		for _, a := range raw {
			out = append(out, CodeAction{Title: a.Title, Kind: a.Kind, Edit: a.Edit})
		}
		return nil
	})
	return out, err
}

// RelPath returns path relative to the workspace, or path itself when it is
// outside it.
func (m *Manager) RelPath(path string) string {
	if rel, err := filepath.Rel(m.workdir, path); err == nil && within(m.workdir, path) {
		return rel
	}
	return path
}

// Workdir is the workspace root.
func (m *Manager) Workdir() string { return m.workdir }
