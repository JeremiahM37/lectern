// Package tui is Scribe's full-screen terminal interface (bubbletea v1).
//
// The Model owns up to MaxTabs tabs. Each tab has its own Backend, transcript
// (chat.View), input box and prompt queue. Everything the Model knows about
// Scribe comes through Backend; the agent's Event stream and the Prompt
// stream are pumped into bubbletea messages one at a time, one outstanding
// read per tab.
package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/chat"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// MaxTabs is the most tabs a Model will open.
const MaxTabs = 5

// Options configures Run and NewModel.
type Options struct {
	NoAltScreen bool
	Theme       string // theme name; "" uses the backend's theme
	Width       int    // tests: initial terminal width (0 waits for WindowSizeMsg)
	Height      int    // tests: initial terminal height
}

// Run starts the full-screen UI. newBackend makes the first tab's Backend and
// every later tab's Backend.
func Run(ctx context.Context, newBackend func() (Backend, error), opts Options) error {
	be, err := newBackend()
	if err != nil {
		return err
	}
	icons.SetNerd(icons.DetectNerd())
	m := NewModel(be, newBackend, opts)
	popts := []tea.ProgramOption{tea.WithMouseCellMotion(), tea.WithContext(ctx)}
	if !opts.NoAltScreen {
		popts = append(popts, tea.WithAltScreen())
	}
	final, err := tea.NewProgram(m, popts...).Run()
	if fm, ok := final.(*Model); ok && fm.resumeLine != "" {
		fmt.Fprintln(os.Stdout, fm.resumeLine)
	}
	return err
}

// phase is the top-level screen.
type phase int

const (
	phaseLanding phase = iota
	phaseTransition
	phaseChat
	phaseShutdown
)

// tab is one conversation: a Backend, its transcript and input, and its
// per-tab flags.
type tab struct {
	id    int
	name  string
	be    Backend
	chat  transcript
	copt  chat.Options
	in    *inputState
	mode  string
	busy  bool
	since time.Time // turn start

	unread  bool
	errFlag bool

	prompts []Prompt // waiting approvals and questions, oldest first
	pui     *promptUI

	git      GitStatus
	ctxShown float64
	ctxUsed  int
	ctxWin   int
	cost     float64
	local    bool // provider runs locally: show "Local" instead of cost
	model    string

	steerQ []string // steering messages sent but not yet consumed
	browse int      // checkpoint browsing: index into cps, -1 = live
	cps    []CheckpointInfo
}

// errEntry is one row of the error log (Alt+R).
type errEntry struct {
	At   time.Time
	Tab  string
	Text string
}

// Messages.
type (
	tickMsg  time.Time
	eventMsg struct {
		tab int
		ev  agent.Event
	}
	promptMsg struct {
		tab int
		p   Prompt
	}
	gitMsg struct {
		tab int
		st  GitStatus
	}
	// backendDoneMsg reports a command that finished with a status line.
	flashMsg struct {
		kind, text string
	}
	closedMsg  struct{}
	turnEndMsg struct{ tab int }
)

// Model is the root bubbletea model.
type Model struct {
	newBackend func() (Backend, error)
	opts       Options
	themeName  string
	th         theme.Theme
	width      int
	height     int
	version    string

	tabs   []*tab
	active int
	nextID int

	modals []Modal
	phase  phase
	trans  time.Time // transition start
	land   landingState
	sd     *shutdownState

	frame      int // animation counter, one step per tick
	quitAt     time.Time
	errLog     []errEntry
	flash      string
	flashKind  string
	flashTill  time.Time
	titleNow   string
	resumeLine string

	// stash of the last tab removed by Ctrl+W, so a close can be undone? (not used)
	tabBellRung bool
}

// NewModel builds a Model around first. newBackend creates Backends for new
// tabs; nil disables new tabs. Tests use it with a FakeBackend.
func NewModel(first Backend, newBackend func() (Backend, error), opts Options) *Model {
	m := &Model{newBackend: newBackend, opts: opts, width: opts.Width, height: opts.Height}
	m.themeName = opts.Theme
	if m.themeName == "" {
		m.themeName = first.Theme()
	}
	if m.themeName == "" {
		m.themeName = theme.ScribeID
	}
	m.th = theme.Resolve(m.themeName)
	m.version = first.Version()
	m.land = newLandingState()
	m.addTab(first, "")
	if len(first.Transcript()) > 0 {
		m.phase = phaseChat
	}
	m.land.start = time.Now()
	if m.width == 0 {
		m.width = 100
	}
	if m.height == 0 {
		m.height = 30
	}
	m.relayout()
	return m
}

