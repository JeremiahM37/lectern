package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// Plan review answers. The agent parses these exact strings (agent/interactive.go).
const (
	answerExecute = "Execute"
	answerCancel  = "Cancel"
	answerRevise  = "Revise" // followed by a space and the feedback
)

// promptUI is the inline panel that replaces the input while a tool waits
// for the user. It answers exactly once through Prompt.Reply.
type promptUI struct {
	p      Prompt
	th     theme.Theme
	kind   string // "approval", "question", "plan"
	sel    int
	text   []rune // free-text answer or revision feedback
	typing bool   // free-text entry is open
	scroll int    // plan body scroll
}

func newPromptUI(p Prompt, th theme.Theme, width int) *promptUI {
	ui := &promptUI{p: p, th: th}
	switch {
	case p.Approval != nil:
		ui.kind = "approval"
	case p.Question != nil && p.Question.Kind == "plan_review":
		ui.kind = "plan"
	default:
		ui.kind = "question"
	}
	return ui
}

// optionCount is how many rows the list has (options plus the free-text row for questions).
func (ui *promptUI) optionCount() int {
	switch ui.kind {
	case "approval":
		return 3
	case "plan":
		return 3
	}
	n := len(ui.p.Question.Options)
	return n + 1 // "Type an answer…"
}

// denyFor is the reply that means "no" for a prompt, used by Ctrl+C.
func denyFor(ui *promptUI) PromptReply {
	switch ui.kind {
	case "approval":
		return PromptReply{Decision: tools.Deny}
	case "plan":
		return PromptReply{Answer: answerCancel}
	}
	return PromptReply{Answer: ""}
}

// resolvePrompt sends the answer and opens the next queued prompt.
func (m *Model) resolvePrompt(t *tab, r PromptReply) tea.Cmd {
	if t.pui == nil {
		return nil
	}
	p := t.pui.p
	t.pui = nil
	m.openNextPrompt(t)
	return func() tea.Msg {
		p.Reply <- r
		return nil
	}
}

// handlePromptKey consumes every key while a prompt is open on the active tab.
func (m *Model) handlePromptKey(km tea.KeyMsg) (tea.Cmd, bool) {
	t := m.cur()
	ui := t.pui
	key := km.String()
	if ui.typing {
		return m.promptTyping(t, km), true
	}
	n := ui.optionCount()
	switch key {
	case "esc":
		return m.resolvePrompt(t, denyFor(ui)), true
	case "up", "shift+tab", "left":
		ui.sel = (ui.sel - 1 + n) % n
		return nil, true
	case "down", "tab", "right":
		ui.sel = (ui.sel + 1) % n
		return nil, true
	case "pgup":
		ui.scroll = max(0, ui.scroll-5)
		return nil, true
	case "pgdown":
		ui.scroll += 5
		return nil, true
	case "y", "Y":
		if ui.kind == "approval" {
			return m.resolvePrompt(t, PromptReply{Decision: tools.AllowOnce}), true
		}
	case "a", "A":
		if ui.kind == "approval" {
			return m.resolvePrompt(t, PromptReply{Decision: tools.AllowSession}), true
		}
	case "n", "N":
		if ui.kind == "approval" {
			return m.resolvePrompt(t, denyFor(ui)), true
		}
	case "enter":
		return m.promptChoose(t), true
	}
	return nil, true
}

// promptChoose acts on the highlighted row.
func (m *Model) promptChoose(t *tab) tea.Cmd {
	ui := t.pui
	switch ui.kind {
	case "approval":
		switch ui.sel {
		case 0:
			return m.resolvePrompt(t, PromptReply{Decision: tools.AllowOnce})
		case 1:
			return m.resolvePrompt(t, PromptReply{Decision: tools.AllowSession})
		}
		return m.resolvePrompt(t, denyFor(ui))
	case "plan":
		switch ui.sel {
		case 0:
			return m.resolvePrompt(t, PromptReply{Answer: answerExecute})
		case 1:
			ui.typing = true
			ui.text = nil
			return nil
		}
		return m.resolvePrompt(t, PromptReply{Answer: answerCancel})
	}
	opts := ui.p.Question.Options
	if ui.sel < len(opts) {
		return m.resolvePrompt(t, PromptReply{Answer: opts[ui.sel]})
	}
	ui.typing = true
	ui.text = nil
	return nil
}

