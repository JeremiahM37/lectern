package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// The secondary (lint) server for a language runs beside its primary. The
// primary answers navigation, rename, hover and formatting; the secondary adds
// diagnostics and code actions, which the spec names for TypeScript (Biome for
// lint actions). Its failures are counted against the secondary alone, so a
// broken linter never degrades navigation.

// errNoSecondary means the language has no secondary server here: none is
// catalogued, it is turned off, or no candidate binary is installed. It is
// not a failure; a secondary is optional.
var errNoSecondary = errors.New("no secondary language server for this file")

// lintSpecFor returns the secondary spec for abs and the project root it runs
// at. ok is false when the language has no secondary, the language is turned
// off, or no candidate is installed. A secondary that is already running stays
// usable even if its binary later disappears from PATH.
func (m *Manager) lintSpecFor(abs string) (*serverSpec, string, bool) {
	info, ok := languageForPath(abs)
	if !ok {
		return nil, "", false
	}
	spec := lintSpecs[info.key]
	if spec == nil || m.isDisabled(info.key) {
		return nil, "", false
	}
	root := m.rootFor(serverSpecs[info.key], abs)
	m.mu.Lock()
	prev := m.sessions[spec.key(root)]
	m.mu.Unlock()
	if prev == nil || !prev.alive() {
		if _, _, found := findServer(spec, root); !found {
			return nil, "", false
		}
	}
	return spec, root, true
}

// runLint is run for the secondary server. It keeps its own health record, so
// the same bookkeeping as the primary applies without the primary sharing it.
func (m *Manager) runLint(ctx context.Context, abs string, spec *serverSpec, root string,
	fn func(s *session, sr syncResult) error) error {
	key := spec.key(root)
	if err := m.degradedSkip(key); err != nil {
		return err
	}
	err := m.lintOnce(ctx, abs, spec, root, fn)
	if err == nil {
		m.noteOpSuccess(key)
		return nil
	}
	var se *serverError
	if ctx.Err() != nil || !errors.As(err, &se) {
		return err
	}
	m.noteOpFailure(key, spec.Language, err)
	return err
}

// lintOnce mirrors runOnce for the secondary: it restarts a secondary that
// died during the call and runs fn once more.
func (m *Manager) lintOnce(ctx context.Context, abs string, spec *serverSpec, root string,
	fn func(s *session, sr syncResult) error) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var s *session
		s, err = m.sessionForSpec(ctx, spec, root)
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

// serverLabel names the server that published a diagnostic, for the source
// label of diagnostics that carry none.
func serverLabel(s *session) string {
	if s.bin != "" {
		return filepath.Base(s.bin)
	}
	return s.spec.Language + " lint"
}

// lintDiagnostics returns the secondary server's diagnostics for abs. A
// diagnostic with no source of its own is labelled with the server's name, so
// merged output still says where each one came from. It returns errNoSecondary
// when there is no secondary to ask.
func (m *Manager) lintDiagnostics(ctx context.Context, abs string) ([]FileDiagnostic, error) {
	abs = m.abs(abs)
	spec, root, ok := m.lintSpecFor(abs)
	if !ok {
		return nil, errNoSecondary
	}
	var out []FileDiagnostic
	err := m.runLint(ctx, abs, spec, root, func(s *session, sr syncResult) error {
		if sr.changed || sr.seq == 0 {
			s.client.waitSettled(ctx, sr.uri, sr.seq, diagQuiet, diagMax)
		}
		label := serverLabel(s)
		for _, d := range s.client.Diagnostics(sr.uri) {
			start := positionToPoint(sr.text, d.Range.Start)
			end := positionToPoint(sr.text, d.Range.End)
			source := d.Source
			if source == "" {
				source = label
			}
			out = append(out, FileDiagnostic{
				Path: abs, Line: start.Line, Column: start.Column,
				EndLine: end.Line, EndCol: end.Column,
				Severity: severityName(d.Severity), Source: source,
				Code: fmt.Sprint(d.Code), Message: strings.TrimSpace(d.Message),
			})
		}
		return nil
	})
	return out, err
}

