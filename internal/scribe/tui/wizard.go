package tui

// The first-run setup wizard. It opens on start when the backend reports
// that no model can be used (setupChecker), and any time with /setup.
// The eleven steps (wizSections) run in a sidebar: welcome, setup (provider,
// key, model, icons), four tour pages, shortcuts, theme and ready. The tour
// pages are copy only. Below wizSidebarMin columns the sidebar gives way to a
// one-line step count.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func init() {
	overrideCommands(&command{Name: "setup", Desc: "Run the first-run setup: provider, key, model, theme", Group: "Settings",
		Run: func(m *Model, _ string) tea.Cmd { return m.openWizard() }})
}

// setupChecker is implemented by backends that can say the wizard is needed.
type setupChecker interface{ NeedsSetup() bool }

// RequiredChecker is implemented by backends that can name the required
// tools missing from PATH. Scribe does not start into chat while any is
// missing.
type RequiredChecker interface{ MissingRequired() []string }

// RequiredMissing names the required tools that are not on PATH, in table
// order. Git is the only required one.
func RequiredMissing() []string {
	var names []string
	for _, p := range missingRequired(checkPrereqs()) {
		names = append(names, p.Name)
	}
	return names
}

// maybeOpenWizard runs from Init. It opens the wizard only when the backend
// asks for it and the session is not already starting with a prompt or picker.
// A missing required tool comes first, and it wins over the prompt.
func (m *Model) maybeOpenWizard() tea.Cmd {
	if m.gateRequired() {
		return nil
	}
	if strings.TrimSpace(m.opts.Prompt) != "" || m.opts.Pick {
		return nil
	}
	sc, ok := m.cur().be.(setupChecker)
	if !ok || !sc.NeedsSetup() {
		return nil
	}
	return m.openWizard()
}

// openWizard pushes the wizard and starts looking for a local Ollama. With a
// required tool missing it pushes the git gate instead.
func (m *Model) openWizard() tea.Cmd {
	if m.gateRequired() {
		return nil
	}
	w := newWizard(m)
	m.pushModal(w)
	return w.start()
}

// gateRequired pushes the git gate when a required tool is missing and
// reports whether it did. Backends that cannot check report nothing missing.
func (m *Model) gateRequired() bool {
	rc, ok := m.cur().be.(RequiredChecker)
	if !ok {
		return false
	}
	miss := rc.MissingRequired()
	if len(miss) == 0 {
		return false
	}
	m.pushModal(&gitGate{m: m, checker: rc, missing: miss})
	return true
}

// gitGate is the setup screen shown before chat when a required tool is
// missing. It leaves only when the tool is found (r) or the user quits (q).
// Esc does nothing. Ctrl+C closes the top modal before the app sees it, so
// OnClose puts the gate back until the user quits.
type gitGate struct {
	m       *Model
	checker RequiredChecker
	missing []string
}

// Title implements Modal.
func (g *gitGate) Title() string { return "Setup" }

// Icon implements Iconed.
func (g *gitGate) Icon() string { return "smithy" }

// Danger implements the error border: nothing runs until this is fixed.
func (g *gitGate) Danger() bool { return true }

// Size implements Sized.
func (g *gitGate) Size(cols, rows int) (int, int) { return min(78, cols-4), min(rows-4, 14) }

// Footer implements Footered.
func (g *gitGate) Footer() string { return "r check again · q quit" }

// Update implements Modal.
func (g *gitGate) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return g, nil, false
	}
	switch km.String() {
	case "r":
		g.missing = g.checker.MissingRequired()
		if len(g.missing) > 0 {
			return g, nil, false
		}
		// The tool is here now. The gate leaves, and the normal first-run
		// check runs as it would have at start.
		return g, g.m.maybeOpenWizard(), true
	case "q":
		return g, g.m.beginShutdown(), true
	}
	return g, nil, false
}

// OnClose implements Closer. A close that is not a quit puts the gate back
// while the tool is still missing.
func (g *gitGate) OnClose() {
	// During a quit (q, or three quick Ctrl+C presses, which clear the stack
	// in a loop) the gate must stay closed.
	if g.m.phase == phaseShutdown || g.m.ctrlCs >= 3 {
		return
	}
	if len(g.checker.MissingRequired()) > 0 {
		g.m.pushModal(g)
	}
}

