package chat

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
)

// reasoningTail is how much of a streaming reasoning block is shown.
const reasoningTail = 4096

// renderReasoning draws a reasoning block: one line when collapsed, a
// bordered box when expanded.
func (v *View) renderReasoning(s *Segment, cw int, now time.Time) builder {
	var b builder
	th := v.th
	if trimmed(s.Text) == "" && !s.Open {
		return b
	}
	lines := strings.Count(s.Text, "\n") + 1
	count := ""
	if lines > 1 {
		count = fmt.Sprintf(" (%d lines)", lines)
	} else {
		count = " (1 line)"
	}
	brain := icons.Icon("brain")
	if !v.expandedFor(&Call{Toggled: s.Toggled}) {
		var head string
		if s.Open {
			head = fg(th.BrandDim, render.Frame(render.Braille, frameAt(now))) + " " +
				fg(th.TextFaint, "▶ "+brain+" reasoning"+count) + " " + fg(th.TextSubtle, "^O")
		} else {
			head = fg(th.Success, "▶") + " " + fgi(th.TextDim, brain+" "+reasoningPreview(s.Text)) +
				fg(th.TextFaint, count) + " " + fg(th.TextSubtle, "^O")
		}
		b.mark(nil, s)
		b.add(head)
		return b
	}
	tail := s.Text
	note := ""
	if s.Open {
		rs := []rune(s.Text)
		if len(rs) > reasoningTail {
			tail = string(rs[len(rs)-reasoningTail:])
			note = fmt.Sprintf(" · tail of %d lines", strings.Count(tail, "\n")+1)
		}
	}
	title, border := fg(th.TextMuted, "▼ "+brain+" reasoning"), th.Border
	if s.Open {
		title, border = fg(th.BrandDim, "▼ "+brain+" reasoning…"), th.BrandDim
	}
	title += fg(th.TextFaint, note)
	body := splitLines(render.Markdown(tail, cw-4, th, s.Open))
	b.mark(nil, s)
	b.add(boxed(title, body, cw, 1, border, nil)...)
	return b
}

// answerText is the turn's final answer: the text after the last tool call,
// or all of the text when the turn used no tools. Whitespace is normalised.
func answerText(rec *Record) string {
	last := -1
	for i, s := range rec.Segments {
		if s.Kind == "tools" {
			last = i
		}
	}
	var parts []string
	for i, s := range rec.Segments {
		if s.Kind == "text" && i > last {
			parts = append(parts, s.Text)
		}
	}
	return normProse(strings.Join(parts, " "))
}

// normProse collapses whitespace and drops the markdown bold marks, so text
// can be compared with the answer regardless of layout.
func normProse(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "**", "")), " ")
}

// isDupReasoning reports whether a reasoning segment only repeats the final
// answer (a prefix of it, or all of it). Such a segment is never shown.
func isDupReasoning(s *Segment, answer string) bool {
	r := normProse(s.Text)
	return r == "" || (answer != "" && strings.HasPrefix(answer, r))
}

// allCalls lists the calls of every tools segment in order.
func allCalls(rec *Record) []*Call {
	var calls []*Call
	for _, s := range rec.Segments {
		if s.Kind == "tools" {
			calls = append(calls, s.Calls...)
		}
	}
	return calls
}

// lastLine is the last non-blank line of s, without markdown emphasis marks.
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(strings.NewReplacer("**", "", "`", "").Replace(lines[i])); l != "" {
			return l
		}
	}
	return ""
}

// renderHead is what sits above the tool rail in auto layout. While the turn
// runs: the latest reasoning as one live row, and the last intermediate line
// of text. Once it is done: one thought summary for the whole turn.
func (v *View) renderHead(rec *Record, lastTools int, answer string, cw int, now time.Time) builder {
	var b builder
	th := v.th
	var reasons []*Segment
	for _, s := range rec.Segments {
		if s.Kind == "reasoning" && !isDupReasoning(s, answer) {
			reasons = append(reasons, s)
		}
	}
	if !rec.Done {
		if n := len(reasons); n > 0 {
			latest := reasons[n-1]
			// "⠸ ∴ Thinking · tail": the spinner stands in for the brain glyph
			// while the reasoning streams, and the glyph is shown once it stops.
			head := fg(th.TextMuted, icons.Icon("brain")+" Thinking")
			if latest.Open {
				head = fg(th.BrandDim, render.Frame(render.Braille, frameAt(now))) + " " + head
			}
			if tail := lastLine(latest.Text); tail != "" {
				room := cw - ansi.StringWidth(head) - 3
				head += fg(th.TextDim, " · ") + fgi(th.TextDim, clip(tail, room))
			}
			b.add(head)
		}
		for i := lastTools - 1; i >= 0; i-- {
			if s := rec.Segments[i]; s.Kind == "text" && trimmed(s.Text) != "" {
				b.add(fg(th.TextDim, clip(lastLine(s.Text), cw)))
				break
			}
		}
	} else if len(reasons) > 0 {
		b.block(v.renderThought(rec, reasons, cw))
	}
	return b
}

