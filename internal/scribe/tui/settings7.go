package tui

// Settings rows for the wave 5 options: diff style, compaction, routing slots,
// agent features, retries and timeouts, the editor, search keys, privacy,
// instructions and hooks. The rows read the merged config and write through
// the optional capabilities below, so the Backend interface is unchanged.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/registry"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/nvim"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/chat"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// ConfigFields7 reads the merged settings (project over global).
type ConfigFields7 interface {
	MergedConfig7() config.Config
}

// ConfigSetter7 writes one flat key (see config.SetField) to a scope, and moves
// one between scopes.
type ConfigSetter7 interface {
	SetField7(key string, value any, project bool) error
	MoveField7(key string, value any, toProject bool) error
}

// SecretStore7 stores the search keys (Brave, Jina) in the key store. The
// settings screen never receives a key value back: SecretSource says only
// where a key comes from.
type SecretStore7 interface {
	SetSecret(id, value string) error
	RemoveSecret(id string) error
	SecretSource(id string) string
}

// fields7 returns the merged settings, or the zero config when the backend
// cannot read them (rows then show the defaults).
func fields7(be Backend) config.Config {
	if f, ok := be.(ConfigFields7); ok {
		return f.MergedConfig7()
	}
	return config.Config{}
}

// is7Key reports whether key is a flat key owned by config.SetField.
func is7Key(key string) bool {
	probe := config.Config{}
	return probe.ClearField(key)
}

// appliesLater reports whether a saved flat key takes effect in the next
// session. Diff style, collapse and the theme style are applied to the open
// tabs at once; the rest are read when a session starts.
func appliesLater(key string) bool {
	return key != "diff_style" && key != "collapse_diffs" && !isThemeStyleKey(key)
}

// setFlat7 saves one flat key to the chosen scope and applies what can be
// applied now.
func (s *settings) setFlat7(key string, value any) tea.Cmd {
	w, ok := s.be.(ConfigSetter7)
	if !ok {
		s.say("err", "this build cannot save "+key)
		return nil
	}
	if err := w.SetField7(key, value, s.project); err != nil {
		s.say("err", err.Error())
		return nil
	}
	if key == "diff_style" || key == "collapse_diffs" {
		s.applyDiffs()
	}
	if isThemeStyleKey(key) {
		s.m.applyThemeStyle()
	}
	if key == "chat_style" {
		s.m.applyChatStyleSetting()
	}
	msg := key + " saved to " + s.scope() + " config"
	if appliesLater(key) {
		msg += "; applies to the next session"
	}
	s.say("ok", msg)
	return nil
}

// applyDiffs gives every open tab the saved diff style and collapse setting.
func (s *settings) applyDiffs() { s.m.applyDiffSettings() }

// applyDiffSettings gives every open tab the saved diff style and collapse
// setting. Commands and the settings screen both use it.
func (m *Model) applyDiffSettings() {
	c := fields7(m.cur().be)
	style := chat.DiffDefault
	if c.DiffStyle != "" {
		style = c.DiffStyle
	}
	for _, t := range m.tabs {
		t.copt.DiffStyle = style
		t.copt.CollapseDiffs = config.Bool(c.CollapseDiffs, false)
		t.chat = rebuildTranscript(t.chat, t.copt, m.width, m.chatRows())
	}
}

// applyChatStyleSetting gives every open tab the saved chat style.
func (m *Model) applyChatStyleSetting() {
	style := fmt.Sprint(m.cur().be.Config()["chat_style"])
	for _, t := range m.tabs {
		t.copt.ChatStyle = style
		t.chat = rebuildTranscript(t.chat, t.copt, m.width, m.chatRows())
	}
}

// flagRow7 is an on/off flat key.
func (s *settings) flagRow7(label, key string, on bool, note string) setRow {
	return setRow{label: label, value: onOffWord(on), note: note, key: key, raw: on, toggle: true, on: on,
		act: func() tea.Cmd { return s.setFlat7(key, !on) }}
}

func onOffWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// pickRow7 cycles a flat key through opts on Enter. The first option is the
// default and is stored as the empty string.
func (s *settings) pickRow7(label, key, cur string, opts []string, note string) setRow {
	shown := cur
	if shown == "" {
		shown = opts[0]
	}
	return setRow{label: label, value: shown, note: note, key: key, raw: cur, act: func() tea.Cmd {
		next := opts[(listIndex(opts, shown)+1+len(opts))%len(opts)]
		if next == opts[0] {
			next = ""
		}
		return s.setFlat7(key, next)
	}}
}

// textRow7 edits a text flat key. Empty clears it.
func (s *settings) textRow7(label, key, cur, placeholder, note string) setRow {
	shown := cur
	if shown == "" {
		shown = "default"
	}
	return setRow{label: label, value: shown, note: note, key: key, raw: cur, act: func() tea.Cmd {
		return s.prompt(label, placeholder, cur, func(v string) tea.Cmd {
			return s.setFlat7(key, strings.TrimSpace(v))
		})
	}}
}

// numRow7 edits a whole-number flat key. 0 is shown as the default.
func (s *settings) numRow7(label, key string, cur int, note string) setRow {
	shown := strconv.Itoa(cur)
	if cur == 0 {
		shown = "default"
	}
	return setRow{label: label, value: shown, note: note, key: key, raw: cur, act: func() tea.Cmd {
		return s.prompt(label, "a whole number, 0 for the default", strconv.Itoa(cur), func(v string) tea.Cmd {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 {
				s.say("err", "enter a whole number, 0 for the default")
				return nil
			}
			return s.setFlat7(key, n)
		})
	}}
}

// listRow7 is a heading that adds an entry to a flat list key, with one row
// per entry that deletes it. The list is replaced whole on each change.
func (s *settings) listRow7(title, key string, items []string, placeholder string) []setRow {
	add := func() tea.Cmd {
		return s.prompt(title, placeholder, "", func(v string) tea.Cmd {
			v = strings.TrimSpace(v)
			if v == "" || listIndex(items, v) >= 0 {
				return nil
			}
			return s.setFlat7(key, append(append([]string(nil), items...), v))
		})
	}
	rows := []setRow{{label: title, value: strconv.Itoa(len(items)), header: true, act: add, add: add}}
	for i := range items {
		i := i
		rows = append(rows, setRow{label: items[i], value: "", add: add,
			del: func() tea.Cmd {
				return s.setFlat7(key, append(append([]string(nil), items[:i]...), items[i+1:]...))
			}})
	}
	return rows
}

// ---------------------------------------------------------------- Appearance

// diffRows are the diff settings shown under the themes.
func (s *settings) diffRows() []setRow {
	c := fields7(s.be)
	rows := []setRow{
		{label: "Diff style", value: diffStyleName(c.DiffStyle), note: "default, side by side, or compact one-liners",
			key: "diff_style", raw: c.DiffStyle, act: func() tea.Cmd {
				next := []string{"default", "sidebyside", "compact"}
				cur := c.DiffStyle
				if cur == "" {
					cur = "default"
				}
				v := next[(listIndex(next, cur)+1)%len(next)]
				if v == "default" {
					v = ""
				}
				return s.setFlat7("diff_style", v)
			}},
		{label: "Collapse long diffs", value: onOffWord(config.Bool(c.CollapseDiffs, false)), toggle: true,
			on: config.Bool(c.CollapseDiffs, false), note: "fold edits in the chat until opened", key: "collapse_diffs",
			raw: config.Bool(c.CollapseDiffs, false),
			act: func() tea.Cmd { return s.setFlat7("collapse_diffs", !config.Bool(c.CollapseDiffs, false)) }},
	}
	return append(rows, s.styleRows()...)
}