// View implements Modal.
func (g *gitGate) View(w, _ int) string {
	th := g.m.th
	var out []string
	out = append(out, fgb(th.TextPrimary).Render(render.Wrap(strings.Join(g.missing, ", ")+" is required before Scribe can run.", w)), "")
	for _, name := range g.missing {
		p, ok := prereqNamed(name)
		if !ok {
			out = append(out, fg(th.Error).Render("  ✗ "+name))
			continue
		}
		out = append(out, fg(th.Error).Render("  ✗ "+p.Name)+fg(th.TextMuted).Render("  missing: "+p.Why))
		out = append(out, fg(th.TextMuted).Render(render.Wrap("      install: "+installHint(p, goos), w)))
	}
	return strings.Join(append(out, "", fg(th.TextMuted).Render(render.Wrap("Scribe has not opened a chat. Install it, then press r to check again, or q to quit.", w))), "\n")
}

// prereqNamed finds a row of prereqTable by its name.
func prereqNamed(name string) (prereq, bool) {
	for _, p := range prereqTable {
		if p.Name == name {
			return p, true
		}
	}
	return prereq{}, false
}

type wizStep int

const (
	wzWelcome wizStep = iota
	wzProvider
	wzKey
	wzModel
	wzIcons
	wzIntel
	wzEdit
	wzModes
	wzWorkflow
	wzAuto
	wzRemote
	wzShortcuts
	wzTheme
	wzDone
)

// wizSection is one sidebar entry: its label, the title and blurb shown on the
// active row, and where it sits in the step order.
type wizSection struct {
	label, title, blurb string
}

// wizSections are the eleven sidebar entries, in order. The setup entry
// covers the four steps from wzProvider to wzIcons.
var wizSections = []wizSection{
	{"welcome", "Welcome", "meet Scribe"},
	{"setup", "Provider", "pick a provider, then its key"},
	{"intelligence", "Code Intel", "how Scribe reads code"},
	{"editing", "Editing", "edits by symbol, not by text"},
	{"modes", "Modes", "auto, plan, architect"},
	{"workflow", "Tabs & Git", "tabs, sessions, git"},
	{"automation", "Automation", "subagents & compaction"},
	{"remote", "Remote", "MCP, skills, remote chat"},
	{"shortcuts", "Shortcuts", "keys & commands"},
	{"theme", "Theme", "make it yours"},
	{"ready", "Ready", "start working"},
}

// wizSidebarMin is the narrowest content width that still fits the sidebar.
const (
	wizSidebarMin = 60
	wizSidebarW   = 18
	wizSidebarSep = 3
)

// section maps a step to its sidebar entry.
func (s wizStep) section() int {
	switch {
	case s == wzWelcome:
		return 0
	case s <= wzIcons:
		return 1
	case s == wzDone:
		return len(wizSections) - 1
	case s == wzTheme:
		return 9
	}
	return int(s) - int(wzIntel) + 2
}

// wizardProviders are the providers offered in the wizard, in order. The
// rest stay on the Providers tab of /settings.
var wizardProviders = []string{"anthropic", "openai", "deepseek", "openrouter", "gemini", "groq", "ollama"}

// keyPages names where to get a key. Hosts only, so the hint cannot go stale
// on a path change.
var keyPages = map[string]string{
	"anthropic":  "console.anthropic.com",
	"openai":     "platform.openai.com",
	"deepseek":   "platform.deepseek.com",
	"openrouter": "openrouter.ai",
	"gemini":     "aistudio.google.com",
	"groq":       "console.groq.com",
}

// Messages from background work.
type (
	wizDetectMsg struct {
		id string
		ok bool
	}
	wizKeyMsg struct {
		w   *wizard
		err error
	}
)

// wizard is a Modal with its own small state machine.
type wizard struct {
	m    *Model
	be   Backend
	step wizStep

	provs  []ProviderStatus // wizardProviders that the backend knows about
	cursor int              // provider row; len(provs) is the custom row
	chosen ProviderStatus   // provider the key and model steps work on

	detect   map[string]int // local provider: 0 checking, 1 running, 2 not running
	key      []rune
	keyErr   string
	checking bool

	chooser *modelChooser

	themes    []string
	themeCur  int
	origTheme string

	prereqs []prereqStatus // tools checked on the welcome step
	note    string         // last problem, shown on the current step

	nerd bool // the icons step: Nerd Font glyphs on or off

	// style is the theme option set the theme step edits (transparency,
	// message and diff background opacity, border strength). It is applied
	// live; saving it waits for the theme config object.
	style theme.Style
}

