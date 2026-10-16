package tui

// The skills browser (/skills and Ctrl+S): installed skills, and the
// instructions of each one. A skill can be loaded into the AI context for
// this project (Ctrl+A), which adds its instructions to the system prompt of
// the next request, and new skills can be installed from a path or git URL
// (Ctrl+N).

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func init() {
	overrideCommands(&command{Name: "skills", Desc: "Browse installed skills and read their instructions", Group: "Intelligence",
		Run: func(m *Model, _ string) tea.Cmd { m.openSkillBrowser(); return nil }})
}

// SkillDetail describes one installed skill.
type SkillDetail struct {
	Name, Description, Scope, Dir, Problem string // Scope: project | global
}

// SkillCatalog is optionally implemented by a Backend that can describe its
// skills. Without it the browser lists Backend.Skills() by name only.
type SkillCatalog interface {
	SkillDetails() []SkillDetail
	// SkillBody returns the instructions a skill gives the model.
	SkillBody(name string) (string, error)
}

// skillsHint is where skills are read from, shown under the search row.
const skillsHint = "Add: .scribe/skills/<name>/SKILL.md  ·  ~/.claude/skills/<name>/"

// openSkillBrowser opens the skills picker. Enter shows a skill's SKILL.md.
func (m *Model) openSkillBrowser() {
	be := m.cur().be
	cat, _ := be.(SkillCatalog)
	th := m.th

	p := NewPicker(th, "Skills", "skills")
	p.KeepOpen = true
	p.Blurb = skillsHint
	p.Hint = "↑↓ move · Enter view · ^A load/unload · ^N install · / filter · Esc close"
	p.Empty = "No skills match, or none are installed yet."

	var details []SkillDetail
	byName := map[string]SkillDetail{}
	loaded := map[string]bool{}
	build := func() []PickItem {
		details = nil
		if cat != nil {
			details = cat.SkillDetails()
		} else {
			for _, n := range be.Skills() {
				details = append(details, SkillDetail{Name: n, Scope: "project"})
			}
		}
		sort.SliceStable(details, func(i, j int) bool {
			if details[i].Scope != details[j].Scope {
				return details[i].Scope == "project"
			}
			return details[i].Name < details[j].Name
		})
		loaded = map[string]bool{}
		for _, n := range be.ActiveSkills() {
			loaded[n] = true
		}
		byName = map[string]SkillDetail{}
		items := make([]PickItem, 0, len(details))
		for _, d := range details {
			byName[d.Name] = d
			group := "Installed"
			if cat != nil {
				group = "Global skills"
				if d.Scope == "project" {
					group = "Project skills"
				}
			}
			it := PickItem{Value: d.Name, Label: d.Name, Desc: oneLine(d.Description), Group: group, Current: loaded[d.Name]}
			if d.Problem != "" {
				it.Icon = icons.Icon("warning")
				it.Color = th.Warning
				it.Desc = "problem: " + oneLine(d.Problem)
			}
			items = append(items, it)
		}
		return items
	}
	p.Items = build()
	p.refilter()
	p.Preview = func(it PickItem) []string {
		d, ok := byName[it.Value]
		if !ok {
			return nil
		}
		lines := []string{fgb(th.TextPrimary).Render(d.Name)}
		if d.Scope != "" {
			lines = append(lines, fg(th.TextDim).Render("scope  "+d.Scope))
		}
		if d.Dir != "" {
			lines = append(lines, fg(th.TextDim).Render("dir    "+d.Dir))
		}
		state := "not loaded"
		if loaded[d.Name] {
			state = "loaded into the AI context"
		}
		lines = append(lines, fg(th.TextDim).Render("context "+state))
		lines = append(lines, "")
		if d.Description != "" {
			lines = append(lines, wrapLines(d.Description, 44)...)
		} else {
			lines = append(lines, fg(th.TextFaint).Render("No description in SKILL.md."))
		}
		if d.Problem != "" {
			lines = append(lines, "", fg(th.Warning).Render("Problem: "+oneLine(d.Problem)))
		}
		lines = append(lines, "", fg(th.TextFaint).Render("Enter shows the full instructions."))
		return lines
	}
	p.OnSelect = func(it PickItem) tea.Cmd {
		if cat == nil {
			m.pushModal(NewInfoView(th, it.Value, "skills", []string{"Skill details are not available from this backend."}))
			return nil
		}
		body, err := cat.SkillBody(it.Value)
		if err != nil {
			body = "Could not read this skill: " + err.Error()
		}
		m.pushModal(NewInfoView(th, it.Value, "skills", strings.Split(body, "\n")))
		return nil
	}
	reload := func() { p.SetItems(build()) }
	p.OnKey = func(key string) (tea.Cmd, bool) {
		switch key {
		case "ctrl+a":
			it, ok := p.Selected()
			if !ok {
				return nil, true
			}
			on := !loaded[it.Value]
			if err := be.SetSkillActive(it.Value, on); err != nil {
				return flashCmd("err", "Could not change "+it.Value+": "+err.Error()), true
			}
			reload()
			if on {
				return flashCmd("ok", fmt.Sprintf("Skill %q loaded into AI context.", it.Value)), true
			}
			return flashCmd("ok", fmt.Sprintf("Skill %q unloaded from AI context.", it.Value)), true
		case "ctrl+n":
			m.pushModal(NewTextPrompt(th, "Install skill", "path or git URL, optionally #subdir", false, func(src string) tea.Cmd {
				m.pushModal(installScopePicker(th, func(scope string) tea.Cmd {
					return installSkill(be, src, scope, p, reload)
				}))
				return nil
			}))
			return nil, true
		}
		return nil, false
	}
	m.pushModal(p)
}

// installScopePicker asks where a skill goes, then calls onPick.
func installScopePicker(th theme.Theme, onPick func(scope string) tea.Cmd) *Picker {
	sp := NewPicker(th, "Install skill to", "skills")
	sp.Hint = "↑↓ move · Enter install · Esc cancel"
	sp.Items = []PickItem{
		{Value: "project", Label: "This project", Desc: ".scribe/skills/"},
		{Value: "global", Label: "Every project", Desc: "Scribe home skills folder"},
	}
	sp.refilter()
	sp.OnSelect = func(it PickItem) tea.Cmd { return onPick(it.Value) }
	return sp
}

// installSkill installs src into scope in the background. Its result lands on
// owner (the browser), which then reloads its list.
func installSkill(be Backend, src, scope string, owner Modal, reload func()) tea.Cmd {
	return asyncModal(owner, func() func() (tea.Cmd, bool) {
		name, err := be.InstallSkill(src, scope)
		return func() (tea.Cmd, bool) {
			if err != nil {
				return flashCmd("err", "Failed to install "+src+": "+err.Error()), false
			}
			reload()
			where := "this project"
			if scope == "global" {
				where = "every project"
			}
			return flashCmd("ok", fmt.Sprintf("Skill %q installed for %s.", name, where)), false
		}
	})
}
