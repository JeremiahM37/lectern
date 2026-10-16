package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// modeInfo describes one forge mode.
type modeInfo struct {
	id, label, desc string
	color           lipgloss.TerminalColor
}

// modeOrder is the Ctrl+D / Shift+Tab cycle.
var modeOrder = []string{"default", "auto", "plan", "architect", "socratic", "challenge"}

// modeStyle returns the label, description and colour for a mode id.
func modeStyle(id string, th theme.Theme) modeInfo {
	switch id {
	case "auto":
		return modeInfo{id, "AUTO", "approves tool calls itself", th.Warning}
	case "plan":
		return modeInfo{id, "PLAN", "research and plan only, no edits", th.Info}
	case "architect":
		return modeInfo{id, "ARCHITECT", "design only, outlines and tradeoffs", th.BrandAlt}
	case "socratic":
		return modeInfo{id, "SOCRATIC", "asks probing questions first", th.Success}
	case "challenge":
		return modeInfo{id, "CHALLENGE", "challenges every assumption", th.Error}
	}
	return modeInfo{"default", "DEFAULT", "implements directly", th.Brand}
}

// nextMode returns the mode after cur in the cycle.
func nextMode(cur string) string {
	for i, id := range modeOrder {
		if id == cur {
			return modeOrder[(i+1)%len(modeOrder)]
		}
	}
	return modeOrder[0]
}

// providerOf returns the provider part of a provider/model ref.
func providerOf(ref string) string {
	if i := strings.Index(ref, "/"); i >= 0 {
		return ref[:i]
	}
	return ref
}