// promptTyping handles the free-text row (question answer or plan revision).
func (m *Model) promptTyping(t *tab, km tea.KeyMsg) tea.Cmd {
	ui := t.pui
	switch km.String() {
	case "esc":
		ui.typing = false
		return nil
	case "enter":
		v := strings.TrimSpace(string(ui.text))
		if v == "" {
			return nil
		}
		if ui.kind == "plan" {
			return m.resolvePrompt(t, PromptReply{Answer: answerRevise + " " + v})
		}
		return m.resolvePrompt(t, PromptReply{Answer: v})
	case "backspace":
		if len(ui.text) > 0 {
			ui.text = ui.text[:len(ui.text)-1]
		}
	default:
		if s := printable(km); s != "" {
			ui.text = append(ui.text, []rune(s)...)
		}
	}
	return nil
}

// promptLines draws the prompt panel that replaces the input box.
func (m *Model) promptLines(t *tab) []string {
	ui := t.pui
	if ui == nil {
		return nil
	}
	w := m.width
	switch ui.kind {
	case "approval":
		return m.approvalLines(ui, w)
	case "plan":
		return m.planLines(ui, w)
	}
	return m.questionLines(ui, w)
}

func (m *Model) approvalLines(ui *promptUI, w int) []string {
	th := m.th
	a := ui.p.Approval
	border := th.BorderFocused
	title := a.Title
	if a.Danger {
		border = th.Error
	}
	var body []string
	detail := a.Detail
	if detail == "" {
		detail = "(no details)"
	}
	for i, l := range strings.Split(render.Wrap(detail, w-6), "\n") {
		if i >= 5 {
			body = append(body, fg(th.TextDim).Render("  …"))
			break
		}
		body = append(body, fg(th.TextSecondary).Render(l))
	}
	body = append(body, "")
	opts := []string{"Allow once (y)", "Allow for this session (a)", "Deny (n)"}
	for i, o := range opts {
		body = append(body, m.optionRow(o, i == ui.sel, border))
	}
	ic := icons.Icon("warning")
	if !a.Danger {
		ic = icons.Icon("question")
	}
	sub := a.Tool
	if sub != "" {
		sub = " · " + sub
	}
	return frame(title+sub, ic, indentRows(body), "↑↓ select · Enter confirm · y/a/n", w, border, th)
}

func (m *Model) questionLines(ui *promptUI, w int) []string {
	th := m.th
	q := ui.p.Question
	body := []string{}
	for _, l := range strings.Split(render.Wrap(q.Question, w-6), "\n") {
		body = append(body, fg(th.TextPrimary).Render(l))
	}
	body = append(body, "")
	for i, o := range q.Options {
		body = append(body, m.optionRow(o, i == ui.sel, th.Brand))
	}
	free := len(q.Options)
	if ui.typing {
		row := fg(th.Brand).Render(" › ") + fg(th.TextPrimary).Render(string(ui.text)) + fg(th.Brand).Render("▌")
		body = append(body, row)
	} else {
		body = append(body, m.optionRow("Type an answer…", ui.sel == free, th.Brand))
	}
	footer := "↑↓ select · Enter confirm · Esc skip"
	if ui.typing {
		footer = "Enter send · Esc back"
	}
	return frame("Question", icons.Icon("question"), indentRows(body), footer, w, th.Brand, th)
}

func (m *Model) planLines(ui *promptUI, w int) []string {
	th := m.th
	q := ui.p.Question
	maxBody := max(4, m.height/3)
	md := render.Markdown(q.Body, w-6, th, false)
	all := strings.Split(md, "\n")
	ui.scroll = clamp(ui.scroll, 0, max(0, len(all)-maxBody))
	end := min(len(all), ui.scroll+maxBody)
	body := append([]string{}, all[ui.scroll:end]...)
	if len(all) > maxBody {
		body = append(body, fg(th.TextFaint).Render("PgUp/PgDn scroll the plan"))
	}
	body = append(body, "")
	labels := []string{"Execute", "Revise", "Cancel"}
	var btns []string
	for i, l := range labels {
		btns = append(btns, m.optionRow(l, i == ui.sel, th.Info))
	}
	if ui.typing {
		body = append(body, fg(th.BrandAlt).Render(" revise › ")+fg(th.TextPrimary).Render(string(ui.text))+fg(th.BrandAlt).Render("▌"))
	} else {
		body = append(body, strings.Join(btns, "    "))
	}
	footer := "[↑↓] select · [Enter] confirm · [Esc] cancel"
	if ui.typing {
		footer = "Enter submit · Esc back"
	}
	title := q.Question
	return frame(title, icons.Icon("plan"), indentRows(body), footer, w, th.Info, th)
}

// optionRow renders one choice; the selected row gets a marker and bold text.
func (m *Model) optionRow(label string, sel bool, col lipgloss.TerminalColor) string {
	th := m.th
	if sel {
		return fg(col).Render("› ") + fgb(th.TextPrimary).Render(label)
	}
	return "  " + fg(th.TextSecondary).Render(label)
}

// indentRows adds the one-column gutter that sits inside the frame.
func indentRows(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = " " + r
	}
	return out
}
