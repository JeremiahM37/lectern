package chat

import (
	"fmt"
	"strings"
	"time"

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
	}
	brain := icons.Icon("brain")
	if !v.expandedFor(&Call{Toggled: s.Toggled}) {
		var head string
		if s.Open {
			head = fg(th.BrandDim, render.Frame(render.Braille, frameAt(now))) + " " +
				fg(th.TextFaint, "▶ "+brain+" reasoning"+count) + " " + fg(th.TextSubtle, "^O")
		} else {
			head = fg(th.Success, "▶") + " " + fg(th.TextMuted, brain+" "+reasoningPreview(s.Text)) +
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

// reasoningPreview is the first line of a finished reasoning block.
func reasoningPreview(text string) string {
	p := strings.TrimSpace(strings.ReplaceAll(firstLine(text), "**", ""))
	if p == "" {
		return "Reasoned"
	}
	return clip(p, 60)
}

var phrases = map[string][][2]string{
	"explore":  {{"Reading the code", "Read the code"}, {"Following references", "Followed references"}, {"Searching", "Searched"}, {"Mapping dependencies", "Mapped dependencies"}},
	"edit":     {{"Writing changes", "Wrote changes"}, {"Editing", "Edited"}, {"Revising", "Revised"}},
	"dispatch": {{"Sending out helpers", "Helpers reported back"}, {"Working in parallel", "Parallel work done"}},
}

var (
	phaseTone = map[string]string{"explore": "brand", "edit": "warning", "dispatch": "info"}
	phaseSpin = map[string]render.Spinner{"explore": render.Explore, "edit": render.Edit, "dispatch": render.Dispatch}
)

const (
	phraseEvery = 8 * time.Second
	garbleStep  = 70 * time.Millisecond
)

// statusHeader is the phase line above a rail: a spinner, the rotating phrase
// (with a short garble when it changes), animated dots and elapsed seconds.
// Once the turn is done it reads "✓ <done phrase>".
func (v *View) statusHeader(phase string, live bool, start, now time.Time) string {
	tone := v.tone(phaseTone[phase])
	pairs := phrases[phase]
	if !live {
		return fg(tone, "✓ "+pairs[0][1])
	}
	el := now.Sub(start)
	if el < 0 {
		el = 0
	}
	k := int(el / phraseEvery)
	i := k % len(pairs)
	off := el % phraseEvery
	label := pairs[i][0]
	switch {
	case k > 0 && off < 3*garbleStep:
		label = garble(pairs[(k-1)%len(pairs)][0], int(off/garbleStep))
	case k > 0 && off < 6*garbleStep:
		label = garble(label, int(off/garbleStep))
	}
	spin := render.Frame(phaseSpin[phase], frameAt(now))
	dots := render.Frame(render.LoadingDots, frameAt(now)/4)
	return fg(tone, spin) + " " + bold(tone, label) + fg(v.th.TextMuted, dots) + " " + fg(v.th.TextDim, secs(el))
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
