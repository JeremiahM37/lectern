package chat

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
)

// Flavour lines shown after an error's category, in the order of the spec.
var flavours = map[string]string{
	"Rate Limited":  "The provider asked us to slow down",
	"Overloaded":    "The provider is overloaded",
	"Auth Error":    "The provider rejected the API key",
	"Config Error":  "The request was not valid for this model",
	"Network Error": "Could not reach the provider",
	"Stream Error":  "The response stream broke off",
}

func flavour(category string) string {
	if f, ok := flavours[category]; ok {
		return f
	}
	return "Something went wrong"
}

func italic(c lipgloss.TerminalColor, s string) string {
	return lipgloss.NewStyle().Foreground(c).Italic(true).Render(s)
}

// renderRecord draws one transcript entry at width w.
func (v *View) renderRecord(i int, rec *Record, w int, now time.Time) builder {
	switch rec.Role {
	case roleUser:
		return v.renderUser(rec, w)
	case roleSystem:
		return v.renderSystem(i, rec, w, now)
	}
	return v.renderAssistant(rec, w, now)
}

// collapseBody keeps the first and last four lines of a long user message
// unless the transcript is expanded.
func (v *View) collapseBody(lines []string) []string {
	if v.expanded || len(lines) <= 10 {
		return lines
	}
	n := len(lines) - 8
	out := append([]string{}, lines[:4]...)
	out = append(out, fg(v.th.TextMuted, fmt.Sprintf("// +%d lines //", n)))
	return append(out, lines[len(lines)-4:]...)
}

func (v *View) renderUser(rec *Record, w int) builder {
	if v.opt.ChatStyle == "bubble" {
		return v.renderBubble(rec, w)
	}
	var b builder
	th := v.th
	cw := w - 3
	if cw < 1 {
		cw = 1
	}
	bg := th.BgUser
	rail := fg(th.AccentUser, "▌") + " "
	row := func(s string) string { return bgLine(rail+s, w, bg) }
	b.add(row(""))
	b.add(row(fit(bold(th.AccentUser, "You")+fg(th.TextDim, " · "+clockText(rec.At))+userSuffix(v, rec), cw)))
	for _, l := range v.collapseBody(wrapText(rec.Text, cw)) {
		b.add(row(fit(l, cw)))
	}
	b.add(row(""))
	return b
}

func (v *View) renderBubble(rec *Record, w int) builder {
	var b builder
	th := v.th
	boxW := w * 3 / 4
	if boxW < 24 {
		boxW = 24
	}
	if boxW > w {
		boxW = w
	}
	inner := boxW - 2 - 4
	body := append([]string{""}, v.collapseBody(wrapText(rec.Text, inner))...)
	body = append(body, "")
	pad := strings.Repeat(" ", w-boxW)
	for _, l := range boxed("", body, boxW, 2, th.AccentUser, th.BgUser) {
		b.add(pad + l)
	}
	footer := fg(th.TextMuted, "You · "+clockText(rec.At)) + userSuffix(v, rec)
	b.add(fit(strings.Repeat(" ", maxInt(0, w-ansi.StringWidth(footer)))+footer, w))
	return b
}

func userSuffix(v *View, rec *Record) string {
	s := ""
	if rec.Steering {
		s += fg(v.th.Warning, " · steering")
	}
	if rec.Images > 0 {
		noun := "images"
		if rec.Images == 1 {
			noun = "image"
		}
		s += fg(v.th.Info, fmt.Sprintf(" · %s %d %s", icons.Icon("image"), rec.Images, noun))
	}
	return s
}

func (v *View) renderSystem(i int, rec *Record, w int, now time.Time) builder {
	var b builder
	th := v.th
	railCol := th.TextMuted
	var head, body string
	last := i == len(v.msgs)-1
	switch rec.Kind {
	case "error":
		railCol = th.Error
		head = bold(th.Error, "✗ "+rec.Category) + fg(th.TextMuted, " — "+flavour(rec.Category))
		body = cleanBody(rec.Text)
	case "retry":
		railCol = th.Warning
		h := "↻ Retry"
		if rec.Max > 0 {
			h = fmt.Sprintf("↻ Retry %d/%d", rec.Attempt, rec.Max)
		}
		head = bold(th.Warning, h)
		if rec.Category != "" {
			head += fg(th.TextMuted, " — "+rec.Category)
		}
		if rec.Delay > 0 {
			head += fg(th.TextMuted, fmt.Sprintf(" — waiting ~%ds", int(math.Round(rec.Delay.Seconds()))))
		}
		if rec.Category == "" {
			body = cleanBody(rec.Text)
		}
	case "interrupted":
		head = fg(th.TextSecondary, "⊘ Interrupted")
	case "switch":
		head = fg(th.Info, "⇄ Switched to "+rec.Model)
		body = cleanBody(rec.Text)
	case "warning":
		railCol = th.Warning
		head = bold(th.Warning, "⚠ Warning")
		body = cleanBody(rec.Text)
	case "compact":
		railCol = th.Info
		if rec.Done {
			head = fg(th.Info, fmt.Sprintf("✓ Compacted: %d → %d messages", rec.Before, rec.After))
		} else {
			head = fg(th.Info, render.Frame(render.Compact, frameAt(now))+" Compacting context")
		}
	default:
		head = fg(th.TextMuted, "› System")
		body = cleanBody(rec.Text)
	}
	rail := fg(railCol, "▌") + " "
	b.add(rail + head)
	if body == "" {
		return b
	}
	// Only the newest system row types itself out: eight chunks, 45 ms apart.
	cursor := ""
	if last {
		chunks := int(now.Sub(rec.At) / (45 * time.Millisecond))
		if chunks < 8 {
			if chunks < 0 {
				chunks = 0
			}
			rs := []rune(body)
			body = string(rs[:len(rs)*chunks/8])
			cursor = fg(railCol, "█")
		}
	}
	lines := wrapText(body, w-2)
	if cursor != "" && len(lines) > 0 {
		lines[len(lines)-1] += cursor
	}
	for _, l := range lines {
		b.add(rail + fg(th.TextSecondary, l))
	}
	return b
}