// modelOf returns the model part of a provider/model ref.
func modelOf(ref string) string {
	if i := strings.Index(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

// ---------------------------------------------------------------- header

// viewHeader is the single top row. The left holds the identity; the middle
// holds model, branch, mode and context; the right holds the cost and the brand
// tag. When the row is too narrow, pieces leave in a fixed order and never
// mid-word: the brand tag first (under 100 columns), then the branch, then the
// context numbers (the bar stays), then the mode badge, and last the model
// name shortened at a word boundary.
func (m *Model) viewHeader() string {
	th := m.th
	t := m.cur()
	w := m.width
	left := fgb(th.Brand).Render("✒ Scribe") + " " + fg(th.TextMuted).Render("v"+m.version)
	sep := fg(th.TextFaint).Render(" │ ")

	var cost, brand string
	if !t.busy && t.cost >= 0 {
		cost = m.costSegment(t)
	}
	if w >= 100 {
		brand = fg(th.TextMuted).Italic(true).Render(typewriter("in Lectern", m.frame))
	}
	right := joinNonEmpty(sep, cost, brand)

	var branch, mode, bar, nums string
	if t.git.Branch != "" {
		branch = m.branchSegment(t)
	}
	if len(m.tabs) <= 1 && t.mode != "default" {
		mi := modeStyle(t.mode, th)
		mode = fgb(mi.color).Render("[" + mi.label + "]")
	}
	if t.ctxWin > 0 {
		bar, nums = m.contextParts(t)
	}

	// build assembles the middle group for the given pieces.
	build := func(model string, withBranch, withNums bool) string {
		parts := []string{model}
		if withBranch && branch != "" {
			parts = append(parts, branch)
		}
		if mode != "" {
			parts = append(parts, mode)
		}
		if bar != "" {
			c := bar
			if withNums && nums != "" {
				c += " " + nums
			}
			parts = append(parts, c)
		}
		return strings.Join(parts, sep)
	}
	fixed := ansi.StringWidth(left) + ansi.StringWidth(right) + 4
	modelFull := m.modelSegment(t, 0)
	for _, opt := range [][2]bool{{true, true}, {false, true}, {false, false}} {
		if mid := build(modelFull, opt[0], opt[1]); fixed+ansi.StringWidth(mid) <= w {
			return m.headerRow(left, mid, right)
		}
	}
	// Still too wide: drop the mode badge, then shorten the model name.
	mode = ""
	room := w - fixed - ansi.StringWidth(build("", false, false))
	if room >= 6 {
		return m.headerRow(left, build(m.modelSegment(t, room), false, false), right)
	}
	// Too narrow for the middle group: keep the identity and the cost.
	return m.headerRow(left, "", cost)
}

// joinNonEmpty joins the non-empty parts with sep.
func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// headerRow lays out left, mid and right on one row of the header's width:
// " left<gap>mid  right ". The gap takes whatever is left.
func (m *Model) headerRow(left, mid, right string) string {
	used := 1 + ansi.StringWidth(left) + ansi.StringWidth(right) + 1
	if mid != "" {
		used += ansi.StringWidth(mid) + 2
	}
	gap := max(1, m.width-used)
	out := " " + left + strings.Repeat(" ", gap)
	if mid != "" {
		out += mid + "  "
	}
	return render.Truncate(out+right+" ", m.width)
}

// typewriter reveals s one rune per two frames and then holds.
func typewriter(s string, frame int) string {
	r := []rune(s)
	n := frame / 2
	if n > len(r) {
		n = len(r)
	}
	return string(r[:n])
}

// modelSegment is the provider mark and the model name. maxW caps the whole
// segment (0 means the default cap); a cut lands on a word boundary.
func (m *Model) modelSegment(t *tab, maxW int) string {
	th := m.th
	prov := providerOf(t.model)
	model := modelOf(t.model)
	if model == "" {
		model = "no model"
	}
	limit := 28
	if prov == "" {
		limit = 40
	}
	if maxW > 0 {
		limit = maxW - ansi.StringWidth(icons.ProviderIcon(prov)) - 1
	}
	return fg(th.BrandAlt).Render(icons.ProviderIcon(prov)) + " " + fg(th.TextPrimary).Render(truncWord(model, limit))
}

// truncWord shortens s to n cells. It cuts back to the last separator so a
// name is never shown half a word, and marks the cut with an ellipsis.
func truncWord(s string, n int) string {
	out, _ := cutWord(s, n)
	return out
}

// cutWord is truncWord that also reports whether the cut landed on a word
// boundary. It is false only when no boundary fits, so the text had to be
// cut mid-word.
func cutWord(s string, n int) (string, bool) {
	if n <= 0 || ansi.StringWidth(s) <= n {
		return s, true
	}
	if n == 1 {
		return "…", false
	}
	rs := []rune(s)
	cut, fits := 0, 0
	for i := range rs {
		if ansi.StringWidth(string(rs[:i+1]))+1 > n {
			break
		}
		fits = i + 1
		if i > 0 && strings.ContainsRune("-_/.+,;:() ", rs[i]) {
			cut = i
		}
	}
	if cut > 0 {
		return string(rs[:cut]) + "…", true
	}
	return string(rs[:fits]) + "…", false
}

func (m *Model) branchSegment(t *tab) string {
	th := m.th
	limit := 15
	if m.width >= 120 {
		limit = 30
	}
	name := render.Truncate(t.git.Branch, limit)
	if t.git.Dirty {
		return fg(th.Amber).Render(icons.Icon("git") + " " + name + "*")
	}
	return fg(th.Success).Render(icons.Icon("git") + " " + name)
}

// contextParts returns the context bar ("● [▰▰▰▱▱▱▱▱] 42%") and the numbers
// ("84k/200k"), separately so the header can drop the numbers first.
func (m *Model) contextParts(t *tab) (string, string) {
	th := m.th
	pct := int(t.ctxShown + 0.5)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	col := th.Success
	switch {
	case pct >= 85:
		col = th.Error
	case pct >= 50:
		col = th.Warning
	}
	filledCol := col
	if pct > 80 {
		filledCol = th.Error
	}
	filled := pct * 8 / 100
	if pct*8%100 >= 50 {
		filled++
	}
	bar := fg(th.Success).Render("●") + " " +
		fg(th.TextFaint).Render("[") +
		fg(filledCol).Render(strings.Repeat("▰", filled)) +
		fg(th.TextSubtle).Render(strings.Repeat("▱", 8-filled)) +
		fg(th.TextFaint).Render("]") + " " +
		fg(col).Render(itoa(pct)+"%")
	nums := fg(col).Render(render.HumanTokens(t.ctxUsed) + "/" + render.HumanTokens(t.ctxWin))
	return bar, nums
}

// costSegment shows "Local", "$0.123" or "$1.23".
func (m *Model) costSegment(t *tab) string {
	th := m.th
	if t.local {
		return fg(th.Success).Render("Local")
	}
	if t.cost <= 0 {
		return fg(th.TextFaint).Render("$0")
	}
	if t.cost < 1 {
		return fg(th.TextSecondary).Render(formatUSD(t.cost, 3))
	}
	return fg(th.TextSecondary).Render(formatUSD(t.cost, 2))
}

func formatUSD(v float64, places int) string {
	pow := 1.0
	for i := 0; i < places; i++ {
		pow *= 10
	}
	n := int64(v*pow + 0.5)
	whole := n / int64(pow)
	frac := n % int64(pow)
	fs := itoa(int(frac))
	for len(fs) < places {
		fs = "0" + fs
	}
	return "$" + itoa(int(whole)) + "." + fs
}

// ---------------------------------------------------------------- tab bar

// viewTabBar draws the tabs, or the checkpoint legend when there is one tab.
func (m *Model) viewTabBar() string {
	th := m.th
	if len(m.tabs) <= 1 {
		return m.checkpointLegend()
	}
	var parts []string
	for i, t := range m.tabs {
		active := i == m.active
		var b strings.Builder
		b.WriteString(itoa(i + 1))
		seg := fg(th.TextMuted).Render("[" + itoa(i+1) + "]")
		if t.mode != "default" {
			mi := modeStyle(t.mode, th)
			mc := mi.color
			if !active {
				seg += " " + fg(mc).Faint(true).Render("["+mi.label+"]")
			} else {
				seg += " " + fg(mc).Render("["+mi.label+"]")
			}
		}
		if n := len(changedFiles(t.chat)); n > 0 {
			seg += " " + fg(th.Success).Render("[✎ "+itoa(n)+"]")
		}
		label := render.Truncate(t.name, 20)
		if t.pui != nil || len(t.prompts) > 0 {
			seg = fg(th.Warning).Render("? ") + seg
		} else if t.busy {
			seg = fg(th.Brand).Render(render.Frame(render.Braille, m.frame/3)+" ") + seg
		}
		seg += " " + fg(th.TextPrimary).Render(label)
		if t.unread {
			seg += " " + fg(th.Amber).Render("[●]")
		}
		if t.errFlag {
			seg += " " + fg(th.Error).Render("[✗]")
		}
		if active {
			seg = lipgloss.NewStyle().Background(th.BgPopup).Render(" " + seg + " ")
		} else {
			seg = " " + seg + " "
		}
		parts = append(parts, seg)
	}
	bar := strings.Join(parts, fg(th.TextFaint).Render("│"))
	return render.Truncate(bar, m.width)
}

// checkpointLegend is the one-row legend shown while browsing checkpoints
// (Ctrl+B / Ctrl+F). It takes no row when there is one checkpoint or when the
// user is not browsing.
func (m *Model) checkpointLegend() string {
	th := m.th
	t := m.cur()
	if len(t.cps) <= 1 || t.browse < 0 || t.browse >= len(t.cps) {
		return ""
	}
	cp := t.cps[t.browse]
	left := fg(th.Warning).Render("◀ viewing checkpoint #" + itoa(cp.Turn))
	right := fg(th.TextDim).Render("send a message to rewind here · ^F forward")
	return spaceBetween(left, right, m.width)
}

// ---------------------------------------------------------------- footer

type shortcut struct{ key, label string }

var footerShortcuts = []shortcut{
	{"^K", "Palette"}, {"^L", "Model"}, {"^D", "Mode"},
	{"^G", "Git"}, {"^S", "Skills"}, {"^P", "Sessions"},
	{"^N", "New"}, {"^T", "Tab"}, {"^C", "Quit"},
}

// tips are shown in the footer, rotating every few seconds.
var tips = []string{
	"? on an empty input opens the help screen",
	"/ lists every command; type to filter the palette with Ctrl+K",
	"Enter while Scribe works queues a steering message for the next step",
	"Shift+Enter or Ctrl+J adds a newline without sending",
	"Ctrl+R searches your prompt history",
	"!command runs a shell command in the project and shows its output",
	"@ followed by part of a path mentions a file",
	"Alt+S stashes the current draft, Alt+P brings it back",
	"Ctrl+B and Ctrl+F step through checkpoints; Ctrl+Z undoes the last turn",
	"Ctrl+O expands or collapses tool output and reasoning",
	"Alt+R opens the error log",
	"Shift+Tab or Ctrl+D cycles the mode: default, auto, plan, architect, socratic, challenge",
	"plan mode writes a plan first and waits for you to approve it",
	"/checkpoints lists the turns that changed files",
	"/diff shows what a turn changed; /diff 3 picks turn 3",
	"/compact shortens the conversation when the context bar fills up",
	"/model switches models; press k in the picker to add a key",
	"/theme previews each theme as you move through the list",
	"/sessions and Ctrl+P bring back an earlier session",
	"/git opens the git menu: status, diff, commit",
	"Ctrl+Z undoes the last turn and restores the files it changed",
	"/export writes the session as markdown or JSON",
	"Ctrl+T opens a new tab; up to five run at once, each with its own session",
	"/reasoning off hides the model's thinking; /reasoning high asks for more",
	"Mouse wheel and PgUp/PgDn scroll the transcript; End jumps to the bottom",
	"/copy puts the last answer on the clipboard via OSC 52",
}

// fitTip picks a tip for a w-cell footer, starting the search at index start.
// It prefers a tip that fits whole, then one that can be cut at a word
// boundary, and cuts the first tip mid-word only when nothing else works.
func fitTip(start, w int) string {
	for i := 0; i < len(tips); i++ {
		t := tips[(start+i)%len(tips)]
		if ansi.StringWidth(t) <= w {
			return t
		}
	}
	for i := 0; i < len(tips); i++ {
		if out, ok := cutWord(tips[(start+i)%len(tips)], w); ok {
			return out
		}
	}
	out, _ := cutWord(tips[start%len(tips)], w)
	return out
}

// viewFooter is the bottom row: shortcuts that fit the width, and a rotating tip.
func (m *Model) viewFooter() string {
	th := m.th
	var keys []string
	for _, s := range footerShortcuts {
		keys = append(keys, fgb(th.TextMuted).Render(s.key)+" "+fg(th.TextDim).Render(s.label))
	}
	// take as many shortcuts as fit, leaving room for a tip
	left := ""
	for n := len(keys); n >= 3; n-- {
		cand := strings.Join(keys[:n], "  ")
		if ansi.StringWidth(cand) <= m.width*2/3 {
			left = cand
			break
		}
	}
	if left == "" {
		left = strings.Join(keys[:3], "  ")
	}
	room := m.width - ansi.StringWidth(left) - 4
	tip := ""
	if room > 12 {
		tip = fg(th.Brand).Render("✦ ") + fg(th.TextSecondary).Render(fitTip(m.frame/(12*20), room-2))
	}
	return spaceBetween(" "+left, tip+" ", m.width)
}
