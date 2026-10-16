package tui

// Slash commands for the wave 5 settings that had no entry point: /diff-style,
// /chat-style, /vim-hints, /agent-features, /instructions, /timeouts, /hooks,
// /privacy, and the strategy picker behind /compact settings. Each writes the
// same flat keys as the settings screen, through the backend.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

func init() {
	registerCommands(
		&command{Name: "diff-style", Args: "[default|sidebyside|compact]", Desc: "Diff style: default, side by side or compact", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd { return m.diffStyleCommand(strings.TrimSpace(args)) }},
		&command{Name: "chat-style", Args: "[accent|bubble]", Desc: "Chat layout: accent or bubble", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd { return m.chatStyleCommand(strings.TrimSpace(args)) }},
		&command{Name: "vim-hints", Args: "[on|off]", Desc: "Vim keys in the input box", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd { return m.vimCommand(strings.TrimSpace(args)) }},
		&command{Name: "agent-features", Desc: "Toggle agent features", Group: "Settings",
			Run: func(m *Model, _ string) tea.Cmd { m.openAgentFeatures(); return nil }},
		&command{Name: "instructions", Desc: "Choose the instruction files loaded", Group: "Settings",
			Run: func(m *Model, _ string) tea.Cmd { m.openInstructions(); return nil }},
		&command{Name: "timeouts", Desc: "Command timeout for shell commands", Group: "Settings",
			Run: func(m *Model, _ string) tea.Cmd { m.openTimeouts(); return nil }},
		&command{Name: "watchdog", Desc: "Stream watchdog: first token, gaps between chunks, whole model call", Group: "Settings",
			Run: func(m *Model, _ string) tea.Cmd { m.openWatchdog(); return nil }},
		&command{Name: "hooks", Args: "[on|off <event>]", Desc: "Hooks on agent events; switch one off for this session", Group: "System",
			Run: func(m *Model, args string) tea.Cmd { return hookCommand(m, args) }},
		&command{Name: "privacy", Args: "[add|remove <pattern>]", Desc: "Paths no tool may touch", Group: "System",
			Run: func(m *Model, args string) tea.Cmd { return m.privacyCommand(strings.TrimSpace(args)) }},
	)
	wrapCompact()
}

// wrapCompact keeps /compact as it was and adds `/compact settings`, which
// opens the strategy picker. It runs after commands.go's registration, so the
// original command is in the table.
func wrapCompact() {
	var orig *command
	for _, c := range commandTable {
		if c.Name == "compact" {
			orig = c
		}
	}
	if orig == nil {
		return
	}
	overrideCommands(&command{Name: "compact", Args: "[settings]", Desc: orig.Desc, Group: orig.Group,
		Run: func(m *Model, args string) tea.Cmd {
			if strings.TrimSpace(args) == "settings" {
				m.openCompactStrategy()
				return nil
			}
			return orig.Run(m, args)
		}})
}

// flatWriter returns the backend's flat-key writer, or an error the caller
// shows.
func (m *Model) flatWriter() (ConfigSetter7, error) {
	w, ok := m.cur().be.(ConfigSetter7)
	if !ok {
		return nil, errors.New("this backend cannot save this setting")
	}
	return w, nil
}

// saveGlobalFlat writes one flat key to the global config and applies it to
// the open tabs where that is possible now.
func (m *Model) saveGlobalFlat(key string, value any) error {
	w, err := m.flatWriter()
	if err != nil {
		return err
	}
	return w.SetField7(key, value, false)
}

