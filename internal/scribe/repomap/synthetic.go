package repomap

import "strings"

// Summary modes (spec 4.11). The mode decides what a symbol line shows after
// its signature. "ast" is the default: the doc comment's first sentence, which
// costs nothing. "synthetic" also fills a symbol that has no doc comment with
// words made from its name, which also costs nothing. "off" hides doc
// summaries. "llm" and "full" need a Summarizer; the model's summaries are
// shown for files that have one, and "full" adds the synthetic fill.
const (
	SummaryOff       = "off"
	SummaryAST       = "ast"
	SummarySynthetic = "synthetic"
	SummaryLLM       = "llm"
	SummaryFull      = "full"
)

// SetSummaryMode chooses the summary mode for rendering. An unknown mode is
// refused, so a typo cannot silently turn summaries off.
func (m *Map) SetSummaryMode(mode string) bool {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", SummaryAST:
		mode = SummaryAST
	case SummaryOff, SummarySynthetic, SummaryLLM, SummaryFull:
	default:
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.summaryMode = mode
	return true
}

// SummaryMode returns the current summary mode ("ast" when unset).
func (m *Map) SummaryMode() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.summaryMode == "" {
		return SummaryAST
	}
	return m.summaryMode
}

// SyntheticSummary turns a symbol name into a short phrase, for a symbol with
// no doc comment: "BuildServer" becomes "Build server", and "parse_http_request"
// becomes "Parse http request". It makes no claim about behaviour beyond the
// name, which is why it is shown only in the synthetic modes.
func SyntheticSummary(name string) string {
	ws := splitWords(name)
	if len(ws) == 0 {
		return ""
	}
	out := strings.Join(ws, " ")
	if len(out) > maxSummaryChars {
		out = out[:maxSummaryChars]
	}
	return strings.ToUpper(out[:1]) + out[1:]
}

// symbolSummary is the summary shown after a symbol's signature in the map,
// for the current mode. Callers hold m.mu for reading.
func (m *Map) symbolSummary(s Symbol) string {
	mode := m.summaryMode
	if mode == SummaryOff {
		return ""
	}
	if s.Summary != "" {
		return s.Summary
	}
	if (mode == SummarySynthetic || mode == SummaryFull) && s.Exported {
		return SyntheticSummary(s.Name)
	}
	return ""
}

// SetAutoRegen records whether the app should ask the Summarizer for summaries
// after each rebuild (the /repo-map "Auto-regenerate" setting). The map only
// stores the choice; running the pass is the caller's job.
func (m *Map) SetAutoRegen(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.autoRegen = on
}

// AutoRegen reports the auto-regenerate choice.
func (m *Map) AutoRegen() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.autoRegen
}

// ClearSummaries drops every model summary, stored and in memory. Symbol
// summaries from doc comments are not affected: they come from the source.
func (m *Map) ClearSummaries() error {
	m.mu.Lock()
	paths := make([]string, 0, len(m.summaries))
	for p := range m.summaries {
		paths = append(paths, p)
	}
	m.summaries = map[string]summaryEntry{}
	m.mu.Unlock()
	return writeSummaries(m.db, nil, paths)
}