// styleRows are the theme style rows under the themes: transparency, the
// message and diff background opacity, and the border strength. Each saves a
// flat key and re-applies the palette at once.
func (s *settings) styleRows() []setRow {
	c := fields7(s.be)
	msg, diff := 100, 100
	if c.ThemeMessageOpacity != nil {
		msg = *c.ThemeMessageOpacity
	}
	if c.ThemeDiffOpacity != nil {
		diff = *c.ThemeDiffOpacity
	}
	border := c.ThemeBorder
	if border == "" {
		border = "default"
	}
	on := config.Bool(c.ThemeTransparent, false)
	return []setRow{
		s.flagRow7("Transparent background", "theme_transparent", on, "let the terminal show through the app background"),
		s.opacityRow7("Message background", "theme_message_opacity", msg, "percent of the user-message fill kept; needs transparent"),
		s.opacityRow7("Diff background", "theme_diff_opacity", diff, "percent of the diff fills kept; needs transparent"),
		{label: "Border strength", value: border, note: "default, strong or op (brighter borders and faint text)",
			key: "theme_border", raw: border, act: func() tea.Cmd {
				next := config.ThemeBorders[(listIndex(config.ThemeBorders, border)+1)%len(config.ThemeBorders)]
				if next == config.ThemeBorders[0] {
					next = ""
				}
				return s.setFlat7("theme_border", next)
			}},
	}
}

// opacityRow7 cycles a percentage through theme.OpacitySteps on Enter.
func (s *settings) opacityRow7(label, key string, cur int, note string) setRow {
	steps := theme.OpacitySteps
	next := steps[0]
	for i, v := range steps {
		if v == cur && i+1 < len(steps) {
			next = steps[i+1]
		}
	}
	return setRow{label: label, value: strconv.Itoa(cur) + "%", note: note, key: key, raw: cur,
		act: func() tea.Cmd { return s.setFlat7(key, next) }}
}

func diffStyleName(v string) string {
	if v == "" {
		return "default"
	}
	return v
}

// ---------------------------------------------------------------- Router extras

// routerSlotRows are the desloppify, verify and semantic slots, and the
// default slot. The notes say which ones the agent does not route yet.
func (s *settings) routerSlotRows() []setRow {
	c := fields7(s.be)
	slot := func(label, key, cur, note string) setRow {
		val := cur
		if val == "" {
			val = "inherit default"
		}
		return setRow{label: label, value: val, note: note, key: key, raw: cur,
			act: func() tea.Cmd {
				return s.pickModel(label, cur, true, func(ref string) tea.Cmd { return s.setFlat7(key, ref) })
			},
			del: func() tea.Cmd { return s.setFlat7(key, "") }}
	}
	return []setRow{
		slot("Default slot", "router.default", c.Router.Default, "used by every slot left empty"),
		slot("De-sloppify pass", "router.desloppify", c.Router.Desloppify, "cleanup after edits; not routed yet"),
		slot("Verify review", "router.verify", c.Router.Verify, "LLM review of edits; not routed yet"),
		slot("Semantic summaries", "router.semantic", c.Router.Semantic, "code-map summaries (else compact, then explore)"),
	}
}