// diffStyleCommand is /diff-style [style]. With no argument it opens the
// picker, where Tab toggles collapsing.
func (m *Model) diffStyleCommand(arg string) tea.Cmd {
	if arg != "" {
		// A direct argument saves where the key lives, or to the scope word
		// given after it (spec 02 §1.2).
		style, word := splitScopeArg(arg)
		if style == "" {
			return cmdErrText("Usage: /diff-style [default|sidebyside|compact] [project|global]")
		}
		project := m.scopeIsProject(word, "diff_style")
		value := style
		if value == "default" {
			value = ""
		}
		if err := m.saveFlatIn("diff_style", value, project); err != nil {
			return cmdErr(err)
		}
		m.applyDiffSettings()
		return flashCmd("ok", "Diff style: "+diffStyleName(style)+" ("+scopeName(project)+")")
	}
	p := NewPicker(m.th, "Diff Style", "diff")
	p.KeepOpen = true
	p.Hint = "↑↓ move · Enter set · Tab toggle collapse · Esc close"
	rebuild := func() {
		c := fields7(m.cur().be)
		cur := diffStyleName(c.DiffStyle)
		collapse := config.Bool(c.CollapseDiffs, false)
		p.Items = []PickItem{
			{Value: "default", Label: "Default", Desc: "unified body, numbered, in a box", Current: cur == "default"},
			{Value: "sidebyside", Label: "Side by side", Desc: "old and new columns", Current: cur == "sidebyside"},
			{Value: "compact", Label: "Compact", Desc: "one line when collapsed", Current: cur == "compact"},
			{Value: "collapse", Label: "Collapse long diffs", Desc: onOffWord(collapse), Current: collapse},
		}
		p.Blurb = ""
		p.SetItems(p.Items)
	}
	p.OnSelect = func(it PickItem) tea.Cmd {
		c := fields7(m.cur().be)
		var err error
		if it.Value == "collapse" {
			err = m.saveGlobalFlat("collapse_diffs", !config.Bool(c.CollapseDiffs, false))
		} else {
			style := it.Value
			if style == "default" {
				style = ""
			}
			err = m.saveGlobalFlat("diff_style", style)
		}
		if err != nil {
			return cmdErr(err)
		}
		m.applyDiffSettings()
		rebuild()
		return nil
	}
	p.OnKey = func(key string) (tea.Cmd, bool) {
		if key != "tab" {
			return nil, false
		}
		c := fields7(m.cur().be)
		if err := m.saveGlobalFlat("collapse_diffs", !config.Bool(c.CollapseDiffs, false)); err != nil {
			return cmdErr(err), true
		}
		m.applyDiffSettings()
		rebuild()
		return nil, true
	}
	rebuild()
	m.pushModal(p)
	return nil
}

// chatStyleCommand is /chat-style [accent|bubble]. A direct argument saves to
// the project; with no argument the picker saves globally.
func (m *Model) chatStyleCommand(arg string) tea.Cmd {
	t := m.cur()
	switch arg {
	case "accent", "bubble":
		if err := t.be.SetConfig("chat_style", arg, true); err != nil {
			return cmdErr(err)
		}
		m.applyChatStyleSetting()
		return flashCmd("ok", "Chat style: "+arg+" (saved to the project)")
	case "":
	default:
		return cmdErrText("Usage: /chat-style [accent|bubble]")
	}
	cur := fmt.Sprint(t.be.Config()["chat_style"])
	p := NewPicker(m.th, "Chat Style", "chat")
	p.Items = []PickItem{
		{Value: "accent", Label: "Accent", Desc: "rails beside each message", Current: cur == "accent"},
		{Value: "bubble", Label: "Bubble", Desc: "messages in bubbles", Current: cur == "bubble"},
	}
	p.refilter()
	p.OnSelect = func(it PickItem) tea.Cmd {
		if err := t.be.SetConfig("chat_style", it.Value, false); err != nil {
			return cmdErr(err)
		}
		m.applyChatStyleSetting()
		return flashCmd("ok", "Chat style: "+it.Value)
	}
	m.pushModal(p)
	return nil
}

