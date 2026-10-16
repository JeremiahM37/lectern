package tui

import (
	"math/rand"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// modeInfo describes one mode.
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

// compactSpinner is the header's compacting notice, one step per tick.
var compactSpinner = render.Spinner{"◐", "◓", "◑", "◒"}

// viewHeader is the single top row. The left holds the identity; the middle
// holds model, branch, mode and context; the right holds the cost and the brand
// tag. When the row is too narrow, pieces leave in a fixed order and never
// mid-word: the brand tag first (under 100 columns), then the branch, then the
// context numbers (the bar stays), then the mode badge, and last the model
// name shortened at a word boundary. A compacting notice sits after the bar.
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

	var branch, mode, bar, nums, compact, stale string
	if t.git.Branch != "" {
		branch = m.branchSegment(t)
	}
	if len(m.tabs) <= 1 && t.mode != "default" {
		mi := modeStyle(t.mode, th)
		mode = fgb(mi.color).Render("[" + mi.label + "]")
	}
	bar, nums = m.contextParts(t)
	if t.stale > 0 {
		stale = fg(th.Warning).Render(staleHint(t.stale))
	}
	if t.compacting && len(m.tabs) <= 1 {
		compact = fg(th.Info).Render(render.Frame(compactSpinner, m.frame) + " compacting")
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
		if withNums && stale != "" {
			parts = append(parts, stale)
		}
		if compact != "" {
			parts = append(parts, compact)
		}
		return strings.Join(parts, sep)
	}
	fixed := ansi.StringWidth(left) + ansi.StringWidth(right) + 4
	modelFull := m.modelSegment(t, 0)
	for _, opt := range [][2]bool{{true, true}, {false, true}, {false, false}} {
		// fixed+mid+1: headerRow always leaves at least one cell of gap
		if mid := build(modelFull, opt[0], opt[1]); fixed+ansi.StringWidth(mid)+1 <= w {
			return m.headerRow(left, mid, right)
		}
	}
	// Still too wide: drop the mode badge, then shorten the model name.
	mode = ""
	room := w - fixed - 1 - ansi.StringWidth(build("", false, false))
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

// modelSegment is the provider mark, the provider name (when it differs from
// the model name) and the model name. maxW caps the whole segment (0 means the
// default cap); a cut lands on a word boundary.
func (m *Model) modelSegment(t *tab, maxW int) string {
	th := m.th
	prov := providerOf(t.model)
	model := modelOf(t.model)
	if model == "" {
		model = "no model"
	}
	head := fg(th.BrandAlt).Render(icons.ProviderIcon(prov)) + " "
	if prov != "" && prov != model {
		head += fg(th.TextMuted).Render(prov + "›")
	}
	limit := 28
	if prov == "" {
		limit = 40
	}
	if maxW > 0 {
		limit = maxW - ansi.StringWidth(head)
	}
	return head + fg(th.TextPrimary).Render(truncWord(model, limit))
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

// browsedCheckpoint is the checkpoint the user is browsing (Ctrl+B and Ctrl+F),
// or nil when the view is live.
func browsedCheckpoint(t *tab) *CheckpointInfo {
	if t.browse < 0 || t.browse >= len(t.cps) {
		return nil
	}
	return &t.cps[t.browse]
}

// staleHint is the memory hint on the context bar. It names the cleanup, so
// the user knows where to act.
func staleHint(n int) string {
	word := "memories"
	if n == 1 {
		word = "memory"
	}
	return "♻ " + itoa(n) + " stale " + word + " — /memory cleanup"
}

// contextParts returns the context bar ("● [▰▰▰▱▱▱▱▱] 42%") and the numbers
// ("84k/200k"), separately so the header can drop the numbers first. While a
// checkpoint is browsed it shows that checkpoint's fill instead:
// "◀ [▰▰▱▱▱▱▱▱]~N% ~used/window", or a note when the turn has none recorded.
func (m *Model) contextParts(t *tab) (string, string) {
	th := m.th
	if cp := browsedCheckpoint(t); cp != nil {
		if cp.CtxWindow <= 0 {
			return fg(th.Warning).Render("◀ ") + fg(th.TextFaint).Render("context not recorded for this turn"), ""
		}
		pct := cp.CtxUsed * 100 / cp.CtxWindow
		return browsedBar(th, pct), fg(th.TextFaint).Render("~" + render.HumanTokens(cp.CtxUsed) + "/" + render.HumanTokens(cp.CtxWindow))
	}
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
	// An estimate (the provider reported no usage yet) is marked with a
	// dim dot and a "~" before the percentage and the token count.
	dot, approx := th.Success, ""
	if t.ctxEst {
		dot, approx = th.TextDim, "~"
	}
	bar := fg(dot).Render("●") + " " +
		fg(th.TextFaint).Render("[") +
		fg(filledCol).Render(strings.Repeat("▰", filled)) +
		fg(th.TextSubtle).Render(strings.Repeat("▱", 8-filled)) +
		fg(th.TextFaint).Render("]") + " " +
		fg(col).Render(approx+itoa(pct)+"%")
	nums := fg(col).Render(approx + render.HumanTokens(t.ctxUsed) + "/" + render.HumanTokens(contextWindow(t)))
	return bar, nums
}

// defaultContextWindow is the window the context bar assumes until the
// backend reports one (spec 01 §9: "Window default 200,000").
const defaultContextWindow = 200000

// contextWindow is the window the live bar measures against.
func contextWindow(t *tab) int {
	if t.ctxWin > 0 {
		return t.ctxWin
	}
	return defaultContextWindow
}

// browsedBar draws the checkpoint's context bar with a back arrow. The filled
// cells take the warning colour: a browsed value is not the live one.
func browsedBar(th theme.Theme, pct int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := pct * 8 / 100
	if pct*8%100 >= 50 {
		filled++
	}
	return fg(th.Warning).Render("◀ ") +
		fg(th.TextFaint).Render("[") +
		fg(th.Warning).Render(strings.Repeat("▰", filled)) +
		fg(th.TextSubtle).Render(strings.Repeat("▱", 8-filled)) +
		fg(th.TextFaint).Render("]") + " " +
		fg(th.Warning).Render("~"+itoa(pct)+"%")
}

// costSegment shows "Local", "FREE", "$0", "$0.123" or "$1.23". FREE means
// tokens were used and the provider billed nothing. The figure turns success
// green once more than half the input came from the prompt cache.
func (m *Model) costSegment(t *tab) string {
	th := m.th
	if t.local {
		return fg(th.Success).Render("Local")
	}
	if t.cost <= 0 {
		if t.tokens > 0 {
			return fg(th.Success).Render("FREE")
		}
		return fg(th.TextFaint).Render("$0")
	}
	col := th.TextSecondary
	if t.cacheHit > 0.5 {
		col = th.Success
	}
	if t.cost < 1 {
		return fg(col).Render(formatUSD(t.cost, 3))
	}
	return fg(col).Render(formatUSD(t.cost, 2))
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

// viewTabBar draws the tabs.
func (m *Model) viewTabBar() string {
	th := m.th
	var parts []string
	for i, t := range m.tabs {
		active := i == m.active
		num := fg(th.TextMuted).Render("[" + itoa(i+1) + "]")
		if active {
			num = fg(th.BorderFocused).Render("[" + itoa(i+1) + "]")
		}
		seg := num
		if t.mode != "default" {
			mi := modeStyle(t.mode, th)
			mc := mi.color
			if !active {
				seg += " " + fg(mc).Faint(true).Render("["+mi.label+"]")
			} else {
				seg += " " + fg(mc).Render("["+mi.label+"]")
			}
		}
		if created, modified := t.changedNow(); len(created)+len(modified) > 0 {
			seg += " " + fg(th.Success).Render("[✎ "+itoa(len(created)+len(modified))+"]")
		}
		if t.defModel != "" && t.model != "" && t.model != t.defModel {
			seg += " " + fg(th.TextMuted).Render("["+render.Truncate(modelOf(t.model), 16)+"]")
		}
		label := render.Truncate(t.name, 20)
		if active && ansi.StringWidth(t.name) > 20 {
			// the active label scrolls so its whole name can be read;
			// inactive ones just truncate
			label = render.Marquee(t.name, 20, m.frame/2)
		}
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

// ---------------------------------------------------------------- footer

// shortcut is one footer key. tier 1 is always shown when there is room;
// tiers 2 and 3 are added as the width allows. icon is the icons table name,
// short the label used when the full labels do not fit.
type shortcut struct {
	key, label, short, icon string
	tier                    int
}

var footerShortcuts = []shortcut{
	{"^K", "Palette", "Pal", "lightning", 1},
	{"^L", "Model", "Mdl", "brain_alt", 1},
	{"^D", "Mode", "Mode", "cog", 1},
	{"^E", "Editor", "Edit", "editor", 2},
	{"^S", "Skills", "Skill", "skills", 2},
	{"^G", "Git", "Git", "git", 2},
	{"^N", "New", "New", "ghost", 3},
	{"^P", "Sessions", "Sess", "clock_alt", 3},
	{"^T", "Tab", "Tab", "tabs", 3},
	{"Alt+C", "Rail", "Rail", "", 3},
	{"^C", "Quit", "Quit", "quit", 3},
}

// footerLabelMode is how much of each shortcut's text the footer draws.
type footerLabelMode struct {
	name string
	gap  int
}

var footerModes = []footerLabelMode{{"full", 2}, {"short", 2}, {"none", 1}}

// footerKeys renders the shortcuts of one tier with one label mode. "none"
// keeps the key and its icon only.
func (m *Model) footerKeys(tier int, mode footerLabelMode) string {
	th := m.th
	var parts []string
	for _, s := range footerShortcuts {
		if s.tier > tier {
			continue
		}
		seg := fgb(th.TextMuted).Render(s.key)
		if s.icon != "" {
			seg += " " + fg(th.TextDim).Render(icons.Icon(s.icon))
		}
		switch mode.name {
		case "full":
			seg += " " + fg(th.TextDim).Render(s.label)
		case "short":
			seg += " " + fg(th.TextDim).Render(s.short)
		}
		parts = append(parts, seg)
	}
	return strings.Join(parts, strings.Repeat(" ", mode.gap))
}

// footerLeft picks the first shortcut set that leaves room for a tip, trying
// full labels before short before none, and within each label mode the
// highest tier first. Tier 1 with icons only is the fallback.
func (m *Model) footerLeft() string {
	room := m.width - 28
	for _, mode := range footerModes {
		for tier := 3; tier >= 1; tier-- {
			if cand := m.footerKeys(tier, mode); ansi.StringWidth(cand) <= room {
				return cand
			}
		}
	}
	return m.footerKeys(1, footerModes[2])
}

// tips are shown in the footer, one at a time, about every 10 seconds.
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
	"Alt+C opens the checkpoint rail: arrows move, Enter opens a checkpoint, Esc gives the keys back",
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

// tipTicks is one tip slot: 10 seconds at the 50 ms tick.
const tipTicks = 10 * 20

// tipOrder is the shuffled bag for one pass through the tips. Every pass uses
// a new permutation, so no tip repeats before the others have shown.
func tipOrder(pass int) []int {
	return rand.New(rand.NewSource(int64(pass)*7919 + 1)).Perm(len(tips))
}

// tipSlot returns the bag order and the position in it for the current frame.
func (m *Model) tipSlot() ([]int, int) {
	slot := m.frame / tipTicks
	return tipOrder(slot / len(tips)), slot % len(tips)
}

// fitTip picks a tip for a w-cell footer, searching from index start in the
// tips' own order.
func fitTip(start, w int) string {
	order := make([]int, len(tips))
	for i := range order {
		order[i] = i
	}
	return fitTipAt(order, start, w)
}

// fitTipAt picks the tip for a w-cell footer, starting at position pos of the
// bag order. It prefers a tip that fits whole, then one that can be cut at a
// word boundary, and cuts the first tip mid-word only when nothing else works.
func fitTipAt(order []int, pos, w int) string {
	n := len(order)
	for i := 0; i < n; i++ {
		t := tips[order[(pos+i)%n]]
		if ansi.StringWidth(t) <= w {
			return t
		}
	}
	for i := 0; i < n; i++ {
		if out, ok := cutWord(tips[order[(pos+i)%n]], w); ok {
			return out
		}
	}
	out, _ := cutWord(tips[order[pos%n]], w)
	return out
}

// tipGlitch is the garble pool for the footer's glitch-in.
var tipGlitch = []rune("░▒▓█▄▀▐▌┤├┼─│┌┐└┘╔╗╚╝")

// glitchText replaces every non-space rune with a block glyph. seed moves the
// pattern from frame to frame.
func glitchText(s string, seed int) string {
	rs := []rune(s)
	for i, r := range rs {
		if r != ' ' {
			rs[i] = tipGlitch[(seed*31+i*17)%len(tipGlitch)]
		}
	}
	return string(rs)
}

// isKeyWord reports whether a tip word names a key or a command. Those words
// are drawn in the brand colour, the rest of the tip in secondary text.
func isKeyWord(w string) bool {
	if len(w) < 2 {
		return false
	}
	for _, p := range []string{"Ctrl+", "Alt+", "Shift+", "PgUp", "PgDn", "Esc", "Enter", "^", "/", "!", "@"} {
		if strings.HasPrefix(w, p) {
			return true
		}
	}
	return w == "End"
}

// renderTip draws a tip with its key and command words highlighted.
func renderTip(s string, th theme.Theme) string {
	words := strings.Split(s, " ")
	out := make([]string, len(words))
	for i, w := range words {
		if isKeyWord(w) {
			out[i] = fg(th.Brand).Render(w)
		} else {
			out[i] = fg(th.TextSecondary).Render(w)
		}
	}
	return strings.Join(out, " ")
}

// viewFooter is the bottom row: the shortcut set the width allows, and one tip
// that changes every 10 seconds and glitches in as it appears.
func (m *Model) viewFooter() string {
	th := m.th
	left := m.footerLeft()
	room := m.width - ansi.StringWidth(left) - 4
	tip := ""
	if cp := browsedCheckpoint(m.cur()); cp != nil && room > 12 {
		// While a checkpoint is browsed, the footer names it and the keys that
		// leave it, in place of a tip the user cannot act on from here.
		tip = checkpointLegend(th, cp.Turn, room)
	} else if room > 12 {
		order, pos := m.tipSlot()
		text := fitTipAt(order, pos, room-2)
		if off := m.frame % tipTicks; off < 4 {
			tip = fg(th.Brand).Render("✦ ") + fg(th.TextFaint).Render(glitchText(text, off))
		} else {
			tip = fg(th.Brand).Render("✦ ") + renderTip(text, th)
		}
	}
	return spaceBetween(" "+left, tip+" ", m.width)
}

// checkpointLegend is the footer while a checkpoint is browsed: the turn and
// how to leave the browsing. It is cut to room cells, never wrapped.
func checkpointLegend(th theme.Theme, turn, room int) string {
	text := fgb(th.Warning).Render("◀ turn "+itoa(turn)) +
		fg(th.TextFaint).Render(" · Ctrl+F forward · /checkpoint live returns")
	return ansi.Truncate(text, room, "…")
}