// addTab creates a tab around be, replays its transcript and returns it.
func (m *Model) addTab(be Backend, name string) *tab {
	m.nextID++
	id := m.nextID
	if name == "" {
		name = fmt.Sprintf("TAB-%d", id)
	}
	cfg := be.Config()
	copt := chat.Options{
		Theme:         m.th,
		Verbose:       boolCfg(cfg, "verbose"),
		ShowReasoning: boolCfgDefault(cfg, "show_reasoning", true),
		Workdir:       be.Workdir(),
	}
	t := &tab{
		id:     id,
		name:   name,
		be:     be,
		mode:   be.Mode(),
		in:     newInputState(),
		browse: -1,
		model:  be.Model(),
		copt:   copt,
		chat:   newTranscript(copt),
	}
	t.in.history = historyFromTranscript(be.Transcript())
	replayEvents(t.chat, be.Transcript())
	t.git = be.Git()
	t.local = isLocalModel(t.model)
	m.tabs = append(m.tabs, t)
	m.active = len(m.tabs) - 1
	return t
}

func boolCfg(cfg map[string]any, key string) bool {
	v, _ := cfg[key].(bool)
	return v
}

func boolCfgDefault(cfg map[string]any, key string, def bool) bool {
	v, ok := cfg[key].(bool)
	if !ok {
		return def
	}
	return v
}

// cur returns the active tab.
func (m *Model) cur() *tab { return m.tabs[m.active] }

func (m *Model) tabByID(id int) *tab {
	for _, t := range m.tabs {
		if t.id == id {
			return t
		}
	}
	return nil
}

// setFlash shows a one-line status in the footer for a few seconds.
func (m *Model) setFlash(kind, text string) {
	m.flash = text
	m.flashKind = kind
	m.flashTill = time.Now().Add(2500 * time.Millisecond)
}

// logError records an error for the error log and marks the tab.
func (m *Model) logError(t *tab, text string) {
	m.errLog = append(m.errLog, errEntry{At: time.Now(), Tab: t.name, Text: text})
	if t != m.cur() {
		t.errFlag = true
	}
}

// Init starts the tick, the event and prompt pumps and the git poll.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tickCmd()}
	for _, t := range m.tabs {
		cmds = append(cmds, waitEvent(t.id, t.be), waitPrompt(t.id, t.be), gitCmd(t.id, t.be))
	}
	return tea.Batch(cmds...)
}

func tickCmd() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func waitEvent(id int, be Backend) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-be.Events()
		if !ok {
			return nil
		}
		return eventMsg{tab: id, ev: ev}
	}
}

func waitPrompt(id int, be Backend) tea.Cmd {
	return func() tea.Msg {
		p, ok := <-be.Prompts()
		if !ok {
			return nil
		}
		return promptMsg{tab: id, p: p}
	}
}

func gitCmd(id int, be Backend) tea.Cmd {
	return func() tea.Msg { return gitMsg{tab: id, st: be.Git()} }
}

// ringBell writes the terminal bell.
func ringBell() tea.Cmd {
	return func() tea.Msg {
		fmt.Fprint(os.Stdout, "\a")
		return nil
	}
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	m.relayout()
	return m, cmd
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return nil
	case tickMsg:
		return m.onTick(time.Time(msg))
	case eventMsg:
		return m.onEvent(msg)
	case promptMsg:
		return m.onPrompt(msg)
	case gitMsg:
		if t := m.tabByID(msg.tab); t != nil {
			t.git = msg.st
		}
		return nil
	case flashMsg:
		m.setFlash(msg.kind, msg.text)
		return nil
	case commitSuggestedMsg:
		m.openCommitPrompt(msg.text)
		return nil
	case shellMsg:
		if t := m.tabByID(msg.tab); t != nil {
			t.chat.AddSystem("shell", msg.text)
		}
		return nil
	case closedMsg:
		return m.finishShutdown()
	case tea.MouseMsg:
		if m.topModal() != nil || m.phase == phaseShutdown {
			return nil
		}
		t := m.cur()
		if m.phase == phaseLanding {
			return nil
		}
		t.chat.HandleMouse(msg)
		return nil
	case tea.KeyMsg:
		return m.onKey(msg)
	}
	// anything else belongs to the open modal (async results, etc.)
	return m.updateModal(msg)
}