// vimCommand is /vim-hints [on|off]. It sets the vim key setting, which is
// what the hint text describes. No argument opens the choice.
func (m *Model) vimCommand(arg string) tea.Cmd {
	t := m.cur()
	on, _ := t.be.Config()["vim"].(bool)
	if arg != "" {
		word, scope := splitScopeArg(arg)
		if word != "on" && word != "off" {
			return cmdErrText("Usage: /vim-hints [on|off] [project|global]")
		}
		project := m.scopeIsProject(scope, "vim")
		if err := t.be.SetConfig("vim", word == "on", project); err != nil {
			return cmdErr(err)
		}
		return flashCmd("ok", "Vim keys "+onOff(word == "on")+" ("+scopeName(project)+")")
	}
	p := NewPicker(m.th, "Vim Hints", "keys")
	p.Items = []PickItem{
		{Value: "on", Label: "On", Desc: "vim keys in the input box", Current: on},
		{Value: "off", Label: "Off", Desc: "plain input", Current: !on},
	}
	p.refilter()
	p.OnSelect = func(it PickItem) tea.Cmd {
		if err := t.be.SetConfig("vim", it.Value == "on", false); err != nil {
			return cmdErr(err)
		}
		return flashCmd("ok", "Vim keys "+onOff(it.Value == "on"))
	}
	m.pushModal(p)
	return nil
}

// openAgentFeatures is the /agent-features picker. It stays open while toggled.
func (m *Model) openAgentFeatures() {
	p := NewPicker(m.th, "Agent Features", "gear")
	p.KeepOpen = true
	p.Hint = "↑↓ move · Enter toggle · Esc close"
	rebuild := func() {
		c := fields7(m.cur().be)
		p.Items = nil
		for _, f := range featureLabels {
			on := c.AgentFeatures.Enabled(f.key)
			p.Items = append(p.Items, PickItem{Value: f.key, Label: f.label, Desc: f.note, Current: on})
		}
		p.SetItems(p.Items)
	}
	p.OnSelect = func(it PickItem) tea.Cmd {
		c := fields7(m.cur().be)
		if err := m.saveGlobalFlat("agent_features."+it.Value, !c.AgentFeatures.Enabled(it.Value)); err != nil {
			return cmdErr(err)
		}
		rebuild()
		return flashCmd("info", it.Label+" saved; applies to the next session")
	}
	rebuild()
	m.pushModal(p)
}

// openInstructions is the /instructions picker: one row per source, toggled
// with Enter. It stays open.
func (m *Model) openInstructions() {
	p := NewPicker(m.th, "Instruction Files", "doc")
	p.KeepOpen = true
	p.Hint = "↑↓ move · Enter toggle · Esc close"
	rebuild := func() {
		c := fields7(m.cur().be)
		effective := c.Instructions
		if effective == nil {
			effective = agent.DefaultInstructionFiles
		}
		p.Items = nil
		for _, src := range agent.InstructionSources {
			p.Items = append(p.Items, PickItem{Value: src.ID, Label: src.Label, Desc: strings.Join(src.Files, ", "),
				Current: listIndex(effective, src.ID) >= 0})
		}
		p.SetItems(p.Items)
	}
	p.OnSelect = func(it PickItem) tea.Cmd {
		c := fields7(m.cur().be)
		next, err := nextInstructions(c.Instructions, it.Value)
		if err != nil {
			return cmdErr(err)
		}
		if err := m.cur().be.SetConfig("instructions", next, false); err != nil {
			return cmdErr(err)
		}
		rebuild()
		return flashCmd("info", "Instruction files saved; applies to the next session")
	}
	rebuild()
	m.pushModal(p)
}

// timeoutChoices are the presets the timeouts picker offers, in minutes. Zero
// means no limit; "custom" asks for a number.
var timeoutChoices = []struct {
	value, label string
}{
	{"0.5", "30 seconds"}, {"1", "1 minute"}, {"2", "2 minutes (default)"},
	{"5", "5 minutes"}, {"10", "10 minutes"}, {"30", "30 minutes"},
	{"0", "No limit"}, {"custom", "Custom…"},
}

