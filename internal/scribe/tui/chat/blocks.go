package chat

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// reasoningTail is how much of a streaming reasoning block is shown.
const reasoningTail = 4096

// stepNumberRe is the "N." or "N)" a step text may start with. The card
// already numbers steps, or draws a bullet, so the number is dropped.
var stepNumberRe = regexp.MustCompile(`^\s*\d{1,3}[.)]\s+`)

// stepLabel is a plan step's text as shown in a card: without a leading number.
func stepLabel(text string) string {
	return stepNumberRe.ReplaceAllString(text, "")
}

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
			// While it streams, the count only appears once there is more than one line.
			live := ""
			if lines > 1 {
				live = count
			}
			head = fg(th.BrandDim, render.Frame(render.Braille, frameAt(now))) + " " +
				fg(th.TextFaint, "▶ "+brain+" reasoning"+live) + " " + fg(th.TextSubtle, "^O")
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
// postAnswerTools are run by Scribe after the model has answered, not by the
// model, so their rows do not make the answer before them intermediate.
var postAnswerTools = map[string]bool{"auto_check": true}

// lastModelTools is the index of the last tool segment holding a call the
// model made, or -1. Text after it is the turn's answer.
func lastModelTools(rec *Record) int {
	last := -1
	for i, s := range rec.Segments {
		if s.Kind != "tools" {
			continue
		}
		for _, c := range s.Calls {
			if !postAnswerTools[c.Tool] {
				last = i
				break
			}
		}
	}
	return last
}

func answerText(rec *Record) string {
	last := lastModelTools(rec)
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
	"review":   {"Waiting for your answer"},
}

