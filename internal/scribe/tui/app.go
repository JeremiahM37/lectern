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
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
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
	Pick        bool   // open the session picker on start (`lectern code --resume`)
	Prompt      string // send this prompt first (`lectern code "text"`)
	Wizard      bool   // open the setup wizard on start (`lectern code --wizard`)
}

// Run starts the full-screen UI. newBackend makes the first tab's Backend and
// every later tab's Backend.
func Run(ctx context.Context, newBackend func() (Backend, error), opts Options) error {
	be, err := newBackend()
	if err != nil {
		return err
	}
	applyEditorSettings(be.Workdir())
	icons.SetNerd(icons.DetectNerd())
	loadUserThemes()
	m := NewModel(be, newBackend, opts)
	// All-motion reporting, so the chat can highlight the row under the pointer
	// without a button held. Motion over panes and side panels is routed away.
	popts := []tea.ProgramOption{tea.WithMouseAllMotion(), tea.WithContext(ctx)}
	if !opts.NoAltScreen {
		popts = append(popts, tea.WithAltScreen())
	}
	final, err := tea.NewProgram(m, popts...).Run()
	if fm, ok := final.(*Model); ok {
		// The caller prints SavedBanner for each session once the terminal is
		// back, so the quit screen's resume hint is not repeated here.
		fm.closePanes()
	}
	if err == nil {
		// /restart asked for a fresh process; it replaces this one on success.
		err = execPendingRestart()
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
	// costShown is the cost the header draws; it eases toward cost (spec
	// 01 §9) so the figure counts up instead of jumping.
	costShown float64
	// turnErrLogged is set when a turn's error reached the error log as it
	// happened, so the same error in the turn's final stats is not logged
	// a second time.
	turnErrLogged bool
	ctxUsed       int
	ctxWin        int
	ctxEst        bool       // ctxUsed is estimated from characters, not reported by the provider
	panels        panelState // model-log recording for this tab's session (not saved)
	cost          float64
	local         bool // provider runs locally: show "Local" instead of cost
	model         string

	lastPhrase string   // last readable busy-line word, shown when a frame is garbled
	steerQ     []string // steering messages sent but not yet consumed
	browse     int      // checkpoint browsing: index into cps, -1 = live
	cps        []CheckpointInfo

	defModel   string  // the model the tab opened with; another one gets a badge
	compacting bool    // a compaction is running
	tokens     int     // input and output tokens used so far
	cacheHit   float64 // share of input tokens served from the prompt cache
	stale      int     // memories the cleanup would review (context bar hint)
}

// errEntry is one row of the error log (Alt+R). Source says where the fault
// came from (see errlog8.go); Count is how many times it repeated.
type errEntry struct {
	At     time.Time
	Tab    string
	Source string
	Text   string
	Count  int
	Args   string // a failed tool call's arguments as JSON, when known
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
		chat       bool // a command error: shown in the chat as "Command error: ..."
		alert      bool // a failure the backend reported: also opens the alert
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

	frame      int       // animation counter, one step per frameGap
	frameAt    time.Time // when frame last stepped
	wheelAt    time.Time // last mouse wheel event, for scroll acceleration
	wheelRun   int       // wheel events in a row, each close to the last
	themesSeen time.Time // newest mtime of the user theme files, for hot reload
	themesAt   time.Time // last time the theme files were checked
	quitAt     time.Time
	// ctrlCs counts Ctrl+C presses less than ctrlCBurst apart; the third
	// always quits, whatever is open (see handleCtrlC).
	ctrlCs     int
	ctrlCAt    time.Time
	errLog     []errEntry
	lastBG     time.Time // last background-fault poll
	lastStale  time.Time // last stale-memory refresh
	flash      string
	flashKind  string
	flashTill  time.Time
	titleNow   string
	resumeLine string

	// stash of the last tab removed by Ctrl+W, so a close can be undone? (not used)
	tabBellRung bool

	panes *paneState // editor and floating terminals (termpane.go)
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
	m.th = startupStyle(theme.Resolve(m.themeName), first)
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
	// The saved diff style and collapse setting apply from the first frame,
	// not only after they are changed in /settings.
	if f := fields7(be); f.DiffStyle != "" || f.CollapseDiffs != nil {
		copt.DiffStyle = chat.DiffDefault
		if f.DiffStyle != "" {
			copt.DiffStyle = f.DiffStyle
		}
		copt.CollapseDiffs = config.Bool(f.CollapseDiffs, false)
	}
	t := &tab{
		id:       id,
		name:     name,
		be:       be,
		mode:     be.Mode(),
		in:       newInputState(),
		browse:   -1,
		model:    be.Model(),
		defModel: be.Model(),
		copt:     copt,
		chat:     newTranscript(copt),
	}
	t.in.history = historyFromTranscript(be.Transcript())
	t.in.mountStash(be.Workdir())
	if hl, ok := be.(HistoryLoader); ok && len(t.in.history) == 0 {
		t.in.history = hl.History()
	}
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
	m.flashTill = time.Now().Add(flashDuration(text))
}

// flashDuration gives a flash time to be read: 3.5 s, plus a little per word
// past the first few, up to 6 s.
func flashDuration(text string) time.Duration {
	d := 3500*time.Millisecond + time.Duration(max(0, len(strings.Fields(text))-4))*250*time.Millisecond
	return min(d, 6*time.Second)
}

// logError records an error for the error log and marks the tab. The source
// is Scribe itself; turn, tool and compaction faults use logErrorFrom.
func (m *Model) logError(t *tab, text string) {
	m.logErrorFrom(t, srcScribe, text)
}

// Init starts the tick, the event and prompt pumps and the git poll.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tickCmd()}
	for _, t := range m.tabs {
		cmds = append(cmds, waitEvent(t.id, t.be), waitPrompt(t.id, t.be), gitCmd(t.id, t.be))
	}
	if m.opts.Wizard {
		cmds = append(cmds, m.openWizard())
	} else {
		cmds = append(cmds, m.maybeOpenWizard())
	}
	if m.opts.Pick {
		m.openSessionPicker()
	}
	// A setup gate (git missing) holds the prompt: its turn would fail.
	if p := strings.TrimSpace(m.opts.Prompt); p != "" && m.topModal() == nil {
		m.phase = phaseChat
		cmds = append(cmds, m.startTurn(m.cur(), p))
	}
	return tea.Batch(cmds...)
}