func (m *Model) onKey(km tea.KeyMsg) tea.Cmd {
	if m.phase == phaseShutdown {
		return nil
	}
	if km.String() == "ctrl+c" {
		return m.handleCtrlC()
	}
	if m.topModal() != nil {
		return m.updateModal(km)
	}
	t := m.cur()
	if t.pui != nil {
		if cmd, handled := m.handlePromptKey(km); handled {
			return cmd
		}
	}
	if cmd, handled := m.handleGlobalKey(km); handled {
		return cmd
	}
	return m.handleInputKey(km)
}

func (m *Model) onTick(now time.Time) tea.Cmd {
	m.frame++
	var cmds []tea.Cmd
	cmds = append(cmds, tickCmd())
	// ease the context bar of every tab
	for _, t := range m.tabs {
		target := 0.0
		if t.ctxWin > 0 {
			target = float64(t.ctxUsed) * 100 / float64(t.ctxWin)
		}
		shownPct := t.ctxShown
		gap := target - shownPct
		if gap > -1 && gap < 1 {
			t.ctxShown = target
		} else {
			t.ctxShown += gap * 0.35
		}
		t.chat.Tick(now)
	}
	if m.frame%40 == 0 {
		cmds = append(cmds, gitCmd(m.cur().id, m.cur().be))
	}
	if m.phase == phaseTransition && time.Since(m.trans) >= 600*time.Millisecond {
		m.phase = phaseChat
	}
	if m.phase == phaseShutdown && m.sd != nil {
		if c := m.tickShutdown(now); c != nil {
			cmds = append(cmds, c)
		}
	}
	if !m.flashTill.IsZero() && now.After(m.flashTill) {
		m.flash = ""
		m.flashTill = time.Time{}
	}
	cmds = append(cmds, m.syncTitle())
	return tea.Batch(cmds...)
}

// syncTitle returns a command that sets the window title when it changed.
func (m *Model) syncTitle() tea.Cmd {
	want := "✒ Scribe"
	if title := m.cur().be.SessionTitle(); title != "" {
		want += " — " + title
	}
	needs := false
	for _, t := range m.tabs {
		if len(t.prompts) > 0 || t.pui != nil {
			needs = true
		}
	}
	if needs {
		want = "✒ Scribe — needs you"
	} else if m.anyBusy() {
		want = render.Frame(render.Braille, m.frame/3) + " " + want
	}
	if want == m.titleNow {
		return nil
	}
	m.titleNow = want
	return tea.SetWindowTitle(want)
}

func (m *Model) anyBusy() bool {
	for _, t := range m.tabs {
		if t.busy {
			return true
		}
	}
	return false
}

func (m *Model) onEvent(msg eventMsg) tea.Cmd {
	t := m.tabByID(msg.tab)
	if t == nil {
		return nil
	}
	next := waitEvent(t.id, t.be)
	ev := msg.ev
	switch ev.Kind {
	case agent.EvTurnStart:
		t.busy = true
		t.since = ev.At
		if ev.Model != "" {
			t.model = ev.Model
			t.local = isLocalModel(ev.Model)
		}
	case agent.EvSteering:
		if len(t.steerQ) > 0 {
			t.steerQ = t.steerQ[1:]
		}
		t.chat.AddUser(ev.Text, 0, ev.At, true)
		return next
	case agent.EvContext:
		t.ctxUsed, t.ctxWin = ev.ContextUsed, ev.ContextWindow
	case agent.EvStepFinish:
		t.cost = ev.CostUSD
		if ev.Total.InputTokens+ev.Total.OutputTokens > 0 {
			t.cost = ev.CostUSD
		}
	case agent.EvError:
		m.logError(t, ev.Text)
	case agent.EvTurnDone:
		t.busy = false
		t.cps = t.be.Checkpoints()
		t.cost = costOf(ev, t.cost)
		if t != m.cur() {
			t.unread = true
		}
		if ev.Stats != nil && ev.Stats.Error != "" {
			m.logError(t, ev.Stats.Error)
		}
		t.chat.Apply(ev)
		return tea.Batch(next, gitCmd(t.id, t.be))
	}
	t.chat.Apply(ev)
	return next
}

// costOf prefers the cumulative cost on the turn-done stats.
func costOf(ev agent.Event, fallback float64) float64 {
	if ev.CostUSD > 0 {
		return ev.CostUSD
	}
	if ev.Stats != nil && ev.Stats.CostUSD > 0 {
		return ev.Stats.CostUSD
	}
	return fallback
}

func (m *Model) onPrompt(msg promptMsg) tea.Cmd {
	t := m.tabByID(msg.tab)
	if t == nil {
		return nil
	}
	t.prompts = append(t.prompts, msg.p)
	m.enterChat()
	var cmds []tea.Cmd
	cmds = append(cmds, waitPrompt(t.id, t.be), ringBell())
	if t.pui == nil {
		m.openNextPrompt(t)
	}
	if t != m.cur() {
		t.unread = true
	}
	return tea.Batch(cmds...)
}

