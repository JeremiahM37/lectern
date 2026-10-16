package tui

// The tools modal (/tools): every tool the agent can call, grouped, with
// enable and disable toggles.

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
)

func init() {
	overrideCommands(&command{Name: "tools", Desc: "Enable or disable tools for the agent", Group: "Intelligence",
		Run: func(m *Model, _ string) tea.Cmd { m.openToolsModal(); return nil }})
}

// toolGroupOf maps built-in tool names to the group they are listed under.
var toolGroupOf = map[string]string{
	"read_file": "Read", "read": "Read", "list_dir": "Read",
	"write_file": "Edit", "edit_file": "Edit", "multi_edit": "Edit", "undo_edit": "Edit", "ast_edit": "Edit", "format_file": "Edit",
	"grep": "Search", "glob": "Search",
	"find_symbol": "Code intelligence", "symbol_refs": "Code intelligence", "file_outline": "Code intelligence",
	"navigate": "Code intelligence", "diagnostics": "Code intelligence", "rename_symbol": "Code intelligence",
	"code_map": "Code intelligence", "code_impact": "Code intelligence",
	"shell": "Run", "shell_output": "Run", "shell_kill": "Run", "project": "Run", "git": "Run", "test": "Run",
	"web_search": "Web", "fetch_page": "Web",
	"dispatch": "Agent", "ask_user": "Agent", "task_list": "Agent", "update_plan_step": "Agent", "plan": "Agent", "memory": "Agent",
}

// toolGroupOrder is the display order of the built-in groups.
var toolGroupOrder = []string{"Read", "Edit", "Search", "Code intelligence", "Run", "Web", "Agent"}

// toolGroup returns the group heading for a tool.
func toolGroup(t ToolInfo) string {
	if strings.HasPrefix(t.Source, "mcp:") {
		return "MCP: " + strings.TrimPrefix(t.Source, "mcp:")
	}
	if g, ok := toolGroupOf[t.Name]; ok {
		return g
	}
	return "Other"
}

// sortToolGroups orders group headings: built-ins first, then MCP servers, then Other.
func sortToolGroups(groups []string) {
	rank := func(g string) (int, string) {
		for i, b := range toolGroupOrder {
			if g == b {
				return i, ""
			}
		}
		if strings.HasPrefix(g, "MCP: ") {
			return len(toolGroupOrder), g
		}
		return len(toolGroupOrder) + 1, g
	}
	sort.SliceStable(groups, func(i, j int) bool {
		ri, si := rank(groups[i])
		rj, sj := rank(groups[j])
		if ri != rj {
			return ri < rj
		}
		return si < sj
	})
}

// openToolsModal lists every tool; Space or Enter toggles the selected one.
func (m *Model) openToolsModal() {
	be := m.cur().be
	th := m.th
	p := NewPicker(th, "Tools", "tools")
	p.KeepOpen = true
	p.Hint = "↑↓ move · Space toggle · / filter · Esc close"
	p.Empty = "No tools match."

	var infos []ToolInfo
	byName := map[string]ToolInfo{}
	build := func() []PickItem {
		infos = be.Tools()
		byName = map[string]ToolInfo{}
		groups := map[string]bool{}
		for _, t := range infos {
			byName[t.Name] = t
			groups[toolGroup(t)] = true
		}
		var order []string
		for g := range groups {
			order = append(order, g)
		}
		sortToolGroups(order)
		rank := map[string]int{}
		for i, g := range order {
			rank[g] = i
		}
		sorted := append([]ToolInfo(nil), infos...)
		sort.SliceStable(sorted, func(i, j int) bool {
			gi, gj := rank[toolGroup(sorted[i])], rank[toolGroup(sorted[j])]
			if gi != gj {
				return gi < gj
			}
			return sorted[i].Name < sorted[j].Name
		})
		enabled := 0
		items := make([]PickItem, 0, len(sorted))
		for _, t := range sorted {
			it := PickItem{Value: t.Name, Label: t.Name, Group: toolGroup(t), Desc: oneLine(t.Description),
				Kind: PickToggle, On: t.Enabled}
			if t.ReadOnly {
				it.Desc = "read-only · " + it.Desc
			}
			if t.Enabled {
				enabled++
			}
			items = append(items, it)
		}
		p.Blurb = fmt.Sprintf("%d / %d enabled", enabled, len(sorted))
		return items
	}
	// The picker flips the switch (PickToggle) before OnSelect runs, so the
	// row's On is the new state to save.
	p.Items = build()
	p.refilter()
	p.OnSelect = func(it PickItem) tea.Cmd {
		if _, ok := byName[it.Value]; !ok {
			return nil
		}
		be.SetToolEnabled(it.Value, it.On)
		p.SetItems(build())
		return nil
	}
	p.Preview = func(it PickItem) []string {
		t, ok := byName[it.Value]
		if !ok {
			return nil
		}
		state := fg(th.Success).Render("enabled")
		if !t.Enabled {
			state = fg(th.Error).Render("disabled")
		}
		lines := []string{
			fgb(th.TextPrimary).Render(t.Name),
			state + fg(th.TextFaint).Render(" · ") + fg(th.TextDim).Render(t.Source),
		}
		if t.ReadOnly {
			lines = append(lines, fg(th.TextDim).Render("read-only: cannot change files"))
		}
		lines = append(lines, "")
		if t.Description != "" {
			for _, l := range strings.Split(render.Wrap(t.Description, 44), "\n") {
				lines = append(lines, fg(th.TextPrimary).Render(l))
			}
		}
		return lines
	}
	m.pushModal(p)
}