// withSecondaryDiagnostics merges the secondary's diagnostics into the
// primary's result. The primary result is authoritative for the error: if the
// primary failed and the secondary answered, the secondary's diagnostics are
// returned on their own, since they are still true. A failing secondary is
// left out and its health record says so; it never turns good primary
// diagnostics into an error.
func (m *Manager) withSecondaryDiagnostics(ctx context.Context, abs string,
	primary []FileDiagnostic, primaryErr error) ([]FileDiagnostic, error) {
	lint, lintErr := m.lintDiagnostics(ctx, abs)
	usable := lintErr == nil
	if primaryErr != nil {
		if usable {
			return lint, nil
		}
		return primary, primaryErr
	}
	if !usable {
		return primary, nil
	}
	return append(primary, lint...), nil
}

// secondaryActions lists the code actions the secondary offers for lines
// startLine to endLine of abs, in the same form as Actions. Quick fixes are
// only offered for diagnostics the server itself published, so the context
// sent is the secondary's own diagnostics that overlap the range.
func (m *Manager) secondaryActions(ctx context.Context, abs string, startLine, endLine int, only []string) ([]CodeAction, error) {
	abs = m.abs(abs)
	spec, root, ok := m.lintSpecFor(abs)
	if !ok {
		return nil, errNoSecondary
	}
	var out []CodeAction
	err := m.runLint(ctx, abs, spec, root, func(s *session, sr syncResult) error {
		rng, err := lineRange(sr.text, startLine, endLine)
		if err != nil {
			return fmt.Errorf("%w in %s", err, filepath.Base(abs))
		}
		ctxDiags := []Diagnostic{}
		for _, d := range s.client.Diagnostics(sr.uri) {
			if d.Range.Start.Line <= rng.End.Line && d.Range.End.Line >= rng.Start.Line {
				ctxDiags = append(ctxDiags, d)
			}
		}
		context := map[string]any{"diagnostics": ctxDiags}
		if len(only) > 0 {
			context["only"] = only
		}
		var raws []json.RawMessage
		if err := s.call(ctx, "textDocument/codeAction", map[string]any{
			"textDocument": map[string]any{"uri": sr.uri},
			"range":        rng,
			"context":      context,
		}, &raws); err != nil {
			return err
		}
		for _, raw := range raws {
			var a rawAction
			if err := json.Unmarshal(raw, &a); err != nil {
				continue
			}
			a.raw = raw
			if !kindWanted(a.Kind, only) {
				continue
			}
			if a.Edit == nil && len(a.Data) > 0 && a.Disabled == nil && !hasCommand(a.Command) {
				var resolved rawAction
				if err := s.call(ctx, "codeAction/resolve", json.RawMessage(raw), &resolved); err == nil && resolved.Edit != nil {
					a.Edit = resolved.Edit
				}
			}
			ca := CodeAction{Title: a.Title, Kind: a.Kind, Edit: a.Edit, Command: hasCommand(a.Command)}
			if a.Disabled != nil {
				ca.Disabled = a.Disabled.Reason
				if ca.Disabled == "" {
					ca.Disabled = "the server did not say why"
				}
			}
			out = append(out, ca)
		}
		return nil
	})
	return out, err
}

// withSecondaryActions merges the secondary's code actions with the primary's
// under the same rules as withSecondaryDiagnostics: a failing or absent
// secondary never hides the primary's own fixes, and a primary failure is
// reported only when the secondary has nothing to offer either.
func (m *Manager) withSecondaryActions(ctx context.Context, abs string, startLine, endLine int, only []string,
	primary []CodeAction, primaryErr error) ([]CodeAction, error) {
	lint, lintErr := m.secondaryActions(ctx, abs, startLine, endLine, only)
	if lintErr != nil {
		return primary, primaryErr
	}
	if primaryErr != nil {
		return lint, nil
	}
	return append(primary, lint...), nil
}

// lineRange is the LSP range for whole lines startLine to endLine (1-based,
// inclusive), as actionsWith computes it.
func lineRange(text string, startLine, endLine int) (Range, error) {
	if _, _, ok := lineBounds(text, startLine-1); !ok {
		return Range{}, fmt.Errorf("line %d is past the end of the file", startLine)
	}
	lastStart, lastEnd, ok := lineBounds(text, endLine-1)
	if !ok {
		return Range{}, fmt.Errorf("line %d is past the end of the file", endLine)
	}
	return Range{
		Start: Position{Line: startLine - 1, Character: 0},
		End:   pointToPosition(text, Point{Line: endLine, Column: utf8.RuneCountInString(text[lastStart:lastEnd]) + 1}),
	}, nil
}
