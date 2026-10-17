package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// Plan review answers. The agent parses these exact strings (agent/interactive.go).
const (
	answerExecute = agent.AnswerExecute
	answerClear   = agent.AnswerClear
	answerRevise  = agent.AnswerRevise // followed by a space and the feedback
	answerCancel  = agent.AnswerCancel
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
	full   bool   // plan review shows the full markdown instead of the step list
}

func newPromptUI(p Prompt, th theme.Theme, width int) *promptUI {
	ui := &promptUI{p: p, th: th}
	switch {
	case p.Approval != nil:
		ui.kind = "approval"
		if p.Approval.Danger {
			// A destructive action defaults to Deny: Enter alone refuses it.
			ui.sel = 2
		}
	case p.Question != nil && p.Question.Kind == "plan_review":
		ui.kind = "plan"
	default:
		ui.kind = "question"
	}
	return ui
}

// choices are the plan review options: the ones the agent offered for this
// plan (a light plan has no Clear), else all of them.
func (ui *promptUI) choices() []string {
	if q := ui.p.Question; q != nil && len(q.Options) > 0 {
		return q.Options
	}
	return planChoices
}

// hasFreeRow reports whether a question list gets its own "Type an answer"
// row. An "Other" option already opens the text row, so it is not repeated.
func hasFreeRow(opts []string) bool {
	for _, o := range opts {
		if isOtherOption(o) {
			return false
		}
	}
	return true
}

// optionCount is how many rows the list has (options plus the free-text row for questions).
func (ui *promptUI) optionCount() int {
	switch ui.kind {
	case "approval":
		return 3
	case "plan":
		return len(ui.choices())
	}
	n := len(ui.p.Question.Options)
	if !hasFreeRow(ui.p.Question.Options) {
		return n
	}
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
	if t.pui.kind == "plan" && (r.Answer == answerExecute || r.Answer == answerClear) {
		// Approving a plan lifts plan mode in the agent (SetMode / ResetHistory
		// run after this reply). The tab caches its mode, so update it now or
		// the status bar keeps saying PLAN while edits are allowed.
		t.mode = "default"
	}
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
	if ui.kind == "plan" {
		switch {
		case len(key) == 1 && key[0] >= '1' && key[0] <= '0'+byte(len(ui.choices())):
			ui.sel = int(key[0] - '1')
			return m.promptChoose(t), true
		case key == "v":
			ui.full = !ui.full
			ui.scroll = 0
			return nil, true
		}
	}
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
		switch ui.choices()[ui.sel] {
		case answerExecute:
			return m.resolvePrompt(t, PromptReply{Answer: answerExecute})
		case answerClear:
			return m.resolvePrompt(t, PromptReply{Answer: answerClear})
		case answerRevise:
			ui.typing = true
			ui.text = nil
			return nil
		}
		return m.resolvePrompt(t, PromptReply{Answer: answerCancel})
	}
	opts := ui.p.Question.Options
	if ui.sel < len(opts) && !isOtherOption(opts[ui.sel]) {
		return m.resolvePrompt(t, PromptReply{Answer: opts[ui.sel]})
	}
	ui.typing = true
	ui.text = nil
	return nil
}