// renderThought is the one reasoning row for a finished turn. Collapsed it
// is a single line; expanded it is a box with a divider per step.
func (v *View) renderThought(rec *Record, reasons []*Segment, cw int) builder {
	var b builder
	th := v.th
	// Every reasoning segment counts toward the time, including one whose
	// text is hidden as a repeat of the answer: the model did think for it.
	var spent time.Duration
	for _, s := range rec.Segments {
		if s.Kind == "reasoning" {
			spent += s.Spent
		}
	}
	title := icons.Icon("brain") + " Thought"
	if spent >= time.Second {
		title += " for " + render.HumanDuration(spent.Truncate(time.Second))
	}
	steps := maxInt(rec.Steps, 1)
	title += " · " + countWord(steps, "step", "steps")
	if !v.expanded && !rec.ReasonOpen {
		b.markSummary()
		b.add(fg(th.Success, "▶") + " " + fg(th.TextSecondary, title) + " " + fg(th.TextSubtle, "^O"))
		return b
	}
	inner := maxInt(cw-4, 1)
	var body []string
	for i, s := range reasons {
		if i > 0 {
			body = append(body, "")
		}
		div := fmt.Sprintf("─ step %d ", s.Step)
		body = append(body, fg(th.TextFaint, div+strings.Repeat("─", maxInt(inner-ansi.StringWidth(div), 0))))
		body = append(body, splitLines(render.Markdown(s.Text, inner, th, false))...)
	}
	b.markSummary()
	b.add(boxed(fg(th.TextMuted, "▼ "+title), body, cw, 1, th.Border, nil)...)
	return b
}

// reasoningPreview is the first line of a finished reasoning block.
func reasoningPreview(text string) string {
	p := strings.TrimSpace(strings.ReplaceAll(firstLine(text), "**", ""))
	if p == "" {
		return "Reasoned"
	}
	return clipWords(p, 60)
}

// phrases are the rotating phase phrases shown while a phase is live.
var phrases = map[string][]string{
	"explore":  {"Reading the code", "Following references", "Searching", "Mapping dependencies"},
	"edit":     {"Writing changes", "Editing", "Revising"},
	"dispatch": {"Sending out helpers", "Working in parallel"},
}

var (
	phaseTone = map[string]string{"explore": "brand", "edit": "warning", "dispatch": "info"}
	phaseSpin = map[string]render.Spinner{"explore": render.Explore, "edit": render.Edit, "dispatch": render.Dispatch}
)

const (
	phraseEvery = 8 * time.Second
	garbleStep  = 70 * time.Millisecond
)

// railHeader is the line above a tool rail. While the turn is live it names
// the phase of the work in progress; once done it summarises the turn.
func (v *View) railHeader(calls []*Call, rec *Record, now time.Time) string {
	if !rec.Done {
		return v.statusHeader(phaseOf(calls), rec.At, now, rec.Steps)
	}
	return v.railSummary(calls, rec.Duration)
}