// fallbackRows lists the per-model fallback chains and the context window
// overrides, each with an add row.
func (s *settings) fallbackRows() []setRow {
	c := fields7(s.be)
	var rows []setRow
	addChain := func() tea.Cmd {
		var ref string
		chain := NewTextPrompt(s.m.th, "Fallback chain for this model", "provider/model, provider/model", false, func(v string) tea.Cmd {
			return s.setFlat7(keyFallbackByModelPrefix+ref, v)
		})
		first := NewTextPrompt(s.m.th, "Model to fall back from", "provider/model", false, func(v string) tea.Cmd {
			ref = strings.TrimSpace(v)
			if !strings.Contains(ref, "/") {
				s.say("err", "the model must be provider/model")
				return nil
			}
			s.m.pushModal(chain)
			return nil
		})
		s.m.pushModal(first)
		return nil
	}
	rows = append(rows, setRow{label: "Per-model fallbacks", value: strconv.Itoa(len(c.Router.FallbackByModel)), header: true,
		note: "a chain for one model, tried before the general one", add: addChain, act: addChain})
	for _, ref := range sortedKeys(c.Router.FallbackByModel) {
		ref := ref
		chain := c.Router.FallbackByModel[ref]
		rows = append(rows, setRow{label: ref, value: strings.Join(chain, ", "), key: keyFallbackByModelPrefix + ref, raw: chain,
			act: func() tea.Cmd {
				return s.prompt(ref, "provider/model, provider/model", strings.Join(chain, ", "), func(v string) tea.Cmd {
					return s.setFlat7(keyFallbackByModelPrefix+ref, v)
				})
			},
			del: func() tea.Cmd { return s.setFlat7(keyFallbackByModelPrefix+ref, "") }})
	}
	addWindow := func() tea.Cmd {
		var ref string
		size := NewTextPrompt(s.m.th, "Context window in tokens", "a whole number, 0 removes it", false, func(v string) tea.Cmd {
			return s.setFlat7(keyContextWindowPrefix+ref, strings.TrimSpace(v))
		})
		first := NewTextPrompt(s.m.th, "Context window: model", "provider/model", false, func(v string) tea.Cmd {
			ref = strings.TrimSpace(v)
			if !strings.Contains(ref, "/") {
				s.say("err", "the model must be provider/model")
				return nil
			}
			s.m.pushModal(size)
			return nil
		})
		s.m.pushModal(first)
		return nil
	}
	rows = append(rows, setRow{label: "Context window overrides", value: strconv.Itoa(len(c.ContextWindowOverrides)), header: true,
		note: "for models whose window is wrong", add: addWindow, act: addWindow})
	for _, ref := range sortedKeysInt(c.ContextWindowOverrides) {
		ref := ref
		n := c.ContextWindowOverrides[ref]
		rows = append(rows, setRow{label: ref, value: strconv.Itoa(n) + " tokens", key: keyContextWindowPrefix + ref, raw: n,
			act: func() tea.Cmd {
				return s.prompt(ref, "a whole number, 0 removes it", strconv.Itoa(n), func(v string) tea.Cmd {
					return s.setFlat7(keyContextWindowPrefix+ref, strings.TrimSpace(v))
				})
			},
			del: func() tea.Cmd { return s.setFlat7(keyContextWindowPrefix+ref, "0") }})
	}
	return rows
}

const (
	keyFallbackByModelPrefix = "router.fallback_by_model."
	keyContextWindowPrefix   = "context_window_overrides."
)

// ---------------------------------------------------------------- Agent tab

// featureLabels names each agent feature in the order the settings show them.
var featureLabels = []struct {
	key, label, note string
}{
	{"desloppify", "De-sloppify after edits", "a cleanup pass; default off"},
	{"verify_edits", "Review edits with a model", "LLM review of each edit; default off"},
	{"tier_routing", "Tier routing", "cheaper models for simple steps; default on"},
	{"dispatch_cache", "Reuse subagent results", "default on"},
	{"target_file_validation", "Check edit targets", "refuse edits to files that do not exist; default on"},
	{"agent_skills", "Offer skills to the agent", "default on"},
	{"on_demand_tools", "Load tools on request", "keep tool lists short; default off"},
}