func newWizard(m *Model) *wizard {
	be := m.cur().be
	w := &wizard{m: m, be: be, detect: map[string]int{}, origTheme: m.themeName, prereqs: checkPrereqs(), style: theme.DefaultStyle()}
	all := be.Providers()
	for _, id := range wizardProviders {
		for _, p := range all {
			if p.ID == id {
				w.provs = append(w.provs, p)
			}
		}
	}
	return w
}

// start asks each local provider whether it is answering.
func (w *wizard) start() tea.Cmd {
	var cmds []tea.Cmd
	for _, p := range w.provs {
		if !p.Local {
			continue
		}
		id := p.ID
		w.detect[id] = 0
		be := w.be
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			_, err := be.Models(ctx, id)
			return wizDetectMsg{id: id, ok: err == nil}
		})
	}
	return tea.Batch(cmds...)
}

// Title implements Modal.
func (w *wizard) Title() string { return "Setup" }

// Icon implements Iconed.
func (w *wizard) Icon() string { return "smithy" }

// Size implements Sized.
func (w *wizard) Size(cols, rows int) (int, int) { return min(78, cols-4), min(rows-4, 24) }

// Busy implements busyModal: a key being checked holds the wizard.
func (w *wizard) Busy() bool { return w.checking }

// Footer implements Footered.
func (w *wizard) Footer() string {
	switch w.step {
	case wzWelcome:
		if len(missingRequired(w.prereqs)) > 0 {
			return "r check again · i install hints · Esc close"
		}
		return "→ next · i install hints · Esc close"
	case wzProvider:
		return "↑↓ select · Enter choose · ← back"
	case wzKey:
		return "Enter save & check · Esc back"
	case wzModel:
		return "↑↓ select · Enter choose · Tab next · Esc back"
	case wzIcons:
		return "Space toggle · Enter next · ← back"
	case wzTheme:
		return "↑↓ preview · Enter keep · Tab/m/d/b options · ← back"
	case wzDone:
		return "Enter start · ← back"
	}
	return "← back · → next · Esc skip"
}

// Update implements Modal.
func (w *wizard) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case wizDetectMsg:
		if msg.ok {
			w.detect[msg.id] = 1
		} else {
			w.detect[msg.id] = 2
		}
		return w, nil, false
	case wizKeyMsg:
		if msg.w != w {
			return w, nil, false
		}
		w.checking = false
		if msg.err != nil {
			w.keyErr = msg.err.Error()
			return w, nil, false
		}
		w.key = nil
		return w, w.enterModel(w.chosen), false
	case chooserMsg:
		if w.chooser != nil {
			_, cmd, _ := w.chooser.Update(msg)
			return w, cmd, false
		}
		return w, nil, false
	case tea.KeyMsg:
		return w.onKey(msg)
	}
	return w, nil, false
}