// enterChat leaves the landing page for the transcript (once).
func (m *Model) enterChat() {
	if m.phase == phaseLanding {
		m.phase = phaseTransition
		m.trans = time.Now()
	}
}

// openNextPrompt makes the oldest queued prompt active on t.
func (m *Model) openNextPrompt(t *tab) {
	if len(t.prompts) == 0 || t.pui != nil {
		return
	}
	p := t.prompts[0]
	t.prompts = t.prompts[1:]
	t.pui = newPromptUI(p, m.th, m.width)
}

// handleCtrlC clears the input, else stops a running turn, else arms quit.
func (m *Model) handleCtrlC() tea.Cmd {
	if m.topModal() != nil {
		m.closeModal(m.topModal())
		return nil
	}
	t := m.cur()
	if t.pui != nil {
		return m.resolvePrompt(t, denyFor(t.pui))
	}
	if !t.in.empty() {
		t.in.clear()
		return nil
	}
	if t.busy {
		t.be.Abort()
		m.setFlash("info", "Stopping…")
		return nil
	}
	if time.Since(m.quitAt) < 2*time.Second {
		return m.beginShutdown()
	}
	m.quitAt = time.Now()
	m.setFlash("info", "Press Ctrl+C again within 2s to quit")
	return nil
}

// ---------------------------------------------------------------- layout

// bottomHeight is the number of rows below the transcript.
func (m *Model) bottomHeight() int {
	t := m.cur()
	h := 0
	if t.busy {
		h++
	}
	h += len(m.steerLines(t))
	if n := len(changedFiles(t.chat)); n > 0 && m.phase != phaseLanding {
		h++
	}
	if t.pui != nil {
		h += len(m.promptLines(t))
	} else if m.phase != phaseLanding {
		h += len(t.in.popupLines(m, m.width)) + t.in.inputHeight() + 2
	}
	if m.phase != phaseLanding && m.phase != phaseShutdown && m.flash != "" {
		// flash shares the busy row when idle; keep it as its own row otherwise
		if !t.busy {
			h++
		}
	}
	return h
}

// chatRows is the height left for the transcript.
func (m *Model) chatRows() int {
	t := m.cur()
	rows := m.height - 1 - m.tabBarRows() - 1 - m.bottomHeight()
	if m.phase == phaseLanding || m.phase == phaseTransition {
		rows = m.height - 2 - m.tabBarRows()
	}
	_ = t
	return max(rows, 1)
}

// tabBarRows is 1 when the tab bar or the checkpoint legend is shown.
func (m *Model) tabBarRows() int {
	if len(m.tabs) > 1 {
		return 1
	}
	if m.cur().browse >= 0 || len(m.cur().cps) > 1 {
		return 1
	}
	return 0
}

// relayout keeps the transcript the right size.
func (m *Model) relayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	rows := m.chatRows()
	for _, t := range m.tabs {
		t.chat.SetSize(m.width, rows)
		t.in.resize(m.inputWidth())
	}
}

// inputWidth is the textarea width: the box is two border and two padding columns wider.
func (m *Model) inputWidth() int {
	w := m.width
	if m.phase == phaseLanding {
		w = m.width * 60 / 100
	}
	return max(w-4, 10)
}

// ---------------------------------------------------------------- View

// View implements tea.Model.
func (m *Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	if m.phase == phaseShutdown {
		return m.viewShutdown()
	}
	var lines []string
	lines = append(lines, m.viewHeader())
	if m.tabBarRows() == 1 {
		lines = append(lines, m.viewTabBar())
	}
	rows := m.chatRows()
	switch m.phase {
	case phaseLanding:
		lines = append(lines, m.viewLanding(m.width, rows)...)
	case phaseTransition:
		p := clampF(float64(time.Since(m.trans))/float64(600*time.Millisecond), 0, 1)
		if p < 0.5 {
			lines = append(lines, m.viewLanding(m.width, rows)...)
		} else {
			lines = append(lines, m.viewChatBody(rows)...)
		}
	default:
		lines = append(lines, m.viewChatBody(rows)...)
	}
	lines = append(lines, m.viewFooter())
	base := fitLines(lines, m.width, m.height)
	if md := m.topModal(); md != nil {
		box := modalFrame(md, m.th, m.width, m.height)
		base = overlay(base, box, m.width, m.height)
	}
	return strings.Join(base, "\n")
}