func (v *View) renderAssistant(rec *Record, w int, now time.Time) builder {
	var b builder
	th := v.th
	cw := w - 2
	if cw < 1 {
		cw = 1
	}
	rail := fg(th.Brand, "▌") + " "
	b.add(rail + bold(th.AccentAssistant, "✒ Scribe") + fg(th.TextDim, " · "+clockText(rec.At)))
	body := v.renderBody(rec, cw, now)
	if len(body.lines) == 0 {
		switch {
		case !rec.Done && rec == v.live:
			msg := "Thinking…"
			if rec.Reasoned && !v.opt.ShowReasoning {
				msg = "Thinking… (reasoning hidden)"
			}
			body.add(italic(th.TextMuted, msg))
		case rec.Done && !rec.Interrupted:
			body.add(italic(th.TextMuted, "Empty response — model returned no content."))
		}
	}
	off := len(b.lines)
	for _, l := range body.lines {
		b.lines = append(b.lines, rail+fit(l, cw))
	}
	for _, h := range body.hits {
		h.line += off
		b.hits = append(b.hits, h)
	}
	if rec.Done && rec.Duration > 0 && !rec.Interrupted {
		b.add(rail)
		b.add(rail + fg(th.TextFaint, "✓ Completed in "+render.HumanDuration(rec.Duration)))
	}
	return b
}

// renderBody lays out an assistant turn's segments. In auto layout the
// tool calls form one rail placed after the opening text and before the
// final answer; verbose keeps segments in arrival order.
func (v *View) renderBody(rec *Record, cw int, now time.Time) builder {
	var out builder
	firstTools, lastTools := -1, -1
	for i, s := range rec.Segments {
		if s.Kind == "tools" {
			if firstTools < 0 {
				firstTools = i
			}
			lastTools = i
		}
	}
	if v.opt.Verbose || firstTools < 0 {
		prev := ""
		for _, s := range rec.Segments {
			out.block(v.renderSeg(s, cw, now, prev == "tools"))
			prev = s.Kind
		}
		return out
	}
	var pre, post builder
	var calls []*Call
	for i, s := range rec.Segments {
		if s.Kind == "tools" {
			calls = append(calls, s.Calls...)
			continue
		}
		blk := v.renderSeg(s, cw, now, i > 0 && rec.Segments[i-1].Kind == "tools")
		if i < lastTools {
			pre.block(blk)
		} else {
			post.block(blk)
		}
	}
	thinking := rec == v.live && rec.Segments[len(rec.Segments)-1].Kind == "tools" && allDone(calls)
	out.block(pre)
	out.block(v.renderRail(rec, calls, now, cw, thinking))
	out.block(post)
	return out
}

func (v *View) renderSeg(s *Segment, cw int, now time.Time, afterTools bool) builder {
	var b builder
	switch s.Kind {
	case "text":
		txt := visibleText(s)
		if trimmed(txt) == "" {
			return b
		}
		src := txt
		if s.Open {
			src += "▊"
		}
		if v.opt.Verbose && afterTools && len([]rune(trimmed(txt))) > 20 {
			b.add(fg(v.th.TextFaint, strings.Repeat("─", minInt(60, cw))))
		}
		b.add(splitLines(render.Markdown(src, cw, v.th, s.Open))...)
	case "reasoning":
		return v.renderReasoning(s, cw, now)
	case "plan":
		return v.renderPlan(s, cw, now)
	case "tasks":
		return v.renderTasks(s, cw, now)
	case "tools":
		b.block(v.assembleRows(v.groupRows(s.Calls, now, cw)))
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