// railSummary is the finished header: "✓ 5 tool calls · 1 file edited · 15s".
// Zero parts are left out.
func (v *View) railSummary(calls []*Call, dur time.Duration) string {
	th := v.th
	shown := v.visibleCalls(calls)
	files := map[string]bool{}
	failed := 0
	for _, c := range shown {
		switch {
		case c.Status == "error":
			failed++
		case isEditTool(c.Tool) && !c.running() && c.Status != "denied":
			files[pathOf(c)] = true
		}
	}
	var parts []string
	if n := len(shown); n > 0 {
		parts = append(parts, countWord(n, "tool call", "tool calls"))
	}
	if n := len(files); n > 0 {
		parts = append(parts, countWord(n, "file edited", "files edited"))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if dur >= time.Second {
		parts = append(parts, render.HumanDuration(dur.Truncate(time.Second)))
	}
	glyph := fg(th.Success, icons.Icon("success"))
	switch {
	case failed > 0 && failed == len(shown):
		glyph = fg(th.Error, icons.Icon("fail"))
	case failed > 0:
		glyph = fg(th.Warning, icons.Icon("warning"))
	}
	if len(parts) == 0 {
		return glyph + " " + fg(th.TextMuted, "Done")
	}
	return glyph + " " + fg(th.TextSecondary, strings.Join(parts, " · "))
}

func countWord(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// phraseFor is the rotating phase phrase after el has passed. The phrase
// garbles for a moment when it changes.
func phraseFor(phase string, el time.Duration) string {
	pairs := phrases[phase]
	if len(pairs) == 0 {
		pairs = phrases["explore"]
	}
	if el < 0 {
		el = 0
	}
	k := int(el / phraseEvery)
	label := pairs[k%len(pairs)]
	off := el % phraseEvery
	switch {
	case k > 0 && off < 3*garbleStep:
		label = garble(pairs[(k-1)%len(pairs)], int(off/garbleStep))
	case k > 0 && off < 6*garbleStep:
		label = garble(label, int(off/garbleStep))
	}
	return label
}

// statusHeader is the live phase line above a rail: a spinner, the rotating
// phrase, animated dots and elapsed seconds.
func (v *View) statusHeader(phase string, start, now time.Time, step int) string {
	tone := v.tone(phaseTone[phase])
	el := now.Sub(start)
	if el < 0 {
		el = 0
	}
	spin := render.Frame(phaseSpin[phase], frameAt(now))
	dots := render.Frame(render.LoadingDots, frameAt(now)/4)
	out := fg(tone, spin) + " " + bold(tone, phraseFor(phase, el)) + fg(v.th.TextMuted, dots)
	if step > 0 {
		out += fg(v.th.TextDim, fmt.Sprintf(" · step %d", step))
	}
	return out + fg(v.th.TextDim, " · "+secs(el))
}

// LivePhrase names what the live turn is doing right now, for the busy line:
// the phase phrase of the running tool, or "Thinking" when no tool runs.
func (v *View) LivePhrase(now time.Time) string {
	if v.live == nil {
		return "Thinking"
	}
	var calls []*Call
	for _, s := range v.live.Segments {
		if s.Kind == "tools" {
			calls = append(calls, s.Calls...)
		}
	}
	for _, c := range calls {
		if c.running() {
			start := c.Start
			if start.IsZero() {
				start = now
			}
			return phraseFor(phaseOf(calls), now.Sub(start))
		}
	}
	return "Thinking"
}

// renderPlan draws the live plan: a rail, a title with done/total, and the
// steps (at most seven around the active one once the list is long).
func (v *View) renderPlan(s *Segment, cw int, now time.Time) builder {
	var b builder
	th := v.th
	p := s.Plan
	if p == nil {
		return b
	}
	total, done, active := len(p.Steps), 0, -1
	for i, st := range p.Steps {
		switch st.Status {
		case "done":
			done++
		case "active":
			if active < 0 {
				active = i
			}
		}
	}
	railCol := th.Info
	if total > 0 && done == total {
		railCol = th.Success
	}
	rail := fg(railCol, "┃") + " "
	b.add(rail + fg(th.BrandAlt, icons.Icon("plan")) + " " + bold(th.BrandAlt, clip(p.Title, cw-12)) + "  " + fg(th.TextMuted, fmt.Sprintf("%d/%d", done, total)))
	start, end := 0, total
	if total > 7 && done < total {
		c := active
		if c < 0 {
			c = done
		}
		start = c - 3
		if start > total-7 {
			start = total - 7
		}
		if start < 0 {
			start = 0
		}
		end = start + 7
	}
	if start > 0 {
		b.add(rail + fg(th.TextDim, fmt.Sprintf("%d completed above", start)))
	}
	for i := start; i < end; i++ {
		st := p.Steps[i]
		var glyph, text string
		switch st.Status {
		case "done":
			glyph, text = fg(th.Success, icons.Icon("success")), fg(th.TextMuted, st.Text)
		case "skipped":
			glyph, text = fg(th.TextDim, icons.Icon("skip")), fg(th.TextDim, st.Text)
		case "active":
			glyph, text = fg(th.Brand, render.Frame(render.Braille, frameAt(now))), bold(th.TextPrimary, st.Text)
		default:
			glyph, text = fg(th.TextMuted, "○"), fg(th.TextMuted, st.Text)
		}
		b.add(rail + glyph + " " + text)
	}
	if end < total {
		b.add(rail + fg(th.TextDim, fmt.Sprintf("%d more pending", total-end)))
	}
	return b
}

// renderTasks draws the standalone task list: done count, then open tasks.
func (v *View) renderTasks(s *Segment, cw int, now time.Time) builder {
	var b builder
	th := v.th
	if len(s.Tasks) == 0 {
		return b
	}
	done := 0
	var open []agent.Task
	for _, t := range s.Tasks {
		if t.Status == "done" {
			done++
		} else {
			open = append(open, t)
		}
	}
	b.add(bold(th.Brand, icons.Icon("plan")+" Tasks") + "  " + fg(th.TextSecondary, fmt.Sprintf("%d/%d", done, len(s.Tasks))))
	if done > 0 {
		b.add("  " + fg(th.Success, fmt.Sprintf("+%d done", done)))
	}
	for i, t := range open {
		if i == 6 {
			b.add("  " + fg(th.TextMuted, fmt.Sprintf("+%d more", len(open)-6)))
			break
		}
		var glyph, text string
		switch t.Status {
		case "in_progress":
			glyph, text = fg(th.Brand, render.Frame(render.Braille, frameAt(now))), bold(th.TextPrimary, clip(t.Text, cw-4))
		case "blocked":
			glyph, text = fg(th.Error, icons.Icon("fail")), fg(th.Error, clip(t.Text, cw-4))
		case "cancelled":
			glyph, text = fg(th.TextDim, icons.Icon("skip")), fg(th.TextDim, clip(t.Text, cw-4))
		default:
			glyph, text = fg(th.TextMuted, "○"), fg(th.TextMuted, clip(t.Text, cw-4))
		}
		b.add("  " + glyph + " " + text)
	}
	return b
}