// agentRows are the agent feature toggles, compaction, retries and timeouts.
func (s *settings) agentRows() []setRow {
	c := fields7(s.be)
	var rows []setRow
	rows = append(rows, setRow{label: "Agent features", header: true})
	for _, f := range featureLabels {
		on := c.AgentFeatures.Enabled(f.key)
		key := "agent_features." + f.key
		rows = append(rows, s.flagRow7(f.label, key, on, f.note))
	}
	rows = append(rows, setRow{label: "Compaction", header: true})
	rows = append(rows, s.pickRow7("Compaction strategy", "compact_strategy", c.CompactStrategy,
		[]string{"v2", "v1", "disabled"}, "v2 is the default; disabled never compacts"))
	rows = append(rows, setRow{label: "Retries and timeouts", header: true})
	rows = append(rows, s.numRow7("Transient retries", "retry.transient", c.Retry.Transient,
		"rate limits and network errors; 0 keeps the default (3)"))
	rows = append(rows, s.numRow7("Stall retries", "retry.stall", c.Retry.Stall,
		"a stream that stops; 0 keeps the default"))
	rows = append(rows, s.numRow7("Messages kept when compacting", "compact_keep_recent", c.CompactKeepRecent,
		"the newest messages kept verbatim; 0 keeps the default"))
	rows = append(rows, setRow{label: "Tools", header: true})
	rows = append(rows, s.flagRow7("Format on edit", "format_on_edit", config.Bool(c.FormatOnEdit, true),
		"run the project formatter on files an edit changed"))
	rows = append(rows, s.flagRow7("Deferred tools", "deferred_tools", config.Bool(c.DeferredTools, true),
		"send only core tools each step; others load on demand"))
	rows = append(rows, s.numRow7("Concurrent subagents", "router.max_agents", c.Router.MaxAgents,
		"2 to 8; 0 keeps the default (3)"))
	rows = append(rows, s.toolTimeoutRow(c))
	if row, ok := s.chatBudgetRow(); ok {
		rows = append(rows, setRow{label: "Budget", header: true})
		rows = append(rows, row)
	}
	return rows
}

// chatBudgetRow sets this chat's daily token budget. It saves through
// ChatBudgeter.SetChatBudget, the same path /budget chat uses, and not through
// setFlat7: the value is not a config.SetField key. The row is absent when the
// backend has no per-chat budget.
func (s *settings) chatBudgetRow() (setRow, bool) {
	cb, ok := chatBudgetOf(s.be)
	if !ok {
		return setRow{}, false
	}
	shown := "no limit"
	cur := ""
	if cb.Limit > 0 {
		shown = render.HumanTokens(cb.Limit)
		cur = strconv.Itoa(cb.Limit)
	}
	return setRow{label: "This chat per day", value: shown, raw: cb.Limit,
		note: "tokens this chat may spend each local day, on top of the daily budget",
		act: func() tea.Cmd {
			return s.prompt("This chat per day", "tokens, for example 500k; 0 or off for no limit", cur, func(v string) tea.Cmd {
				return s.setChatBudget(strings.TrimSpace(v))
			})
		}}, true
}

// setChatBudget parses a typed per-chat budget and saves it. A parse error is
// shown in the settings footer and nothing is saved.
func (s *settings) setChatBudget(v string) tea.Cmd {
	cb, ok := s.be.(ChatBudgeter)
	if !ok {
		s.say("err", "this build cannot save the per-chat budget")
		return nil
	}
	n := 0
	switch strings.ToLower(v) {
	case "", "0", "off", "none", "no", "unlimited":
	default:
		parsed, err := parseTokenCount(v)
		if err != nil {
			s.say("err", "enter tokens, for example 500k, or 0 for no limit")
			return nil
		}
		n = parsed
	}
	if err := cb.SetChatBudget(n); err != nil {
		s.say("err", err.Error())
		return nil
	}
	if n == 0 {
		s.say("ok", "per-chat daily budget off")
	} else {
		s.say("ok", "per-chat daily budget set to "+render.HumanTokens(n))
	}
	return nil
}