// frameGap is one animation step. The tick runs at 50 ms, and at 16 ms while
// the landing page hands over to the chat, but frame (and everything that
// steps with it) only moves once per frameGap, so the spinners keep their
// speed during the transition.
const frameGap = 45 * time.Millisecond

func tickCmd() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func fastTickCmd() tea.Cmd {
	return tea.Tick(16*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
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
// kittyWriteCmd writes Kitty graphics escapes to the terminal, beside the
// frame, the way ringBell writes the bell.
func kittyWriteCmd(esc string) tea.Cmd {
	return func() tea.Msg {
		fmt.Fprint(os.Stdout, esc)
		return nil
	}
}

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
		// A command's error always shows in the flash line, where the user is
		// looking. A real failure is also kept in the chat; a usage hint is not,
		// so a typo does not clutter the transcript.
		if msg.chat && !strings.HasPrefix(msg.text, "Usage:") {
			m.cur().chat.AddSystem("error", "Command error: "+msg.text)
			m.enterChat()
			// A failed command the user must notice gets a dismissable alert
			// (spec 01 §7.3). Usage hints stay in the flash line only.
			// Only failures the backend reported (cmdErr); a hint about how
			// the command is typed stays in the flash line and the chat.
			if msg.alert {
				m.showAlert("Command failed", msg.text)
			}
		}
		m.setFlash(msg.kind, msg.text)
		return nil
	case draftMsg:
		m.restoreDraft(m.tabByID(msg.tab), msg.text)
		return nil
	case commitSuggestedMsg:
		m.openCommitPrompt(msg.text)
		return nil
	case modalDone:
		return m.deliverModal(msg)
	case imageProbeMsg:
		return m.finishImageProbe(msg)
	case shellMsg:
		if t := m.tabByID(msg.tab); t != nil {
			t.chat.AddSystem("shell", msg.text)
		}
		return nil
	case closedMsg:
		return m.finishShutdown()
	case paneMsg:
		return m.onPaneMsg(msg)
	case tea.MouseMsg:
		// The wheel scrolls a list modal by row, accelerating on bursts.
		if p, ok := m.topModal().(*Picker); ok && m.phase != phaseShutdown {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				return p.wheel(-1, time.Now())
			case tea.MouseButtonWheelDown:
				return p.wheel(1, time.Now())
			}
		}
		if m.topModal() != nil || m.phase == phaseShutdown {
			return nil
		}
		if cmd, handled := m.handlePaneMouse(msg); handled {
			return cmd
		}
		t := m.cur()
		if m.phase == phaseLanding {
			return nil
		}
		if m.inSidePanel(msg) {
			t.chat.ClearHover()
			return nil
		}
		cm := m.chatMouse(msg)
		if cmd, handled := m.railMouse(cm); handled {
			return cmd
		}
		switch cm.Button {
		case tea.MouseButtonWheelUp:
			t.chat.ScrollUp(m.wheelLines(time.Now()))
			return nil
		case tea.MouseButtonWheelDown:
			t.chat.ScrollDown(m.wheelLines(time.Now()))
			return nil
		}
		t.chat.HandleMouse(cm)
		if text, ok := t.chat.ReleasedSelection(); ok && text != "" {
			copyToTerminal(text)
			m.setFlash("ok", "Copied "+itoa(strings.Count(text, "\n")+1)+" lines")
		}
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
	if cmd, handled := m.handlePaneKey(km); handled {
		return cmd
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
	var cmds []tea.Cmd
	if m.phase == phaseTransition {
		cmds = append(cmds, fastTickCmd())
	} else {
		cmds = append(cmds, tickCmd())
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
	if now.Sub(m.frameAt) < frameGap {
		return tea.Batch(cmds...)
	}
	m.frame++
	m.frameAt = now
	m.checkThemes(now)
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
		t.costShown = easeCost(t.costShown, t.cost)
		t.chat.Tick(now)
		// Kitty images (opt-in) are sent beside the frame, and their placement
		// is re-sent at 50 and 200 ms; see chat.KittyPending.
		if t.chat.KittyWanted() {
			if esc := t.chat.KittyPending(now); esc != "" {
				cmds = append(cmds, kittyWriteCmd(esc))
			}
		}
	}
	if m.frame%40 == 0 {
		cmds = append(cmds, gitCmd(m.cur().id, m.cur().be))
	}
	m.pollBackground(now)
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
	// A subagent's events describe that subagent: its turn start and end,
	// steps, context and cost must not change the tab's busy state, header
	// or context bar. The transcript draws them in the dispatch rows.
	if ev.AgentID != "" {
		t.chat.Apply(ev)
		return next
	}
	switch ev.Kind {
	case agent.EvTurnStart:
		t.busy = true
		t.since = ev.At
		if ev.Mode != "" {
			t.mode = ev.Mode
		}
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
		t.ctxUsed, t.ctxWin, t.ctxEst = ev.ContextUsed, ev.ContextWindow, ev.Estimated
	case agent.EvStepFinish:
		t.cost = ev.CostUSD
		if ev.Total.InputTokens+ev.Total.OutputTokens > 0 {
			t.cost = ev.CostUSD
		}
		t.tokens = ev.Total.InputTokens + ev.Total.OutputTokens
		if in := ev.Total.InputTokens + ev.Total.CacheReadTokens + ev.Total.CacheWriteTokens; in > 0 {
			t.cacheHit = float64(ev.Total.CacheReadTokens) / float64(in)
		}
	case agent.EvCompactStart:
		t.compacting = true
	case agent.EvCompactDone:
		t.compacting = false
	case agent.EvError:
		m.logErrorFrom(t, srcTurn, ev.Text)
		t.turnErrLogged = true
		m.showTurnError(t, ev.Text)
	case agent.EvToolResult:
		if r := ev.Result; r != nil && r.IsError {
			src, text := toolErrorEntry(ev)
			m.logErrorFrom(t, src, text)
			m.attachErrorArgs(ev)
		}
	case agent.EvWarning:
		if strings.HasPrefix(ev.Text, "compaction failed") {
			m.logErrorFrom(t, srcCompaction, ev.Text)
		}
	case agent.EvTurnDone:
		t.busy = false
		t.cps = t.be.Checkpoints()
		// The agent can change its own mode during a turn (a plan approved
		// with Execute leaves plan mode), so read it back instead of trusting
		// the tab's copy.
		if md := t.be.Mode(); md != "" {
			t.mode = md
		}
		t.cost = costOf(ev, t.cost)
		if t != m.cur() {
			t.unread = true
		}
		if ev.Stats != nil && ev.Stats.Error != "" && !t.turnErrLogged {
			m.logErrorFrom(t, srcTurn, ev.Stats.Error)
		}
		t.turnErrLogged = false
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
// ctrlCBurst is the most time between Ctrl+C presses that still counts them
// as one burst.
const ctrlCBurst = time.Second

func (m *Model) handleCtrlC() tea.Cmd {
	// Each press peels one layer (dialog, prompt, input, running turn), which
	// can take several presses and, with a turn that keeps asking, never
	// reach the quit. Three quick presses always quit.
	now := time.Now()
	if now.Sub(m.ctrlCAt) < ctrlCBurst {
		m.ctrlCs++
	} else {
		m.ctrlCs = 1
	}
	m.ctrlCAt = now
	if m.ctrlCs >= 3 {
		// Deny what is waiting first, so no agent sits on an answer while
		// its backend closes.
		var cmds []tea.Cmd
		for _, t := range m.tabs {
			if t.pui != nil {
				cmds = append(cmds, m.resolvePrompt(t, denyFor(t.pui)))
			}
		}
		for m.topModal() != nil {
			m.closeModal(m.topModal())
		}
		return tea.Batch(append(cmds, m.beginShutdown())...)
	}
	defer func() {
		if m.ctrlCs == 2 && m.flash == "" {
			m.setFlash("info", "Press Ctrl+C once more to quit")
		}
	}()
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
	if c, mo := t.changedNow(); len(c)+len(mo) > 0 && m.phase != phaseLanding {
		h++
	}
	if t.pui != nil {
		h += len(m.promptLines(t))
	} else if m.phase != phaseLanding {
		h += len(t.in.popupLines(m, m.width)) + t.in.inputHeight(m.height) + 2
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
	if m.phase == phaseLanding {
		return m.landingRows()
	}
	return max(m.height-1-m.tabBarRows()-1-m.bottomHeight(), 1)
}

// landingRows is the height of the landing page: everything but the header,
// the tab bar and the footer. Its input box is part of the page, not the
// bottom panel, so the transcript's bottom rows do not apply.
func (m *Model) landingRows() int {
	return max(m.height-2-m.tabBarRows(), 1)
}

// tabBarRows is 1 when the tab bar is shown (more than one tab).
func (m *Model) tabBarRows() int {
	if len(m.tabs) > 1 {
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
		cols, insetRows := m.chatInset()
		t.chat.SetSize(max(1, m.bodyWidth()-m.sideWidth()-cols), max(0, rows-insetRows))
		t.in.resize(m.inputWidth())
	}
	m.resizePanes()
}

// inputWidth is the textarea width: the box is two border and two padding columns wider.
func (m *Model) inputWidth() int {
	w := m.bodyWidth()
	if m.phase == phaseLanding || m.phase == phaseTransition {
		w = m.width * m.landingPct() / 100
	}
	return max(w-4, 10)
}

// landingPct is the input box's share of the screen while the landing page
// hands over to the chat: 60 on the landing page, growing to the full width
// by the end of the 600 ms transition (spec 1, useLandingTransition).
func (m *Model) landingPct() int {
	switch m.phase {
	case phaseLanding:
		return 60
	case phaseTransition:
		p := clampF(float64(time.Since(m.trans))/float64(600*time.Millisecond), 0, 1)
		return 60 + int(40*p)
	}
	return 100
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
		lines = append(lines, m.viewLanding(m.width, m.landingRows())...)
	case phaseTransition:
		p := clampF(float64(time.Since(m.trans))/float64(600*time.Millisecond), 0, 1)
		if p < 0.5 {
			lines = append(lines, m.viewLanding(m.width, m.landingRows())...)
		} else {
			lines = append(lines, m.viewChatBody(rows)...)
		}
	default:
		lines = append(lines, m.viewBody(rows)...)
	}
	lines = append(lines, m.viewFooter())
	base := fitLines(lines, m.width, m.height)
	base = m.overlayFloat(base)
	if md := m.topModal(); md != nil {
		box := modalFrame(md, m.th, m.width, m.height)
		base = overlay(base, box, m.width, m.height)
	}
	return strings.Join(base, "\n")
}

// viewChatBody is the transcript plus the bottom panel. The transcript sits
// in a rounded border and, on wide screens, beside the changes and terminals
// panels; the bottom panel runs under all of it.
func (m *Model) viewChatBody(rows int) []string {
	t := m.cur()
	var out []string
	if m.phase != phaseLanding {
		// the transcript fills what the bottom panel leaves
		m.syncRail(t)
		side := m.sideWidth()
		colW := m.width - side
		cols, insetRows := m.chatInset()
		innerW, innerH := max(1, colW-cols), max(0, rows-insetRows)
		t.chat.SetSize(innerW, innerH)
		col := fitLines(strings.Split(t.chat.View(), "\n"), innerW, innerH)
		if m.chatFramed() {
			col = m.frameChat(t, col, colW)
		}
		if side > 0 {
			col = joinCols(col, m.sideColumn(t, rows))
		}
		out = append(out, col...)
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
	if c, mo := t.changedNow(); len(c)+len(mo) > 0 {
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

// busyQuill is the busy-line spinner: the quill's ink dots grow and shrink.
var busyQuill = render.Spinner{"✒ ·", "✒ ··", "✒ ···", "✒ ··"}

// busyLine shows the quill spinner, what the turn is doing, the elapsed time
// and the stop hint on the right.
func (m *Model) busyLine(t *tab) string {
	now := time.Now()
	spin := render.Frame(busyQuill, m.frame/3)
	elapsed := ""
	if !t.since.IsZero() {
		elapsed = render.Elapsed(now.Sub(t.since))
	}
	left := fgb(m.th.Brand).Render(spin) + " "
	if m.flash != "" {
		// A flash (a commit, a cancel) takes the phrase's place at the front,
		// where a long phrase and token count cannot push it off the row.
		col := m.th.TextSecondary
		switch m.flashKind {
		case "ok":
			col = m.th.Success
		case "err":
			col = m.th.Error
		}
		text := render.Truncate(m.flash, max(12, m.width-40))
		left += fg(col).Render(text) + " " + fg(m.th.TextDim).Render(elapsed)
	} else {
		phrase := fg(theme.Breath(m.th.TextMuted, m.th.Brand, m.frame, 60)).Render(busyPhrase(t, now))
		left += phrase + " " + fg(m.th.TextDim).Render(elapsed)
		// While text streams, an estimate of the tokens received so far.
		if n := t.chat.StreamTokenEstimate(); n > 0 {
			left += fg(m.th.TextDim).Render(" · ~" + render.HumanTokens(n) + " tokens")
		}
	}
	right := fgb(m.th.Error).Render("■ Esc to stop")
	return spaceBetween(left, right, m.width)
}

// busyPhrase is the status word on the busy line. It is always a readable
// word: a turn waiting on the user says so, and a garbled frame (the chat's
// glitch-in between phrases) shows the last readable word instead.
func busyPhrase(t *tab, now time.Time) string {
	if t.pui != nil {
		if t.pui.kind == "approval" {
			return "Waiting for your approval"
		}
		return "Waiting for your answer"
	}
	if len(t.prompts) > 0 {
		// queued behind an open prompt: still the user's turn
		if t.prompts[0].Approval != nil {
			return "Waiting for your approval"
		}
		return "Waiting for your answer"
	}
	p := t.chat.LivePhrase(now)
	if chat.IsGarbled(p) || p == "" {
		if t.lastPhrase != "" {
			return t.lastPhrase
		}
		return "Working"
	}
	t.lastPhrase = p
	return p
}

// steerLines shows queued steering messages above the input.
func (m *Model) steerLines(t *tab) []string {
	var out []string
	for i, s := range t.steerQ {
		if i >= 1 {
			break
		}
		line := fg(m.th.Warning).Render("│ Steering: ") + fg(m.th.TextSecondary).Render(render.Truncate(oneLine(s), m.width-30))
		if n := strings.Count(s, "\n"); n > 0 {
			line += " " + fg(m.th.TextDim).Render("(+"+itoa(n)+" lines)")
		}
		if n := len(t.steerQ); n > 1 {
			line += " " + fg(m.th.TextDim).Render("(+"+itoa(n-1)+" queued)")
		}
		out = append(out, line)
	}
	return out
}

// changedBar lists files touched this session: +created in success, ~modified
// in amber, then the first three names coloured the same way, then the count
// of the rest.
func (m *Model) changedBar(t *tab) string {
	created, modified := t.changedNow()
	line := fg(m.th.Amber).Render(" ✎ ")
	if len(created) > 0 {
		line += fg(m.th.Success).Render("+"+itoa(len(created))) + " "
	}
	if len(modified) > 0 {
		line += fg(m.th.Amber).Render("~"+itoa(len(modified))) + " "
	}
	line += fg(m.th.TextFaint).Render("│ ")
	var names []string
	for i, f := range append(append([]string{}, created...), modified...) {
		if i >= 3 {
			break
		}
		col := m.th.Amber
		if i < len(created) {
			col = m.th.Success
		}
		names = append(names, fg(col).Render(render.Truncate(baseName(f), 24)))
	}
	line += strings.Join(names, fg(m.th.TextMuted).Render(", "))
	if total := len(created) + len(modified); total > 3 {
		line += fg(m.th.TextDim).Render(" +" + itoa(total-3))
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