// viewChatBody is the transcript plus the bottom panel.
func (m *Model) viewChatBody(rows int) []string {
	t := m.cur()
	var out []string
	if m.phase != phaseLanding {
		// the transcript fills what the bottom panel leaves
		out = append(out, fitLines(strings.Split(t.chat.View(), "\n"), m.width, rows)...)
	}
	out = append(out, m.viewBottom()...)
	return out
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// viewBottom is everything under the transcript: busy line, steering queue,
// changed files, the prompt or the input box with its popup.
func (m *Model) viewBottom() []string {
	t := m.cur()
	var out []string
	if t.busy {
		out = append(out, m.busyLine(t))
	} else if m.flash != "" && m.phase != phaseLanding {
		out = append(out, m.flashLine())
	}
	out = append(out, m.steerLines(t)...)
	if n := len(changedFiles(t.chat)); n > 0 {
		out = append(out, m.changedBar(t))
	}
	if t.pui != nil {
		out = append(out, m.promptLines(t)...)
		return out
	}
	if m.phase == phaseLanding {
		return out
	}
	out = append(out, t.in.popupLines(m, m.width)...)
	out = append(out, m.boxLines(t, m.width, m.modeColor(t))...)
	return out
}

func (m *Model) flashLine() string {
	col := m.th.Info
	ic := "ⓘ"
	switch m.flashKind {
	case "ok":
		col, ic = m.th.Success, "✓"
	case "err":
		col, ic = m.th.Error, "✗"
	}
	return fg(col).Render(ic+" "+m.flash) + ""
}

// busyLine shows the spinner, the elapsed time and the stop hint.
func (m *Model) busyLine(t *tab) string {
	spin := render.Frame(render.Rune, m.frame/3)
	elapsed := ""
	if !t.since.IsZero() {
		elapsed = render.Elapsed(time.Since(t.since))
	}
	left := fgb(m.th.Brand).Render(spin) + " " + fg(m.th.TextMuted).Render("Working") + " " + fg(m.th.TextDim).Render(elapsed)
	right := fgb(m.th.Error).Render("■ Esc to stop")
	if m.flash != "" {
		left += "  " + fg(m.th.TextDim).Render(m.flash)
	}
	return spaceBetween(left, right, m.width)
}

// steerLines shows queued steering messages above the input.
func (m *Model) steerLines(t *tab) []string {
	var out []string
	for i, s := range t.steerQ {
		if i >= 1 {
			break
		}
		line := fg(m.th.Warning).Render("│ Steering: ") + fg(m.th.TextSecondary).Render(render.Truncate(oneLine(s), m.width-30))
		if n := len(t.steerQ); n > 1 {
			line += " " + fg(m.th.TextDim).Render("(+"+itoa(n-1)+" queued)")
		}
		out = append(out, line)
	}
	return out
}

// changedBar lists files touched this session: created, modified and the
// first few names, with a pointer to /changes.
func (m *Model) changedBar(t *tab) string {
	files := changedFiles(t.chat)
	var names []string
	for i, f := range files {
		if i < 3 {
			names = append(names, render.Truncate(baseName(f), 24))
		}
	}
	line := fg(m.th.Amber).Render(" ✎ ") + fg(m.th.TextSecondary).Render(itoa(len(files))+" changed") + " " +
		fg(m.th.TextFaint).Render("│ ") + fg(m.th.TextMuted).Render(strings.Join(names, ", "))
	if len(files) > 3 {
		line += fg(m.th.TextDim).Render(" +" + itoa(len(files)-3))
	}
	return render.Truncate(line, m.width)
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// oneLine collapses newlines so a message fits one row.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// modeColor is the accent for the active mode.
func (m *Model) modeColor(t *tab) lipgloss.TerminalColor {
	return modeStyle(t.mode, m.th).color
}

// isLocalModel reports whether a provider/model ref runs on this machine.
func isLocalModel(ref string) bool {
	p := ref
	if i := strings.Index(ref, "/"); i >= 0 {
		p = ref[:i]
	}
	return p == "ollama" || p == "lmstudio"
}

// historyFromTranscript seeds prompt history from the user turns of a resumed session.
func historyFromTranscript(evs []agent.Event) []string {
	var out []string
	for _, ev := range evs {
		if ev.Kind == agent.EvTurnStart && strings.TrimSpace(ev.Prompt) != "" {
			out = append(out, ev.Prompt)
		}
	}
	return out
}

// Compile-time checks that the palette helpers exist for the view code.
var _ = lipgloss.NewStyle
