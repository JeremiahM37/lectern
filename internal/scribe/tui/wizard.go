package tui

// The first-run setup wizard. It opens on start when the backend reports
// that no model can be used (setupChecker), and any time with /setup.
// Steps: Welcome, Provider, Key, Model, Theme, Ready.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func init() {
	overrideCommands(&command{Name: "setup", Desc: "Run the first-run setup: provider, key, model, theme", Group: "Settings",
		Run: func(m *Model, _ string) tea.Cmd { return m.openWizard() }})
}

// setupChecker is implemented by backends that can say the wizard is needed.
type setupChecker interface{ NeedsSetup() bool }

// maybeOpenWizard runs from Init. It opens the wizard only when the backend
// asks for it and the session is not already starting with a prompt or picker.
func (m *Model) maybeOpenWizard() tea.Cmd {
	if strings.TrimSpace(m.opts.Prompt) != "" || m.opts.Pick {
		return nil
	}
	sc, ok := m.cur().be.(setupChecker)
	if !ok || !sc.NeedsSetup() {
		return nil
	}
	return m.openWizard()
}

// openWizard pushes the wizard and starts looking for a local Ollama.
func (m *Model) openWizard() tea.Cmd {
	w := newWizard(m)
	m.pushModal(w)
	return w.start()
}

type wizStep int

const (
	wzWelcome wizStep = iota
	wzProvider
	wzKey
	wzModel
	wzTheme
	wzDone
)

var wizStepNames = []string{"Welcome", "Provider", "Key", "Model", "Theme", "Ready"}

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

	note string // last problem, shown on the current step
}

func newWizard(m *Model) *wizard {
	be := m.cur().be
	w := &wizard{m: m, be: be, detect: map[string]int{}, origTheme: m.themeName}
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

// Footer implements Footered.
func (w *wizard) Footer() string {
	switch w.step {
	case wzWelcome:
		return "→ next · Esc close"
	case wzProvider:
		return "↑↓ select · Enter choose · ← back"
	case wzKey:
		return "Enter save & check · Esc back"
	case wzModel:
		return "↑↓ select · Enter choose · Tab next · Esc back"
	case wzTheme:
		return "↑↓ preview · Enter keep · ← back"
	}
	return "Enter start · ← back"
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
		case "enter", "right", "l", "tab":
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
	case wzTheme:
		switch k {
		case "up", "k":
			w.moveTheme(-1)
		case "down", "j":
			w.moveTheme(1)
		case "esc", "left", "h":
			w.revertTheme()
			w.step = wzModel
		case "enter", "right", "l", "tab":
			name := w.themes[w.themeCur]
			w.be.SetTheme(name)
			w.origTheme = name
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
			w.note = p.Name + " is not running on this machine. Start it, or pick another provider."
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
	w.themes = theme.Names()
	w.themeCur = listIndex(w.themes, w.m.themeName)
	if w.themeCur < 0 {
		w.themeCur = 0
	}
	w.origTheme = w.m.themeName
	w.step = wzTheme
	return nil
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

func (w *wizard) previewTheme() {
	name := w.themes[w.themeCur]
	if th, ok := theme.Lookup(name); ok {
		w.m.setTheme(name, th)
	}
}

func (w *wizard) revertTheme() {
	if th, ok := theme.Lookup(w.origTheme); ok && w.m.themeName != w.origTheme {
		w.m.setTheme(w.origTheme, th)
	}
}

// View implements Modal.
func (w *wizard) View(width, height int) string {
	th := w.m.th
	var out []string
	out = append(out, w.progress(th), "")
	switch w.step {
	case wzWelcome:
		out = append(out, w.welcome(width)...)
	case wzProvider:
		out = append(out, w.providerList(th, width)...)
	case wzKey:
		out = append(out, w.keyView(th, width)...)
	case wzModel:
		out = append(out, fgb(th.TextPrimary).Render("Pick a model from "+w.chosen.Name), "")
		if w.chooser != nil {
			out = append(out, strings.Split(w.chooser.View(width, max(3, height-6)), "\n")...)
		}
	case wzTheme:
		out = append(out, w.themeList(th, height-4)...)
	case wzDone:
		out = append(out, w.done(th, width)...)
	}
	if w.note != "" {
		out = append(out, "", fg(th.Warning).Render(render.Wrap(w.note, width)))
	}
	return strings.Join(out, "\n")
}

// progress is the row of step names; the current one is highlighted.
func (w *wizard) progress(th theme.Theme) string {
	parts := make([]string, 0, len(wizStepNames))
	for i, name := range wizStepNames {
		switch {
		case i == int(w.step):
			parts = append(parts, lipgloss.NewStyle().Bold(true).Foreground(th.Brand).Render("● "+name))
		case i < int(w.step):
			parts = append(parts, fg(th.Success).Render("✓ "+name))
		default:
			parts = append(parts, fg(th.TextFaint).Render("○ "+name))
		}
	}
	return strings.Join(parts, "  ")
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
	return out
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
			status = fg(th.Success).Render("✓ running on this machine")
		default:
			status = fg(th.TextFaint).Render("not running")
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
		// Length only: the key itself is never drawn.
		line += fg(th.TextPrimary).Render(strings.Repeat("•", min(len(w.key), max(0, width-6)))) + fg(th.Brand).Render("▌")
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

// themeList draws the themes with the highlighted one applied live.
func (w *wizard) themeList(th theme.Theme, rows int) []string {
	out := []string{fgb(th.TextPrimary).Render("Pick your theme"),
		fg(th.TextFaint).Render("Moving the cursor previews the theme. Enter keeps it."), ""}
	if rows < 1 {
		rows = 1
	}
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
