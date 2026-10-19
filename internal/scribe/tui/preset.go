package tui

// /preset lists the presets (config.ListPresets) or applies one. Applying
// goes through Backend.SetConfig, one key at a time, so it takes the same path
// as the settings screen: the preset's own scope (global or project) is where
// the values are saved, and the open session picks them up.

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func init() {
	overrideCommands(&command{Name: "preset", Args: "[name...] | remove <name...>", Desc: "List presets, apply one or more, or remove presets", Group: "Settings",
		Run: func(m *Model, args string) tea.Cmd { return runPreset(m, strings.TrimSpace(args)) }})
}

// presetWrite is one SetConfig call a preset turns into.
type presetWrite struct {
	key   string
	value any
}

func runPreset(m *Model, args string) tea.Cmd {
	t := m.cur()
	m.enterChat()
	root := t.be.Workdir()
	names := presetNames(args)
	if len(names) == 0 {
		listPresets(t.chat.AddSystem, root)
		return nil
	}
	if rm, ok := presetRemoveArgs(args); ok {
		removePresets(t, root, rm)
		return nil
	}
	// Several names apply in the order given; a bad name is reported and the
	// others still apply.
	for _, name := range names {
		applyOnePreset(m, t, root, name)
	}
	return nil
}

// presetNames splits the /preset argument into preset names. Commas and
// spaces both separate names, so "a, b" and "a b" mean the same thing.
func presetNames(args string) []string {
	return strings.FieldsFunc(args, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
}

// presetRemoveArgs reports whether args is "remove NAME..." (or rm, delete)
// and returns the names to remove. A bare "remove" is not a removal.
func presetRemoveArgs(args string) ([]string, bool) {
	words := presetNames(args)
	if len(words) < 2 {
		return nil, false
	}
	switch strings.ToLower(words[0]) {
	case "remove", "rm", "delete":
		return words[1:], true
	}
	return nil, false
}

// removePresets deletes the named preset files in the scope each one is
// defined in. A global copy of a name that is also a project preset stays.
func removePresets(t *tab, root string, names []string) {
	for _, name := range names {
		p, err := config.FindPreset(root, name)
		if err != nil {
			t.chat.AddSystem("error", err.Error())
			continue
		}
		// Hooks and servers the preset wrote come out first, so a removed
		// preset does not keep running commands it no longer offers.
		reverted, err := config.RevertPresetCommands(root, p)
		if err != nil {
			t.chat.AddSystem("error", fmt.Sprintf("preset %s: its hooks and MCP servers were not all removed: %v", name, err))
		}
		if err := config.RemovePreset(root, p); err != nil {
			t.chat.AddSystem("error", "preset "+name+" was not removed: "+err.Error())
			continue
		}
		msg := fmt.Sprintf("Removed preset %s (%s file %s)", name, p.Scope(), p.Path)
		if !reverted.Empty() {
			msg += fmt.Sprintf("; took back %s it had added to %s config", presetCommandCounts(reverted), p.Scope())
		}
		t.chat.AddSystem("info", msg)
	}
}

// applyOnePreset applies one named preset through the backend, one key at a
// time, and reports what it did and what it could not set. Hooks and MCP
// servers do not go through the backend's flat keys. They are added only
// after the user confirms (see askPresetCommands).
func applyOnePreset(m *Model, t *tab, root, name string) {
	p, err := config.FindPreset(root, name)
	if err != nil {
		t.chat.AddSystem("info", err.Error())
		return
	}
	cur := t.be.Config()
	cur["disabled_tools"] = disabledToolNames(t.be.Tools())
	writes := presetWrites(p.Config, cur)
	var failed []string
	for _, w := range writes {
		if err := presetSet(t, w, p.Project); err != nil {
			failed = append(failed, w.key+": "+err.Error())
		}
	}
	for _, f := range failed {
		t.chat.AddSystem("error", "preset "+name+" did not apply "+f)
	}
	if len(failed) == 0 {
		t.chat.AddSystem("info", fmt.Sprintf("Applied preset %s: %d settings saved to %s config", name, len(writes), p.Scope()))
	}
	askPresetCommands(m, t, root, p)
}

// presetFlatKeys are the preset keys that the settings writer (SetField7)
// owns rather than SetConfig. The settings writer is the one that reloads
// them the same way the settings screen does.
var presetFlatKeys = map[string]bool{"deferred_tools": true, "auto_check": true, "disabled_tools": true}

// presetSet writes one preset key to the preset's scope.
func presetSet(t *tab, w presetWrite, project bool) error {
	if !presetFlatKeys[w.key] {
		return t.be.SetConfig(w.key, w.value, project)
	}
	ws, ok := t.be.(ConfigSetter7)
	if !ok {
		return fmt.Errorf("this backend cannot save %s", w.key)
	}
	return ws.SetField7(w.key, w.value, project)
}

// disabledToolNames is the list of tools the session has turned off.
func disabledToolNames(tools []ToolInfo) []string {
	var out []string
	for _, t := range tools {
		if !t.Enabled {
			out = append(out, t.Name)
		}
	}
	return out
}

// askPresetCommands shows the hooks and MCP servers that applying p would add
// to its scope's config, and writes them only if the user says yes. Nothing is
// added when the preset has none that are new.
func askPresetCommands(m *Model, t *tab, root string, p config.Preset) {
	cmds, err := config.PendingPresetCommands(root, p)
	if err != nil {
		t.chat.AddSystem("error", fmt.Sprintf("preset %s did not apply its hooks or MCP servers: %v", p.Name, err))
		return
	}
	if cmds.Empty() {
		return
	}
	t.chat.AddSystem("info", fmt.Sprintf("Preset %s also adds hooks or MCP servers; they are added only if you confirm.", p.Name))
	m.pushModal(presetCommandConfirm(m.th, p, cmds,
		func() tea.Cmd {
			writePresetCommands(t, root, p)
			return nil
		},
		func() {
			t.chat.AddSystem("info", fmt.Sprintf("Skipped %s from preset %s. Run /preset %s again to be asked again.", presetCommandCounts(cmds), p.Name, p.Name))
		}))
}

// writePresetCommands writes the confirmed hooks and MCP servers. Hooks and
// servers are read from the config file when a session starts, so they take
// effect in the next session.
func writePresetCommands(t *tab, root string, p config.Preset) {
	before, err := config.LoadPresetScope(root, p)
	if err != nil {
		t.chat.AddSystem("error", fmt.Sprintf("preset %s did not add its hooks or MCP servers: %v", p.Name, err))
		return
	}
	added, err := config.SavePresetCommands(root, p)
	if err != nil {
		t.chat.AddSystem("error", fmt.Sprintf("preset %s did not add its hooks or MCP servers: %v", p.Name, err))
		return
	}
	// Without the record, removing the preset could not tell its hooks from
	// the user's, so say so rather than fail silently.
	if err := config.RecordPresetCommands(root, p, before, added); err != nil {
		t.chat.AddSystem("error", fmt.Sprintf("preset %s: its hooks and MCP servers will not be removed with it: %v", p.Name, err))
	}
	t.chat.AddSystem("info", fmt.Sprintf("Added %s from preset %s to %s config. They start with the next session.", presetCommandCounts(added), p.Name, p.Scope()))
}

// presetCommandCounts is "N hooks and M MCP servers", with the singular where
// it applies.
func presetCommandCounts(c config.PresetCommands) string {
	return presetPlural(len(c.Hooks), "hook") + " and " + presetPlural(len(c.MCP), "MCP server")
}

func presetPlural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// presetCommandConfirm is the dialog that asks before a preset adds hooks or
// MCP servers. The No answer is the default, and the frame is red because a
// hook or a server runs a command on this machine.
func presetCommandConfirm(th theme.Theme, p config.Preset, c config.PresetCommands, onYes func() tea.Cmd, onNo func()) *listConfirm {
	intro := fmt.Sprintf("Preset %s would add these to the %s config. A hook runs a shell command on agent events. An MCP server starts a process, or connects to a URL, with your environment. Nothing is added unless you say yes.", p.Name, p.Scope())
	if presetHasEveryToolHook(c) {
		intro += " At least one hook runs on every tool call."
	}
	lc := newListConfirm(th, "Add hooks and MCP servers from preset "+p.Name+"?", intro, presetCommandLines(c), true, onYes)
	lc.WithLabels("Add them", "Skip")
	lc.OnNo(onNo)
	return lc
}

// presetCommandLines describes each hook and server in one line each, with
// the command that will run.
func presetCommandLines(c config.PresetCommands) []string {
	var lines []string
	for _, h := range c.Hooks {
		line := "hook " + h.Event
		switch {
		case h.Matcher != "":
			line += " matching " + h.Matcher
		case h.If != "":
			line += " when " + h.If
		case isToolEvent(h.Event):
			line += " (every tool call)"
		}
		lines = append(lines, line+": "+h.Command)
	}
	for _, m := range c.MCP {
		line := "MCP server " + m.Name + " ("
		if m.Command != "" {
			line += "runs " + strings.Join(append([]string{m.Command}, m.Args...), " ")
		} else {
			line += "connects to " + m.URL
		}
		line += ")"
		if m.Disabled {
			line += " [disabled]"
		}
		if c.Replaced[m.Name] {
			line += " replaces the one already there"
		}
		lines = append(lines, line)
	}
	return lines
}

// presetHasEveryToolHook reports whether a hook would run on every tool call:
// a tool event with no matcher and no condition.
func presetHasEveryToolHook(c config.PresetCommands) bool {
	for _, h := range c.Hooks {
		if isToolEvent(h.Event) && h.Matcher == "" && h.If == "" {
			return true
		}
	}
	return false
}

func isToolEvent(event string) bool {
	return event == "PreToolUse" || event == "PostToolUse"
}

// listPresets prints the presets visible from root, and any file that cannot
// be used. Each line is one system row.
func listPresets(add func(kind, text string), root string) {
	ps, errs := config.ListPresets(root)
	if len(ps) == 0 && len(errs) == 0 {
		add("info", "No presets. Add one as JSON in "+config.PresetDir()+" (or .scribe/presets/ in this project).")
		return
	}
	// One row for the whole list: a row per preset would repeat the
	// "System" heading above every line.
	if len(ps) > 0 {
		lines := []string{"Presets. /preset NAME applies one."}
		for _, p := range ps {
			lines = append(lines, fmt.Sprintf("  %s  (%s)  %s", p.Name, p.Scope(), p.Path))
		}
		add("info", strings.Join(lines, "\n"))
	}
	for _, e := range errs {
		add("error", e.Error())
	}
}

// presetWrites maps a preset onto the flat setting keys the backend accepts.
// Scalars overwrite. Lists follow Merge: allow, deny and disabled tools add
// to what is there, and fallback replaces it.
func presetWrites(p config.Config, cur map[string]any) []presetWrite {
	var writes []presetWrite
	add := func(k string, v any) { writes = append(writes, presetWrite{k, v}) }
	if p.Model != "" {
		add("model", p.Model)
	}
	if p.Mode != "" {
		add("mode", p.Mode)
	}
	if p.Theme != "" {
		add("theme", p.Theme)
	}
	if p.Reasoning != "" {
		add("reasoning", p.Reasoning)
	}
	if p.MaxTokens != 0 {
		add("max_tokens", p.MaxTokens)
	}
	if p.ChatStyle != "" {
		add("chat_style", p.ChatStyle)
	}
	if p.CompactAt != 0 {
		add("compact_at", p.CompactAt)
	}
	if p.SearchURL != "" {
		add("search_url", p.SearchURL)
	}
	if p.Instructions != nil {
		add("instructions", p.Instructions)
	}
	for _, b := range []struct {
		key string
		v   *bool
	}{
		{"nerd_font", p.NerdFont}, {"show_reasoning", p.ShowReasoning}, {"verbose", p.Verbose},
		{"vim", p.Vim}, {"repo_map", p.RepoMap}, {"auto_compact", p.AutoCompact},
		{"memory", p.Memory}, {"checkpoints", p.Checkpoints},
	} {
		if b.v != nil {
			add(b.key, *b.v)
		}
	}
	if len(p.Allow) > 0 {
		add("allow", unionStrings(stringsOf(cur["allow"]), p.Allow))
	}
	if len(p.Deny) > 0 {
		add("deny", unionStrings(stringsOf(cur["deny"]), p.Deny))
	}
	r := p.Router
	for _, s := range []struct{ key, v string }{
		{"router.explore", r.Explore}, {"router.code", r.Code}, {"router.web_search", r.WebSearch},
		{"router.compact", r.Compact}, {"router.title", r.Title},
	} {
		if s.v != "" {
			add(s.key, s.v)
		}
	}
	if r.Fallback != nil {
		add("router.fallback", r.Fallback)
	}
	// Providers go through the same provider.add write the settings screen
	// uses: one write per provider, upserting by id. A built-in id is refused
	// there and reported as a failed write.
	for _, pr := range p.Providers {
		add("provider.add", map[string]any{"id": pr.ID, "name": pr.Name, "base_url": pr.BaseURL, "api_key_env": pr.APIKeyEnv})
	}
	if r.MaxAgents != 0 {
		add("router.max_agents", r.MaxAgents)
	}
	providers := make([]string, 0, len(p.ReasoningByProvider))
	for id := range p.ReasoningByProvider {
		providers = append(providers, id)
	}
	sort.Strings(providers)
	for _, id := range providers {
		rs := p.ReasoningByProvider[id]
		prefix := reasoningKeyPrefix + id + "."
		if rs.Effort != "" {
			add(prefix+"effort", rs.Effort)
		}
		if rs.Budget != 0 {
			add(prefix+"budget", rs.Budget)
		}
		if rs.ServiceTier != "" {
			add(prefix+"service_tier", rs.ServiceTier)
		}
		if rs.Verbosity != "" {
			add(prefix+"verbosity", rs.Verbosity)
		}
		if rs.Exclude {
			add(prefix+"exclude", rs.Exclude)
		}
	}
	if p.DeferredTools != nil {
		add("deferred_tools", *p.DeferredTools)
	}
	if p.AutoCheck != nil {
		add("auto_check", *p.AutoCheck)
	}
	if len(p.DisabledTools) > 0 {
		add("disabled_tools", unionStrings(stringsOf(cur["disabled_tools"]), p.DisabledTools))
	}
	return writes
}

// stringsOf reads a []string from a Config() value, which may be nil.
func stringsOf(v any) []string {
	s, _ := v.([]string)
	return s
}

// unionStrings returns base followed by the items of add that base lacks.
func unionStrings(base, add []string) []string {
	out := append([]string(nil), base...)
	for _, a := range add {
		if listIndex(out, a) < 0 {
			out = append(out, a)
		}
	}
	return out
}
