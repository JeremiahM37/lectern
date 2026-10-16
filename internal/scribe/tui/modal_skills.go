package tui

// The skills browser (/skills and Ctrl+S): installed skills, and the
// instructions of each one.

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
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
	p.Hint = "↑↓ move · Enter view · / filter · Esc close"
	p.Empty = "No skills match, or none are installed yet."

	var details []SkillDetail
	byName := map[string]SkillDetail{}
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
			it := PickItem{Value: d.Name, Label: d.Name, Desc: oneLine(d.Description), Group: group}
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
	m.pushModal(p)
}