func (w *wizard) onKey(km tea.KeyMsg) (Modal, tea.Cmd, bool) {
	if w.checking {
		return w, nil, false
	}
	k := km.String()
	w.note = ""
	switch w.step {
	case wzWelcome:
		switch k {
		case "esc":
			return w, nil, true
		case "r":
			w.prereqs = checkPrereqs()
		case "i":
			// Show the install commands for what is missing; never run them.
			if hints := missingHints(w.prereqs, goos); len(hints) > 0 {
				w.note = "Install (not run): " + strings.Join(hints, " · ")
			} else {
				w.note = "Everything on this list is installed"
			}
		case "enter", "right", "l", "tab":
			if miss := missingRequired(w.prereqs); len(miss) > 0 {
				w.note = requiredMessage(miss, goos)
				return w, nil, false
			}
			w.step = wzProvider
		}
	case wzProvider:
		n := len(w.provs) + 1
		switch k {
		case "esc", "left", "h":
			w.step = wzWelcome
		case "up", "k":
			w.cursor = clamp(w.cursor-1, 0, n-1)
		case "down", "j":
			w.cursor = clamp(w.cursor+1, 0, n-1)
		case "enter", "right", "l", "tab":
			return w, w.choose(), false
		}
	case wzKey:
		switch k {
		case "esc":
			w.step = wzProvider
			w.key, w.keyErr = nil, ""
		case "enter":
			return w, w.validate(), false
		case "backspace":
			if len(w.key) > 0 {
				w.key = w.key[:len(w.key)-1]
			}
		case "ctrl+u":
			w.key = nil
		default:
			if s := printable(km); s != "" && len(w.key) < 400 {
				w.key = append(w.key, []rune(s)...)
			}
		}
	case wzModel:
		switch k {
		case "esc":
			if w.chooser != nil && w.chooser.query != "" {
				_, cmd, _ := w.chooser.Update(km)
				return w, cmd, false
			}
			w.step = wzProvider
		case "left":
			w.step = wzProvider
		case "tab", "right":
			if it, ok := w.chooser.Picker.Selected(); ok && !it.Disabled && it.Value != "" {
				return w, w.chooser.pick(it.Value), false
			}
		default:
			_, cmd, _ := w.chooser.Update(km)
			return w, cmd, false
		}
	case wzIcons:
		switch k {
		case "esc", "left", "h":
			w.step = wzModel
		case " ", "space":
			w.setNerd(!w.nerd)
		case "enter", "right", "l", "tab":
			w.step = wzIntel
		}
	case wzIntel, wzEdit, wzModes, wzWorkflow, wzAuto, wzRemote, wzShortcuts:
		// Tour pages: copy only. Enter and right go on, left goes back, Esc skips.
		switch k {
		case "esc":
			return w, nil, true
		case "left", "h":
			w.step--
		case "enter", "right", "l", "tab":
			if w.step == wzShortcuts {
				w.enterTheme()
			} else {
				w.step++
			}
		}
	case wzTheme:
		switch k {
		case "up", "k":
			w.moveTheme(-1)
		case "down", "j":
			w.moveTheme(1)
		case "esc":
			w.revertTheme()
			return w, nil, true
		case "left", "h":
			w.revertTheme()
			w.step = wzShortcuts
		case "tab":
			w.style.Transparent = !w.style.Transparent
			w.previewTheme()
		case "m":
			w.style.UserMessageOpacity = nextOpacity(w.style.UserMessageOpacity)
			w.previewTheme()
		case "d":
			w.style.DiffOpacity = nextOpacity(w.style.DiffOpacity)
			w.previewTheme()
		case "b":
			w.style.BorderStrength = nextBorder(w.style.BorderStrength)
			w.previewTheme()
		case "enter", "right", "l":
			name := w.themes[w.themeCur]
			w.be.SetTheme(name)
			w.origTheme = name
			w.previewTheme()
			w.step = wzDone
		}
	case wzDone:
		switch k {
		case "esc", "left", "h":
			w.step = wzTheme
		case "enter":
			return w, flashCmd("ok", "Setup done. Type a message to start."), true
		}
	}
	return w, nil, false
}

// choose acts on the highlighted provider row.
func (w *wizard) choose() tea.Cmd {
	if w.cursor == len(w.provs) {
		w.addCustom()
		return nil
	}
	p := w.provs[w.cursor]
	switch {
	case p.Local:
		switch w.detect[p.ID] {
		case 0:
			w.note = "still looking for " + p.Name + "…"
		case 2:
			w.note = p.Name + " is not running " + localWhere(p.ID) + ". Start it, or pick another provider."
		default:
			return w.enterModel(p)
		}
		return nil
	case p.HasKey:
		return w.enterModel(p)
	}
	w.chosen = p
	w.step = wzKey
	w.key, w.keyErr = nil, ""
	return nil
}

// enterModel moves to the model step for p.
func (w *wizard) enterModel(p ProviderStatus) tea.Cmd {
	w.chosen = p
	w.step = wzModel
	current := ""
	if p.DefaultModel != "" {
		current = p.ID + "/" + p.DefaultModel
	}
	mc, cmd := newModelChooser(w.m, "Pick a model from "+p.Name, p.ID, false, current, w.chooseModel)
	w.chooser = mc
	return cmd
}

// chooseModel saves the model, then moves on to the theme.
func (w *wizard) chooseModel(ref string) tea.Cmd {
	if ref == "" {
		return nil
	}
	if err := w.be.SetModel(ref); err != nil {
		w.note = err.Error()
		return nil
	}
	if err := w.be.SetConfig("model", ref, false); err != nil {
		w.note = err.Error()
		return nil
	}
	w.m.syncModel()
	w.nerd = icons.NerdEnabled()
	w.step = wzIcons
	return nil
}

// enterTheme moves to the theme step, with the current theme highlighted.
func (w *wizard) enterTheme() {
	w.themes = theme.Names()
	w.themeCur = listIndex(w.themes, w.m.themeName)
	if w.themeCur < 0 {
		w.themeCur = 0
	}
	w.origTheme = w.m.themeName
	w.step = wzTheme
}

