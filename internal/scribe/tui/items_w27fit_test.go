package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// w27Widths are the terminal widths every fit test runs at.
var w27Widths = []int{80, 120}

// w27Body is the width a popup's View gets on a terminal cols wide: its frame
// width less the border and padding.
func w27Body(md Modal, cols, rows int) int {
	w, _ := md.(Sized).Size(cols, rows)
	return w - 4
}

// Item 1: a long health reason wraps onto indented lines inside the popup. No
// word is lost, nothing is cut with an ellipsis, and no line is wider than the
// popup.
func TestW27HealthReasonWrapsAtBothWidths(t *testing.T) {
	reason := "ollama/qwen3.6:35b-a3b (parent model) answered in 1.4s; the tool-call format was parsed and the stream closed cleanly after 32 tokens"
	fix := "set the parent model to one the provider lists, then press r to run the check again"
	for _, cols := range w27Widths {
		p := &healthPopup{th: theme.Default(), backends: []HealthBackend{{Name: "Model providers", Probes: []HealthProbe{
			{Name: "ollama", State: "warn", Reason: reason, Fix: fix, Ms: 1400},
		}}}}
		w := w27Body(p, cols, 30)
		out := p.View(w, 40)
		if strings.Contains(out, "…") {
			t.Fatalf("%d cols: a health reason was cut:\n%s", cols, ansi.Strip(out))
		}
		for _, l := range strings.Split(out, "\n") {
			if ansi.StringWidth(l) > w {
				t.Fatalf("%d cols: line wider than the popup (%d > %d): %q", cols, ansi.StringWidth(l), w, ansi.Strip(l))
			}
		}
		stripped := ansi.Strip(out)
		words := append(strings.Fields(reason), strings.Fields(fix)...)
		for _, word := range words {
			if !strings.Contains(stripped, word) {
				t.Fatalf("%d cols: the popup lost %q:\n%s", cols, word, stripped)
			}
		}
	}
}

// A short reason stays on its probe row. Only a long one moves down.
func TestW27HealthShortReasonStaysOnItsRow(t *testing.T) {
	p := &healthPopup{th: theme.Default(), backends: []HealthBackend{{Name: "Git", Probes: []HealthProbe{
		{Name: "repository", State: "pass", Reason: "clean", Ms: -1},
	}}}}
	for _, cols := range w27Widths {
		out := ansi.Strip(p.View(w27Body(p, cols, 30), 40))
		row := ""
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "repository") {
				row = l
			}
		}
		if !strings.Contains(row, "clean") {
			t.Fatalf("%d cols: a short reason moved off its row: %q\n%s", cols, row, out)
		}
	}
}

// wrapPrefixed keeps the marker on the first line and the indent on the rest,
// and breaks a word that is wider than the line rather than dropping it.
func TestW27WrapPrefixedKeepsIndentAndBreaksLongWords(t *testing.T) {
	got := wrapPrefixed("one two three four five", "> ", "  ", 10)
	want := []string{"> one two", "  three", "  four", "  five"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("wrapPrefixed = %q, want %q", got, want)
	}
	long := wrapPrefixed("abcdefghijklmnop", "", "  ", 8)
	for _, l := range long {
		if ansi.StringWidth(l) > 8 {
			t.Fatalf("a long word overflowed the line: %q", l)
		}
	}
	if got := strings.ReplaceAll(strings.Join(long, ""), " ", ""); got != "abcdefghijklmnop" {
		t.Fatalf("a long word lost text: %q", long)
	}
}

// Item 2: /perf shows every value. Each line fits the panel, and the rendered
// panel shows all of them at 80 and at 120 columns.
func TestW27PerfPanelShowsEveryValueAtBothWidths(t *testing.T) {
	off := false
	p := config.Performance{Thinking: "adaptive", Effort: "high", Speed: "fast", CacheTTL: "1h", SendReasoning: &off}
	lines := perfLines(p)
	for _, cols := range w27Widths {
		v := NewInfoView(theme.Default(), "Performance", "gauge", lines)
		w := w27Body(v, cols, 40)
		for _, l := range lines {
			if ansi.StringWidth(l) > w {
				t.Fatalf("%d cols: /perf line wider than the panel (%d > %d): %q", cols, ansi.StringWidth(l), w, l)
			}
		}
		out := ansi.Strip(v.View(w, len(lines)+4))
		for _, want := range []string{"thinking", "adaptive", "effort", "high", "speed", "fast", "cache lifetime", "1h", "tool streaming", "send reasoning", "one tool at a time", "/seqtools"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%d cols: /perf panel lacks %q:\n%s", cols, want, out)
			}
		}
	}
}