// isOtherOption reports whether a question option is the "Other" escape the
// model offers, such as "Other" or "Other (I'll type my choice)". Choosing it
// must open the text row, not send its label back as the answer.
func isOtherOption(opt string) bool {
	lower := strings.ToLower(strings.TrimSpace(opt))
	if !strings.HasPrefix(lower, "other") {
		return false
	}
	rest := lower[len("other"):]
	return rest == "" || !unicode.IsLetter(rune(rest[0]))
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

// approvalWhy is the dim line under the options: what the selected choice does.
var approvalWhy = []string{
	"Runs this one call. You are asked again next time.",
	"Runs calls like this without asking again in this session.",
	"Refuses this call. The model is told and carries on.",
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
	lang := ""
	if a.Kind == "shell" {
		lang = "bash"
	}
	for i, l := range strings.Split(render.Wrap(detail, w-6), "\n") {
		if i >= 5 {
			body = append(body, fg(th.TextDim).Render("…"))
			break
		}
		if lang != "" {
			l = strings.TrimRight(render.Highlight(l, lang, th), "\n")
		} else {
			l = fg(th.TextPrimary).Render(l)
		}
		body = append(body, l)
	}
	body = append(body, "")
	opts := []string{"Allow once (y)", "Allow for this session (a)", "Deny (n)"}
	for i, o := range opts {
		body = append(body, m.optionRow(o, i == ui.sel, border))
	}
	body = append(body, "", lipgloss.NewStyle().Italic(true).Foreground(th.TextDim).Render(approvalWhy[ui.sel]))
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
	for _, l := range strings.Split(strings.TrimRight(render.Markdown(q.Question, w-6, th, false), "\n"), "\n") {
		body = append(body, l)
	}
	body = append(body, "")
	for i, o := range q.Options {
		body = append(body, m.optionRow(o, i == ui.sel, th.Brand))
	}
	if ui.typing {
		body = append(body, m.entryRow(ui.text, "type your answer...", th.Brand))
	} else if hasFreeRow(q.Options) {
		body = append(body, m.optionRow("Type an answer…", ui.sel == len(q.Options), th.Brand))
	}
	footer := "↑↓ select · Enter confirm · Esc skip"
	if ui.typing {
		footer = "Enter send · Esc back"
	}
	return frame("Question", icons.Icon("question"), indentRows(body), footer, w, th.Brand, th)
}

// planChoices are the plan review options in key order (keys 1-4).
var planChoices = agent.PlanOptions()

// planChoiceLabel is how an option reads in the review. The answers the agent
// parses (agent.Answer*) are unchanged; only the words and glyph differ.
func planChoiceLabel(choice string) string {
	switch choice {
	case answerExecute:
		return "⏎ Implement"
	case answerClear:
		return "↻ Clear & Implement"
	case answerRevise:
		return icons.Icon("pencil") + " Revise"
	}
	return icons.Icon("fail") + " Cancel"
}

// planChoiceColor is the accent of one plan review option.
func planChoiceColor(th theme.Theme, choice string) lipgloss.TerminalColor {
	switch choice {
	case answerExecute:
		return th.Info
	case answerClear:
		return th.Warning
	case answerRevise:
		return th.BrandAlt
	}
	return th.BrandSecondary
}

// planPath reads the saved-file path the review body starts with ("Saved to `path`").
func planPath(body string) string {
	first, _, _ := strings.Cut(body, "\n")
	p, ok := strings.CutPrefix(first, "Saved to `")
	if !ok {
		return ""
	}
	return strings.TrimSuffix(p, "`")
}

// symbolMark is the glyph and colour for a symbol action in the plan review.
func symbolMark(th theme.Theme, action string) (string, lipgloss.TerminalColor) {
	switch action {
	case "add":
		return "+", th.Success
	case "modify":
		return "~", th.Amber
	case "remove":
		return "-", th.Error
	}
	return "→", th.Info // rename
}

// planLines draws the plan review: a header (title, step count, saved path),
// the steps as ○ rows (two columns above five), the symbols and verification
// the plan records, then the options. v swaps the list for the full markdown.
func (m *Model) planLines(ui *promptUI, w int) []string {
	th := m.th
	q := ui.p.Question
	steps := agent.ParsePlanSteps(q.Body)
	inner := max(10, w-6)
	var body []string
	if ui.full || len(steps) == 0 {
		maxBody := max(4, m.height/3)
		all := strings.Split(render.Markdown(q.Body, w-6, th, false), "\n")
		ui.scroll = clamp(ui.scroll, 0, max(0, len(all)-maxBody))
		end := min(len(all), ui.scroll+maxBody)
		body = append(body, all[ui.scroll:end]...)
		if len(all) > maxBody {
			body = append(body, fg(th.TextFaint).Render("PgUp/PgDn scroll the plan"))
		}
	} else {
		body = append(body, planHeader(th, q.Question, len(steps), planPath(q.Body), inner), "")
		body = append(body, stepRows(th, steps, inner, max(5, m.height/2))...)
		if syms := planSymbols(steps); len(syms) > 0 {
			body = append(body, "", fg(th.BrandAlt).Render(fmt.Sprintf("Symbols (%d)", len(syms))))
			body = append(body, listCapped(syms, 5, th.TextDim, func(s agent.PlanSymbol) string {
				g, col := symbolMark(th, s.Action)
				label := s.Name
				if s.Kind != "" {
					label += " (" + s.Kind + ")"
				}
				return fg(col).Render(g+" ") + fg(th.TextSecondary).Render(render.Truncate(label, inner-2))
			})...)
		}
		if ver := agent.ParsePlanVerification(q.Body); len(ver) > 0 {
			body = append(body, "", fg(th.BrandAlt).Render("Verification"))
			body = append(body, listCapped(ver, 5, th.TextDim, func(v string) string {
				return fg(th.Success).Render(icons.Icon("success")+" ") + fg(th.TextSecondary).Render(render.Truncate(v, inner-2))
			})...)
		}
	}
	body = append(body, "")
	for i, c := range ui.choices() {
		body = append(body, m.optionRow(fmt.Sprintf("%d  %s", i+1, planChoiceLabel(c)), i == ui.sel, planChoiceColor(th, c)))
	}
	if ui.typing {
		body = append(body, m.entryRow(ui.text, "what should change...", th.BrandAlt))
	}
	// The spec hint, plus "v full plan": the key is otherwise undiscoverable.
	footer := "[↑↓] select · [Enter] confirm · v full plan · [Esc] cancel"
	if ui.full {
		footer = "[↑↓] select · [Enter] confirm · v steps · [Esc] cancel"
	}
	if ui.typing {
		footer = "⏎ submit │ esc back"
	}
	return frame("", "", indentRows(body), footer, w, th.Info, th)
}

// planHeader is the review's first row: plan icon and bold title, then the
// step count and the saved path, split by faint rules. The path is cut to
// whatever room the header has left.
func planHeader(th theme.Theme, title string, steps int, path string, w int) string {
	sep := fg(th.TextFaint).Render(" │ ")
	head := fg(th.Info).Render(icons.Icon("plan")+" ") + fgb(th.Info).Render(render.Truncate(title, max(10, w/2)))
	head += sep + fg(th.TextDim).Render(fmt.Sprintf("%d %s", steps, plural(steps, "step", "steps")))
	if path != "" {
		room := w - ansi.StringWidth(head) - ansi.StringWidth(" │ ")
		if room > 4 {
			head += sep + fg(th.TextDim).Render(render.Truncate(path, room))
		}
	}
	return render.Truncate(head, w)
}

// stepRows draws the plan steps as ○ rows. Above five steps they fill two
// columns split at ceil(n/2), joined by a faint rule. A list longer than
// maxRows rows ends in "+N more".
func stepRows(th theme.Theme, steps []agent.PlanStep, w, maxRows int) []string {
	cell := func(s agent.PlanStep, cw int) string {
		return fg(th.TextMuted).Render("○") + " " + fg(th.TextSecondary).Render(render.Truncate(s.Text, max(1, cw-2)))
	}
	if len(steps) <= 5 {
		var rows []string
		for i, s := range steps {
			if i == maxRows {
				rows = append(rows, fg(th.TextDim).Render(fmt.Sprintf("  +%d more", len(steps)-i)))
				break
			}
			rows = append(rows, cell(s, w-1))
		}
		return rows
	}
	colW := (w - 3) / 2
	half := (len(steps) + 1) / 2
	var rows []string
	for i := 0; i < half; i++ {
		if i == maxRows {
			rows = append(rows, fg(th.TextDim).Render(fmt.Sprintf("+%d more", len(steps)-2*i)))
			break
		}
		left := padRight(cell(steps[i], colW), colW)
		if j := half + i; j < len(steps) {
			rows = append(rows, left+fg(th.TextSubtle).Render(" │ ")+cell(steps[j], colW))
		} else {
			rows = append(rows, left)
		}
	}
	return rows
}

// planSymbols gathers every symbol the plan's steps touch, in step order.
func planSymbols(steps []agent.PlanStep) []agent.PlanSymbol {
	var out []agent.PlanSymbol
	for _, s := range steps {
		out = append(out, s.Symbols...)
	}
	return out
}

// listCapped renders up to limit items, then a dim "+N more" line.
func listCapped[T any](items []T, limit int, dim lipgloss.TerminalColor, row func(T) string) []string {
	var out []string
	for i, it := range items {
		if i == limit {
			out = append(out, fg(dim).Render(fmt.Sprintf("+%d more", len(items)-i)))
			break
		}
		out = append(out, row(it))
	}
	return out
}

// optionRow renders one choice. The selected row reads " › label" in the
// option colour with bold text; the others are indented by the same three
// cells, so labels line up whether or not they are selected.
func (m *Model) optionRow(label string, sel bool, col lipgloss.TerminalColor) string {
	th := m.th
	if sel {
		return fg(col).Render(" › ") + fgb(th.TextPrimary).Render(label)
	}
	return "   " + fg(th.TextSecondary).Render(label)
}

// entryRow is the free-text line under the options: a brand prompt, the text
// typed so far, and a dim placeholder while it is still empty.
func (m *Model) entryRow(text []rune, placeholder string, col lipgloss.TerminalColor) string {
	th := m.th
	row := fg(col).Render(" › ")
	if len(text) == 0 {
		return row + fg(th.TextDim).Render(placeholder) + fg(col).Render("▌")
	}
	return row + fg(th.TextPrimary).Render(string(text)) + fg(col).Render("▌")
}

// indentRows adds the one-column gutter that sits inside the frame.
func indentRows(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = " " + r
	}
	return out
}
