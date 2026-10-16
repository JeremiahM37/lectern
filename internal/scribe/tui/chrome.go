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

// viewHeader is the single top row: identity on the left, model, branch,
// mode, context and cost, and the brand tag at the far right.
func (m *Model) viewHeader() string {
	th := m.th
	t := m.cur()
	left := fgb(th.Brand).Render("✒ Scribe") + " " + fg(th.TextMuted).Render("v"+m.version)

	sep := fg(th.TextFaint).Render(" │ ")
	var mid []string
	mid = append(mid, m.modelSegment(t))
	if t.git.Branch != "" {
		mid = append(mid, m.branchSegment(t))
	}
	if len(m.tabs) <= 1 && t.mode != "default" {
		mi := modeStyle(t.mode, th)
		mid = append(mid, fgb(mi.color).Render("["+mi.label+"]"))
	}
	if t.ctxWin > 0 {
		mid = append(mid, m.contextBar(t))
	}
	if !t.busy && t.cost >= 0 {
		mid = append(mid, m.costSegment(t))
	}
	midStr := strings.Join(mid, sep)

	brand := ""
	if m.width >= 80 {
		brand = fg(th.TextMuted).Italic(true).Render(typewriter("in Lectern", m.frame))
	}
	room := m.width - ansi.StringWidth(left) - ansi.StringWidth(brand) - 4
	if room > 0 && ansi.StringWidth(midStr) > room {
		midStr = render.Truncate(midStr, room)
	}
	gap := m.width - ansi.StringWidth(left) - ansi.StringWidth(midStr) - ansi.StringWidth(brand) - 2
	if gap < 1 {
		gap = 1
	}
	return render.Truncate(" "+left+strings.Repeat(" ", gap-1)+midStr+" "+brand, m.width)
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

func (m *Model) modelSegment(t *tab) string {
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
	return fg(th.BrandAlt).Render(icons.ProviderIcon(prov)) + " " + fg(th.TextPrimary).Render(render.Truncate(model, limit))
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

// contextBar draws "● [▰▰▰▱▱▱▱▱] 42% 84k/200k" with the eased fill.
func (m *Model) contextBar(t *tab) string {
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
	bar := fg(th.TextFaint).Render("[") +
		fg(filledCol).Render(strings.Repeat("▰", filled)) +
		fg(th.TextSubtle).Render(strings.Repeat("▱", 8-filled)) +
		fg(th.TextFaint).Render("]")
	dot := fg(th.Success).Render("●")
	label := itoa(pct) + "% " + render.HumanTokens(t.ctxUsed) + "/" + render.HumanTokens(t.ctxWin)
	return dot + " " + bar + " " + fg(col).Render(label)
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

// checkpointLegend is the one-row legend shown when a single tab has checkpoints.
func (m *Model) checkpointLegend() string {
	th := m.th
	t := m.cur()
	if len(t.cps) <= 1 && t.browse < 0 {
		return ""
	}
	if t.browse >= 0 && t.browse < len(t.cps) {
		cp := t.cps[t.browse]
		return fg(th.Warning).Render("◀ viewing checkpoint #" + itoa(cp.Turn) + " — send a message to rewind to it, ^F to go forward")
	}
	left := fg(th.TextDim).Render("◆ latest │ ● viewing │ ● edits │ ○ read")
	right := fg(th.TextDim).Render("^B/^F navigate")
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
	tipIdx := (m.frame / (12 * 20)) % len(tips)
	tip := ""
	room := m.width - ansi.StringWidth(left) - 4
	if room > 12 {
		tip = fg(th.Brand).Render("✦ ") + fg(th.TextSecondary).Render(render.Truncate(tips[tipIdx], room-2))
	}
	return spaceBetween(" "+left, tip+" ", m.width)
}
