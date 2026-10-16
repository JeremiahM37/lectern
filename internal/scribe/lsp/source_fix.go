package lsp

import (
	"context"
	"errors"
	"strings"
)

// sourceFixKinds are the file-wide fixes run after a compound edit, in order.
// Organizing imports first means fix-all sees the final import block.
var sourceFixKinds = []string{"source.organizeImports", "source.fixAll"}

// SourceFix applies the server's file-wide fixes to abs: organized imports,
// then fixes for auto-fixable diagnostics. A server that does not offer one of
// them is skipped. It returns the absolute paths it changed and writes the
// changes to disk, keeping open documents in step. A server that cannot start
// is an error; one that answers "not supported" is not.
func (m *Manager) SourceFix(ctx context.Context, abs string) ([]string, error) {
	var changed []string
	err := m.run(ctx, abs, func(s *session, sr syncResult) error {
		end := Point{Line: strings.Count(sr.text, "\n") + 1, Column: 1}
		rng := Range{Start: pointToPosition(sr.text, Point{Line: 1, Column: 1}), End: pointToPosition(sr.text, end)}
		for _, kind := range sourceFixKinds {
			var raw []struct {
				Title string         `json:"title"`
				Kind  string         `json:"kind"`
				Edit  *WorkspaceEdit `json:"edit"`
			}
			err := s.call(ctx, "textDocument/codeAction", map[string]any{
				"textDocument": map[string]any{"uri": sr.uri},
				"range":        rng,
				"context":      map[string]any{"diagnostics": []any{}, "only": []string{kind}},
			}, &raw)
			var rpc *rpcError
			if errors.As(err, &rpc) {
				continue // the server does not offer this kind
			}
			if err != nil {
				return err
			}
			for _, a := range raw {
				if a.Edit == nil || (len(a.Edit.Changes) == 0 && len(a.Edit.DocumentChanges) == 0) {
					continue
				}
				applied, err := ApplyWorkspaceEdit(*a.Edit)
				if err != nil {
					return err
				}
				for _, p := range applied.Paths() {
					m.FileChanged(p)
					changed = append(changed, p)
				}
			}
		}
		return nil
	})
	return dedupe(changed), err
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// QuickFixes returns the quick fixes the server offers for one diagnostic. The
// diagnostic is sent back in the request context, because servers answer
// quick-fix requests only for diagnostics they are shown. Nothing is applied.
func (m *Manager) QuickFixes(ctx context.Context, abs string, d FileDiagnostic) ([]CodeAction, error) {
	var out []CodeAction
	err := m.run(ctx, abs, func(s *session, sr syncResult) error {
		pos := pointToPosition(sr.text, Point{Line: d.Line, Column: d.Column})
		rng := Range{Start: pos, End: pos}
		diag := map[string]any{"range": rng, "message": d.Message, "severity": 1}
		if d.Source != "" {
			diag["source"] = d.Source
		}
		if d.Code != "" && d.Code != "<nil>" {
			diag["code"] = d.Code
		}
		var raw []struct {
			Title string         `json:"title"`
			Kind  string         `json:"kind"`
			Edit  *WorkspaceEdit `json:"edit"`
		}
		if err := s.call(ctx, "textDocument/codeAction", map[string]any{
			"textDocument": map[string]any{"uri": sr.uri},
			"range":        rng,
			"context":      map[string]any{"diagnostics": []any{diag}, "only": []string{"quickfix"}},
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