// toolTimeoutRow sets the shell and project command limit, in minutes.
func (s *settings) toolTimeoutRow(c config.Config) setRow {
	shown := fmt.Sprintf("%g min", config.DefaultToolTimeout)
	if c.ToolTimeout != nil {
		if *c.ToolTimeout <= 0 {
			shown = "no limit"
		} else {
			shown = fmt.Sprintf("%g min", *c.ToolTimeout)
		}
	}
	cur := ""
	if c.ToolTimeout != nil {
		cur = strconv.FormatFloat(*c.ToolTimeout, 'f', -1, 64)
	}
	return setRow{label: "Command timeout", value: shown, note: "0.5 to 30 minutes; 0 means no limit", key: "tool_timeout", raw: cur,
		act: func() tea.Cmd {
			return s.prompt("Command timeout", "minutes, for example 2; 0 for no limit", cur, func(v string) tea.Cmd {
				return s.setFlat7("tool_timeout", strings.TrimSpace(v))
			})
		},
		del: func() tea.Cmd { return s.setFlat7("tool_timeout", "default") }}
}

// ---------------------------------------------------------------- Tools tab

// toolRows are the editor, search keys, privacy, instructions and hooks.
func (s *settings) toolRows() []setRow {
	c := fields7(s.be)
	rows := []setRow{{label: "Editor", header: true}}
	rows = append(rows,
		s.textRow7("Editor command", "editor.command", c.Editor.Command, "nvim, vim, or a path",
			"used by Ctrl+E and /editor; SCRIBE_EDITOR still wins"),
		s.textRow7("Editor arguments", "editor.args", strings.Join(c.Editor.Args, ", "), "comma separated, before the file name",
			"passed before the file name"),
		s.editorAccessRow(),
	)

	rows = append(rows, s.editorIntegrationRows(c)...)

	rows = append(rows, setRow{label: "Web search", header: true})
	rows = append(rows, s.secretRow("Brave Search key", registry.BraveKeyID, "masked; stored in the key store"))
	rows = append(rows, s.textRow7("Brave variable", "search.brave_key_env", c.Search.BraveKeyEnv, "BRAVE_SEARCH_API_KEY",
		"environment variable that may hold the key"))
	rows = append(rows, s.secretRow("Jina Reader key", registry.JinaKeyID, "optional; not used by the web tool yet"))
	rows = append(rows, s.textRow7("Jina variable", "search.jina_key_env", c.Search.JinaKeyEnv, "JINA_API_KEY", ""))
	rows = append(rows, s.flagRow7("Read pages through Jina", "search.jina_reader", c.Search.UseJinaReader(),
		"shares every URL with r.jina.ai; default off"))

	rows = append(rows, setRow{label: "Privacy", header: true})
	rows = append(rows, s.listRow7("Forbidden paths", "forbidden", c.Forbidden, "a path pattern, e.g. secrets/**")...)

	rows = append(rows, s.instructionRows(c)...)
	rows = append(rows, s.hookRows(c)...)
	return rows
}

// keySourceBadge names where a key comes from: [env] for an environment
// variable (the variable is in the source string), [config] for a copy in the
// key store under the config directory, [none] when nothing resolves. This
// build has no OS keychain, so [keychain] never appears.
func keySourceBadge(src string) string {
	switch {
	case strings.HasPrefix(src, "env:"):
		return "[env]"
	case src == "store":
		return "[config]"
	}
	return "[none]"
}

// secretRow shows a search key's source and sets or removes it. The value is
// read through a masked prompt and goes straight to the key store.
func (s *settings) secretRow(label, id, note string) setRow {
	src := ""
	ss, hasStore := s.be.(SecretStore7)
	if hasStore {
		src = ss.SecretSource(id)
	}
	return setRow{label: label, value: keySourceBadge(src), note: note,
		act: func() tea.Cmd {
			if !hasStore {
				s.say("err", "this build cannot store keys")
				return nil
			}
			p := NewTextPrompt(s.m.th, label, "paste the key", true, func(v string) tea.Cmd {
				if err := ss.SetSecret(id, v); err != nil {
					s.say("err", err.Error())
					return nil
				}
				s.say("ok", label+" saved (not shown)")
				return nil
			})
			p.icon = "key"
			s.m.pushModal(p)
			return nil
		},
		del: func() tea.Cmd {
			if !hasStore {
				return nil
			}
			if err := ss.RemoveSecret(id); err != nil {
				s.say("err", err.Error())
				return nil
			}
			s.say("ok", label+" removed")
			return nil
		}}
}