// openTimeouts is the /timeouts picker. Values are global, as the settings
// screen saves them.
func (m *Model) openTimeouts() {
	p := NewPicker(m.th, "Command Timeout", "clock")
	p.Hint = "↑↓ move · Enter set · Esc close"
	c := fields7(m.cur().be)
	cur := strconv.FormatFloat(config.DefaultToolTimeout, 'f', -1, 64)
	if c.ToolTimeout != nil {
		cur = strconv.FormatFloat(*c.ToolTimeout, 'f', -1, 64)
	}
	for _, ch := range timeoutChoices {
		p.Items = append(p.Items, PickItem{Value: ch.value, Label: ch.label, Current: ch.value == cur})
	}
	p.refilter()
	p.OnSelect = func(it PickItem) tea.Cmd {
		if it.Value == "custom" {
			m.pushModal(NewTextPrompt(m.th, "Command timeout", "minutes, 0.5 to 30; 0 for no limit", false, func(v string) tea.Cmd {
				if err := m.saveGlobalFlat("tool_timeout", strings.TrimSpace(v)); err != nil {
					return cmdErr(err)
				}
				return flashCmd("ok", "Command timeout saved: "+strings.TrimSpace(v)+" min")
			}))
			return nil
		}
		f, err := strconv.ParseFloat(it.Value, 64)
		if err != nil {
			return cmdErr(err)
		}
		if err := m.saveGlobalFlat("tool_timeout", f); err != nil {
			return cmdErr(err)
		}
		return flashCmd("ok", "Command timeout: "+it.Label)
	}
	m.pushModal(p)
}

// privacyCommand lists, adds or removes forbidden path patterns. Patterns are
// saved to the project config, as a project's privacy rules belong to it.
func (m *Model) privacyCommand(args string) tea.Cmd {
	c := fields7(m.cur().be)
	verb, pat, _ := strings.Cut(args, " ")
	pat = strings.TrimSpace(pat)
	if verb == "session" {
		return privacySessionCommand(m, strings.TrimSpace(strings.TrimPrefix(args, verb)))
	}
	switch verb {
	case "":
		lines := []string{"Paths no file or shell tool may touch. Add with /privacy add <pattern>."}
		if len(c.Forbidden) == 0 {
			lines = append(lines, "", "No patterns yet.")
		}
		for _, f := range c.Forbidden {
			lines = append(lines, "  "+f)
		}
		m.openInfo("Forbidden Paths", "shield", lines)
		return nil
	case "add", "remove":
		if pat == "" {
			return cmdErrText("Usage: /privacy " + verb + " <pattern>")
		}
		var next []string
		if verb == "add" {
			if listIndex(c.Forbidden, pat) >= 0 {
				return flashCmd("info", pat+" is already forbidden")
			}
			next = append(append([]string(nil), c.Forbidden...), pat)
		} else {
			if listIndex(c.Forbidden, pat) < 0 {
				return cmdErrText(pat + " is not in the list")
			}
			for _, f := range c.Forbidden {
				if f != pat {
					next = append(next, f)
				}
			}
		}
		w, err := m.flatWriter()
		if err != nil {
			return cmdErr(err)
		}
		if err := w.SetField7("forbidden", next, true); err != nil {
			return cmdErr(err)
		}
		return flashCmd("ok", "Forbidden paths: "+verb+" "+pat+" (project)")
	}
	return cmdErrText("Usage: /privacy, /privacy add <pattern>, /privacy remove <pattern>")
}

// openCompactStrategy is the picker behind `/compact settings`.
func (m *Model) openCompactStrategy() {
	p := NewPicker(m.th, "Compaction", "compact")
	p.Hint = "↑↓ move · Enter set · Esc close"
	rebuild := func() {
		c := fields7(m.cur().be)
		cur := c.CompactStrategy
		if cur == "" {
			cur = "v2"
		}
		p.Items = []PickItem{
			{Value: "v2", Label: "v2", Desc: "summary plus the recent turns (default)", Current: cur == "v2"},
			{Value: "v1", Label: "v1", Desc: "the earlier single-summary strategy", Current: cur == "v1"},
			{Value: "disabled", Label: "Disabled", Desc: "never compact; the context fills", Current: cur == "disabled"},
		}
		p.SetItems(p.Items)
	}
	p.OnSelect = func(it PickItem) tea.Cmd {
		v := it.Value
		if v == "v2" {
			v = ""
		}
		if err := m.saveGlobalFlat("compact_strategy", v); err != nil {
			return cmdErr(err)
		}
		rebuild()
		return flashCmd("ok", "Compaction: "+it.Label+"; applies to the next session")
	}
	p.KeepOpen = true
	rebuild()
	m.pushModal(p)
}
