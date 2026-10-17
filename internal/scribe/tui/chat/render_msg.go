package chat

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
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

// planExecuteLead opens the message that starts a plan's execution.
const planExecuteLead = "Execute this plan."

// planExecution reports whether a user message is the plan-execution request,
// and returns its title (the first heading, else "Plan") and line count.
func planExecution(text string) (title string, lines int, ok bool) {
	if !strings.HasPrefix(text, planExecuteLead) {
		return "", 0, false
	}
	title = "Plan"
	for _, l := range strings.Split(text, "\n") {
		if h := strings.TrimSpace(l); strings.HasPrefix(h, "# ") {
			if t := strings.TrimSpace(strings.TrimPrefix(h, "# ")); t != "" {
				title = t
			}
			break
		}
	}
	return title, strings.Count(text, "\n") + 1, true
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
	if title, n, ok := planExecution(rec.Text); ok && !v.expanded {
		b.add(row(""))
		head := fg(th.Brand, icons.Icon("plan")) + " " + fg(th.TextSecondary, "Execute plan: "+title) +
			fg(th.TextFaint, fmt.Sprintf(" (%d lines)", n)) + " " + fg(th.TextSubtle, "^O")
		b.add(row(fit(head, cw)))
		b.add(row(""))
		return b
	}
	b.add(row(""))
	b.add(row(fit(bold(th.AccentUser, "You")+fg(th.TextDim, " · "+clockText(rec.At))+userSuffix(v, rec), cw)))
	for _, l := range v.collapseBody(wrapText(rec.Text, cw)) {
		b.add(row(fit(l, cw)))
	}
	if len(rec.imgs) > 0 {
		b.add(row(""))
		for _, l := range v.imageLines(rec, cw) {
			b.add(row(fit(l, cw)))
		}
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
	if rec.Origin != "" && rec.Origin != "local" {
		s += fg(v.th.Info, " · "+rec.Origin)
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

// statusTag is the HTTP status of a provider error or retry, as " [429]",
// or nothing when the provider gave none.
func statusTag(col lipgloss.TerminalColor, status int) string {
	if status <= 0 {
		return ""
	}
	return " " + bold(col, fmt.Sprintf("[%d]", status))
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
		head = bold(th.Error, "✗ "+rec.Category) + statusTag(th.Error, rec.Status) + fg(th.TextMuted, " — "+flavour(rec.Category))
		body = cleanBody(rec.Text)
	case "retry":
		railCol = th.Warning
		h := "↻ Retry"
		if rec.Max > 0 {
			h = fmt.Sprintf("↻ Retry %d/%d", rec.Attempt, rec.Max)
		}
		head = bold(th.Warning, h) + statusTag(th.Warning, rec.Status)
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
	case "notice":
		// One line: an information note is not worth a header and a body.
		railCol = th.Info
		head = fg(th.Info, "ⓘ "+strings.TrimSpace(cleanBody(rec.Text)))
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
	// An unfinished row with nothing in it (not the one streaming now) has no
	// header either: a bare "Scribe" heading with no body is noise.
	if !rec.Done && rec != v.live && !rec.hasContent() {
		return b
	}
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
		case rec.Done && !rec.Interrupted && len(allCalls(rec)) == 0:
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
	if rec.Done && !rec.Interrupted {
		if line := finishedFooter(rec, th); line != "" {
			b.add(rail)
			b.add(rail + line)
		}
	}
	return b
}

// finishedFooter is the last line of a finished turn. A turn that ended on
// tool results with no final text reads "Done" rather than an empty answer.
// Retries that resolved along the way are noted quietly.
func finishedFooter(rec *Record, th theme.Theme) string {
	text := ""
	switch {
	case cancelledTurn(rec):
		// The user stopped the turn at a plan: it must not read as success.
		text = "⊘ Cancelled"
	case answerText(rec) == "" && len(allCalls(rec)) > 0:
		text = "✓ Done"
	case rec.Duration > 0:
		text = "✓ Completed in " + render.HumanDuration(rec.Duration)
	}
	if rec.Retries > 0 {
		note := fmt.Sprintf("%d retries", rec.Retries)
		if rec.Retries == 1 {
			note = "1 retry"
		}
		if text == "" {
			text = "✓ Done"
		}
		text += " · " + note
	}
	if text == "" {
		return ""
	}
	return fg(th.TextFaint, text)
}

// cancelledTurn reports whether the user cancelled a plan in this turn. The
// turn still ends, but its footer must not say it finished.
func cancelledTurn(rec *Record) bool {
	for _, s := range rec.Segments {
		if s.Kind == "plan" && s.Plan != nil && s.Plan.Outcome == "cancelled" {
			return true
		}
	}
	return false
}

// renderBody lays out an assistant turn's segments. Verbose keeps them in
// arrival order. Auto layout shows one reasoning row (live while the turn
// runs, a summary once it is done), the tool rail, and the final answer.
func (v *View) renderBody(rec *Record, cw int, now time.Time) builder {
	var out builder
	answer := answerText(rec)
	if v.opt.Verbose {
		prev := ""
		for _, s := range mergeTools(rec.Segments) {
			if s.Kind == "reasoning" && isDupReasoning(s, answer) {
				prev = s.Kind
				continue
			}
			out.block(v.renderLinkedSeg(rec, s, cw, now, prev == "tools"))
			prev = s.Kind
		}
		return out
	}
	lastTools, answerFrom := -1, lastModelTools(rec)
	for i, s := range rec.Segments {
		if s.Kind == "tools" {
			lastTools = i
		}
	}
	var pre, post builder
	for i, s := range rec.Segments {
		switch s.Kind {
		case "tools", "reasoning":
			continue
		case "text":
			// Intermediate text is shown by the head or the rail. Text after
			// the model's last tool call is the answer, even when checks that
			// run after it has answered (auto_check) add rows below it.
			if i < answerFrom {
				continue
			}
			post.block(v.renderSeg(s, cw, now, i > 0 && rec.Segments[i-1].Kind == "tools"))
		default: // plan and tasks keep their place
			blk := v.renderLinkedSeg(rec, s, cw, now, false)
			if i < lastTools {
				pre.block(blk)
			} else {
				post.block(blk)
			}
		}
	}
	out.block(pre)
	out.block(v.renderHead(rec, lastTools, answer, cw, now))
	if lastTools >= 0 {
		calls := allCalls(rec)
		thinking := rec == v.live && rec.Segments[len(rec.Segments)-1].Kind == "tools" && allDone(calls)
		out.block(v.renderRail(rec, now, cw, thinking))
	}
	out.block(post)
	return out
}

// streamOpacity is how far streaming text is blended toward the background
// (spec 01 §2.12). Settled text is drawn at full strength.
const streamOpacity = 0.65

// textThemeFor returns the theme a text segment is drawn with at its steady
// state: streaming text at streamOpacity while open, settled text unchanged.
// textThemeAt adds the fades on top of this.
func textThemeFor(th theme.Theme, open bool) theme.Theme {
	if !open {
		return th
	}
	return blendedText(th, streamOpacity)
}

// textThemeAt returns the theme a text segment is drawn with at now. While it
// fades in or out, its body colours move between the background and full
// strength; once the fade has finished the theme is returned unchanged, so a
// settled segment draws the same as it always did.
func textThemeAt(th theme.Theme, s *Segment, now time.Time) theme.Theme {
	return blendedText(th, streamAlpha(s, now))
}

// blendedText sets the body text colours to a, where 0 is the background and 1
// is full strength. With truecolour the colour is blended. Without it there is
// nothing to blend (a transparent background, NO_COLOR, an ANSI palette), so
// the text steps down to TextFaint for the first half of a fade and TextDim
// after, and returns to the theme once it reaches full strength.
func blendedText(th theme.Theme, a float64) theme.Theme {
	if a >= 1 {
		return th
	}
	var c lipgloss.TerminalColor
	switch {
	case isHexColour(th.BgApp) && isHexColour(th.TextPrimary):
		c = theme.Lerp(th.BgApp, th.TextPrimary, a)
	case a < 0.5:
		c = th.TextFaint
	default:
		c = th.TextDim
	}
	th.TextPrimary, th.TextSecondary = c, c
	return th
}

// streamAlpha is how far a text segment is blended toward the background at
// now. It rises from 0 to streamOpacity over fadeIn after the segment opens,
// holds there while it streams, then rises to 1 over fadeOut after it closes.
// A segment that was never seen open, or that has finished fading, is 1.
func streamAlpha(s *Segment, now time.Time) float64 {
	if s.Open {
		if s.openedAt.IsZero() {
			return streamOpacity
		}
		return fadeInAlpha(now.Sub(s.openedAt))
	}
	if s.openedAt.IsZero() || s.closedAt.IsZero() {
		return 1
	}
	since := now.Sub(s.closedAt)
	if since >= fadeOut {
		return 1
	}
	from := fadeInAlpha(s.closedAt.Sub(s.openedAt))
	if since <= 0 {
		return from
	}
	return from + (1-from)*outQuad(float64(since)/float64(fadeOut))
}

// fadeInAlpha is the opacity of an open segment that has been streaming for
// elapsed: 0 at the start, streamOpacity from fadeIn on.
func fadeInAlpha(elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	if elapsed >= fadeIn {
		return streamOpacity
	}
	return streamOpacity * outQuad(float64(elapsed)/float64(fadeIn))
}

// outQuad is the ease-out quadratic curve over t in [0, 1].
func outQuad(t float64) float64 {
	t = math.Max(0, math.Min(1, t))
	return 1 - (1-t)*(1-t)
}

// fading reports whether a text segment is still inside a fade at now, which
// is what keeps its record redrawing. Once it reports false the segment is
// settled and its record can be cached.
func (s *Segment) fading(now time.Time) bool {
	if s.Kind != "text" || s.openedAt.IsZero() {
		return false
	}
	if s.Open {
		return now.Sub(s.openedAt) < fadeIn
	}
	return !s.closedAt.IsZero() && now.Sub(s.closedAt) < fadeOut
}

// isHexColour reports whether c is a truecolor value (#rgb or #rrggbb).
// lipgloss.NoColor and ANSI indexes are not.
func isHexColour(c lipgloss.TerminalColor) bool {
	lc, ok := c.(lipgloss.Color)
	return ok && strings.HasPrefix(string(lc), "#")
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
		if v.opt.Verbose {
			src = quoteReminders(src)
		}
		if s.Open {
			src += "▊"
		}
		if v.opt.Verbose && afterTools && len([]rune(trimmed(txt))) > 20 {
			b.add(fg(v.th.TextFaint, strings.Repeat("─", minInt(60, cw))))
		}
		b.add(splitLines(render.Markdown(src, cw, textThemeAt(v.th, s, now), s.Open))...)
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

// mergeTools joins adjacent tool segments into one, so a run of tool calls
// batches the same way whether or not the stream split it. The records are
// not changed: a merged segment is a copy holding the same call pointers.
func mergeTools(segs []*Segment) []*Segment {
	out := make([]*Segment, 0, len(segs))
	for _, s := range segs {
		if n := len(out); s.Kind == "tools" && n > 0 && out[n-1].Kind == "tools" {
			prev := out[n-1]
			merged := *prev
			merged.Calls = append(append([]*Call{}, prev.Calls...), s.Calls...)
			out[n-1] = &merged
			continue
		}
		out = append(out, s)
	}
	return out
}

var reminderBlockRe = regexp.MustCompile(`(?s)<system-reminder>(.*?)</system-reminder>[ \t]*\n?`)

// quoteReminders turns each complete <system-reminder> block into a blockquote
// headed by the gear glyph, for the verbose layout. Unclosed blocks are left
// alone; the renderer hides those while streaming.
func quoteReminders(src string) string {
	return reminderBlockRe.ReplaceAllStringFunc(src, func(m string) string {
		body := strings.TrimSpace(reminderBlockRe.FindStringSubmatch(m)[1])
		lines := []string{"> ⚙ **system reminder**", ">"}
		for _, l := range strings.Split(body, "\n") {
			lines = append(lines, strings.TrimRight("> "+l, " "))
		}
		return strings.Join(lines, "\n") + "\n\n"
	})
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