// editorAccessRow is the agent's access to the editor. It is saved to the
// global config, as /editor access is, and applies at once.
func (s *settings) editorAccessRow() setRow {
	cur := nvim.GetAccess()
	opts := []nvim.Access{nvim.AccessOn, nvim.AccessWhenOpen, nvim.AccessOff}
	return setRow{label: "Agent editor access", value: cur.Label(), note: "saved; /editor access sets it too",
		act: func() tea.Cmd {
			next := opts[(int(cur)+1)%len(opts)]
			nvim.SetAccess(next)
			if err := saveEditorSetting(func(c *config.Config) { c.EditorAccess = next.String() }); err != nil {
				s.say("error", "agent editor access: "+next.Label()+" (not saved: "+err.Error()+")")
				return nil
			}
			s.say("ok", "agent editor access: "+next.Label())
			return nil
		}}
}

// instructionRows toggles each instruction source. An unset list means the
// defaults; the list is never saved empty, since an empty list is dropped on
// save and would bring the defaults back.
func (s *settings) instructionRows(c config.Config) []setRow {
	effective := c.Instructions
	if effective == nil {
		effective = agent.DefaultInstructionFiles
	}
	rows := []setRow{{label: "Instruction files", header: true, note: "loaded into the system prompt"}}
	on := func(id string) bool { return listIndex(effective, id) >= 0 }
	toggle := func(id string) tea.Cmd {
		next, err := nextInstructions(c.Instructions, id)
		if err != nil {
			s.say("err", err.Error())
			return nil
		}
		return s.set("instructions", next)
	}
	for _, src := range agent.InstructionSources {
		id := src.ID
		rows = append(rows, setRow{label: src.Label, value: onOffWord(on(id)), toggle: true, on: on(id), id: "",
			note: strings.Join(src.Files, ", "), key: "instructions", raw: effective,
			act: func() tea.Cmd { return toggle(id) }})
	}
	return rows
}

// hookRows lists the configured hooks. They are edited in config.json.
func (s *settings) hookRows(c config.Config) []setRow {
	rows := []setRow{{label: "Hooks", header: true, value: strconv.Itoa(len(c.Hooks)),
		note: "shell commands on agent events; edit in config.json"}}
	if len(c.Hooks) == 0 {
		return append(rows, setRow{label: "No hooks configured", value: ""})
	}
	for _, h := range c.Hooks {
		matcher := h.Matcher
		if matcher == "" {
			matcher = "all"
		}
		var flags []string
		if h.If != "" {
			flags = append(flags, "if "+h.If)
		}
		if h.Once {
			flags = append(flags, "once")
		}
		if h.Async {
			flags = append(flags, "async")
		}
		note := h.Command
		if len(flags) > 0 {
			note = strings.Join(flags, " · ") + " · " + h.Command
		}
		rows = append(rows, setRow{label: h.Event, value: matcher, note: note})
	}
	return rows
}

// keyPriorityRow sets whether a stored key or an environment variable wins.
func (s *settings) keyPriorityRow(c config.Config) setRow {
	return s.pickRow7("Key priority", "key_priority", c.KeyPriority, []string{"env", "app"},
		"env: a variable beats a stored key; app: the stored key wins")
}

