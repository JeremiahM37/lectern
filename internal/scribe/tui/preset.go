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
)

func init() {
	overrideCommands(&command{Name: "preset", Args: "[name]", Desc: "List presets, or apply one", Group: "Settings",
		Run: func(m *Model, args string) tea.Cmd { return runPreset(m, strings.TrimSpace(args)) }})
}

// presetWrite is one SetConfig call a preset turns into.
type presetWrite struct {
	key   string
	value any
}

func runPreset(m *Model, name string) tea.Cmd {
	t := m.cur()
	m.enterChat()
	root := t.be.Workdir()
	if name == "" {
		listPresets(t.chat.AddSystem, root)
		return nil
	}
	p, err := config.FindPreset(root, name)
	if err != nil {
		t.chat.AddSystem("info", err.Error())
		return nil
	}
	writes, skipped := presetWrites(p.Config, t.be.Config())
	var failed []string
	for _, w := range writes {
		if err := t.be.SetConfig(w.key, w.value, p.Project); err != nil {
			failed = append(failed, w.key+": "+err.Error())
		}
	}
	for _, f := range failed {
		t.chat.AddSystem("error", "preset "+name+" did not apply "+f)
	}
	if len(failed) == 0 {
		t.chat.AddSystem("info", fmt.Sprintf("Applied preset %s (%d settings, saved to %s config)", name, len(writes), p.Scope()))
	}
	if len(skipped) > 0 {
		t.chat.AddSystem("info", fmt.Sprintf("Not applied here: %s. Edit %s to change them.", strings.Join(skipped, ", "), p.Path))
	}
	return nil
}

// listPresets prints the presets visible from root, and any file that cannot
// be used. Each line is one system row.
func listPresets(add func(kind, text string), root string) {
	ps, errs := config.ListPresets(root)
	if len(ps) == 0 && len(errs) == 0 {
		add("info", "No presets. Add one as JSON in "+config.PresetDir()+" (or .scribe/presets/ in this project).")
		return
	}
	add("info", "Presets. /preset NAME applies one.")
	for _, p := range ps {
		add("info", fmt.Sprintf("  %s  (%s)  %s", p.Name, p.Scope(), p.Path))
	}
	for _, e := range errs {
		add("error", e.Error())
	}
}

// presetWrites maps a preset onto the flat setting keys the backend accepts.
// Scalars overwrite. Lists follow Merge: allow and deny add to what is there,
// and fallback replaces it. Fields with no flat key are returned as skipped.
func presetWrites(p config.Config, cur map[string]any) (writes []presetWrite, skipped []string) {
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
	}
	for _, s := range []struct {
		name string
		set  bool
	}{
		{"providers", len(p.Providers) > 0}, {"hooks", len(p.Hooks) > 0}, {"mcp servers", len(p.MCP) > 0},
		{"disabled tools", len(p.DisabledTools) > 0}, {"deferred tools", p.DeferredTools != nil},
		{"auto-check", p.AutoCheck != nil},
	} {
		if s.set {
			skipped = append(skipped, s.name)
		}
	}
	return writes, skipped
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