// Item 3: the placeholder fits the input it is drawn in. At 120 columns every
// tip shows whole; at 80 a cut lands on a word boundary and ends with an
// ellipsis, and never runs past the input.
func TestW27PlaceholderFitsTheInputWidth(t *testing.T) {
	m, _ := newTestModel(t, 120, 40)
	tb := m.cur()

	// 120 columns in chat: the input is the full width.
	tb.in.resize(120 - 4)
	for _, tip := range idleTips {
		if got := fitPlaceholder(tb, tip); got != tip {
			t.Fatalf("120 cols: tip cut to %q", got)
		}
	}
	// 120 columns on the landing page: the box is 60% of the width.
	tb.in.resize(120*60/100 - 4)
	for _, tip := range idleTips {
		if got := fitPlaceholder(tb, tip); got != tip {
			t.Fatalf("120 cols landing: tip cut to %q (room %d)", got, tb.in.ta.Width()-1)
		}
	}

	// 80 columns: chat, then the landing box.
	for _, w := range []int{80 - 4, 80*60/100 - 4} {
		tb.in.resize(w)
		room := tb.in.ta.Width() - 1
		for _, tip := range idleTips {
			fit := fitPlaceholder(tb, tip)
			if ansi.StringWidth(fit) > room {
				t.Fatalf("width %d: placeholder %q is %d cells, room is %d", w, fit, ansi.StringWidth(fit), room)
			}
			if fit == tip {
				continue
			}
			stem := strings.TrimSuffix(fit, "…")
			if !strings.HasSuffix(fit, "…") || !strings.HasPrefix(tip, stem) || len(tip) <= len(stem) ||
				!strings.ContainsRune(" -_/.+,;:()", rune(tip[len(stem)])) {
				t.Fatalf("width %d: cut %q is not on a word boundary of %q", w, fit, tip)
			}
		}
	}
}

// The busy placeholder is fitted the same way, and shows whole at 80 columns
// in chat.
func TestW27BusyPlaceholderShowsWholeAtBothWidths(t *testing.T) {
	const busy = "'/' for commands · or steer by sending a new message"
	m, _ := newTestModel(t, 120, 40)
	tb := m.cur()
	tb.busy = true
	for _, cols := range w27Widths {
		tb.in.resize(cols - 4)
		if got, _ := inputPlaceholderAt(tb, m.th, 0); got != busy {
			t.Fatalf("%d cols: busy placeholder = %q", cols, got)
		}
	}
}

// Item 4: the Reasoning row on the Overview tab shows the effective setting,
// or "default" when none is set, at both widths.
func TestW27StatusReasoningShowsItsValue(t *testing.T) {
	for _, cols := range w27Widths {
		for _, tc := range []struct{ set, want string }{{"", "default"}, {"high", "high"}} {
			m, fb := newTestModel(t, cols, 40)
			fb.reasoning = tc.set
			v := newStatusView(m.th, m.cur().be)
			found := false
			for _, l := range modalFrame(v, m.th, cols, 40) {
				line := ansi.Strip(l)
				if !strings.Contains(line, "Reasoning") {
					continue
				}
				found = true
				if !strings.Contains(line, tc.want) {
					t.Fatalf("%d cols, reasoning %q: row is %q, want %q", cols, tc.set, strings.TrimSpace(line), tc.want)
				}
			}
			if !found {
				t.Fatalf("%d cols: no Reasoning row on the Overview tab", cols)
			}
		}
	}
}

// Item 5: every /context line fits the panel. The worst cases are covered: a
// long model id, two dispatches with a large cost, and a per-chat budget with
// no limit. The rendered panel shows each line whole at 80 and 120 columns.
func TestW27ContextLinesFitThePanel(t *testing.T) {
	m, fb := newTestModel(t, 80, 40)
	fb.mu.Lock()
	fb.model = "openrouter/some-long-provider-name/a-model-with-a-very-long-identifier-2026-10-17"
	fb.transcript = append(historyFixture(),
		agent.Event{Kind: agent.EvToolResult, Tool: "dispatch", Result: &tools.Result{Meta: map[string]any{
			"subagent_usage":    llm.Usage{InputTokens: 85000, OutputTokens: 9000},
			"subagent_cost_usd": 12.3456,
		}}})
	fb.mu.Unlock()
	m.cur().be = &chatBudgetFake{Backend: fb, cb: ChatBudget{Today: 105000}}

	lines := contextLines(m.cur())
	for _, l := range lines {
		if ansi.StringWidth(l) > contextPanelWidth {
			t.Fatalf("/context line wider than the panel (%d > %d): %q", ansi.StringWidth(l), contextPanelWidth, l)
		}
	}
	for _, cols := range w27Widths {
		v := NewInfoView(m.th, "Context", "gauge", lines)
		out := ansi.Strip(v.View(contextPanelWidth, len(lines)+4))
		for _, l := range lines {
			if strings.TrimSpace(l) == "" {
				continue
			}
			if !strings.Contains(out, strings.TrimRight(l, " ")) {
				t.Fatalf("%d cols: /context line not shown whole: %q\n%s", cols, l, out)
			}
		}
	}
}

// The per-chat row without a limit keeps its config hint in one piece, so the
// hint can be read and typed as written.
func TestW27ChatBudgetHintStaysWhole(t *testing.T) {
	row := chatBudgetLine(ChatBudget{Today: 50000})
	if !strings.Contains(row, "no per-chat budget; set chat_daily_token_budget") {
		t.Fatalf("the hint was split: %q", row)
	}
	for _, l := range fitContextLine(strings.Split(row, "\n")[0]) {
		if ansi.StringWidth(l) > contextPanelWidth {
			t.Fatalf("row too wide: %q", l)
		}
	}
}