// sortedKeys and sortedKeysInt list map keys in order, so rows keep still.
func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysInt(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// nextInstructions returns the instruction list with id toggled. An unset list
// starts from the defaults. It refuses to empty the list, since an empty list
// is dropped on save and would bring the defaults back.
func nextInstructions(cur []string, id string) ([]string, error) {
	effective := cur
	if effective == nil {
		effective = agent.DefaultInstructionFiles
	}
	if listIndex(effective, id) < 0 {
		return append(append([]string(nil), effective...), id), nil
	}
	var next []string
	for _, x := range effective {
		if x != id {
			next = append(next, x)
		}
	}
	if len(next) == 0 {
		return nil, fmt.Errorf("keep at least one instruction file on")
	}
	return next, nil
}

// performanceRows are the request options the Think tab shows for the
// current model. Each row appears only when the provider and the model's
// reasoning style take it: thinking on/off depends on the style, effort only
// on adaptive and effort-style models, and the cache lifetime only on
// Anthropic. Speed, tool streaming and send-reasoning are not in the rows
// because no adapter reads them yet; their config keys stay settable.
func (s *settings) performanceRows(c config.Config, provider, style string) []setRow {
	p := c.Performance
	var rows []setRow
	if provider == "anthropic" {
		rows = append(rows, setRow{label: "Claude options", header: true, note: "sent with each Claude request"})
		rows = append(rows, s.claudeContextRows(c.ContextManagement)...)
		rows = append(rows, s.claudeRuntimeRows(p)...)
		switch style {
		case llm.ReasoningBudget:
			rows = append(rows, s.pickRow7("Thinking", "performance.thinking", p.Thinking,
				[]string{"default", "off", "enabled"}, "enabled uses the budget above"))
		case llm.ReasoningAdaptive:
			rows = append(rows, s.pickRow7("Thinking", "performance.thinking", p.Thinking,
				[]string{"default", "off", "adaptive"}, "adaptive: the model decides how long to think"))
			rows = append(rows, s.pickRow7("Effort", "performance.effort", p.Effort,
				[]string{"default", "low", "medium", "high", "xhigh", "max"}, "the Effort row above wins when set"))
		}
		rows = append(rows, s.pickRow7("Cache lifetime", "performance.cache_ttl", p.CacheTTL,
			[]string{"default", "5m", "1h"}, "1h keeps the prompt cached longer, at a higher write cost"))
	}
	if provider == "anthropic" || provider == "openai" {
		rows = append(rows, s.flagRow7("One tool at a time", "performance.disable_parallel_tool_use",
			config.Bool(p.DisableParallelToolUse, false), "the model calls one tool per turn"))
	}
	return rows
}

// editorIntegrationLabels names the eleven editor integration toggles in the
// order the settings show them, with the note each one carries.
var editorIntegrationLabels = map[string][2]string{
	"diagnostics":    {"LSP diagnostics", "errors and warnings in the prompt"},
	"symbols":        {"Document symbols", "outline of the open file"},
	"hover":          {"Hover and type info", "types under the cursor"},
	"references":     {"Find references", "who uses a symbol"},
	"definition":     {"Go to definition", "jump to where a symbol is defined"},
	"code_actions":   {"Code actions", "quick fixes the server offers"},
	"rename":         {"LSP rename", "rename a symbol across files"},
	"lsp_status":     {"LSP status", "which language servers are running"},
	"format":         {"LSP format", "format with the language server"},
	"editor_context": {"Editor context", "file, cursor and selection in the prompt"},
	"sync_on_edit":   {"Sync on edit", "jump the editor to edited files; off refreshes the current one"},
}

// editorIntegrationRows are the per-feature editor toggles (/editor
// settings). They are saved as the editor_integration object. Each row says
// the value is saved, because the language-server tools do not read the
// toggles yet.
func (s *settings) editorIntegrationRows(c config.Config) []setRow {
	rows := []setRow{{label: "Editor integrations", header: true,
		note: "saved per feature; the language-server tools do not read them yet"}}
	for _, name := range config.EditorIntegrationKeys {
		lab := editorIntegrationLabels[name]
		on := c.EditorIntegrationOn(name)
		rows = append(rows, s.flagRow7(lab[0], "editor_integration."+name, on, lab[1]))
	}
	return rows
}