// nextOpacity steps a background opacity through theme.OpacitySteps.
func nextOpacity(cur int) int {
	for i, v := range theme.OpacitySteps {
		if v == cur {
			return theme.OpacitySteps[(i+1)%len(theme.OpacitySteps)]
		}
	}
	return theme.OpacitySteps[len(theme.OpacitySteps)-1]
}

// borderStrengths is the order the Borders option cycles through.
var borderStrengths = []string{"default", "strong", "op"}

// nextBorder steps the border strength; "" counts as default.
func nextBorder(cur string) string {
	if cur == "" {
		cur = "default"
	}
	for i, v := range borderStrengths {
		if v == cur {
			return borderStrengths[(i+1)%len(borderStrengths)]
		}
	}
	return borderStrengths[0]
}

// setNerd saves the Nerd Font choice and applies it to the glyphs at once.
func (w *wizard) setNerd(on bool) {
	if err := w.be.SetConfig("nerd_font", on, false); err != nil {
		w.note = err.Error()
		return
	}
	w.nerd = on
	icons.SetNerd(on)
}

// validate saves the typed key and checks it by listing the provider's models.
func (w *wizard) validate() tea.Cmd {
	key := strings.TrimSpace(string(w.key))
	if key == "" {
		w.keyErr = "Paste or type a key first."
		return nil
	}
	w.keyErr = ""
	w.checking = true
	id, be := w.chosen.ID, w.be
	return func() tea.Msg {
		if err := be.SetKey(id, key); err != nil {
			return wizKeyMsg{w: w, err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, err := be.Models(ctx, id)
		if err != nil {
			err = fmt.Errorf("the key was not accepted: %w", err)
		}
		return wizKeyMsg{w: w, err: err}
	}
}

// addCustom asks for a custom OpenAI-compatible provider, then goes to its key.
func (w *wizard) addCustom() {
	var id, base string
	env := NewTextPrompt(w.m.th, "Custom provider: key variable", "e.g. MYPROXY_API_KEY, or blank for none", false, func(v string) tea.Cmd {
		err := w.be.SetConfig("provider.add", map[string]any{"id": id, "base_url": base, "api_key_env": v}, false)
		if err != nil {
			w.note = err.Error()
			return nil
		}
		w.chosen = ProviderStatus{ID: id, Name: id}
		w.step = wzKey
		w.key, w.keyErr = nil, ""
		return nil
	}).AllowEmpty()
	url := NewTextPrompt(w.m.th, "Custom provider: base URL", "https://host/v1", false, func(v string) tea.Cmd {
		base = v
		w.m.pushModal(env)
		return nil
	})
	first := NewTextPrompt(w.m.th, "Custom provider: id", "short name, e.g. litellm", false, func(v string) tea.Cmd {
		id = v
		w.m.pushModal(url)
		return nil
	})
	w.m.pushModal(first)
}

func (w *wizard) moveTheme(d int) {
	if len(w.themes) == 0 {
		return
	}
	w.themeCur = clamp(w.themeCur+d, 0, len(w.themes)-1)
	w.revertTheme()
	w.previewTheme()
}

// previewTheme applies the highlighted theme with the chosen options.
func (w *wizard) previewTheme() {
	if len(w.themes) == 0 {
		return
	}
	name := w.themes[w.themeCur]
	if th, ok := theme.Lookup(name); ok {
		w.m.setTheme(name, theme.Adjust(th, w.style))
	}
}

// revertTheme puts back the theme the wizard started with, keeping the options.
func (w *wizard) revertTheme() {
	if th, ok := theme.Lookup(w.origTheme); ok {
		w.m.setTheme(w.origTheme, theme.Adjust(th, w.style))
	}
}

// View implements Modal. Wide enough, the sidebar sits left of the step; if
// not, a one-line count stands in for it.
func (w *wizard) View(width, height int) string {
	th := w.m.th
	side := width >= wizSidebarMin
	cw := width
	var head []string
	if side {
		cw = width - wizSidebarW - wizSidebarSep
	} else {
		head = []string{w.progress(th), ""}
	}
	var out []string
	switch w.step {
	case wzWelcome:
		out = append(out, w.welcome(cw)...)
	case wzProvider:
		out = append(out, w.providerList(th, cw)...)
	case wzKey:
		out = append(out, w.keyView(th, cw)...)
	case wzModel:
		out = append(out, fgb(th.TextPrimary).Render("Pick a model from "+w.chosen.Name), "")
		if w.chooser != nil {
			out = append(out, strings.Split(w.chooser.View(cw, max(3, height-6)), "\n")...)
		}
	case wzIcons:
		out = append(out, w.iconsView(th, cw)...)
	case wzTheme:
		out = append(out, w.themeView(th, cw, height-4)...)
	case wzDone:
		out = append(out, w.done(th, cw)...)
	default:
		out = append(out, w.tourView(th, cw)...)
	}
	if w.note != "" {
		out = append(out, "", fg(th.Warning).Render(render.Wrap(w.note, cw)))
	}
	// Some step helpers return a wrapped block as one string. The sidebar
	// merges row by row, so every entry must be exactly one line.
	out = oneLineEach(out)
	if !side {
		return strings.Join(append(head, out...), "\n")
	}
	left := w.sidebar(th, height)
	sep := fg(th.Border).Render(" │ ")
	n := max(len(left), len(out))
	rows := make([]string, 0, n)
	for i := 0; i < n; i++ {
		l := ""
		if i < len(left) {
			l = left[i]
		}
		r := ""
		if i < len(out) {
			r = out[i]
		}
		rows = append(rows, padRight(l, wizSidebarW)+sep+r)
	}
	return strings.Join(rows, "\n")
}

// oneLineEach splits every entry on newlines, so each element is one row.
func oneLineEach(lines []string) []string {
	var out []string
	for _, l := range lines {
		out = append(out, strings.Split(l, "\n")...)
	}
	return out
}

// progress is the narrow-terminal stand-in for the sidebar: the step count
// and its title.
func (w *wizard) progress(th theme.Theme) string {
	sec := w.step.section()
	return fgb(th.Brand).Render(fmt.Sprintf("Step %d of %d", sec+1, len(wizSections))) +
		fg(th.TextFaint).Render("  "+wizSections[sec].title)
}

// sidebar lists the eleven sections. The active one shows its blurb under
// it; earlier ones are ticked, later ones are idle.
func (w *wizard) sidebar(th theme.Theme, height int) []string {
	cur := w.step.section()
	var out []string
	for i, sec := range wizSections {
		var mark string
		var label lipgloss.Style
		switch {
		case i == cur:
			mark = "● "
			label = lipgloss.NewStyle().Bold(true).Foreground(th.Warning)
		case i < cur:
			mark = "✓ "
			label = lipgloss.NewStyle().Foreground(th.Success)
		default:
			mark = "○ "
			label = lipgloss.NewStyle().Foreground(th.TextFaint)
		}
		out = append(out, label.Render(padRight(mark+sec.label, wizSidebarW)))
		if i == cur {
			for _, l := range strings.Split(render.Wrap(sec.blurb, wizSidebarW-2), "\n") {
				out = append(out, fg(th.TextSecondary).Render("  "+l))
			}
		}
	}
	return out
}

// tourView is the copy for the tour pages. Each page is a heading and a short
// body; the modes page lists what each mode does.
func (w *wizard) tourView(th theme.Theme, width int) []string {
	heading := func(t string) string { return fgb(th.TextPrimary).Render(t) }
	body := func(t string) []string {
		var out []string
		for _, l := range strings.Split(render.Wrap(t, width), "\n") {
			out = append(out, fg(th.TextSecondary).Render(l))
		}
		return out
	}
	var out []string
	switch w.step {
	case wzIntel:
		out = append([]string{heading("Codebase Intelligence"), ""},
			body("Scribe reads your repo as a graph: symbols, imports and call sites, not a pile of text.")...)
	case wzEdit:
		out = append([]string{heading("Editing: by symbol, not by string"), ""},
			body("Scribe edits a function or type by its name, not by matching text. A post-edit check runs after each change, so a broken edit shows up at once.")...)
	case wzModes:
		out = append([]string{heading("Modes: how Scribe approaches work"), ""},
			body("Cycle with Ctrl+D. Auto runs hands-free. Plan is research only.")...)
		out = append(out, "")
		for _, l := range modeHelpLines() {
			out = append(out, fg(th.TextSecondary).Render(l))
		}
	case wzWorkflow:
		out = append([]string{heading("Tabs, Sessions & Git"), ""},
			body(fmt.Sprintf("Up to %d tabs, as many sessions as you like, and a checkpoint after every turn. Rewind anything.", MaxTabs))...)
	case wzAuto:
		out = append([]string{heading("Automation: scale without friction"), ""},
			body("Subagents take focused jobs in parallel. A long session compacts its history when the context fills up; /compact does it now.")...)
	case wzRemote:
		out = append([]string{heading("MCP, Skills & Remote"), ""},
			body("Add MCP servers with /mcp. Ctrl+S browses and installs skills. Remote chat lets you talk to Scribe from another device.")...)
	case wzShortcuts:
		out = []string{heading("Keyboard & Commands"), ""}
		for _, k := range [][2]string{
			{"Ctrl+K", "command palette"},
			{"Ctrl+L", "switch model"},
			{"Ctrl+P", "sessions"},
			{"Ctrl+G", "git menu"},
			{"Ctrl+S", "skills"},
			{"Ctrl+T/W", "open or close a tab"},
			{"Ctrl+D", "cycle the mode"},
			{"Ctrl+X", "stop the running turn"},
			{"Ctrl+B/F", "step through checkpoints"},
		} {
			out = append(out, fg(th.Brand).Render(padRight(k[0], 10))+" "+fg(th.TextSecondary).Render(k[1]))
		}
		out = append(out, "")
		out = append(out, body("Type / for commands, or ? on an empty input for every key.")...)
	}
	return out
}

// themeView is the theme page: the four options, then the theme list.
func (w *wizard) themeView(th theme.Theme, width, rows int) []string {
	out := []string{fgb(th.TextPrimary).Render("Pick your theme"),
		fg(th.TextFaint).Render("Moving the cursor previews the theme. Enter keeps it."), ""}
	out = append(out, w.styleRows(th)...)
	out = append(out, "")
	return append(out, w.themeList(th, rows-len(out))...)
}

// styleRows draws the four theme options with their keys.
func (w *wizard) styleRows(th theme.Theme) []string {
	pct := func(n int) string { return fmt.Sprintf("%d%%", n) }
	border := w.style.BorderStrength
	if border == "" {
		border = "default"
	}
	border = map[string]string{"default": "Default", "strong": "Strong", "op": "OP"}[border]
	row := func(name, value, key string) string {
		return fg(th.TextSecondary).Render(padRight(name, 14)) + fgb(th.TextPrimary).Render(padRight(value, 10)) + fg(th.TextFaint).Render(key)
	}
	return []string{
		fg(th.TextSecondary).Render(padRight("Transparent", 14)) + Toggle(th, w.style.Transparent, true) + "  " + fg(th.TextFaint).Render("Tab"),
		row("Message BG", pct(w.style.UserMessageOpacity), "m cycles"),
		row("Diff BG", pct(w.style.DiffOpacity), "d cycles"),
		row("Borders", border, "b cycles"),
	}
}

func (w *wizard) welcome(width int) []string {
	th := w.m.th
	lines := []string{
		"Scribe is a coding agent for your terminal. It reads your code, edits files and runs commands, and asks before anything risky.",
		"Bring a model: an API key for a hosted provider such as Anthropic, OpenAI or DeepSeek, or a local one through Ollama.",
		"This takes under a minute. Keys are stored in Scribe's home directory, readable only by you.",
	}
	var out []string
	for _, l := range lines {
		out = append(out, fg(th.TextPrimary).Render(render.Wrap(l, width)), "")
	}
	return append(out, prereqLines(th, w.prereqs, goos, width)...)
}

func (w *wizard) providerList(th theme.Theme, width int) []string {
	out := []string{fgb(th.TextPrimary).Render("Choose a provider"), ""}
	for i, p := range w.provs {
		out = append(out, w.providerRow(th, p, i == w.cursor, width))
	}
	custom := "+ Custom OpenAI-compatible endpoint"
	if w.cursor == len(w.provs) {
		custom = fgb(th.TextPrimary).Render("› " + custom)
	} else {
		custom = fg(th.TextSecondary).Render("  " + custom)
	}
	return append(out, custom)
}

// localHost is the address a local provider is reached at when it is not this
// machine: OLLAMA_HOST for Ollama. "" means this machine (the default address).
func localHost(id string) string {
	if id != "ollama" {
		return ""
	}
	host := strings.TrimSpace(os.Getenv("OLLAMA_HOST"))
	if host == "" {
		return ""
	}
	u := host
	if !strings.Contains(u, "://") {
		u = "http://" + u
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host == "" {
		return host
	}
	switch parsed.Hostname() {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0", "":
		return ""
	}
	return parsed.Host
}

// localRunningWhere names where a local provider answered: this machine, or
// the remote host from OLLAMA_HOST.
func localRunningWhere(id string) string {
	if h := localHost(id); h != "" {
		return "running on " + h
	}
	return "running on this machine"
}

// localWhere is the "on this machine" / "at <host>" phrase for a local provider.
func localWhere(id string) string {
	if h := localHost(id); h != "" {
		return "at " + h
	}
	return "on this machine"
}

// providerRow draws one provider with its status.
func (w *wizard) providerRow(th theme.Theme, p ProviderStatus, sel bool, width int) string {
	mark := "  "
	name := fg(th.TextSecondary).Render(p.Name)
	if sel {
		mark = fg(th.Brand).Render("› ")
		name = fgb(th.TextPrimary).Render(p.Name)
	}
	var status string
	switch {
	case p.Local:
		switch w.detect[p.ID] {
		case 0:
			status = fg(th.TextFaint).Render("checking…")
		case 1:
			status = fg(th.Success).Render("✓ " + localRunningWhere(p.ID))
		default:
			status = fg(th.TextFaint).Render("not running " + localWhere(p.ID))
		}
	case p.HasKey:
		status = fg(th.Success).Render(keyStatus(p))
	default:
		status = fg(th.TextFaint).Render("no key yet")
	}
	return spaceBetween(mark+name, status, width)
}

func (w *wizard) keyView(th theme.Theme, width int) []string {
	name := w.chosen.Name
	if name == "" {
		name = w.chosen.ID
	}
	out := []string{fgb(th.TextPrimary).Render("Set your " + name + " key")}
	if host, ok := keyPages[w.chosen.ID]; ok {
		out = append(out, fg(th.TextMuted).Render("Get one at "+host), "")
	} else {
		out = append(out, "")
	}
	line := fg(th.Brand).Render("› ")
	if len(w.key) == 0 {
		line += fg(th.TextDim).Render("paste or type your key") + fg(th.Brand).Render("▌")
	} else {
		// All but the last four characters are bullets (maskRunes).
		line += fg(th.TextPrimary).Render(maskRunes(w.key, width-6)) + fg(th.Brand).Render("▌")
	}
	out = append(out, line, "")
	switch {
	case w.checking:
		out = append(out, fg(th.Info).Render("Checking the key…"))
	case w.keyErr != "":
		out = append(out, fg(th.Error).Render(render.Wrap(w.keyErr, width)))
	default:
		out = append(out, fg(th.TextFaint).Render("Stored in Scribe's key file with owner-only permissions."))
	}
	return out
}

// iconsView is the Nerd Font step: one switch and what it changes.
func (w *wizard) iconsView(th theme.Theme, width int) []string {
	return []string{
		fgb(th.TextPrimary).Render("Show Nerd Font icons?"),
		fg(th.TextMuted).Render(render.Wrap("File types, tools and status marks are drawn with Nerd Font glyphs. Without a Nerd Font in this terminal they show as boxes.", width)),
		"",
		Toggle(th, w.nerd, true) + "  " + fg(th.TextFaint).Render("Nerd Font icons"),
		"",
		fg(th.TextFaint).Render("You can change this later with /settings, General."),
	}
}

// themeList draws the themes with the highlighted one applied live.
func (w *wizard) themeList(th theme.Theme, rows int) []string {
	if rows < 1 {
		rows = 1
	}
	var out []string
	start := keepInView(w.themeCur, 0, rows)
	end := min(len(w.themes), start+rows)
	for i := start; i < end; i++ {
		name := w.themes[i]
		if i == w.themeCur {
			out = append(out, fg(th.Brand).Render("› ")+fgb(th.TextPrimary).Render(name))
		} else {
			out = append(out, "  "+fg(th.TextSecondary).Render(name))
		}
	}
	return out
}

func (w *wizard) done(th theme.Theme, width int) []string {
	model := w.m.cur().model
	out := []string{
		fg(th.Success).Render("✓ You're all set."),
		fg(th.TextSecondary).Render(render.Wrap("Model: "+model, width)),
		"",
		fgb(th.TextPrimary).Render("Four keys to know"),
	}
	shortcuts := [][2]string{
		{"/", "commands and settings (/settings, /setup)"},
		{"Ctrl+K", "command palette"},
		{"Ctrl+L", "switch model"},
		{"Ctrl+D", "cycle the mode: default, auto, plan…"},
	}
	for _, s := range shortcuts {
		out = append(out, fg(th.Brand).Render(fmt.Sprintf("%-8s", s[0]))+" "+fg(th.TextSecondary).Render(s[1]))
	}
	return out
}
