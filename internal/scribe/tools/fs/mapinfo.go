package fs

import (
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// mapSymbols returns the code map's symbols for abs, in line order. known is
// false when there is no map in this session or it has not indexed the file.
// It is the only path by which the file tools read the map, so every caller
// degrades to plain output without it.
func mapSymbols(env *tools.Env, abs string) ([]repomap.Symbol, bool) {
	if abs == "" {
		return nil, false
	}
	m, ok := tools.Service[*repomap.Map](env, "repomap")
	if !ok || m == nil {
		return nil, false
	}
	return m.FileSymbols(abs)
}

// symbolLabel names a symbol the way the outline shows it: the qualified name
// and its kind.
func symbolLabel(s repomap.Symbol) string {
	name := s.Name
	if s.Parent != "" {
		name = s.Parent + "." + s.Name
	}
	return name
}

// symbolEnd is the last line of s, never before its first.
func symbolEnd(s repomap.Symbol) int {
	if s.EndLine < s.Line {
		return s.Line
	}
	return s.EndLine
}

// outlineBelow formats the symbols that start after line after, as
// ":start-end kind qualifiedName" lines. It is empty when there are none.
func outlineBelow(syms []repomap.Symbol, after, total int) string {
	var sb strings.Builder
	n := 0
	for _, s := range syms {
		if s.Line <= after || s.Line > total {
			continue
		}
		n++
		if n > maxOutlineEntries {
			continue
		}
		fmt.Fprintf(&sb, "\n  :%d-%d %s %s", s.Line, symbolEnd(s), s.Kind, symbolLabel(s))
	}
	if n == 0 {
		return ""
	}
	if n > maxOutlineEntries {
		fmt.Fprintf(&sb, "\n  +%d more", n-maxOutlineEntries)
	}
	return "\nSymbols below:" + sb.String()
}

// enclosingAt returns "kind qualifiedName" for the innermost symbol of syms
// whose lines contain line, or "" when none does. syms must be in line order
// (FileSymbols returns them so).
func enclosingAt(syms []repomap.Symbol, line int) string {
	var best *repomap.Symbol
	for i := range syms {
		s := &syms[i]
		if s.Line <= line && line <= symbolEnd(*s) && (best == nil || s.Line >= best.Line) {
			best = s
		}
	}
	if best == nil {
		return ""
	}
	return best.Kind + " " + symbolLabel(*best)
}

// mapRank returns the code map's rank for a file, or nil when there is no map
// in this session. Listings order files by it, most important first.
func mapRank(env *tools.Env) func(abs string) float64 {
	m, ok := tools.Service[*repomap.Map](env, "repomap")
	if !ok || m == nil {
		return nil
	}
	return m.FileRank
}

// fileAnnotator returns the note listings add after a file: its symbol count
// when the code map knows it, or "" when there is nothing to say.
func fileAnnotator(env *tools.Env) func(abs string) string {
	return func(abs string) string {
		syms, known := mapSymbols(env, abs)
		if !known || len(syms) == 0 {
			return ""
		}
		return fmt.Sprintf("  · %d symbols", len(syms))
	}
}