var (
	phaseTone = map[string]string{"explore": "brand", "edit": "warning", "dispatch": "info", "review": "warning"}
	phaseSpin = map[string]render.Spinner{"explore": render.Explore, "edit": render.Edit, "dispatch": render.Dispatch, "review": {"◎"}}
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

// renderPlan draws a plan segment with no task links. See renderPlanTasks.
func (v *View) renderPlan(s *Segment, cw int, now time.Time) builder {
	return v.renderPlanTasks(s, nil, cw, now)
}

// renderPlanTasks draws a plan segment in the state it is in. A plan the user
// cancelled or sent back is a rejected artifact. A plan no step has started
// is the structured artifact: context, files, steps and verification. While
// it runs, it is the progress box, with the tasks linked to the active step
// nested under it. Once every step is finished it folds to one line until it
// is clicked. tasks are the task-list entries of the turn.
func (v *View) renderPlanTasks(s *Segment, tasks []agent.Task, cw int, now time.Time) builder {
	var b builder
	p := s.Plan
	if p == nil {
		return b
	}
	if rejectedPlan(p) {
		return v.renderRejectedPlan(p, cw)
	}
	if p.Outcome == "approved" && !s.Toggled && !v.expanded && planProposed(p) {
		// Approved, and no step has reported yet: the approved summary.
		files := planFileList(p)
		line := fg(v.th.Success, icons.Icon("success")) + " " + bold(v.th.TextPrimary, p.Title) +
			fg(v.th.TextMuted, fmt.Sprintf(" (%d files, %d steps)", len(files), len(p.Steps)))
		if p.Path != "" {
			line += fg(v.th.TextFaint, " ─ ") + fg(v.th.TextMuted, p.Path)
		}
		b.mark(nil, s)
		b.add(line)
		return b
	}
	if planProposed(p) {
		return v.renderStructuredPlan(p, cw)
	}
	th := v.th
	total, done, finished, active := len(p.Steps), 0, 0, -1
	for i, st := range p.Steps {
		switch st.Status {
		case "done":
			done++
			finished++
		case "skipped":
			finished++
		case "active":
			if active < 0 {
				active = i
			}
		}
	}
	allDone := total > 0 && finished == total
	files := planFileList(p)
	if allDone && !s.Toggled && !v.expanded {
		line := fg(th.Success, icons.Icon("success")) + " " + bold(th.TextPrimary, p.Title) +
			fg(th.TextMuted, fmt.Sprintf(" (%d files, %d steps)", len(files), total))
		if p.Path != "" {
			line += fg(th.TextFaint, " ─ ") + fg(th.TextMuted, p.Path)
		}
		b.mark(nil, s)
		b.add(line)
		return b
	}
	border := th.BrandAlt
	if allDone {
		border = th.Success
	}
	start, end := 0, total
	if total > 7 && !allDone {
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
	var body []string
	if start > 0 {
		body = append(body, fg(th.TextDim, fmt.Sprintf("%d completed above", start)))
	}
	for i := start; i < end; i++ {
		st := p.Steps[i]
		var glyph, text string
		label := stepLabel(st.Text)
		switch st.Status {
		case "done":
			glyph, text = fg(th.Success, icons.Icon("success")), fg(th.TextMuted, label)
		case "skipped":
			glyph, text = fg(th.TextDim, icons.Icon("skip")), fg(th.TextDim, label)
		case "active":
			glyph, text = fg(th.Brand, render.Frame(render.Braille, frameAt(now))), bold(th.TextPrimary, label)
		default:
			glyph, text = fg(th.TextMuted, "○"), fg(th.TextMuted, label)
		}
		body = append(body, glyph+" "+clip(text, cw-6))
		if st.Status == "active" {
			body = append(body, planSymbolRows("", st.Symbols, cw-4, th)...)
			body = append(body, prefixRows(planTaskRows(linkedTo(tasks, st.ID), cw-4, th, now), "  ")...)
		}
	}
	if end < total {
		body = append(body, fg(th.TextDim, fmt.Sprintf("%d more pending", total-end)))
	}
	if allDone {
		body = append(body, planFilesSection(files, th)...)
		body = append(body, planVerifySection(p.Verification, th)...)
	}
	title := fg(th.BrandAlt, icons.Icon("plan")) + " " + bold(th.BrandAlt, clip(p.Title, maxInt(cw-20, 8))) +
		fg(th.TextMuted, fmt.Sprintf("  %d/%d", done, total))
	if allDone {
		b.mark(nil, s)
	}
	b.add(boxed(title, body, cw, 1, border, nil)...)
	return b
}

// rejectedPlan reports whether the user sent the plan back or cancelled it.
func rejectedPlan(p *agent.Plan) bool {
	return p.Outcome == "cancelled" || p.Outcome == "revision"
}

// planProposed reports whether a plan has steps and none has started: it is
// still the proposal the user is reviewing.
func planProposed(p *agent.Plan) bool {
	if len(p.Steps) == 0 {
		return false
	}
	for _, st := range p.Steps {
		if st.Status != "" && st.Status != "pending" {
			return false
		}
	}
	return true
}

// renderStructuredPlan is the plan as an artifact in the transcript: a box
// titled with the plan, then Context, Files (with what happens to each),
// Steps and Verification. Five rows per list, then "+N more".
func (v *View) renderStructuredPlan(p *agent.Plan, cw int) builder {
	var b builder
	th := v.th
	title := fg(th.Info, icons.Icon("plan")) + " " + bold(th.Info, clip(p.Title, maxInt(cw-24, 8)))
	if p.Path != "" {
		title += fg(th.TextFaint, " ─ ") + fg(th.TextMuted, clip(p.Path, maxInt(cw-24-len(p.Title), 8)))
	}
	var body []string
	if ctx := strings.TrimSpace(p.Summary); ctx != "" {
		body = append(body, bold(th.BrandAlt, "Context"))
		for _, line := range strings.Split(render.Wrap(ctx, maxInt(cw-6, 10)), "\n") {
			body = append(body, fg(th.TextSecondary, line))
		}
		body = append(body, "")
	}
	if files := planFileList(p); len(files) > 0 {
		body = append(body, bold(th.BrandAlt, fmt.Sprintf("Files (%d)", len(files))))
		for i, f := range files {
			if i == 5 {
				body = append(body, fg(th.TextMuted, fmt.Sprintf("+%d more", len(files)-5)))
				break
			}
			g, col := planFileMark(f.Action, th)
			body = append(body, fg(col, g)+" "+fg(th.TextPrimary, clip(f.Path, cw-8)))
			if f.Description != "" {
				body = append(body, "   "+fg(th.TextMuted, clip(f.Description, cw-8)))
			}
		}
		body = append(body, "")
	}
	body = append(body, bold(th.BrandAlt, fmt.Sprintf("Steps (%d)", len(p.Steps))))
	for i, st := range p.Steps {
		if i == 5 {
			body = append(body, fg(th.TextMuted, fmt.Sprintf("+%d more", len(p.Steps)-5)))
			break
		}
		body = append(body, fg(th.BrandAlt, fmt.Sprintf("%d.", i+1))+" "+fg(th.TextSecondary, clip(stepLabel(st.Text), cw-8)))
	}
	body = append(body, planVerifySection(p.Verification, th)...)
	b.add(boxed(title, body, cw, 1, th.TextFaint, nil)...)
	return b
}

// renderRejectedPlan is a plan the user did not approve. It keeps its title
// in a muted, subtle box and says what happened: cancelled, or the changes the
// user asked for.
func (v *View) renderRejectedPlan(p *agent.Plan, cw int) builder {
	var b builder
	th := v.th
	title := fg(th.TextMuted, icons.Icon("plan")) + " " + fg(th.TextMuted, clip(p.Title, maxInt(cw-12, 8)))
	var line string
	if p.Outcome == "revision" {
		line = fg(th.Warning, "↻ Revision requested: ") + fg(th.TextSecondary, clip(p.Feedback, maxInt(cw-30, 8)))
	} else {
		line = fg(th.Error, icons.Icon("fail")+" Plan cancelled")
	}
	b.add(boxed(title, []string{line}, cw, 1, th.TextSubtle, nil)...)
	return b
}

// planFileList is the plan's files with their actions. A plan that gave no
// file list falls back to the paths its steps name, with no action.
func planFileList(p *agent.Plan) []agent.PlanFile {
	if len(p.Files) > 0 {
		return p.Files
	}
	var out []agent.PlanFile
	seen := map[string]bool{}
	for _, st := range p.Steps {
		for _, f := range st.Files {
			if f != "" && !seen[f] {
				seen[f] = true
				out = append(out, agent.PlanFile{Path: f})
			}
		}
	}
	return out
}

// planFilesSection is the files part of a finished plan, at most five rows.
func planFilesSection(files []agent.PlanFile, th theme.Theme) []string {
	if len(files) == 0 {
		return nil
	}
	out := []string{"", bold(th.BrandAlt, fmt.Sprintf("Files (%d)", len(files)))}
	for i, f := range files {
		if i == 5 {
			out = append(out, fg(th.TextMuted, fmt.Sprintf("+%d more", len(files)-5)))
			break
		}
		g, col := planFileMark(f.Action, th)
		out = append(out, fg(col, g)+" "+fg(th.TextPrimary, f.Path))
		if f.Description != "" {
			out = append(out, "   "+fg(th.TextMuted, f.Description))
		}
	}
	return out
}

// planVerifySection is the checklist that proves the change, at most five rows.
func planVerifySection(items []string, th theme.Theme) []string {
	if len(items) == 0 {
		return nil
	}
	out := []string{"", bold(th.BrandAlt, "Verification")}
	for i, it := range items {
		if i == 5 {
			out = append(out, fg(th.TextMuted, fmt.Sprintf("+%d more", len(items)-5)))
			break
		}
		out = append(out, fg(th.Success, "✓ ")+fg(th.TextSecondary, it))
	}
	return out
}

// planStepIDs are the step ids of the record's live plans (not rejected ones).
func planStepIDs(rec *Record) map[string]bool {
	ids := map[string]bool{}
	for _, s := range rec.Segments {
		if s.Kind == "plan" && s.Plan != nil && s.Plan.Outcome == "" {
			for _, st := range s.Plan.Steps {
				ids[st.ID] = true
			}
		}
	}
	return ids
}

// recordTasks is every task-list entry of the record, in order.
func recordTasks(rec *Record) []agent.Task {
	var out []agent.Task
	for _, s := range rec.Segments {
		if s.Kind == "tasks" {
			out = append(out, s.Tasks...)
		}
	}
	return out
}

// linkedTo is the tasks linked to one plan step.
func linkedTo(tasks []agent.Task, stepID string) []agent.Task {
	var out []agent.Task
	for _, t := range tasks {
		if t.StepID == stepID {
			out = append(out, t)
		}
	}
	return out
}

// unlinkedTasks is a copy of a task segment without the tasks nested under a
// step of the record's plan. Those are shown under the step instead.
func unlinkedTasks(rec *Record, s *Segment) *Segment {
	ids := planStepIDs(rec)
	if len(ids) == 0 {
		return s
	}
	cp := *s
	cp.Tasks = nil
	for _, t := range s.Tasks {
		if t.StepID == "" || !ids[t.StepID] {
			cp.Tasks = append(cp.Tasks, t)
		}
	}
	return &cp
}

// renderLinkedSeg draws a plan or task-list segment of rec with the task links
// applied, and any other segment as renderSeg does.
func (v *View) renderLinkedSeg(rec *Record, s *Segment, cw int, now time.Time, afterTools bool) builder {
	switch s.Kind {
	case "plan":
		return v.renderPlanTasks(s, recordTasks(rec), cw, now)
	case "tasks":
		return v.renderTasks(unlinkedTasks(rec, s), cw, now)
	}
	return v.renderSeg(s, cw, now, afterTools)
}

// tasksLinger is how long a task list stays up after every task is done.
const tasksLinger = 3 * time.Second

// tasksDone reports whether every task is finished (done or cancelled).
func tasksDone(tasks []agent.Task) bool {
	for _, t := range tasks {
		if t.Status != "done" && t.Status != "cancelled" {
			return false
		}
	}
	return len(tasks) > 0
}

// tasksHidden reports whether a finished task list has lingered long enough
// to fold away.
func (s *Segment) tasksHidden(now time.Time) bool {
	return !s.DoneAt.IsZero() && now.Sub(s.DoneAt) >= tasksLinger
}

// renderTasks draws the task list in a box: the done count, then up to six
// open tasks. Once every task is done the box stays for tasksLinger and then
// hides.
func (v *View) renderTasks(s *Segment, cw int, now time.Time) builder {
	var b builder
	th := v.th
	if len(s.Tasks) == 0 || s.tasksHidden(now) {
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
	var body []string
	if done > 0 {
		body = append(body, fg(th.Success, fmt.Sprintf("+%d done", done)))
	}
	for i, t := range open {
		if i == 6 {
			body = append(body, fg(th.TextMuted, fmt.Sprintf("+%d more", len(open)-6)))
			break
		}
		body = append(body, taskRow(t, cw, th, now))
	}
	title := bold(th.Brand, icons.Icon("plan")+" Tasks") + "  " + fg(th.TextSecondary, fmt.Sprintf("%d/%d", done, len(s.Tasks)))
	b.add(boxed(title, body, cw, 1, th.BrandDim, nil)...)
	return b
}
